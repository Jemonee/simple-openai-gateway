// Channel verification detector composition and scoring are adapted from
// Veridrop (https://github.com/canarybyte/veridrop), licensed under AGPL-3.0.
package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	VerificationModeQuick              = "quick"
	VerificationModeStandard           = "standard"
	VerificationModeFull               = "full"
	verificationMaxBodyBytes     int64 = 4 << 20
	verificationAttemptBodyBytes int64 = 128 << 10
)

type VerificationRequest struct {
	ChannelID uint64 `json:"channelId"`
	ModelID   uint64 `json:"modelId"`
	Mode      string `json:"mode"`
}

type VerificationCheckResult struct {
	Name     string         `json:"name"`
	Label    string         `json:"label"`
	Category string         `json:"category"`
	Status   string         `json:"status"`
	Score    float64        `json:"score"`
	Weight   float64        `json:"weight"`
	Summary  string         `json:"summary"`
	Critical bool           `json:"critical"`
	Evidence map[string]any `json:"evidence,omitempty"`
}

type VerificationReportView struct {
	ID                  string                    `json:"id"`
	SessionID           string                    `json:"sessionId"`
	ChannelID           uint64                    `json:"channelId"`
	ChannelName         string                    `json:"channelName"`
	ModelID             uint64                    `json:"modelId"`
	PublicModel         string                    `json:"publicModel"`
	UpstreamModel       string                    `json:"upstreamModel"`
	Mode                string                    `json:"mode"`
	Score               float64                   `json:"score"`
	Verdict             string                    `json:"verdict"`
	Summary             string                    `json:"summary"`
	Checks              []VerificationCheckResult `json:"checks"`
	StartedAt           time.Time                 `json:"startedAt"`
	FinishedAt          time.Time                 `json:"finishedAt"`
	RequestCount        int64                     `json:"requestCount"`
	InputTokens         int64                     `json:"inputTokens"`
	OutputTokens        int64                     `json:"outputTokens"`
	EstimatedCostMicros int64                     `json:"estimatedCostMicros"`
	UpstreamCostMicros  int64                     `json:"upstreamCostMicros"`
}

type VerificationSessionView struct {
	ID                  string    `json:"id"`
	ChannelID           uint64    `json:"channelId"`
	ChannelName         string    `json:"channelName"`
	ModelID             uint64    `json:"modelId"`
	PublicModel         string    `json:"publicModel"`
	UpstreamModel       string    `json:"upstreamModel"`
	Mode                string    `json:"mode"`
	Status              string    `json:"status"`
	Score               float64   `json:"score"`
	Verdict             string    `json:"verdict"`
	Summary             string    `json:"summary"`
	RequestCount        int64     `json:"requestCount"`
	SuccessCount        int64     `json:"successCount"`
	InputTokens         int64     `json:"inputTokens"`
	OutputTokens        int64     `json:"outputTokens"`
	CachedTokens        int64     `json:"cachedTokens"`
	CacheWriteTokens    int64     `json:"cacheWriteTokens"`
	EstimatedCostMicros int64     `json:"estimatedCostMicros"`
	UpstreamCostMicros  int64     `json:"upstreamCostMicros"`
	ErrorMessage        string    `json:"errorMessage"`
	StartedAt           time.Time `json:"startedAt"`
	FinishedAt          time.Time `json:"finishedAt"`
}

type VerificationSessionPage struct {
	Items    []VerificationSessionView `json:"items"`
	Total    int64                     `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"pageSize"`
}

type VerificationAttemptView struct {
	ID                    string    `json:"id"`
	Sequence              int       `json:"sequence"`
	DetectorName          string    `json:"detectorName"`
	Method                string    `json:"method"`
	Path                  string    `json:"path"`
	Stream                bool      `json:"stream"`
	RequestBody           string    `json:"requestBody"`
	RequestBodyTruncated  bool      `json:"requestBodyTruncated"`
	ResponseBody          string    `json:"responseBody"`
	ResponseBodyTruncated bool      `json:"responseBodyTruncated"`
	ContentType           string    `json:"contentType"`
	StatusCode            int       `json:"statusCode"`
	Outcome               string    `json:"outcome"`
	ErrorMessage          string    `json:"errorMessage"`
	InputTokens           int64     `json:"inputTokens"`
	OutputTokens          int64     `json:"outputTokens"`
	CachedTokens          int64     `json:"cachedTokens"`
	CacheWriteTokens      int64     `json:"cacheWriteTokens"`
	EstimatedCostMicros   int64     `json:"estimatedCostMicros"`
	UpstreamCostMicros    int64     `json:"upstreamCostMicros"`
	CostSource            string    `json:"costSource"`
	UsageSource           string    `json:"usageSource"`
	LatencyMS             int64     `json:"latencyMs"`
	StartedAt             time.Time `json:"startedAt"`
	FinishedAt            time.Time `json:"finishedAt"`
}

