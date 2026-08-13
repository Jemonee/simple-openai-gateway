package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	verificationWorkers          = 8
	verificationMaxAttempts      = 3
	verificationMaxBodyBytes     = 4 << 20
	verificationAttemptBodyBytes = 128 << 10
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
	OutcomeCode         string                    `json:"outcomeCode"`
	Title               string                    `json:"title"`
	Summary             string                    `json:"summary"`
	FingerprintModel    string                    `json:"fingerprintModel,omitempty"`
	FingerprintMatch    map[string]float64        `json:"fingerprintMatch,omitempty"`
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
	ID                  string     `json:"id"`
	ChannelID           uint64     `json:"channelId"`
	ChannelName         string     `json:"channelName"`
	ModelID             uint64     `json:"modelId"`
	PublicModel         string     `json:"publicModel"`
	UpstreamModel       string     `json:"upstreamModel"`
	Mode                string     `json:"mode"`
	Status              string     `json:"status"`
	PlannedCount        int64      `json:"plannedCount"`
	CompletedCount      int64      `json:"completedCount"`
	Score               float64    `json:"score"`
	Verdict             string     `json:"verdict"`
	Summary             string     `json:"summary"`
	RequestCount        int64      `json:"requestCount"`
	SuccessCount        int64      `json:"successCount"`
	InputTokens         int64      `json:"inputTokens"`
	OutputTokens        int64      `json:"outputTokens"`
	CachedTokens        int64      `json:"cachedTokens"`
	CacheWriteTokens    int64      `json:"cacheWriteTokens"`
	EstimatedCostMicros int64      `json:"estimatedCostMicros"`
	UpstreamCostMicros  int64      `json:"upstreamCostMicros"`
	ErrorMessage        string     `json:"errorMessage"`
	StopRequestedAt     *time.Time `json:"stopRequestedAt"`
	StartedAt           time.Time  `json:"startedAt"`
	FinishedAt          time.Time  `json:"finishedAt"`
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
	JobID                 string    `json:"jobId"`
	AttemptNo             int       `json:"attemptNo"`
	DetectorName          string    `json:"detectorName"`
	ProbeID               string    `json:"probeId"`
	RequestFormat         string    `json:"requestFormat"`
	ContextMode           string    `json:"contextMode"`
	Effort                string    `json:"effort"`
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

type VerificationService struct {
	store      *Store
	estimator  *TokenEstimator
	client     *http.Client
	mu         sync.Mutex
	runs       map[string]context.CancelFunc
	assetsOnce sync.Once
	assetsDir  string
	assetsErr  error
}

func NewVerificationService(store *Store, estimator *TokenEstimator) *VerificationService {
	service := &VerificationService{store: store, estimator: estimator, client: &http.Client{Timeout: 310 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, runs: map[string]context.CancelFunc{}}
	go service.resumeRunningSessions()
	return service
}

func (s *VerificationService) Start(ctx context.Context, input VerificationRequest) (*VerificationSessionView, error) {
	mode, channel, mapping, model, claimedModel, err := s.validateRequest(ctx, input)
	if err != nil {
		return nil, err
	}
	jobs, err := buildVerificationJobs(mode)
	if err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	session := VerificationSession{ID: uuid.NewString(), ChannelID: channel.ID, ChannelName: channel.Name, ModelID: model.ID, PublicModel: model.Name, UpstreamModel: mapping.UpstreamModel, Mode: mode, Status: "running", PlannedCount: int64(len(jobs)), StartedAt: started, Summary: fmt.Sprintf("%s 指纹检测已启动，共 %d 个逻辑请求。", verificationModeLabel(mode), len(jobs))}
	err = s.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		rows := make([]VerificationJob, 0, len(jobs))
		for index, manifest := range jobs {
			jobID, err := verificationJobID(manifest)
			if err != nil {
				return err
			}
			// A session prefix keeps independently generated coverage values and repeated runs distinct.
			jobID = session.ID + ":" + jobID[:24]
			encoded, err := json.Marshal(manifest)
			if err != nil {
				return err
			}
			rows = append(rows, VerificationJob{ID: jobID, SessionID: session.ID, Sequence: index + 1, ProbeID: manifest.ProbeID, RequestFormat: manifest.RequestFormat, ContextMode: manifest.ContextMode, Effort: manifest.Effort, Status: "pending", ManifestJSON: string(encoded), CreatedAt: started, UpdatedAt: started})
		}
		return tx.Create(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	s.startRun(session.ID, claimedModel)
	view := verificationSessionView(session)
	return &view, nil
}

func (s *VerificationService) validateRequest(ctx context.Context, input VerificationRequest) (string, Channel, ChannelModel, GatewayModel, string, error) {
	if input.ChannelID == 0 || input.ModelID == 0 {
		return "", Channel{}, ChannelModel{}, GatewayModel{}, "", errors.New("channelId and modelId are required")
	}
	mode, err := normalizeVerificationMode(input.Mode)
	if err != nil {
		return "", Channel{}, ChannelModel{}, GatewayModel{}, "", err
	}
	var channel Channel
	if err := s.store.db.WithContext(ctx).First(&channel, input.ChannelID).Error; err != nil {
		return "", channel, ChannelModel{}, GatewayModel{}, "", err
	}
	var mapping ChannelModel
	if err := s.store.db.WithContext(ctx).Where("channel_id = ? AND model_id = ? AND enabled = ?", input.ChannelID, input.ModelID, true).First(&mapping).Error; err != nil {
		return "", channel, mapping, GatewayModel{}, "", err
	}
	var model GatewayModel
	if err := s.store.db.WithContext(ctx).First(&model, input.ModelID).Error; err != nil {
		return "", channel, mapping, model, "", err
	}
	claimed := strings.ToLower(strings.TrimSpace(model.Name))
	if !containsString(verificationTargetModels, claimed) {
		claimed = strings.ToLower(strings.TrimSpace(mapping.UpstreamModel))
	}
	if !containsString(verificationTargetModels, claimed) {
		return "", channel, mapping, model, "", errors.New("模型指纹检测仅支持申报为 gpt-5.6-sol、gpt-5.6-terra 或 gpt-5.6-luna 的映射")
	}
	if _, err := normalizeVerificationBaseURL(channel.BaseURL); err != nil {
		return "", channel, mapping, model, "", err
	}
	return mode, channel, mapping, model, claimed, nil
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func (s *VerificationService) resumeRunningSessions() {
	var sessions []VerificationSession
	if err := s.store.db.Where("status IN ? AND stop_requested_at IS NOT NULL", []string{"running", "stopping"}).Find(&sessions).Error; err == nil {
		for _, session := range sessions {
			claimed := strings.ToLower(strings.TrimSpace(session.PublicModel))
			if !containsString(verificationTargetModels, claimed) {
				claimed = strings.ToLower(strings.TrimSpace(session.UpstreamModel))
			}
			if containsString(verificationTargetModels, claimed) {
				if err := s.cancelRemainingJobsAndFinalize(session.ID, claimed); err != nil {
					s.failSession(session.ID, err)
				}
			}
		}
	}
	sessions = nil
	if err := s.store.db.Where("status = ? AND stop_requested_at IS NULL", "running").Find(&sessions).Error; err != nil {
		return
	}
	for _, session := range sessions {
		claimed := strings.ToLower(strings.TrimSpace(session.PublicModel))
		if !containsString(verificationTargetModels, claimed) {
			claimed = strings.ToLower(strings.TrimSpace(session.UpstreamModel))
		}
		if containsString(verificationTargetModels, claimed) {
			s.startRun(session.ID, claimed)
		}
	}
}

func (s *VerificationService) startRun(sessionID, claimedModel string) {
	s.mu.Lock()
	if _, exists := s.runs[sessionID]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.runs[sessionID] = cancel
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); delete(s.runs, sessionID); s.mu.Unlock() }()
		s.runSession(ctx, sessionID, claimedModel)
	}()
}

