package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
)

const sessionGroupExpression = "CASE WHEN codex_session_id <> '' THEN codex_session_id ELSE id END"

type SessionLogQuery struct {
	Session   string
	Model     string
	TokenID   uint64
	ChannelID uint64
	From      time.Time
	To        time.Time
	Page      int
	PageSize  int
}

type SessionDetailQuery struct {
	SessionID string
	RequestID string
	TokenID   uint64
	Status    string
	Page      int
	PageSize  int
}

type SessionTitleInput struct {
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId"`
	TokenID   uint64 `json:"tokenId"`
	Title     string `json:"title"`
}

type SessionChannelView struct {
	ChannelID        uint64                    `json:"channelId"`
	ChannelName      string                    `json:"channelName"`
	ChannelBaseURL   string                    `json:"channelBaseUrl"`
	ChannelModelID   uint64                    `json:"channelModelId"`
	UpstreamModel    string                    `json:"upstreamModel"`
	AssignmentSource string                    `json:"assignmentSource"`
	Enabled          bool                      `json:"enabled"`
	MappingEnabled   bool                      `json:"mappingEnabled"`
	CircuitOpenUntil *time.Time                `json:"circuitOpenUntil"`
	LastUsedAt       time.Time                 `json:"lastUsedAt"`
	MigrationHistory []SessionChannelMigration `json:"migrationHistory"`
}

type SessionChannelMigration struct {
	FromChannelID   uint64    `json:"fromChannelId"`
	FromChannelName string    `json:"fromChannelName"`
	ToChannelID     uint64    `json:"toChannelId"`
	ToChannelName   string    `json:"toChannelName"`
	Reason          string    `json:"reason"`
	Detail          string    `json:"detail"`
	RequestID       string    `json:"requestId"`
	OccurredAt      time.Time `json:"occurredAt"`
}

type SessionChannelUsage struct {
	ChannelID    uint64  `json:"channelId"`
	ChannelName  string  `json:"channelName"`
	AttemptCount int64   `json:"attemptCount"`
	Share        float64 `json:"share"`
}

type SessionLogSummary struct {
	GroupID                  string              `gorm:"column:group_id" json:"-"`
	SessionID                string              `json:"sessionId"`
	SessionName              string              `json:"sessionName"`
	SessionSource            string              `json:"sessionSource"`
	ClientKind               string              `json:"clientKind"`
	ThreadSource             string              `json:"threadSource"`
	Identified               bool                `json:"identified"`
	FallbackRequestID        string              `json:"fallbackRequestId"`
	TokenID                  uint64              `json:"tokenId"`
	TokenName                string              `json:"tokenName"`
	TokenKeyPrefix           string              `json:"tokenKeyPrefix"`
	LatestModel              string              `json:"latestModel"`
	LatestEndpoint           string              `json:"latestEndpoint"`
	PrimaryModel             string              `json:"primaryModel"`
	ContextWindowTokens      int64               `json:"contextWindowTokens"`
	ContextWindowSource      string              `json:"contextWindowSource"`
	ContextWindowSamples     int64               `json:"contextWindowSampleCount"`
	RequestCount             int64               `json:"requestCount"`
	CompactionCount          int64               `json:"compactionCount"`
	SuccessCount             int64               `json:"successCount"`
	CanceledCount            int64               `json:"canceledCount"`
	ProcessingCount          int64               `json:"processingCount"`
	SuccessRate              float64             `json:"successRate"`
	AttemptCount             int64               `json:"attemptCount"`
	InputTokens              int64               `json:"inputTokens"`
	NormalInputTokens        int64               `json:"normalInputTokens"`
	OutputTokens             int64               `json:"outputTokens"`
	CachedTokens             int64               `json:"cachedTokens"`
	CacheWriteTokens         int64               `json:"cacheWriteTokens"`
	SentTokens               int64               `json:"sentTokens"`
	CacheHitRate             float64             `json:"cacheHitRate"`
	EstimatedCost            int64               `json:"estimatedCostMicros"`
	UpstreamCost             int64               `json:"upstreamCostMicros"`
	AverageFirstTokenMS      float64             `json:"averageFirstTokenMs"`
	FirstTokenSampleCount    int64               `json:"firstTokenSampleCount"`
	AverageFirstResponseMS   float64             `json:"averageFirstResponseMs"`
	FirstResponseSampleCount int64               `json:"firstResponseSampleCount"`
	AverageLatencyMS         float64             `json:"averageLatencyMs"`
	LatencySampleCount       int64               `json:"latencySampleCount"`
	AverageDurationMS        float64             `json:"averageDurationMs"`
	DurationSampleCount      int64               `json:"durationSampleCount"`
	FirstSeenAt              time.Time           `json:"firstSeenAt"`
	LastSeenAt               time.Time           `json:"lastSeenAt"`
	CurrentChannel           *SessionChannelView `gorm:"-" json:"currentChannel"`
}