type VerificationSessionDetailView struct {
	Session  VerificationSessionView   `json:"session"`
	Attempts []VerificationAttemptView `json:"attempts"`
}

type VerificationReportPage struct {
	Items    []VerificationReportView `json:"items"`
	Total    int64                    `json:"total"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"pageSize"`
}

type VerificationReportQuery struct {
	ChannelID uint64
	ModelID   uint64
	Page      int
	PageSize  int
}

type VerificationSessionQuery struct {
	ChannelID uint64
	ModelID   uint64
	Page      int
	PageSize  int
}

type VerificationDetector interface {
	Definition() verificationDetectorDefinition
	Run(context.Context, *verificationProbe) VerificationCheckResult
}

type verificationDetectorDefinition struct {
	Name     string
	Label    string
	Category string
	Weight   float64
}

type VerificationService struct {
	store     *Store
	estimator *TokenEstimator
	client    *http.Client
}

func NewVerificationService(store *Store, estimator *TokenEstimator) *VerificationService {
	return &VerificationService{
		store:     store,
		estimator: estimator,
		client: &http.Client{
			Timeout: 35 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (s *VerificationService) VerifyChannel(ctx context.Context, input VerificationRequest) (*VerificationReportView, error) {
	if input.ChannelID == 0 || input.ModelID == 0 {
		return nil, errors.New("channelId and modelId are required")
	}
	mode, err := normalizeVerificationMode(input.Mode)
	if err != nil {
		return nil, err
	}

	var channel Channel
	if err := s.store.db.WithContext(ctx).First(&channel, input.ChannelID).Error; err != nil {
		return nil, err
	}
	var mapping ChannelModel
	if err := s.store.db.WithContext(ctx).Where("channel_id = ? AND model_id = ?", input.ChannelID, input.ModelID).First(&mapping).Error; err != nil {
		return nil, err
	}
	var model GatewayModel
	if err := s.store.db.WithContext(ctx).First(&model, input.ModelID).Error; err != nil {
		return nil, err
	}
	apiKey, err := s.store.secretBox.Decrypt(channel.APIKeyCipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt channel API key: %w", err)
	}
	baseURL, err := normalizeVerificationBaseURL(channel.BaseURL)
	if err != nil {
		return nil, err
	}

	started := time.Now().UTC()
	session := VerificationSession{
		ID: uuid.NewString(), ChannelID: channel.ID, ChannelName: channel.Name,
		ModelID: model.ID, PublicModel: model.Name, UpstreamModel: mapping.UpstreamModel,
		Mode: mode, Status: "running", StartedAt: started,
	}
	if err := s.store.db.WithContext(ctx).Create(&session).Error; err != nil {
		return nil, err
	}
	probe := &verificationProbe{
		client:    s.client,
		baseURL:   baseURL,
		apiKey:    apiKey,
		model:     mapping.UpstreamModel,
		mapping:   mapping,
		estimator: s.estimator,
		service:   s,
		sessionID: session.ID,
	}
	runTimeout := 45 * time.Second
	if mode == VerificationModeStandard {
		runTimeout = 90 * time.Second
	} else if mode == VerificationModeFull {
		runTimeout = 3 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	results := make([]VerificationCheckResult, 0, 6)
	for _, detector := range verificationDetectors(mode) {
		if err := runCtx.Err(); err != nil {
			s.failVerificationSession(ctx, session.ID, err)
			return nil, err
		}
		definition := detector.Definition()
		probe.detectorName = definition.Name
		result := detector.Run(runCtx, probe)
		result.Name = definition.Name
		result.Label = definition.Label
		result.Category = definition.Category
		result.Weight = definition.Weight
		results = append(results, result)
	}

	score, verdict := verificationScore(results)
	finished := time.Now().UTC()
	aggregate, aggregateErr := s.verificationAggregate(ctx, session.ID)
	if aggregateErr != nil {
		s.failVerificationSession(ctx, session.ID, aggregateErr)
		return nil, aggregateErr
	}
	report := VerificationReport{
		ID:                  uuid.NewString(),
		SessionID:           session.ID,
		ChannelID:           channel.ID,
		ChannelName:         channel.Name,
		ModelID:             model.ID,
		PublicModel:         model.Name,
		UpstreamModel:       mapping.UpstreamModel,
		Mode:                mode,
		Score:               score,
		Verdict:             verdict,
		Summary:             verificationSummary(score, verdict, results),
		RequestCount:        aggregate.RequestCount,
		InputTokens:         aggregate.InputTokens,
		OutputTokens:        aggregate.OutputTokens,
		EstimatedCostMicros: aggregate.EstimatedCostMicros,
		UpstreamCostMicros:  aggregate.UpstreamCostMicros,
		StartedAt:           started,
		FinishedAt:          finished,
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		return nil, err
	}
	report.ChecksJSON = string(encoded)
	if err := s.store.db.WithContext(ctx).Create(&report).Error; err != nil {
		s.failVerificationSession(ctx, session.ID, err)
		return nil, err
	}
	if err := s.finishVerificationSession(ctx, session.ID, report); err != nil {
		return nil, err
	}
	view := reportView(report)
	return &view, nil
}

func (s *VerificationService) ListReports(ctx context.Context, query VerificationReportQuery) (*VerificationReportPage, error) {
	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	db := s.store.db.WithContext(ctx).Model(&VerificationReport{})
	if query.ChannelID > 0 {
		db = db.Where("channel_id = ?", query.ChannelID)
	}
	if query.ModelID > 0 {
		db = db.Where("model_id = ?", query.ModelID)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, err
	}
	var reports []VerificationReport
	if err := db.Order("finished_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&reports).Error; err != nil {
		return nil, err
	}
	items := make([]VerificationReportView, 0, len(reports))
	for _, report := range reports {
		items = append(items, reportView(report))
	}
	return &VerificationReportPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *VerificationService) Report(ctx context.Context, id string) (*VerificationReportView, error) {
	var report VerificationReport
	if err := s.store.db.WithContext(ctx).First(&report, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		return nil, err
	}
	view := reportView(report)
	return &view, nil
}

func (s *VerificationService) ListSessions(ctx context.Context, query VerificationSessionQuery) (*VerificationSessionPage, error) {
	page := query.Page
	if page < 1 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	db := s.store.db.WithContext(ctx).Model(&VerificationSession{})
	if query.ChannelID > 0 {
		db = db.Where("channel_id = ?", query.ChannelID)
	}
	if query.ModelID > 0 {
		db = db.Where("model_id = ?", query.ModelID)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, err
	}
	var sessions []VerificationSession
	if err := db.Order("started_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&sessions).Error; err != nil {
		return nil, err
	}
	items := make([]VerificationSessionView, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, verificationSessionView(session))
	}
	return &VerificationSessionPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *VerificationService) Session(ctx context.Context, id string) (*VerificationSessionDetailView, error) {
	var session VerificationSession
	if err := s.store.db.WithContext(ctx).First(&session, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		return nil, err
	}
	var attempts []VerificationAttempt
	if err := s.store.db.WithContext(ctx).Where("session_id = ?", session.ID).Order("sequence asc").Find(&attempts).Error; err != nil {
		return nil, err
	}
	views := make([]VerificationAttemptView, 0, len(attempts))
	for _, attempt := range attempts {
		views = append(views, verificationAttemptView(attempt))
	}
	return &VerificationSessionDetailView{Session: verificationSessionView(session), Attempts: views}, nil
}

type verificationAggregate struct {
	RequestCount        int64
	SuccessCount        int64
	InputTokens         int64
	OutputTokens        int64
	CachedTokens        int64
	CacheWriteTokens    int64
	EstimatedCostMicros int64
	UpstreamCostMicros  int64
}

func (s *VerificationService) verificationAggregate(ctx context.Context, sessionID string) (verificationAggregate, error) {
	var aggregate verificationAggregate
	err := s.store.db.WithContext(ctx).Model(&VerificationAttempt{}).
		Select("COUNT(*) AS request_count, COALESCE(SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END), 0) AS success_count, COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(cached_tokens), 0) AS cached_tokens, COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(estimated_cost_micros), 0) AS estimated_cost_micros, COALESCE(SUM(upstream_cost_micros), 0) AS upstream_cost_micros").
		Where("session_id = ?", sessionID).Scan(&aggregate).Error
	return aggregate, err
}

func (s *VerificationService) finishVerificationSession(ctx context.Context, sessionID string, report VerificationReport) error {
	aggregate, err := s.verificationAggregate(ctx, sessionID)
	if err != nil {
		return err
	}
	return s.store.db.WithContext(ctx).Model(&VerificationSession{}).Where("id = ?", sessionID).Updates(map[string]any{
		"status": "completed", "score": report.Score, "verdict": report.Verdict, "summary": report.Summary,
		"request_count": aggregate.RequestCount, "success_count": aggregate.SuccessCount,
		"input_tokens": aggregate.InputTokens, "output_tokens": aggregate.OutputTokens,
		"cached_tokens": aggregate.CachedTokens, "cache_write_tokens": aggregate.CacheWriteTokens,
		"estimated_cost_micros": aggregate.EstimatedCostMicros, "upstream_cost_micros": aggregate.UpstreamCostMicros,
		"finished_at": report.FinishedAt,
	}).Error
}

func (s *VerificationService) failVerificationSession(ctx context.Context, sessionID string, err error) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	_ = s.store.db.WithContext(context.WithoutCancel(ctx)).Model(&VerificationSession{}).Where("id = ?", sessionID).Updates(map[string]any{
		"status": "failed", "error_message": truncateVerificationText(errorString(err), 512), "finished_at": time.Now().UTC(),
	})
}

func reportView(report VerificationReport) VerificationReportView {
	var checks []VerificationCheckResult
	if err := json.Unmarshal([]byte(report.ChecksJSON), &checks); err != nil {
		checks = []VerificationCheckResult{}
	}
	return VerificationReportView{ID: report.ID, SessionID: report.SessionID, ChannelID: report.ChannelID, ChannelName: report.ChannelName, ModelID: report.ModelID, PublicModel: report.PublicModel, UpstreamModel: report.UpstreamModel, Mode: report.Mode, Score: report.Score, Verdict: report.Verdict, Summary: report.Summary, Checks: checks, StartedAt: report.StartedAt, FinishedAt: report.FinishedAt, RequestCount: report.RequestCount, InputTokens: report.InputTokens, OutputTokens: report.OutputTokens, EstimatedCostMicros: report.EstimatedCostMicros, UpstreamCostMicros: report.UpstreamCostMicros}
}

func verificationSessionView(session VerificationSession) VerificationSessionView {
	return VerificationSessionView{ID: session.ID, ChannelID: session.ChannelID, ChannelName: session.ChannelName, ModelID: session.ModelID, PublicModel: session.PublicModel, UpstreamModel: session.UpstreamModel, Mode: session.Mode, Status: session.Status, Score: session.Score, Verdict: session.Verdict, Summary: session.Summary, RequestCount: session.RequestCount, SuccessCount: session.SuccessCount, InputTokens: session.InputTokens, OutputTokens: session.OutputTokens, CachedTokens: session.CachedTokens, CacheWriteTokens: session.CacheWriteTokens, EstimatedCostMicros: session.EstimatedCostMicros, UpstreamCostMicros: session.UpstreamCostMicros, ErrorMessage: session.ErrorMessage, StartedAt: session.StartedAt, FinishedAt: session.FinishedAt}
}

func verificationAttemptView(attempt VerificationAttempt) VerificationAttemptView {
	return VerificationAttemptView{ID: attempt.ID, Sequence: attempt.Sequence, DetectorName: attempt.DetectorName, Method: attempt.Method, Path: attempt.Path, Stream: attempt.Stream, RequestBody: attempt.RequestBody, RequestBodyTruncated: attempt.RequestBodyTruncated, ResponseBody: attempt.ResponseBody, ResponseBodyTruncated: attempt.ResponseBodyTruncated, ContentType: attempt.ContentType, StatusCode: attempt.StatusCode, Outcome: attempt.Outcome, ErrorMessage: attempt.ErrorMessage, InputTokens: attempt.InputTokens, OutputTokens: attempt.OutputTokens, CachedTokens: attempt.CachedTokens, CacheWriteTokens: attempt.CacheWriteTokens, EstimatedCostMicros: attempt.EstimatedCostMicros, UpstreamCostMicros: attempt.UpstreamCostMicros, CostSource: attempt.CostSource, UsageSource: attempt.UsageSource, LatencyMS: attempt.LatencyMS, StartedAt: attempt.StartedAt, FinishedAt: attempt.FinishedAt}
}

func normalizeVerificationMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", VerificationModeQuick:
		return VerificationModeQuick, nil
	case VerificationModeStandard:
		return VerificationModeStandard, nil
	case VerificationModeFull:
		return VerificationModeFull, nil
	default:
		return "", fmt.Errorf("unsupported verification mode %q", value)
	}
}