func (s *VerificationService) runSession(ctx context.Context, sessionID, claimedModel string) {
	var session VerificationSession
	if err := s.store.db.First(&session, "id = ?", sessionID).Error; err != nil {
		return
	}
	var channel Channel
	var mapping ChannelModel
	if err := s.store.db.First(&channel, session.ChannelID).Error; err != nil {
		s.failSession(sessionID, err)
		return
	}
	if err := s.store.db.Where("channel_id = ? AND model_id = ?", session.ChannelID, session.ModelID).First(&mapping).Error; err != nil {
		s.failSession(sessionID, err)
		return
	}
	apiKey, err := s.store.secretBox.Decrypt(channel.APIKeyCipher)
	if err != nil {
		s.failSession(sessionID, err)
		return
	}
	defer func() { apiKey = "" }()
	baseURL, err := normalizeVerificationBaseURL(channel.BaseURL)
	if err != nil {
		s.failSession(sessionID, err)
		return
	}

	var jobs []VerificationJob
	if err := s.store.db.Where("session_id = ? AND status IN ?", sessionID, []string{"pending", "running"}).Order("sequence asc").Find(&jobs).Error; err != nil {
		s.failSession(sessionID, err)
		return
	}
	for index := range jobs {
		if jobs[index].Status == "running" {
			jobs[index].Status = "pending"
			_ = s.store.db.Model(&VerificationJob{}).Where("id = ?", jobs[index].ID).Update("status", "pending").Error
		}
	}
	jobCh := make(chan VerificationJob)
	var wg sync.WaitGroup
	for range verificationWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				if ctx.Err() != nil || s.stopRequested(sessionID) {
					return
				}
				if err := s.runJob(ctx, session, mapping, baseURL, apiKey, claimedModel, job); err != nil && errors.Is(err, errVerificationAuth) {
					s.requestStop(sessionID, "认证或权限失败，已停止后续请求")
					return
				}
			}
		}()
	}
sendLoop:
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			break sendLoop
		case jobCh <- job:
		}
		if s.stopRequested(sessionID) {
			break
		}
	}
	close(jobCh)
	wg.Wait()
	if s.stopRequested(sessionID) {
		if err := s.cancelRemainingJobsAndFinalize(sessionID, claimedModel); err != nil {
			s.failSession(sessionID, err)
		}
		return
	}
	if err := s.finalizeSession(sessionID, claimedModel); err != nil {
		s.failSession(sessionID, err)
	}
}