type SessionLogPage struct {
	Items    []SessionLogSummary `json:"items"`
	Summary  LogAggregateSummary `json:"summary"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
}

type SessionLogDetail struct {
	Summary      SessionLogSummary     `json:"summary"`
	Channels     []SessionChannelUsage `json:"channels"`
	Requests     []RelayRequestView    `json:"requests"`
	RequestTotal int64                 `json:"requestTotal"`
	Page         int                   `json:"page"`
	PageSize     int                   `json:"pageSize"`
}

func normalizeSessionPage(page *int, pageSize *int) {
	if *page < 1 {
		*page = 1
	}
	if *pageSize < 1 || *pageSize > 200 {
		*pageSize = 25
	}
}

func (s *ManagementService) SessionLogs(ctx context.Context, query SessionLogQuery) (*SessionLogPage, error) {
	normalizeSessionPage(&query.Page, &query.PageSize)
	cutoff := time.Now().UTC().Add(-DetailedLogRetentionDays * 24 * time.Hour)

	grouped := applySessionLogFilters(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}), query, cutoff).
		Select("token_id, " + sessionGroupExpression + " AS group_id").
		Group("token_id, " + sessionGroupExpression)
	var total int64
	if err := s.store.db.WithContext(ctx).Table("(?) AS session_groups", grouped).Count(&total).Error; err != nil {
		return nil, err
	}
	summary, err := aggregateLogSummary(applySessionLogFilters(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}), query, cutoff))
	if err != nil {
		return nil, err
	}

	items := make([]SessionLogSummary, 0, query.PageSize)
	selectSQL := sessionGroupExpression + " AS group_id, " +
		"CASE WHEN codex_session_id <> '' THEN codex_session_id ELSE '' END AS session_id, " +
		"CASE WHEN codex_session_id <> '' THEN 1 ELSE 0 END AS identified, " +
		"CASE WHEN codex_session_id = '' THEN id ELSE '' END AS fallback_request_id, token_id, " +
		"COUNT(*) AS request_count, SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) AS success_count, " +
		"SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END) AS canceled_count, " +
		"SUM(CASE WHEN outcome = 'processing' THEN 1 ELSE 0 END) AS processing_count, " +
		"COALESCE(SUM(attempt_count), 0) AS attempt_count, COALESCE(SUM(input_tokens), 0) AS input_tokens, " +
		"COALESCE(SUM(normal_input_tokens), 0) AS normal_input_tokens, " +
		"COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(cached_tokens), 0) AS cached_tokens, " +
		"COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens, " +
		"COALESCE(SUM(sent_tokens), 0) AS sent_tokens, " +
		"COALESCE(SUM(estimated_cost), 0) AS estimated_cost, COALESCE(SUM(upstream_cost), 0) AS upstream_cost, " +
		"COALESCE(SUM(first_token_ms), 0) AS total_first_token_ms, SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END) AS first_token_sample_count, " +
		"COALESCE(SUM(first_response_ms), 0) AS total_first_response_ms, SUM(CASE WHEN first_response_ms > 0 THEN 1 ELSE 0 END) AS first_response_sample_count, " +
		"COALESCE(SUM(latency_ms), 0) AS total_latency_ms, SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END) AS latency_sample_count, " +
		"COALESCE(SUM(duration_ms), 0) AS total_duration_ms, COUNT(*) AS duration_sample_count, " +
		"MIN(unixepoch(created_at)) AS first_seen_unix, MAX(unixepoch(created_at)) AS last_seen_unix"
	type aggregateRow struct {
		SessionLogSummary
		TotalFirstTokenMS    int64
		TotalFirstResponseMS int64
		TotalLatencyMS       int64
		TotalDurationMS      int64
		FirstSeenUnix        int64
		LastSeenUnix         int64
	}
	var rows []aggregateRow
	if err := applySessionLogFilters(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}), query, cutoff).
		Select(selectSQL).Group("token_id, " + sessionGroupExpression).
		Order("first_seen_unix DESC, last_seen_unix DESC").Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		summary := row.SessionLogSummary
		summary.FirstSeenAt = time.Unix(row.FirstSeenUnix, 0).UTC()
		summary.LastSeenAt = time.Unix(row.LastSeenUnix, 0).UTC()
		finishSessionSummary(&summary, row.TotalFirstTokenMS, row.TotalLatencyMS, row.TotalDurationMS)
		if summary.FirstResponseSampleCount > 0 {
			summary.AverageFirstResponseMS = float64(row.TotalFirstResponseMS) / float64(summary.FirstResponseSampleCount)
		}
		if err := s.populateSessionSummary(ctx, &summary, cutoff); err != nil {
			return nil, err
		}
		items = append(items, summary)
	}
	return &SessionLogPage{Items: items, Summary: summary, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func applySessionLogFilters(db *gorm.DB, query SessionLogQuery, cutoff time.Time) *gorm.DB {
	db = db.Where("created_at >= ?", cutoff)
	if value := strings.TrimSpace(query.Session); value != "" {
		pattern := "%" + value + "%"
		db = db.Where(
			"(session_name LIKE ? OR codex_session_id LIKE ? OR id LIKE ? OR EXISTS ("+
				"SELECT 1 FROM relay_session_states s WHERE s.token_id = relay_request_logs.token_id "+
				"AND s.session_id = relay_request_logs.codex_session_id AND s.title LIKE ?))",
			pattern, pattern, pattern, pattern,
		)
	}
	if value := strings.TrimSpace(query.Model); value != "" {
		db = db.Where("requested_model = ?", value)
	}
	if query.TokenID > 0 {
		db = db.Where("token_id = ?", query.TokenID)
	}
	if query.ChannelID > 0 {
		db = db.Where("(SELECT a.channel_id FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id ORDER BY a.id DESC LIMIT 1) = ?", query.ChannelID)
	}
	if !query.From.IsZero() {
		db = db.Where("created_at >= ?", utcQueryTime(query.From))
	}
	if !query.To.IsZero() {
		db = db.Where("created_at <= ?", utcInclusiveMillisecondEnd(query.To))
	}
	return db
}

func finishSessionSummary(summary *SessionLogSummary, totalFirstTokenMS int64, totalLatencyMS int64, totalDurationMS int64) {
	if summary.FirstTokenSampleCount > 0 {
		summary.AverageFirstTokenMS = float64(totalFirstTokenMS) / float64(summary.FirstTokenSampleCount)
	}
	if summary.LatencySampleCount > 0 {
		summary.AverageLatencyMS = float64(totalLatencyMS) / float64(summary.LatencySampleCount)
	}
	summary.SuccessRate = attemptSuccessRate(summary.SuccessCount, summary.AttemptCount)
	if durationSamples := summary.RequestCount - summary.ProcessingCount; durationSamples > 0 {
		summary.AverageDurationMS = float64(totalDurationMS) / float64(durationSamples)
		summary.DurationSampleCount = durationSamples
	}
	summary.InputTokens = max(summary.InputTokens, 0)
	summary.NormalInputTokens = max(summary.NormalInputTokens, 0)
	summary.OutputTokens = max(summary.OutputTokens, 0)
	summary.CachedTokens = min(max(summary.CachedTokens, 0), summary.InputTokens)
	summary.CacheWriteTokens = min(max(summary.CacheWriteTokens, 0), summary.InputTokens-summary.CachedTokens)
	summary.NormalInputTokens = min(summary.NormalInputTokens, summary.InputTokens-summary.CachedTokens-summary.CacheWriteTokens)
	summary.SentTokens = max(summary.SentTokens, 0)
	if summary.InputTokens > 0 {
		summary.CacheHitRate = float64(summary.CachedTokens) / float64(summary.InputTokens)
	}
}

func attemptSuccessRate(successCount int64, attemptCount int64) float64 {
	if successCount <= 0 || attemptCount <= 0 {
		return 0
	}
	return float64(min(successCount, attemptCount)) / float64(attemptCount)
}

func (s *ManagementService) SessionLogDetail(ctx context.Context, query SessionDetailQuery) (*SessionLogDetail, error) {
	normalizeSessionPage(&query.Page, &query.PageSize)
	query.SessionID = strings.TrimSpace(query.SessionID)
	query.RequestID = strings.TrimSpace(query.RequestID)
	if (query.SessionID == "" && query.RequestID == "") || (query.SessionID != "" && query.TokenID == 0) {
		return nil, errors.New("sessionId with tokenId or requestId is required")
	}
	cutoff := time.Now().UTC().Add(-DetailedLogRetentionDays * 24 * time.Hour)
	base := applySessionDetailStatus(applySessionIdentity(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).Where("created_at >= ?", cutoff), query), query.Status)
	var requestTotal int64
	if err := base.Count(&requestTotal).Error; err != nil {
		return nil, err
	}

	type detailAggregate struct {
		RequestCount         int64
		CompactionCount      int64
		SuccessCount         int64
		CanceledCount        int64
		ProcessingCount      int64
		AttemptCount         int64
		InputTokens          int64
		NormalInputTokens    int64
		OutputTokens         int64
		CachedTokens         int64
		CacheWriteTokens     int64
		SentTokens           int64
		EstimatedCost        int64
		UpstreamCost         int64
		TotalFirstTokenMS    int64
		FirstTokenSamples    int64
		TotalFirstResponseMS int64
		FirstResponseSamples int64
		TotalLatencyMS       int64
		LatencySamples       int64
		TotalDurationMS      int64
		FirstSeenUnix        int64
		LastSeenUnix         int64
	}
	var aggregate detailAggregate
	if err := applySessionIdentity(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).Where("created_at >= ?", cutoff), query).
		Select("COUNT(*) AS request_count, SUM(CASE WHEN is_compaction AND (outcome = 'success' OR (outcome = '' AND status_code >= 200 AND status_code < 300)) THEN 1 ELSE 0 END) AS compaction_count, " +
			"SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) AS success_count, " +
			"SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END) AS canceled_count, " +
			"SUM(CASE WHEN outcome = 'processing' THEN 1 ELSE 0 END) AS processing_count, " +
			"COALESCE(SUM(attempt_count), 0) AS attempt_count, COALESCE(SUM(input_tokens), 0) AS input_tokens, " +
			"COALESCE(SUM(normal_input_tokens), 0) AS normal_input_tokens, " +
			"COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(cached_tokens), 0) AS cached_tokens, " +
			"COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens, " +
			"COALESCE(SUM(sent_tokens), 0) AS sent_tokens, " +
			"COALESCE(SUM(estimated_cost), 0) AS estimated_cost, COALESCE(SUM(upstream_cost), 0) AS upstream_cost, " +
			"COALESCE(SUM(first_token_ms), 0) AS total_first_token_ms, SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END) AS first_token_samples, " +
			"COALESCE(SUM(first_response_ms), 0) AS total_first_response_ms, SUM(CASE WHEN first_response_ms > 0 THEN 1 ELSE 0 END) AS first_response_samples, " +
			"COALESCE(SUM(latency_ms), 0) AS total_latency_ms, SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END) AS latency_samples, " +
			"COALESCE(SUM(duration_ms), 0) AS total_duration_ms, " +
			"MIN(unixepoch(created_at)) AS first_seen_unix, MAX(unixepoch(created_at)) AS last_seen_unix").Scan(&aggregate).Error; err != nil {
		return nil, err
	}
	if aggregate.RequestCount == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	summary := SessionLogSummary{
		SessionID:                query.SessionID,
		Identified:               query.SessionID != "",
		FallbackRequestID:        query.RequestID,
		TokenID:                  query.TokenID,
		RequestCount:             aggregate.RequestCount,
		SuccessCount:             aggregate.SuccessCount,
		CanceledCount:            aggregate.CanceledCount,
		ProcessingCount:          aggregate.ProcessingCount,
		AttemptCount:             aggregate.AttemptCount,
		InputTokens:              aggregate.InputTokens,
		NormalInputTokens:        aggregate.NormalInputTokens,
		OutputTokens:             aggregate.OutputTokens,
		CachedTokens:             aggregate.CachedTokens,
		CacheWriteTokens:         aggregate.CacheWriteTokens,
		SentTokens:               aggregate.SentTokens,
		EstimatedCost:            aggregate.EstimatedCost,
		UpstreamCost:             aggregate.UpstreamCost,
		FirstTokenSampleCount:    aggregate.FirstTokenSamples,
		FirstResponseSampleCount: aggregate.FirstResponseSamples,
		LatencySampleCount:       aggregate.LatencySamples,
		DurationSampleCount:      aggregate.RequestCount,
		FirstSeenAt:              time.Unix(aggregate.FirstSeenUnix, 0).UTC(),
		LastSeenAt:               time.Unix(aggregate.LastSeenUnix, 0).UTC(),
	}
	finishSessionSummary(&summary, aggregate.TotalFirstTokenMS, aggregate.TotalLatencyMS, aggregate.TotalDurationMS)
	if summary.FirstResponseSampleCount > 0 {
		summary.AverageFirstResponseMS = float64(aggregate.TotalFirstResponseMS) / float64(summary.FirstResponseSampleCount)
	}
	if err := s.populateSessionSummary(ctx, &summary, cutoff); err != nil {
		return nil, err
	}
	summary.CompactionCount = aggregate.CompactionCount
	channels, err := s.sessionChannelUsage(ctx, query, cutoff)
	if err != nil {
		return nil, err
	}

	var logs []RelayRequestLog
	if err := applySessionDetailStatus(applySessionIdentity(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).Where("created_at >= ?", cutoff), query), query.Status).
		Omit("request_body", "response_body").
		Order("created_at DESC, id DESC").Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).
		Find(&logs).Error; err != nil {
		return nil, err
	}
	requests := make([]RelayRequestView, 0, len(logs))
	for _, log := range logs {
		view, err := s.relayRequestView(ctx, log, false, true)
		if err != nil {
			return nil, err
		}
		requests = append(requests, view)
	}
	return &SessionLogDetail{Summary: summary, Channels: channels, Requests: requests, RequestTotal: requestTotal, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *ManagementService) sessionChannelUsage(ctx context.Context, query SessionDetailQuery, cutoff time.Time) ([]SessionChannelUsage, error) {
	requestIDs := applySessionIdentity(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).
		Select("id").Where("created_at >= ?", cutoff), query)
	type usageRow struct {
		ChannelID    uint64
		ChannelName  string
		AttemptCount int64
	}
	var rows []usageRow
	if err := s.store.db.WithContext(ctx).Table("relay_attempt_logs AS a").
		Select("a.channel_id, COALESCE(NULLIF(c.name, ''), MAX(a.channel_name)) AS channel_name, COUNT(*) AS attempt_count").
		Joins("LEFT JOIN channels AS c ON c.id = a.channel_id").
		Where("a.request_id IN (?)", requestIDs).
		Group("a.channel_id, c.name").
		Order("attempt_count DESC, channel_name ASC, a.channel_id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	var total int64
	for _, row := range rows {
		total += row.AttemptCount
	}
	usage := make([]SessionChannelUsage, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.ChannelName)
		if name == "" {
			if row.ChannelID > 0 {
				name = fmt.Sprintf("渠道 #%d", row.ChannelID)
			} else {
				name = "未知渠道"
			}
		}
		share := 0.0
		if total > 0 {
			share = float64(row.AttemptCount) / float64(total)
		}
		usage = append(usage, SessionChannelUsage{ChannelID: row.ChannelID, ChannelName: name, AttemptCount: row.AttemptCount, Share: share})
	}
	return usage, nil
}

func applySessionIdentity(db *gorm.DB, query SessionDetailQuery) *gorm.DB {
	if query.SessionID != "" {
		return db.Where("token_id = ? AND codex_session_id = ?", query.TokenID, query.SessionID)
	}
	return db.Where("id = ? AND codex_session_id = ''", query.RequestID)
}

func applySessionDetailStatus(db *gorm.DB, status string) *gorm.DB {
	switch strings.TrimSpace(status) {
	case "success":
		return db.Where("outcome = ?", RelayOutcomeSuccess)
	case "canceled":
		return db.Where("outcome = ?", RelayOutcomeCanceled)
	case "failure":
		return db.Where("outcome = ?", RelayOutcomeFailed)
	case "compaction":
		return db.Where("is_compaction = ? AND (outcome = ? OR (outcome = '' AND status_code >= 200 AND status_code < 300))",
			true, RelayOutcomeSuccess)
	default:
		return db
	}
}

func (s *ManagementService) populateSessionSummary(ctx context.Context, summary *SessionLogSummary, cutoff time.Time) error {
	latestDB := s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).Where("created_at >= ?", cutoff)
	firstDB := s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).Where("created_at >= ?", cutoff)
	if summary.Identified {
		latestDB = latestDB.Where("token_id = ? AND codex_session_id = ?", summary.TokenID, summary.SessionID)
		firstDB = firstDB.Where("token_id = ? AND codex_session_id = ?", summary.TokenID, summary.SessionID)
	} else {
		latestDB = latestDB.Where("id = ? AND codex_session_id = ''", summary.FallbackRequestID)
		firstDB = firstDB.Where("id = ? AND codex_session_id = ''", summary.FallbackRequestID)
	}
	var first RelayRequestLog
	if err := firstDB.Order("created_at ASC, id ASC").First(&first).Error; err != nil {
		return err
	}
	summary.SessionName = first.SessionName
	if summary.Identified {
		var state RelaySessionState
		if err := s.store.db.WithContext(ctx).
			Where("token_id = ? AND session_id = ?", summary.TokenID, summary.SessionID).
			First(&state).Error; err == nil {
			if strings.TrimSpace(state.Title) != "" {
				summary.SessionName = state.Title
			}
			summary.ClientKind = state.ClientKind
			summary.ThreadSource = state.ThreadSource
			summary.CompactionCount = state.CompactionCount
			summary.PrimaryModel = state.PrimaryModel
			summary.ContextWindowTokens = state.ContextWindowTokens
			summary.ContextWindowSource = state.ContextWindowSource
			summary.ContextWindowSamples = state.ContextWindowSamples
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	var latest RelayRequestLog
	if err := latestDB.
		Order("CASE WHEN codex_session_source IN ('codex_title_generation', 'codex_guardian') THEN 1 ELSE 0 END ASC").
		Order("created_at DESC, id DESC").First(&latest).Error; err != nil {
		return err
	}
	summary.TokenID = latest.TokenID
	if summary.ClientKind == "" {
		summary.ClientKind = latest.ClientKind
	}
	summary.TokenName = latest.TokenName
	summary.TokenKeyPrefix = latest.TokenKeyPrefix
	if summary.TokenName == "" || summary.TokenKeyPrefix == "" {
		var token ClientToken
		if err := s.store.db.WithContext(ctx).First(&token, latest.TokenID).Error; err == nil {
			if summary.TokenName == "" {
				summary.TokenName = token.Name
			}
			if summary.TokenKeyPrefix == "" {
				summary.TokenKeyPrefix = token.KeyPrefix
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	if summary.Identified {
		summary.SessionID = latest.CodexSessionID
		summary.SessionSource = latest.CodexSessionSource
	} else {
		summary.SessionSource = "unavailable"
		summary.FallbackRequestID = latest.ID
	}
	if summary.SessionSource == "" {
		summary.SessionSource = "unavailable"
	}
	if summary.ThreadSource == "" {
		summary.ThreadSource = codexThreadSourceUnknown
	}
	summary.LatestModel = latest.RequestedModel
	if summary.PrimaryModel == "" {
		summary.PrimaryModel = latest.RequestedModel
	}
	summary.LatestEndpoint = latest.Endpoint
	current, err := s.currentSessionChannel(ctx, *summary, latest.RequestedModel, cutoff)
	if err != nil {
		return err
	}
	summary.CurrentChannel = current
	return nil
}

func (s *ManagementService) RenameSession(ctx context.Context, input SessionTitleInput) error {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Title = normalizeSessionName(input.Title)
	if input.Title == "" {
		return errors.New("session title is required")
	}
	if len([]rune(input.Title)) > 80 {
		return errors.New("session title must not exceed 80 characters")
	}
	if (input.SessionID == "" && input.RequestID == "") || (input.SessionID != "" && input.TokenID == 0) {
		return errors.New("sessionId with tokenId or requestId is required")
	}
	cutoff := time.Now().UTC().Add(-DetailedLogRetentionDays * 24 * time.Hour)
	return s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if input.SessionID == "" {
			result := db.Model(&RelayRequestLog{}).
				Where("id = ? AND codex_session_id = '' AND created_at >= ?", input.RequestID, cutoff).
				Update("session_name", input.Title)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
			return nil
		}

		var count int64
		if err := db.Model(&RelayRequestLog{}).
			Where("token_id = ? AND codex_session_id = ? AND created_at >= ?", input.TokenID, input.SessionID, cutoff).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
		now := time.Now().UTC()
		var state RelaySessionState
		err := db.Where("token_id = ? AND session_id = ?", input.TokenID, input.SessionID).First(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := db.Create(&RelaySessionState{
				TokenID: input.TokenID, SessionID: input.SessionID, Title: input.Title, TitleCustomized: true,
				CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if err := db.Model(&state).Updates(map[string]any{
			"title": input.Title, "title_customized": true, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return db.Model(&RelayRequestLog{}).
			Where("token_id = ? AND codex_session_id = ?", input.TokenID, input.SessionID).
			Update("session_name", input.Title).Error
	})
}

func (s *ManagementService) currentSessionChannel(ctx context.Context, summary SessionLogSummary, modelName string, cutoff time.Time) (*SessionChannelView, error) {
	history, err := s.sessionChannelHistory(ctx, summary, cutoff)
	if err != nil {
		return nil, err
	}
	if summary.Identified {
		var model GatewayModel
		modelErr := s.store.db.WithContext(ctx).Where("name = ?", modelName).First(&model).Error
		if modelErr == nil {
			var affinity SessionAffinity
			affinityErr := s.store.db.WithContext(ctx).
				Where("token_id = ? AND model_id = ? AND session_hash = ? AND expires_at > ?", summary.TokenID, model.ID, hashSecret(summary.SessionID), time.Now()).
				First(&affinity).Error
			if affinityErr == nil {
				var mapping ChannelModel
				if err := s.store.db.WithContext(ctx).First(&mapping, affinity.ChannelModelID).Error; err == nil {
					var channel Channel
					if err := s.store.db.WithContext(ctx).First(&channel, mapping.ChannelID).Error; err == nil {
						return &SessionChannelView{
							ChannelID:        channel.ID,
							ChannelName:      channel.Name,
							ChannelBaseURL:   channel.BaseURL,
							ChannelModelID:   mapping.ID,
							UpstreamModel:    mapping.UpstreamModel,
							AssignmentSource: "session_affinity",
							Enabled:          channel.Enabled,
							MappingEnabled:   mapping.Enabled,
							CircuitOpenUntil: channel.CircuitOpenUntil,
							LastUsedAt:       affinity.UpdatedAt,
							MigrationHistory: history,
						}, nil
					} else if !errors.Is(err, gorm.ErrRecordNotFound) {
						return nil, err
					}
				} else if !errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, err
				}
			} else if !errors.Is(affinityErr, gorm.ErrRecordNotFound) {
				return nil, affinityErr
			}
		} else if !errors.Is(modelErr, gorm.ErrRecordNotFound) {
			return nil, modelErr
		}
	}

	attemptDB := s.store.db.WithContext(ctx).Table("relay_attempt_logs AS a").
		Select("a.*").Joins("JOIN relay_request_logs AS r ON r.id = a.request_id").
		Where("r.created_at >= ?", cutoff).
		Where("r.codex_session_source NOT IN ?", codexAuxiliarySessionSources)
	if summary.Identified {
		attemptDB = attemptDB.Where("r.token_id = ? AND r.codex_session_id = ?", summary.TokenID, summary.SessionID)
	} else {
		attemptDB = attemptDB.Where("r.id = ? AND r.codex_session_id = ''", summary.FallbackRequestID)
	}
	var attempt RelayAttemptLog
	if err := attemptDB.Order("a.success DESC, a.created_at DESC, a.id DESC").First(&attempt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	current := &SessionChannelView{
		ChannelID:        attempt.ChannelID,
		ChannelName:      attempt.ChannelName,
		ChannelBaseURL:   attempt.ChannelBaseURL,
		ChannelModelID:   attempt.ChannelModelID,
		UpstreamModel:    attempt.UpstreamModel,
		AssignmentSource: "latest_attempt",
		LastUsedAt:       attempt.CreatedAt,
		MigrationHistory: history,
	}
	if attempt.Success {
		current.AssignmentSource = "latest_successful_attempt"
	}
	var mapping ChannelModel
	if err := s.store.db.WithContext(ctx).First(&mapping, attempt.ChannelModelID).Error; err == nil {
		current.MappingEnabled = mapping.Enabled
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var channel Channel
	if err := s.store.db.WithContext(ctx).First(&channel, attempt.ChannelID).Error; err == nil {
		current.Enabled = channel.Enabled
		current.CircuitOpenUntil = channel.CircuitOpenUntil
		if current.ChannelName == "" {
			current.ChannelName = channel.Name
		}
		if current.ChannelBaseURL == "" {
			current.ChannelBaseURL = channel.BaseURL
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return current, nil
}

func (s *ManagementService) sessionChannelHistory(ctx context.Context, summary SessionLogSummary, cutoff time.Time) ([]SessionChannelMigration, error) {
	attemptDB := s.store.db.WithContext(ctx).Table("relay_attempt_logs AS a").
		Select("a.*, r.id AS request_log_id, r.created_at AS request_created_at").
		Joins("JOIN relay_request_logs AS r ON r.id = a.request_id").
		Where("r.created_at >= ?", cutoff).
		Where("r.codex_session_source NOT IN ?", codexAuxiliarySessionSources)
	if summary.Identified {
		attemptDB = attemptDB.Where("r.token_id = ? AND r.codex_session_id = ?", summary.TokenID, summary.SessionID)
	} else {
		attemptDB = attemptDB.Where("r.id = ? AND r.codex_session_id = ''", summary.FallbackRequestID)
	}
	type historyAttempt struct {
		RelayAttemptLog
		RequestLogID     string    `gorm:"column:request_log_id"`
		RequestCreatedAt time.Time `gorm:"column:request_created_at"`
	}
	var attempts []historyAttempt
	if err := attemptDB.Order("r.created_at ASC, a.created_at ASC, a.id ASC").Scan(&attempts).Error; err != nil {
		return nil, err
	}
	history := make([]SessionChannelMigration, 0)
	var previous RelayAttemptLog
	for _, item := range attempts {
		current := item.RelayAttemptLog
		if previous.ChannelID != 0 && current.ChannelID != 0 && previous.ChannelID != current.ChannelID {
			fromName := current.PreviousChannelName
			if fromName == "" {
				fromName = previous.ChannelName
			}
			history = append(history, SessionChannelMigration{
				FromChannelID: previous.ChannelID, FromChannelName: fromName,
				ToChannelID: current.ChannelID, ToChannelName: current.ChannelName,
				Reason: current.SelectionReason, Detail: current.SelectionDetail,
				RequestID: current.RequestID, OccurredAt: current.CreatedAt,
			})
		}
		previous = current
	}
	return history, nil
}

func (s *ManagementService) relayRequestView(ctx context.Context, log RelayRequestLog, includePayloads bool, includeSteps bool) (RelayRequestView, error) {
	if includePayloads {
		log.RequestBody = decompressStoredPayload(log.RequestBody)
		log.ResponseBody = decompressStoredPayload(log.ResponseBody)
	}
	attempts := make([]RelayAttemptLog, 0)
	attemptDB := s.store.db.WithContext(ctx).Where("request_id = ?", log.ID)
	if !includePayloads {
		attemptDB = attemptDB.Omit("request_body", "response_body")
	}
	if err := attemptDB.Order("created_at ASC, id ASC").Find(&attempts).Error; err != nil {
		return RelayRequestView{}, err
	}
	steps := make([]RelayStepLog, 0)
	if includeSteps {
		if err := s.store.db.WithContext(ctx).Where("request_id = ?", log.ID).
			Order("started_offset_us ASC, id ASC").Find(&steps).Error; err != nil {
			return RelayRequestView{}, err
		}
	}
	channelCache := make(map[uint64]Channel)
	for index := range attempts {
		if includePayloads {
			attempts[index].RequestBody = decompressStoredPayload(attempts[index].RequestBody)
			attempts[index].ResponseBody = decompressStoredPayload(attempts[index].ResponseBody)
		}
		if attempts[index].RouteDecisionJSON != "" {
			var decision RouteDecision
			if json.Unmarshal([]byte(attempts[index].RouteDecisionJSON), &decision) == nil {
				attempts[index].RouteDecision = &decision
			}
		}
		if attempts[index].ChannelName != "" && attempts[index].ChannelBaseURL != "" {
			continue
		}
		channel, ok := channelCache[attempts[index].ChannelID]
		if !ok {
			if err := s.store.db.WithContext(ctx).First(&channel, attempts[index].ChannelID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return RelayRequestView{}, err
			}
			channelCache[channel.ID] = channel
		}
		if attempts[index].ChannelName == "" {
			attempts[index].ChannelName = channel.Name
		}
		if attempts[index].ChannelBaseURL == "" {
			attempts[index].ChannelBaseURL = channel.BaseURL
		}
	}
	for index := range attempts {
		attempts[index].APIPath = relayAPIPath(attempts[index].ChannelBaseURL, log.Endpoint)
	}
	parameters := decodeRequestParameters(log.RequestParametersJSON)
	apiPath := relayAPIPath("", log.Endpoint)
	if len(attempts) > 0 {
		apiPath = attempts[0].APIPath
	}
	return RelayRequestView{
		RelayRequestLog:   log,
		RequestParameters: parameters,
		APIPath:           apiPath,
		ReasoningEffort:   requestReasoningEffort(parameters),
		Attempts:          attempts,
		Steps:             steps,
	}, nil
}

func relayAPIPath(baseURL string, endpoint string) string {
	suffix := endpointPath(endpoint)
	if parsed, err := url.Parse(strings.TrimSpace(baseURL)); err == nil {
		basePath := strings.TrimRight(parsed.Path, "/")
		if index := strings.LastIndex(basePath, "/v1"); index >= 0 {
			boundary := index + len("/v1")
			if boundary == len(basePath) || basePath[boundary] == '/' {
				return strings.TrimRight(basePath[index:], "/") + "/" + suffix
			}
		}
	}
	return "/v1/" + suffix
}

func requestReasoningEffort(parameters map[string]any) string {
	if reasoning, ok := parameters["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok && strings.TrimSpace(effort) != "" {
			return strings.TrimSpace(effort)
		}
	}
	if effort, ok := parameters["reasoning_effort"].(string); ok {
		return strings.TrimSpace(effort)
	}
	return ""
}

func decodeRequestParameters(value string) map[string]any {
	parameters := make(map[string]any)
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.UseNumber()
	if strings.TrimSpace(value) == "" || decoder.Decode(&parameters) != nil {
		return map[string]any{}
	}
	return parameters
}