func verificationDetectors(mode string) []VerificationDetector {
	detectors := []VerificationDetector{basicRequestDetector{}, modelConsistencyDetector{}}
	if mode == VerificationModeStandard || mode == VerificationModeFull {
		detectors = append(detectors, streamIntegrityDetector{}, usageBillingDetector{})
	}
	if mode == VerificationModeFull {
		detectors = append(detectors, toolCallsDetector{}, structuredOutputDetector{})
	}
	return detectors
}

func verificationScore(results []VerificationCheckResult) (float64, string) {
	var weighted, total float64
	critical := false
	for _, result := range results {
		if result.Status == "skipped" {
			continue
		}
		weighted += result.Score * result.Weight
		total += result.Weight
		critical = critical || (result.Critical && result.Status == "failed")
	}
	score := 0.0
	if total > 0 {
		score = weighted / total
	}
	verdict := "failed"
	if score >= 70 {
		verdict = "passed"
	} else if score >= 50 {
		verdict = "marginal"
	}
	if critical && verdict == "passed" {
		verdict = "marginal"
	}
	return mathRound(score), verdict
}

func mathRound(value float64) float64 { return float64(int(value*10+0.5)) / 10 }

func verificationSummary(score float64, verdict string, results []VerificationCheckResult) string {
	failed := 0
	for _, result := range results {
		if result.Status == "failed" {
			failed++
		}
	}
	if failed == 0 {
		return fmt.Sprintf("验证完成，%.1f 分，结果为 %s。", score, verdict)
	}
	return fmt.Sprintf("验证完成，%.1f 分，结果为 %s，%d 项检测未通过。", score, verdict, failed)
}