func (s *VerificationService) cancelRemainingJobsAndFinalize(sessionID, claimedModel string) error {
	cancelled := verificationJobResult{Status: "cancelled", ErrorCategory: "cancelled", ErrorMessage: "用户已停止检测"}
	encoded, _ := json.Marshal(cancelled)
	if err := s.store.db.Model(&VerificationJob{}).
		Where("session_id = ? AND status IN ?", sessionID, []string{"pending", "running"}).
		Updates(map[string]any{"status": "cancelled", "result_json": string(encoded), "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	return s.finalizeSession(sessionID, claimedModel)
}

var errVerificationAuth = errors.New("verification authentication failed")

func (s *VerificationService) runJob(ctx context.Context, session VerificationSession, mapping ChannelModel, baseURL, apiKey, claimedModel string, job VerificationJob) error {
	var manifest verificationJobManifest
	if err := json.Unmarshal([]byte(job.ManifestJSON), &manifest); err != nil {
		return s.finishJobError(job, "manifest_invalid", err)
	}
	_ = s.store.db.Model(&VerificationJob{}).Where("id = ?", job.ID).Updates(map[string]any{"status": "running", "updated_at": time.Now().UTC()}).Error
	for attemptNo := job.AttemptsSent + 1; attemptNo <= verificationMaxAttempts; attemptNo++ {
		if ctx.Err() != nil || s.stopRequested(session.ID) {
			return s.cancelJob(job)
		}
		result, requestBody, responseBody, meta, err := s.performProbe(ctx, baseURL, apiKey, session.UpstreamModel, job.ID, manifest)
		s.persistAttempt(session.ID, mapping, job, manifest, attemptNo, requestBody, responseBody, meta, err)
		_ = s.store.db.Model(&VerificationJob{}).Where("id = ?", job.ID).Updates(map[string]any{"attempts_sent": attemptNo, "updated_at": time.Now().UTC()}).Error
		if err == nil {
			classified := classifyVerificationResult(manifest, claimedModel, result.Answer)
			classified.HTTPStatus = meta.Status
			encoded, _ := json.Marshal(classified)
			return s.store.db.Model(&VerificationJob{}).Where("id = ?", job.ID).Updates(map[string]any{"status": "succeeded", "result_json": string(encoded), "updated_at": time.Now().UTC()}).Error
		}
		if meta.Status == http.StatusUnauthorized || meta.Status == http.StatusForbidden {
			_ = s.finishJobError(job, "authentication_failed", err)
			return errVerificationAuth
		}
		if !retryableVerificationError(meta.Status, err) || attemptNo == verificationMaxAttempts {
			return s.finishJobError(job, verificationErrorCategory(meta.Status, err), err)
		}
		select {
		case <-ctx.Done():
			return s.cancelJob(job)
		case <-time.After(time.Duration(250*(1<<(attemptNo-1))) * time.Millisecond):
		}
	}
	return nil
}

type verificationTransportResult struct {
	Answer         string
	Usage          Usage
	UpstreamCost   int64
	UpstreamCostOK bool
}
type verificationTransportMeta struct {
	Status        int
	ContentType   string
	LatencyMS     int64
	Path          string
	Stream        bool
	RequestFormat string
}

func (s *VerificationService) performProbe(ctx context.Context, baseURL, apiKey, model, jobID string, manifest verificationJobManifest) (verificationTransportResult, []byte, []byte, verificationTransportMeta, error) {
	if manifest.RequestFormat == "native_codex" {
		return s.performNativeProbe(ctx, baseURL, apiKey, model, jobID, manifest)
	}
	return s.performNormalProbe(ctx, baseURL, apiKey, model, jobID, manifest)
}

func (s *VerificationService) performNormalProbe(ctx context.Context, baseURL, apiKey, model, jobID string, manifest verificationJobManifest) (verificationTransportResult, []byte, []byte, verificationTransportMeta, error) {
	input := make([]any, 0, len(manifest.Messages)+128)
	for index, message := range manifest.Messages {
		if index == len(manifest.Messages)-1 && manifest.ContextMode == "fixed_32k_history" {
			history, err := loadVerificationHistory()
			if err != nil {
				return verificationTransportResult{}, nil, nil, verificationTransportMeta{}, err
			}
			input = append(input, history...)
		}
		input = append(input, map[string]any{"role": message.Role, "content": message.Content})
	}
	payload := map[string]any{"model": model, "input": input, "reasoning": map[string]any{"effort": manifest.Effort}, "include": []string{"reasoning.encrypted_content"}, "store": false, "stream": true, "prompt_cache_key": uuid.NewString()}
	body, err := json.Marshal(payload)
	if err != nil {
		return verificationTransportResult{}, nil, nil, verificationTransportMeta{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return verificationTransportResult{}, body, nil, verificationTransportMeta{}, err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("User-Agent", "simple-openai-gateway/gpt56-detector-4.1.0")
	started := time.Now()
	response, err := s.client.Do(request)
	meta := verificationTransportMeta{Path: "/responses", Stream: true, RequestFormat: "normal", LatencyMS: time.Since(started).Milliseconds()}
	if err != nil {
		return verificationTransportResult{}, body, nil, meta, err
	}
	defer response.Body.Close()
	meta.Status = response.StatusCode
	meta.ContentType = response.Header.Get("Content-Type")
	raw, err := readVerificationSSE(response.Body)
	meta.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		return verificationTransportResult{}, body, raw, meta, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return verificationTransportResult{}, body, raw, meta, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	parsed, err := parseResponsesSSE(raw)
	if err != nil {
		return verificationTransportResult{}, body, raw, meta, err
	}
	parsed.UpstreamCost, parsed.UpstreamCostOK = ParseUpstreamCostMicros(raw)
	return parsed, body, raw, meta, nil
}

func readVerificationSSE(reader io.Reader) ([]byte, error) {
	buffered := bufio.NewReader(reader)
	raw := make([]byte, 0, 4096)
	dataLines := []string{}
	consumeEvent := func() (bool, error) {
		if len(dataLines) == 0 {
			return false, nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if payload == "" || payload == "[DONE]" {
			return false, nil
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return false, errors.New("invalid JSON in Responses SSE")
		}
		typeName, _ := event["type"].(string)
		return typeName == "response.completed" || typeName == "response.failed" || typeName == "response.incomplete", nil
	}
	for {
		line, err := buffered.ReadString('\n')
		if len(raw)+len(line) > verificationMaxBodyBytes {
			remaining := int(verificationMaxBodyBytes) - len(raw)
			if remaining > 0 {
				raw = append(raw, line[:remaining]...)
			}
			return raw, errors.New("verification response exceeded 4 MiB")
		}
		raw = append(raw, line...)
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			terminal, consumeErr := consumeEvent()
			if consumeErr != nil {
				return raw, consumeErr
			}
			if terminal {
				return raw, nil
			}
		} else if strings.HasPrefix(trimmed, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return raw, err
			}
			terminal, consumeErr := consumeEvent()
			if consumeErr != nil {
				return raw, consumeErr
			}
			if terminal || len(raw) > 0 {
				return raw, nil
			}
			return raw, io.EOF
		}
	}
}

func loadVerificationHistory() ([]any, error) {
	data, err := verificationAssets.ReadFile("verification_assets/fixed_32k_history.json")
	if err != nil {
		return nil, err
	}
	var rows []struct{ Role, Text string }
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	items := make([]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{"role": row.Role, "content": row.Text})
	}
	return items, nil
}

func parseResponsesSSE(raw []byte) (verificationTransportResult, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), verificationMaxBodyBytes)
	var deltas strings.Builder
	terminal := ""
	terminalAnswer := ""
	usage := Usage{}
	terminalSeen := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return verificationTransportResult{}, errors.New("invalid JSON in Responses SSE")
		}
		typeName, _ := event["type"].(string)
		if typeName == "response.output_text.delta" {
			if delta, ok := event["delta"].(string); ok {
				deltas.WriteString(delta)
			}
		}
		if typeName == "response.completed" || typeName == "response.failed" || typeName == "response.incomplete" {
			terminal, terminalSeen = typeName, true
			response, _ := event["response"].(map[string]any)
			terminalAnswer = responseOutputText(response)
			usage = responsesUsage(response)
		}
	}
	if err := scanner.Err(); err != nil {
		return verificationTransportResult{}, err
	}
	if !terminalSeen {
		return verificationTransportResult{}, errors.New("Responses stream ended without a terminal event")
	}
	if terminal != "response.completed" {
		return verificationTransportResult{}, fmt.Errorf("Responses stream ended with %s", terminal)
	}
	deltaAnswer := strings.TrimSpace(deltas.String())
	terminalAnswer = strings.TrimSpace(terminalAnswer)
	if terminalAnswer != "" && deltaAnswer != "" && terminalAnswer != deltaAnswer {
		return verificationTransportResult{}, errors.New("SSE terminal output conflicts with streamed text")
	}
	if terminalAnswer == "" {
		terminalAnswer = deltaAnswer
	}
	if terminalAnswer == "" {
		return verificationTransportResult{}, errors.New("Responses stream completed without output text")
	}
	return verificationTransportResult{Answer: terminalAnswer, Usage: usage}, nil
}