type verificationProbe struct {
	client       *http.Client
	baseURL      string
	apiKey       string
	model        string
	mapping      ChannelModel
	estimator    *TokenEstimator
	service      *VerificationService
	sessionID    string
	detectorName string
	sequence     int
}

type probeResponse struct {
	status  int
	latency int64
	body    []byte
	json    map[string]any
	headers http.Header
}

func (p *verificationProbe) recordAttempt(started time.Time, stream bool, requestBody, responseBody []byte, status int, contentType, outcome string, err error, usage Usage, upstreamCost int64, upstreamCostOK bool) {
	if p.service == nil || p.sessionID == "" {
		return
	}
	p.sequence++
	finished := time.Now().UTC()
	usageSource := usage.Source
	inputTokens := usage.InputTokens
	outputTokens := usage.OutputTokens
	if inputTokens == 0 && p.estimator != nil {
		inputTokens = p.estimator.EstimateJSON(requestBody)
	}
	if outputTokens == 0 && p.estimator != nil {
		outputTokens = p.estimator.EstimateJSON(responseBody)
	}
	usage.InputTokens = inputTokens
	usage.OutputTokens = outputTokens
	if usageSource == "" && (inputTokens > 0 || outputTokens > 0) {
		usageSource = "estimated_verification"
	}
	estimatedCost := CalculateCostMicros(p.mapping, usage)
	costSource := CostSourceFallback
	if err != nil || status < 200 || status >= 300 {
		estimatedCost = 0
		upstreamCost = 0
		costSource = CostSourceFailedZero
	} else if upstreamCostOK {
		costSource = CostSourceUpstream
	} else {
		upstreamCost = estimatedCost
	}
	if err != nil {
		outcome = RelayOutcomeFailed
	}
	attempt := VerificationAttempt{
		ID: uuid.NewString(), SessionID: p.sessionID, Sequence: p.sequence, DetectorName: p.detectorName,
		Method: http.MethodPost, Path: "/chat/completions", Stream: stream,
		RequestBody: string(requestBody), ResponseBody: string(responseBody), ContentType: contentType,
		StatusCode: status, Outcome: outcome, ErrorMessage: truncateVerificationText(errorString(err), 512),
		InputTokens: inputTokens, OutputTokens: outputTokens, CachedTokens: usage.CachedTokens, CacheWriteTokens: usage.CacheWriteTokens,
		EstimatedCostMicros: estimatedCost, UpstreamCostMicros: upstreamCost, CostSource: costSource, UsageSource: usageSource,
		LatencyMS: time.Since(started).Milliseconds(), StartedAt: started.UTC(), FinishedAt: finished,
	}
	attempt.RequestBody, attempt.RequestBodyTruncated = boundVerificationBody(requestBody)
	attempt.ResponseBody, attempt.ResponseBodyTruncated = boundVerificationBody(responseBody)
	_ = p.service.store.db.WithContext(context.Background()).Create(&attempt).Error
}

func boundVerificationBody(data []byte) (string, bool) {
	if int64(len(data)) <= verificationAttemptBodyBytes {
		return string(data), false
	}
	return string(data[:verificationAttemptBodyBytes]) + "\n... [truncated]", true
}

func truncateVerificationText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func (p *verificationProbe) request(ctx context.Context, payload map[string]any) (probeResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return probeResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return probeResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
	request.Header.Set("Content-Type", "application/json")
	started := time.Now()
	response, err := p.client.Do(request)
	if err != nil {
		p.recordAttempt(started, false, body, nil, 0, "", RelayOutcomeFailed, err, Usage{}, 0, false)
		return probeResponse{}, err
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, verificationMaxBodyBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		p.recordAttempt(started, false, body, responseBody, response.StatusCode, response.Header.Get("Content-Type"), RelayOutcomeFailed, err, Usage{}, 0, false)
		return probeResponse{}, err
	}
	if int64(len(responseBody)) > verificationMaxBodyBytes {
		p.recordAttempt(started, false, body, responseBody, response.StatusCode, response.Header.Get("Content-Type"), RelayOutcomeFailed, errors.New("verification response exceeded 4 MiB"), Usage{}, 0, false)
		return probeResponse{}, errors.New("verification response exceeded 4 MiB")
	}
	result := probeResponse{status: response.StatusCode, latency: time.Since(started).Milliseconds(), body: responseBody, headers: response.Header.Clone()}
	usage, usageOK := ParseUsage(responseBody)
	upstreamCost, upstreamCostOK := ParseUpstreamCostMicros(responseBody)
	if !usageOK {
		usage = Usage{}
	}
	outcome := RelayOutcomeFailed
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		_ = json.Unmarshal(responseBody, &result.json)
		outcome = RelayOutcomeSuccess
	}
	p.recordAttempt(started, false, body, responseBody, response.StatusCode, response.Header.Get("Content-Type"), outcome, nil, usage, upstreamCost, upstreamCostOK)
	return result, nil
}