func responseOutputText(response map[string]any) string {
	if value, ok := response["output_text"].(string); ok {
		return value
	}
	var text strings.Builder
	output, _ := response["output"].([]any)
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		content, _ := item["content"].([]any)
		for _, rawPart := range content {
			part, _ := rawPart.(map[string]any)
			if part["type"] == "output_text" {
				if value, ok := part["text"].(string); ok {
					text.WriteString(value)
				}
			}
		}
	}
	return text.String()
}

func responsesUsage(response map[string]any) Usage {
	value, _ := response["usage"].(map[string]any)
	usage := Usage{Source: "upstream"}
	usage.InputTokens, _ = number(value["input_tokens"])
	usage.OutputTokens, _ = number(value["output_tokens"])
	if details, ok := value["input_tokens_details"].(map[string]any); ok {
		usage.CachedTokens, _ = number(details["cached_tokens"])
	}
	return usage
}

func (s *VerificationService) materializeNativeAssets() (string, error) {
	s.assetsOnce.Do(func() {
		dir, err := os.MkdirTemp("", "simple-openai-gateway-gpt56-")
		if err != nil {
			s.assetsErr = err
			return
		}
		for _, name := range []string{"codex-native-transport.mjs", "fixed_32k_history.json", "native-0.147.0.raw"} {
			data, err := verificationAssets.ReadFile("verification_assets/" + name)
			if err != nil {
				s.assetsErr = err
				return
			}
			if name == "native-0.147.0.raw" && !bytes.Contains(data, []byte("\r\n\r\n")) {
				data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				s.assetsErr = err
				return
			}
		}
		s.assetsDir = dir
	})
	return s.assetsDir, s.assetsErr
}