type streamProbeResponse struct {
	status        int
	latency       int64
	contentType   string
	chunkCount    int
	eventNames    []string
	responseID    string
	responseModel string
	usage         map[string]any
	firstToken    bool
	terminal      bool
	body          []byte
}

func (p *verificationProbe) stream(ctx context.Context, payload map[string]any) (streamProbeResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return streamProbeResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return streamProbeResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
	request.Header.Set("Content-Type", "application/json")
	started := time.Now()
	response, err := p.client.Do(request)
	if err != nil {
		p.recordAttempt(started, true, body, nil, 0, "", RelayOutcomeFailed, err, Usage{}, 0, false)
		return streamProbeResponse{}, err
	}
	defer response.Body.Close()
	result := streamProbeResponse{status: response.StatusCode, latency: time.Since(started).Milliseconds(), contentType: response.Header.Get("Content-Type"), eventNames: []string{}}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, verificationAttemptBodyBytes+1))
		result.body, _ = responseBody, false
		p.recordAttempt(started, true, body, responseBody, response.StatusCode, result.contentType, RelayOutcomeFailed, nil, Usage{}, 0, false)
		return result, nil
	}
	var captured bytes.Buffer
	scanner := bufio.NewScanner(io.LimitReader(response.Body, verificationMaxBodyBytes))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if captured.Len() < int(verificationAttemptBodyBytes) {
			captured.WriteString(line)
			captured.WriteByte('\n')
		}
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			result.terminal = true
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		result.chunkCount++
		if value, ok := chunk["id"].(string); ok {
			result.responseID = value
		}
		if value, ok := chunk["model"].(string); ok {
			result.responseModel = value
		}
		if choices, ok := chunk["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
					result.terminal = true
				}
				if delta, ok := choice["delta"].(map[string]any); ok {
					if content, ok := delta["content"].(string); ok && content != "" {
						result.firstToken = true
					}
				}
			}
		}
		if usage, ok := chunk["usage"].(map[string]any); ok {
			result.usage = usage
		}
	}
	if err := scanner.Err(); err != nil {
		result.body = captured.Bytes()
		p.recordAttempt(started, true, body, result.body, result.status, result.contentType, RelayOutcomeFailed, err, Usage{}, 0, false)
		return result, err
	}
	result.body = captured.Bytes()
	usage := Usage{}
	if len(result.usage) > 0 {
		encoded, _ := json.Marshal(map[string]any{"usage": result.usage})
		usage, _ = ParseUsage(encoded)
	}
	upstreamCost, upstreamCostOK := ParseUpstreamCostMicros(result.body)
	outcome := RelayOutcomeFailed
	if result.terminal {
		outcome = RelayOutcomeSuccess
	}
	p.recordAttempt(started, true, body, result.body, result.status, result.contentType, outcome, nil, usage, upstreamCost, upstreamCostOK)
	return result, nil
}

func normalizeVerificationBaseURL(value string) (string, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(value), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return "", errors.New("channel base URL must be an absolute HTTP or HTTPS URL")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("channel base URL cannot contain user info, query parameters, or fragments")
	}
	return strings.TrimRight(base.String(), "/"), nil
}

func basicPayload(model string, prompt string) map[string]any {
	return map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": prompt}}, "max_tokens": 16}
}

func responseUsage(value map[string]any) map[string]any {
	if usage, ok := value["usage"].(map[string]any); ok {
		return usage
	}
	return nil
}
func number(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case int64:
		return typed, true
	default:
		return 0, false
	}
}
func usageNumbers(usage map[string]any) (int64, int64, int64, bool) {
	if usage == nil {
		return 0, 0, 0, false
	}
	in, okIn := number(usage["prompt_tokens"])
	if !okIn {
		in, okIn = number(usage["input_tokens"])
	}
	out, okOut := number(usage["completion_tokens"])
	if !okOut {
		out, okOut = number(usage["output_tokens"])
	}
	total, okTotal := number(usage["total_tokens"])
	return in, out, total, okIn && okOut && okTotal
}
func responseModel(value map[string]any) string { model, _ := value["model"].(string); return model }
func responseChoices(value map[string]any) []any {
	choices, _ := value["choices"].([]any)
	return choices
}
func responseMessage(value map[string]any) map[string]any {
	choices := responseChoices(value)
	if len(choices) == 0 {
		return nil
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	return message
}

type basicRequestDetector struct{}

func (basicRequestDetector) Definition() verificationDetectorDefinition {
	return verificationDetectorDefinition{"basic_request", "基础请求", "真伪", 15}
}
func (basicRequestDetector) Run(ctx context.Context, probe *verificationProbe) VerificationCheckResult {
	response, err := probe.request(ctx, basicPayload(probe.model, "Reply with exactly: ok"))
	if err != nil {
		return failedCheck("无法完成基础请求: "+err.Error(), true, nil)
	}
	choices := responseChoices(response.json)
	message := responseMessage(response.json)
	content, _ := message["content"].(string)
	passed := response.status >= 200 && response.status < 300 && len(choices) > 0 && strings.TrimSpace(content) != ""
	evidence := map[string]any{"httpStatus": response.status, "latencyMs": response.latency, "responseModel": responseModel(response.json), "hasChoices": len(choices) > 0}
	if passed {
		return passedCheck("基础请求返回了可解析的 OpenAI Chat Completions 响应。", evidence)
	}
	return failedCheck("基础请求未返回有效的 choices.message.content。", true, evidence)
}

type modelConsistencyDetector struct{}

func (modelConsistencyDetector) Definition() verificationDetectorDefinition {
	return verificationDetectorDefinition{"model_consistency", "model 字段一致性", "协议", 15}
}
func (modelConsistencyDetector) Run(ctx context.Context, probe *verificationProbe) VerificationCheckResult {
	models := []string{}
	statuses := []int{}
	for index := 0; index < 2; index++ {
		response, err := probe.request(ctx, basicPayload(probe.model, "Return the word ok."))
		if err != nil {
			return failedCheck("模型一致性请求失败: "+err.Error(), false, nil)
		}
		statuses = append(statuses, response.status)
		models = append(models, responseModel(response.json))
	}
	exact := len(models) == 2 && models[0] == probe.model && models[1] == probe.model
	stable := len(models) == 2 && models[0] != "" && models[0] == models[1]
	score := 0.0
	if stable {
		score = 50
	}
	if exact {
		score = 100
	}
	evidence := map[string]any{"expectedModel": probe.model, "responseModels": models, "httpStatuses": statuses}
	if exact {
		return passedCheck("连续请求的 response.model 与渠道映射一致。", evidence)
	}
	result := failedCheck("response.model 与渠道映射不一致或多次响应不稳定。", false, evidence)
	result.Score = score
	return result
}

type streamIntegrityDetector struct{}

func (streamIntegrityDetector) Definition() verificationDetectorDefinition {
	return verificationDetectorDefinition{"stream_integrity", "流式协议完整性", "协议", 15}
}
func (streamIntegrityDetector) Run(ctx context.Context, probe *verificationProbe) VerificationCheckResult {
	prompt := "Reply with exactly: streamed"
	nonStream, err := probe.request(ctx, basicPayload(probe.model, prompt))
	if err != nil {
		return failedCheck("非流式对照请求失败: "+err.Error(), false, nil)
	}
	response, err := probe.stream(ctx, basicPayload(probe.model, prompt))
	if err != nil {
		return failedCheck("流式请求失败: "+err.Error(), false, nil)
	}
	nonStreamMessage := responseMessage(nonStream.json)
	nonStreamContent, _ := nonStreamMessage["content"].(string)
	modelConsistent := responseModel(nonStream.json) == probe.model && response.responseModel == probe.model
	passed := nonStream.status >= 200 && nonStream.status < 300 && strings.TrimSpace(nonStreamContent) != "" && response.status >= 200 && response.status < 300 && strings.Contains(strings.ToLower(response.contentType), "text/event-stream") && response.chunkCount > 0 && response.firstToken && response.terminal && modelConsistent
	evidence := map[string]any{"nonStreamStatus": nonStream.status, "streamStatus": response.status, "contentType": response.contentType, "chunkCount": response.chunkCount, "firstToken": response.firstToken, "terminal": response.terminal, "nonStreamModel": responseModel(nonStream.json), "streamModel": response.responseModel, "responseIdPresent": response.responseID != "", "nonStreamOutputPresent": strings.TrimSpace(nonStreamContent) != ""}
	if passed {
		return passedCheck("同一提示词的流式与非流式响应均可解析，模型字段和输出阶段一致。", evidence)
	}
	return failedCheck("流式与非流式响应在模型、SSE、输出或终止阶段存在差异。", false, evidence)
}

type usageBillingDetector struct{}

func (usageBillingDetector) Definition() verificationDetectorDefinition {
	return verificationDetectorDefinition{"usage_billing", "usage 计费合理性", "计费", 20}
}
func (usageBillingDetector) Run(ctx context.Context, probe *verificationProbe) VerificationCheckResult {
	short, err := probe.request(ctx, basicPayload(probe.model, "Reply with exactly: ok"))
	if err != nil {
		return failedCheck("短请求失败: "+err.Error(), false, nil)
	}
	longPrompt := "Reply with exactly: ok. Reference text: " + strings.Repeat("apple ", 80)
	long, err := probe.request(ctx, basicPayload(probe.model, longPrompt))
	if err != nil {
		return failedCheck("长请求失败: "+err.Error(), false, nil)
	}
	streamPayload := basicPayload(probe.model, "Reply with exactly: ok")
	streamPayload["stream"] = true
	streamPayload["stream_options"] = map[string]any{"include_usage": true}
	streamed, streamErr := probe.stream(ctx, streamPayload)
	shortIn, shortOut, shortTotal, shortOK := usageNumbers(responseUsage(short.json))
	longIn, _, longTotal, longOK := usageNumbers(responseUsage(long.json))
	arithmetic := shortOK && shortIn+shortOut == shortTotal && longOK && longIn <= longTotal
	delta := longIn - shortIn
	expected := int64(0)
	if probe.estimator != nil {
		expected = probe.estimator.EstimateText(longPrompt) - probe.estimator.EstimateText("Reply with exactly: ok")
	}
	deltaOK := delta > 0 && (expected == 0 || (delta >= expected/3 && delta <= expected*3))
	streamIn, streamOut, streamTotal, streamOK := usageNumbers(streamed.usage)
	streamCompatible := streamErr == nil && streamOK && streamIn >= 0 && streamOut > 0 && streamTotal == streamIn+streamOut && (streamIn-shortIn <= 2 && shortIn-streamIn <= 2)
	passedCount := 0
	if arithmetic {
		passedCount++
	}
	if deltaOK {
		passedCount++
	}
	if streamCompatible {
		passedCount++
	}
	score := float64(passedCount) / 3 * 100
	evidence := map[string]any{"shortUsage": responseUsage(short.json), "longUsage": responseUsage(long.json), "streamUsage": streamed.usage, "promptTokenDelta": delta, "estimatedPromptDelta": expected, "streamError": errorString(streamErr)}
	result := failedCheck("usage 字段存在性、自洽性或流式一致性存在问题。", false, evidence)
	result.Score = score
	if score >= 66.6 {
		result.Status = "passed"
		result.Summary = "usage 字段基本自洽，但仍有部分交叉校验未通过。"
		result.Critical = false
	}
	if score == 100 {
		result.Summary = "usage 加法、长文本增量和流式统计均通过校验。"
	}
	return result
}

type toolCallsDetector struct{}

func (toolCallsDetector) Definition() verificationDetectorDefinition {
	return verificationDetectorDefinition{"tool_calls", "Tool Calls", "能力", 20}
}
func (toolCallsDetector) Run(ctx context.Context, probe *verificationProbe) VerificationCheckResult {
	payload := basicPayload(probe.model, "Call the verification_echo function with value ok.")
	payload["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "verification_echo", "description": "Echo a verification value.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}, "additionalProperties": false}}}}
	payload["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "verification_echo"}}
	response, err := probe.request(ctx, payload)
	if err != nil {
		return failedCheck("Tool Calls 请求失败: "+err.Error(), false, nil)
	}
	message := responseMessage(response.json)
	calls, _ := message["tool_calls"].([]any)
	valid := false
	callID := ""
	functionName := ""
	if len(calls) > 0 {
		call, _ := calls[0].(map[string]any)
		callID, _ = call["id"].(string)
		function, _ := call["function"].(map[string]any)
		functionName, _ = function["name"].(string)
		valid = strings.HasPrefix(callID, "call_") && functionName == "verification_echo"
	}
	evidence := map[string]any{"httpStatus": response.status, "toolCallCount": len(calls), "callIdPrefixValid": strings.HasPrefix(callID, "call_"), "functionName": functionName}
	if valid {
		return passedCheck("上游返回了符合 OpenAI 结构的 tool_calls。", evidence)
	}
	return failedCheck("未返回符合要求的 tool_calls 结构。", false, evidence)
}