func (s *VerificationService) performNativeProbe(ctx context.Context, baseURL, apiKey, model, jobID string, manifest verificationJobManifest) (verificationTransportResult, []byte, []byte, verificationTransportMeta, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return verificationTransportResult{}, nil, nil, verificationTransportMeta{Path: "/responses", Stream: true, RequestFormat: "native_codex"}, errors.New("Node.js is required for Native Codex verification")
	}
	dir, err := s.materializeNativeAssets()
	if err != nil {
		return verificationTransportResult{}, nil, nil, verificationTransportMeta{}, err
	}
	messages, _ := json.Marshal(manifest.Messages)
	args := []string{filepath.Join(dir, "codex-native-transport.mjs"), "send-native", "--profile", filepath.Join(dir, "native-0.147.0.raw"), "--url", baseURL + "/responses", "--model", model, "--timeout", "300000", "--input-json", "true", "--effort", manifest.Effort, "--capture-sanitized-request", "false"}
	if manifest.ContextMode == "fixed_32k_history" {
		args = append(args, "--history-file", filepath.Join(dir, "fixed_32k_history.json"))
	}
	cmd := exec.CommandContext(ctx, node, args...)
	cmd.Stdin = bytes.NewReader(messages)
	cmd.Env = verificationCommandEnv(apiKey)
	started := time.Now()
	output, err := cmd.Output()
	meta := verificationTransportMeta{Path: "/responses", Stream: true, RequestFormat: "native_codex", LatencyMS: time.Since(started).Milliseconds()}
	if err != nil {
		return verificationTransportResult{}, messages, output, meta, fmt.Errorf("native Codex transport failed: %w", err)
	}
	var report struct {
		Status  int                 `json:"status"`
		Body    string              `json:"body"`
		Headers map[string][]string `json:"headers"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		return verificationTransportResult{}, messages, output, meta, err
	}
	meta.Status = report.Status
	raw := []byte(report.Body)
	if values := report.Headers["content-type"]; len(values) > 0 {
		meta.ContentType = values[0]
	}
	if report.Status < 200 || report.Status >= 300 {
		return verificationTransportResult{}, messages, raw, meta, fmt.Errorf("upstream returned HTTP %d", report.Status)
	}
	parsed, err := parseResponsesSSE(raw)
	if err != nil {
		return verificationTransportResult{}, messages, raw, meta, err
	}
	parsed.UpstreamCost, parsed.UpstreamCostOK = ParseUpstreamCostMicros(raw)
	return parsed, messages, raw, meta, nil
}

func verificationCommandEnv(apiKey string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GPT56_NATIVE_AUTHORIZATION=") {
			env = append(env, entry)
		}
	}
	return append(env, "GPT56_NATIVE_AUTHORIZATION="+apiKey)
}

func retryableVerificationError(status int, err error) bool {
	if err == nil {
		return false
	}
	return status == 0 || status == 408 || status == 429 || status >= 500
}
func verificationErrorCategory(status int, err error) string {
	switch {
	case status == 401 || status == 403:
		return "authentication_failed"
	case status == 404:
		return "not_found"
	case status == 429:
		return "rate_limited"
	case status >= 500:
		return "upstream_error"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "transport_or_stream_error"
	}
}

func (s *VerificationService) persistAttempt(sessionID string, mapping ChannelModel, job VerificationJob, manifest verificationJobManifest, attemptNo int, requestBody, responseBody []byte, meta verificationTransportMeta, requestErr error) {
	usage := Usage{}
	upstreamCost := int64(0)
	upstreamOK := false
	if requestErr == nil {
		if parsed, err := parseResponsesSSE(responseBody); err == nil {
			usage = parsed.Usage
		}
		upstreamCost, upstreamOK = ParseUpstreamCostMicros(responseBody)
	}
	inputTokens, outputTokens := usage.InputTokens, usage.OutputTokens
	if inputTokens == 0 && s.estimator != nil {
		inputTokens = s.estimator.EstimateJSON(requestBody)
	}
	if outputTokens == 0 && s.estimator != nil {
		outputTokens = s.estimator.EstimateJSON(responseBody)
	}
	usage.InputTokens = inputTokens
	usage.OutputTokens = outputTokens
	estimated := CalculateCostMicros(mapping, usage)
	costSource := CostSourceFallback
	outcome := RelayOutcomeSuccess
	if requestErr != nil || meta.Status < 200 || meta.Status >= 300 {
		estimated = 0
		upstreamCost = 0
		costSource = CostSourceFailedZero
		outcome = RelayOutcomeFailed
	} else if upstreamOK {
		costSource = CostSourceUpstream
	} else {
		upstreamCost = estimated
	}
	reqText, reqTrunc := boundVerificationBody(requestBody)
	respText, respTrunc := boundVerificationBody(responseBody)
	now := time.Now().UTC()
	attempt := VerificationAttempt{ID: uuid.NewString(), SessionID: sessionID, Sequence: job.Sequence, JobID: job.ID, AttemptNo: attemptNo, DetectorName: manifest.ProbeID, ProbeID: manifest.ProbeID, RequestFormat: manifest.RequestFormat, ContextMode: manifest.ContextMode, Effort: manifest.Effort, Method: http.MethodPost, Path: "/responses", Stream: true, RequestBody: reqText, RequestBodyTruncated: reqTrunc, ResponseBody: respText, ResponseBodyTruncated: respTrunc, ContentType: meta.ContentType, StatusCode: meta.Status, Outcome: outcome, ErrorMessage: truncateVerificationText(errorString(requestErr), 512), InputTokens: inputTokens, OutputTokens: outputTokens, CachedTokens: usage.CachedTokens, CacheWriteTokens: usage.CacheWriteTokens, EstimatedCostMicros: estimated, UpstreamCostMicros: upstreamCost, CostSource: costSource, UsageSource: usage.Source, LatencyMS: meta.LatencyMS, StartedAt: now.Add(-time.Duration(meta.LatencyMS) * time.Millisecond), FinishedAt: now}
	_ = s.store.db.Create(&attempt).Error
}

func (s *VerificationService) finishJobError(job VerificationJob, category string, err error) error {
	result := verificationJobResult{Status: "error", ErrorCategory: category, ErrorMessage: truncateVerificationText(errorString(err), 512)}
	encoded, _ := json.Marshal(result)
	return s.store.db.Model(&VerificationJob{}).Where("id = ?", job.ID).Updates(map[string]any{"status": "error", "result_json": string(encoded), "updated_at": time.Now().UTC()}).Error
}
func (s *VerificationService) cancelJob(job VerificationJob) error {
	result := verificationJobResult{Status: "cancelled", ErrorCategory: "cancelled", ErrorMessage: "用户已停止检测"}
	encoded, _ := json.Marshal(result)
	return s.store.db.Model(&VerificationJob{}).Where("id = ?", job.ID).Updates(map[string]any{"status": "cancelled", "result_json": string(encoded), "updated_at": time.Now().UTC()}).Error
}

func (s *VerificationService) stopRequested(sessionID string) bool {
	var count int64
	s.store.db.Model(&VerificationSession{}).Where("id = ? AND stop_requested_at IS NOT NULL", sessionID).Count(&count)
	return count > 0
}
func (s *VerificationService) requestStop(sessionID, message string) {
	now := time.Now().UTC()
	_ = s.store.db.Model(&VerificationSession{}).Where("id = ?", sessionID).Updates(map[string]any{"stop_requested_at": &now, "error_message": message}).Error
	s.mu.Lock()
	cancel := s.runs[sessionID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func (s *VerificationService) Stop(ctx context.Context, sessionID string) (*VerificationSessionView, error) {
	var session VerificationSession
	if err := s.store.db.WithContext(ctx).First(&session, "id = ?", strings.TrimSpace(sessionID)).Error; err != nil {
		return nil, err
	}
	if session.Status != "running" && session.Status != "stopping" {
		return nil, fmt.Errorf("verification session is already %s", session.Status)
	}
	if session.Status == "stopping" {
		view := verificationSessionView(session)
		return &view, nil
	}
	s.requestStop(session.ID, "用户已请求停止检测")
	session.StopRequestedAt = verificationTimePtr(time.Now().UTC())
	session.Status = "stopping"
	view := verificationSessionView(session)
	return &view, nil
}
func verificationTimePtr(value time.Time) *time.Time { return &value }

type verificationAggregate struct{ RequestCount, SuccessCount, InputTokens, OutputTokens, CachedTokens, CacheWriteTokens, EstimatedCostMicros, UpstreamCostMicros int64 }

func (s *VerificationService) verificationAggregate(ctx context.Context, sessionID string) (verificationAggregate, error) {
	var a verificationAggregate
	err := s.store.db.WithContext(ctx).Model(&VerificationAttempt{}).Select("COUNT(*) AS request_count, COALESCE(SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END),0) AS success_count, COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(output_tokens),0) AS output_tokens, COALESCE(SUM(cached_tokens),0) AS cached_tokens, COALESCE(SUM(cache_write_tokens),0) AS cache_write_tokens, COALESCE(SUM(estimated_cost_micros),0) AS estimated_cost_micros, COALESCE(SUM(upstream_cost_micros),0) AS upstream_cost_micros").Where("session_id = ?", sessionID).Scan(&a).Error
	return a, err
}

type verificationCombinedReport struct {
	OutcomeCode     string             `json:"outcomeCode"`
	Title           string             `json:"title"`
	Subtitle        string             `json:"subtitle"`
	JuiceState      string             `json:"juiceState"`
	Fingerprint     fingerprintSummary `json:"fingerprintSummary"`
	OutputSummary   map[string]any     `json:"outputIntegritySummary"`
	CoverageSummary map[string]any     `json:"coverageSummary"`
}

func (s *VerificationService) finalizeSession(sessionID, claimedModel string) error {
	var session VerificationSession
	if err := s.store.db.First(&session, "id = ?", sessionID).Error; err != nil {
		return err
	}
	var existing VerificationReport
	if err := s.store.db.Where("session_id = ?", sessionID).First(&existing).Error; err == nil {
		status := "completed"
		if session.StopRequestedAt != nil {
			status = "stopped"
		}
		var completed int64
		if err := s.store.db.Model(&VerificationJob{}).Where("session_id = ? AND status IN ?", sessionID, []string{"succeeded", "error", "cancelled"}).Count(&completed).Error; err != nil {
			return err
		}
		aggregate, err := s.verificationAggregate(context.Background(), sessionID)
		if err != nil {
			return err
		}
		return s.store.db.Model(&VerificationSession{}).Where("id = ?", sessionID).Updates(map[string]any{"status": status, "completed_count": completed, "verdict": existing.Verdict, "summary": existing.Summary, "request_count": aggregate.RequestCount, "success_count": aggregate.SuccessCount, "input_tokens": aggregate.InputTokens, "output_tokens": aggregate.OutputTokens, "cached_tokens": aggregate.CachedTokens, "cache_write_tokens": aggregate.CacheWriteTokens, "estimated_cost_micros": aggregate.EstimatedCostMicros, "upstream_cost_micros": aggregate.UpstreamCostMicros, "finished_at": existing.FinishedAt}).Error
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var jobs []VerificationJob
	if err := s.store.db.Where("session_id = ?", sessionID).Order("sequence asc").Find(&jobs).Error; err != nil {
		return err
	}
	manifests := map[string]verificationJobManifest{}
	results := map[string]verificationJobResult{}
	completed := int64(0)
	for _, job := range jobs {
		var manifest verificationJobManifest
		_ = json.Unmarshal([]byte(job.ManifestJSON), &manifest)
		manifests[job.ID] = manifest
		if job.ResultJSON != "" {
			var result verificationJobResult
			_ = json.Unmarshal([]byte(job.ResultJSON), &result)
			results[job.ID] = result
			completed++
		}
	}
	juiceState, juiceEvidence := summarizeJuice(session.Mode, claimedModel, manifests, results)
	output := summarizeOutput(manifests, results)
	coverage := summarizeCoverage(manifests, results)
	if output["hardAnomaly"] == true || coverage["hardAnomaly"] == true {
		juiceState = "mismatch"
	}
	fingerprint, err := scoreFingerprint(session.Mode, manifests, results)
	if err != nil {
		return err
	}
	outcome, title, subtitle := verificationOutcome(juiceState, fingerprint)
	checks := []VerificationCheckResult{{Name: "juice", Label: "Juice 型号指纹", Category: "确定性证据", Status: verificationLayerStatus(juiceState), Summary: verificationJuiceSummary(juiceState), Evidence: juiceEvidence}, {Name: "output_integrity", Label: "32/48 输出完整性", Category: "确定性证据", Status: boolLayerStatus(output["hardAnomaly"] == true), Summary: "检查固定 32/48 是否被改写为 40 前缀。", Evidence: output}, {Name: "coverage", Label: "Juice 显式覆盖", Category: "确定性证据", Status: boolLayerStatus(coverage["hardAnomaly"] == true), Summary: "检查显式 developer 定义是否被隐藏 Juice 提示覆盖。", Evidence: coverage}, {Name: "fingerprint", Label: "行为指纹", Category: "相对行为证据", Status: fingerprintLayerStatus(fingerprint), Summary: verificationFingerprintSummary(fingerprint), Evidence: map[string]any{"scoringVersion": fingerprint.ScoringVersion, "baselineId": fingerprint.BaselineID, "baselineSha256": fingerprint.BaselineSHA256, "fingerprintModel": fingerprint.Model, "fingerprintMatch": fingerprint.Match, "thresholds": fingerprint.Thresholds, "officialEligible": fingerprint.OfficialEligible, "unclearReasons": fingerprint.UnclearReasons, "cells": fingerprint.Cells, "families": fingerprint.FamilyContributions}}}
	checksJSON, _ := json.Marshal(checks)
	combined := verificationCombinedReport{OutcomeCode: outcome, Title: title, Subtitle: subtitle, JuiceState: juiceState, Fingerprint: fingerprint, OutputSummary: output, CoverageSummary: coverage}
	reportJSON, _ := json.Marshal(combined)
	aggregate, err := s.verificationAggregate(context.Background(), sessionID)
	if err != nil {
		return err
	}
	finished := time.Now().UTC()
	status := "completed"
	if session.StopRequestedAt != nil {
		status = "stopped"
	}
	report := VerificationReport{ID: uuid.NewString(), SessionID: session.ID, ChannelID: session.ChannelID, ChannelName: session.ChannelName, ModelID: session.ModelID, PublicModel: session.PublicModel, UpstreamModel: session.UpstreamModel, Mode: session.Mode, Score: 0, Verdict: outcome, Summary: subtitle, RequestCount: aggregate.RequestCount, InputTokens: aggregate.InputTokens, OutputTokens: aggregate.OutputTokens, EstimatedCostMicros: aggregate.EstimatedCostMicros, UpstreamCostMicros: aggregate.UpstreamCostMicros, ChecksJSON: string(checksJSON), ReportJSON: string(reportJSON), StartedAt: session.StartedAt, FinishedAt: finished}
	return s.store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&report).Error; err != nil {
			return err
		}
		return tx.Model(&VerificationSession{}).Where("id = ?", sessionID).Updates(map[string]any{"status": status, "completed_count": completed, "verdict": outcome, "summary": subtitle, "request_count": aggregate.RequestCount, "success_count": aggregate.SuccessCount, "input_tokens": aggregate.InputTokens, "output_tokens": aggregate.OutputTokens, "cached_tokens": aggregate.CachedTokens, "cache_write_tokens": aggregate.CacheWriteTokens, "estimated_cost_micros": aggregate.EstimatedCostMicros, "upstream_cost_micros": aggregate.UpstreamCostMicros, "finished_at": finished}).Error
	})
}

func summarizeJuice(mode, claimed string, manifests map[string]verificationJobManifest, results map[string]verificationJobResult) (string, map[string]any) {
	effortNeeded := map[string]int{}
	for _, m := range manifests {
		if strings.HasPrefix(m.ProbeID, "juice_") && m.ProbeID != "juice_coverage" {
			if _, ok := effortNeeded[m.Effort]; !ok {
				effortNeeded[m.Effort] = m.Repeat
			}
		}
	}
	counts := map[string]map[string]int{}
	mixedModels := map[string]bool{}
	for id, r := range results {
		m := manifests[id]
		if !strings.HasPrefix(m.ProbeID, "juice_") || m.ProbeID == "juice_coverage" {
			continue
		}
		if counts[m.Effort] == nil {
			counts[m.Effort] = map[string]int{}
		}
		counts[m.Effort][r.Classification]++
		for _, model := range r.MixedModels {
			mixedModels[model] = true
		}
	}
	state := "pass"
	mixed := false
	allUnsuccessful := len(effortNeeded) > 0
	missing := []string{}
	for effort, needed := range effortNeeded {
		c := counts[effort]
		if c["mixed"] > 0 {
			mixed = true
		}
		if c["current_success"] == 0 {
			missing = append(missing, effort)
		}
		valid := c["current_success"] + c["mixed"] + c["unsuccessful"]
		if valid < needed || c["current_success"] > 0 || c["mixed"] > 0 || c["unsuccessful"] != valid {
			allUnsuccessful = false
		}
	}
	if mixed {
		state = "mismatch"
	} else if allUnsuccessful {
		state = "possible_non_gpt"
	} else if len(missing) > 0 {
		state = "insufficient"
	}
	models := []string{}
	for model := range mixedModels {
		models = append(models, model)
	}
	sort.Strings(models)
	sort.Strings(missing)
	return state, map[string]any{"claimedModel": claimed, "perEffort": counts, "minimumValidByEffort": effortNeeded, "mixedModelsObserved": models, "missingCurrentSuccessEfforts": missing}
}
func summarizeOutput(manifests map[string]verificationJobManifest, results map[string]verificationJobResult) map[string]any {
	requests, exact, invalid := 0, 0, 0
	failures := []map[string]any{}
	for id, r := range results {
		m := manifests[id]
		if !strings.HasPrefix(m.ProbeID, "output_") || r.Status != "ok" {
			continue
		}
		requests++
		if r.Exact {
			exact++
		}
		if r.Classification == "unsuccessful" {
			invalid++
		}
		if r.HardAnomaly {
			failures = append(failures, map[string]any{"probeId": m.ProbeID, "observed": r.Answer, "classification": r.Classification})
		}
	}
	return map[string]any{"requests": requests, "exact": exact, "invalid": invalid, "hardAnomaly": len(failures) > 0, "failures": failures}
}
func summarizeCoverage(manifests map[string]verificationJobManifest, results map[string]verificationJobResult) map[string]any {
	counts := map[string]int{}
	failures := []map[string]any{}
	for id, r := range results {
		m := manifests[id]
		if m.ProbeID != "juice_coverage" || r.Status != "ok" {
			continue
		}
		counts[r.Classification]++
		if r.HardAnomaly {
			failures = append(failures, map[string]any{"observed": r.Answer, "expected": m.SyntheticValue, "classification": r.Classification})
		}
	}
	return map[string]any{"requests": sumMapCounts(counts), "classifications": counts, "hardAnomaly": len(failures) > 0, "failures": failures}
}
func sumMapCounts(values map[string]int) int {
	total := 0
	for _, v := range values {
		total += v
	}
	return total
}

func verificationOutcome(juice string, fingerprint fingerprintSummary) (string, string, string) {
	strong := fingerprint.Status == "strong_match" && fingerprint.Model != ""
	fp := "指纹证据不明确"
	if strong {
		fp = "指纹强烈指向 " + verificationModelLabel(fingerprint.Model)
	}
	if juice == "possible_non_gpt" {
		return "possible_non_gpt", "可能非GPT", "所有达到数量要求的有效 Juice 响应均未命中已知型号指纹；也可能由统一输出改写、参数未透传或探针拦截造成。"
	}
	juiceLabel := map[string]string{"pass": "Juice通过", "mismatch": "Juice与申报型号不一致", "insufficient": "Juice证据不足"}[juice]
	codePart := map[string]string{"pass": "pass", "mismatch": "mismatch", "insufficient": "insufficient"}[juice]
	code := fmt.Sprintf("juice_%s_fingerprint_%s", codePart, map[bool]string{true: "strong", false: "unclear"}[strong])
	title := juiceLabel + "；" + fp
	subtitle := title + "。两层证据必须分别阅读，指纹匹配度不是真实路由概率。"
	return code, title, subtitle
}
func verificationModelLabel(model string) string {
	return map[string]string{"gpt-5.6-sol": "Sol", "gpt-5.6-terra": "Terra", "gpt-5.6-luna": "Luna"}[model]
}
func verificationLayerStatus(state string) string {
	if state == "mismatch" || state == "possible_non_gpt" {
		return "failed"
	}
	if state == "pass" {
		return "passed"
	}
	return "skipped"
}
func boolLayerStatus(failed bool) string {
	if failed {
		return "failed"
	}
	return "passed"
}
func fingerprintLayerStatus(f fingerprintSummary) string {
	if f.Status == "strong_match" {
		return "passed"
	}
	return "skipped"
}
func verificationJuiceSummary(state string) string {
	return map[string]string{"pass": "所有启用思考档均取得申报型号命中，且未发现其他已知型号。", "mismatch": "检测到其他已知型号 Juice 或确定性输出异常。", "possible_non_gpt": "足量有效响应均未命中任何已知 GPT Juice。", "insufficient": "至少一个启用思考档没有取得申报型号命中。"}[state]
}
func verificationFingerprintSummary(f fingerprintSummary) string {
	if f.Status == "strong_match" {
		return fmt.Sprintf("行为分布越过正式门槛，强烈指向 %s（这不是身份概率）。", verificationModelLabel(f.Model))
	}
	if containsString(f.UnclearReasons, "builtin_fingerprint_not_enabled") {
		return "低档未启用行为指纹，这是预期的主动弃权。"
	}
	return "行为层未形成正式强指向，请结合完成率和线路错误阅读。"
}
func verificationModeLabel(mode string) string {
	return map[string]string{"low": "低档", "medium": "中档", "high": "高档"}[mode]
}

func (s *VerificationService) failSession(sessionID string, err error) {
	_ = s.store.db.Model(&VerificationSession{}).Where("id = ?", sessionID).Updates(map[string]any{"status": "failed", "error_message": truncateVerificationText(errorString(err), 512), "finished_at": time.Now().UTC()}).Error
}

func (s *VerificationService) ListReports(ctx context.Context, q VerificationReportQuery) (*VerificationReportPage, error) {
	page, size := normalizeVerificationPage(q.Page, q.PageSize)
	db := s.store.db.WithContext(ctx).Model(&VerificationReport{})
	if q.ChannelID > 0 {
		db = db.Where("channel_id = ?", q.ChannelID)
	}
	if q.ModelID > 0 {
		db = db.Where("model_id = ?", q.ModelID)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, err
	}
	var rows []VerificationReport
	if err := db.Order("finished_at desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]VerificationReportView, 0, len(rows))
	for _, row := range rows {
		items = append(items, reportView(row))
	}
	return &VerificationReportPage{Items: items, Total: total, Page: page, PageSize: size}, nil
}
func (s *VerificationService) Report(ctx context.Context, id string) (*VerificationReportView, error) {
	var row VerificationReport
	if err := s.store.db.WithContext(ctx).First(&row, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		return nil, err
	}
	view := reportView(row)
	return &view, nil
}
func (s *VerificationService) ListSessions(ctx context.Context, q VerificationSessionQuery) (*VerificationSessionPage, error) {
	page, size := normalizeVerificationPage(q.Page, q.PageSize)
	db := s.store.db.WithContext(ctx).Model(&VerificationSession{})
	if q.ChannelID > 0 {
		db = db.Where("channel_id = ?", q.ChannelID)
	}
	if q.ModelID > 0 {
		db = db.Where("model_id = ?", q.ModelID)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, err
	}
	var rows []VerificationSession
	if err := db.Order("started_at desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]VerificationSessionView, 0, len(rows))
	for _, row := range rows {
		s.refreshSessionProgress(&row)
		items = append(items, verificationSessionView(row))
	}
	return &VerificationSessionPage{Items: items, Total: total, Page: page, PageSize: size}, nil
}
func (s *VerificationService) Session(ctx context.Context, id string) (*VerificationSessionDetailView, error) {
	var session VerificationSession
	if err := s.store.db.WithContext(ctx).First(&session, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		return nil, err
	}
	s.refreshSessionProgress(&session)
	var attempts []VerificationAttempt
	if err := s.store.db.WithContext(ctx).Where("session_id = ?", session.ID).Order("sequence asc, attempt_no asc").Find(&attempts).Error; err != nil {
		return nil, err
	}
	views := make([]VerificationAttemptView, 0, len(attempts))
	for _, attempt := range attempts {
		views = append(views, verificationAttemptView(attempt))
	}
	return &VerificationSessionDetailView{Session: verificationSessionView(session), Attempts: views}, nil
}
func (s *VerificationService) refreshSessionProgress(session *VerificationSession) {
	if session.Status != "running" && session.Status != "stopping" {
		return
	}
	var completed int64
	_ = s.store.db.Model(&VerificationJob{}).Where("session_id = ? AND status IN ?", session.ID, []string{"succeeded", "error", "cancelled"}).Count(&completed).Error
	session.CompletedCount = completed
	aggregate, _ := s.verificationAggregate(context.Background(), session.ID)
	session.RequestCount = aggregate.RequestCount
	session.SuccessCount = aggregate.SuccessCount
	session.InputTokens = aggregate.InputTokens
	session.OutputTokens = aggregate.OutputTokens
	session.UpstreamCostMicros = aggregate.UpstreamCostMicros
	if session.StopRequestedAt != nil {
		session.Status = "stopping"
	}
}
func normalizeVerificationPage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	return page, size
}
func reportView(row VerificationReport) VerificationReportView {
	var checks []VerificationCheckResult
	_ = json.Unmarshal([]byte(row.ChecksJSON), &checks)
	combined := verificationCombinedReport{}
	_ = json.Unmarshal([]byte(row.ReportJSON), &combined)
	return VerificationReportView{ID: row.ID, SessionID: row.SessionID, ChannelID: row.ChannelID, ChannelName: row.ChannelName, ModelID: row.ModelID, PublicModel: row.PublicModel, UpstreamModel: row.UpstreamModel, Mode: row.Mode, Score: row.Score, Verdict: row.Verdict, OutcomeCode: combined.OutcomeCode, Title: combined.Title, Summary: row.Summary, FingerprintModel: combined.Fingerprint.Model, FingerprintMatch: combined.Fingerprint.Match, Checks: checks, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, RequestCount: row.RequestCount, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, EstimatedCostMicros: row.EstimatedCostMicros, UpstreamCostMicros: row.UpstreamCostMicros}
}
func verificationSessionView(row VerificationSession) VerificationSessionView {
	return VerificationSessionView{ID: row.ID, ChannelID: row.ChannelID, ChannelName: row.ChannelName, ModelID: row.ModelID, PublicModel: row.PublicModel, UpstreamModel: row.UpstreamModel, Mode: row.Mode, Status: row.Status, PlannedCount: row.PlannedCount, CompletedCount: row.CompletedCount, Score: row.Score, Verdict: row.Verdict, Summary: row.Summary, RequestCount: row.RequestCount, SuccessCount: row.SuccessCount, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CachedTokens: row.CachedTokens, CacheWriteTokens: row.CacheWriteTokens, EstimatedCostMicros: row.EstimatedCostMicros, UpstreamCostMicros: row.UpstreamCostMicros, ErrorMessage: row.ErrorMessage, StopRequestedAt: row.StopRequestedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt}
}
func verificationAttemptView(a VerificationAttempt) VerificationAttemptView {
	return VerificationAttemptView{ID: a.ID, Sequence: a.Sequence, JobID: a.JobID, AttemptNo: a.AttemptNo, DetectorName: a.DetectorName, ProbeID: a.ProbeID, RequestFormat: a.RequestFormat, ContextMode: a.ContextMode, Effort: a.Effort, Method: a.Method, Path: a.Path, Stream: a.Stream, RequestBody: a.RequestBody, RequestBodyTruncated: a.RequestBodyTruncated, ResponseBody: a.ResponseBody, ResponseBodyTruncated: a.ResponseBodyTruncated, ContentType: a.ContentType, StatusCode: a.StatusCode, Outcome: a.Outcome, ErrorMessage: a.ErrorMessage, InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, CachedTokens: a.CachedTokens, CacheWriteTokens: a.CacheWriteTokens, EstimatedCostMicros: a.EstimatedCostMicros, UpstreamCostMicros: a.UpstreamCostMicros, CostSource: a.CostSource, UsageSource: a.UsageSource, LatencyMS: a.LatencyMS, StartedAt: a.StartedAt, FinishedAt: a.FinishedAt}
}

func normalizeVerificationBaseURL(value string) (string, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(value), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return "", errors.New("channel base URL must be an absolute HTTP or HTTPS URL")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("channel base URL cannot contain user info, query parameters, or fragments")
	}
	path := strings.TrimRight(base.Path, "/")
	if path == "" {
		path = "/v1"
	}
	base.Path = path
	return strings.TrimRight(base.String(), "/"), nil
}
func boundVerificationBody(data []byte) (string, bool) {
	if len(data) <= verificationAttemptBodyBytes {
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
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
	case int:
		return int64(typed), true
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

// Keep imports used by generated builds that compile without Native support paths.
var _ = base64.StdEncoding