type structuredOutputDetector struct{}

func (structuredOutputDetector) Definition() verificationDetectorDefinition {
	return verificationDetectorDefinition{"structured_output", "Structured Output", "能力", 15}
}
func (structuredOutputDetector) Run(ctx context.Context, probe *verificationProbe) VerificationCheckResult {
	payload := basicPayload(probe.model, "Return a JSON object with ok=true.")
	payload["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "verification_result", "strict": true, "schema": map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []string{"ok"}, "additionalProperties": false}}}
	response, err := probe.request(ctx, payload)
	if err != nil {
		return failedCheck("Structured Output 请求失败: "+err.Error(), false, nil)
	}
	message := responseMessage(response.json)
	content, _ := message["content"].(string)
	var value map[string]any
	parseErr := json.Unmarshal([]byte(content), &value)
	valid := response.status >= 200 && response.status < 300 && parseErr == nil && value["ok"] == true
	evidence := map[string]any{"httpStatus": response.status, "jsonParsed": parseErr == nil, "okField": value["ok"]}
	if valid {
		return passedCheck("上游返回了符合 strict JSON Schema 的结构化输出。", evidence)
	}
	return failedCheck("未返回符合 strict JSON Schema 的结构化输出。", false, evidence)
}

func passedCheck(summary string, evidence map[string]any) VerificationCheckResult {
	return VerificationCheckResult{Status: "passed", Score: 100, Summary: summary, Evidence: evidence}
}
func failedCheck(summary string, critical bool, evidence map[string]any) VerificationCheckResult {
	return VerificationCheckResult{Status: "failed", Score: 0, Summary: summary, Critical: critical, Evidence: evidence}
}
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
