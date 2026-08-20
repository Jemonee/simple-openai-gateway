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
	TokenIDs  []uint64
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
	GroupID                          string              `gorm:"column:group_id" json:"-"`
	SessionID                        string              `json:"sessionId"`
	SessionName                      string              `json:"sessionName"`
	SessionSource                    string              `json:"sessionSource"`
	ClientKind                       string              `json:"clientKind"`
	ThreadSource                     string              `json:"threadSource"`
	ParentSessionID                  string              `json:"parentSessionId"`
	ChildCount                       int64               `gorm:"-" json:"childCount"`
	Identified                       bool                `json:"identified"`
	FallbackRequestID                string              `json:"fallbackRequestId"`
	TokenID                          uint64              `json:"tokenId"`
	TokenName                        string              `json:"tokenName"`
	TokenKeyPrefix                   string              `json:"tokenKeyPrefix"`
	LatestModel                      string              `json:"latestModel"`
	LatestEndpoint                   string              `json:"latestEndpoint"`
	PrimaryModel                     string              `json:"primaryModel"`
	ContextWindowTokens              int64               `json:"contextWindowTokens"`
	ContextWindowSource              string              `json:"contextWindowSource"`
	ContextWindowSamples             int64               `json:"contextWindowSampleCount"`
	RequestCount                     int64               `json:"requestCount"`
	CompactionCount                  int64               `json:"compactionCount"`
	SuccessCount                     int64               `json:"successCount"`
	CanceledCount                    int64               `json:"canceledCount"`
	VerificationAttemptSuccessCount  int64               `json:"verificationAttemptSuccessCount"`
	VerificationAttemptFailureCount  int64               `json:"verificationAttemptFailureCount"`
	VerificationAttemptCanceledCount int64               `json:"verificationAttemptCanceledCount"`
	ProcessingCount                  int64               `json:"processingCount"`
	SuccessRate                      float64             `json:"successRate"`
	AttemptCount                     int64               `json:"attemptCount"`
	InputTokens                      int64               `json:"inputTokens"`
	NormalInputTokens                int64               `json:"normalInputTokens"`
	OutputTokens                     int64               `json:"outputTokens"`
	CachedTokens                     int64               `json:"cachedTokens"`
	CacheWriteTokens                 int64               `json:"cacheWriteTokens"`
	SentTokens                       int64               `json:"sentTokens"`
	CacheHitRate                     float64             `json:"cacheHitRate"`
	EstimatedCost                    int64               `json:"estimatedCostMicros"`
	UpstreamCost                     int64               `json:"upstreamCostMicros"`
	AverageFirstTokenMS              float64             `json:"averageFirstTokenMs"`
	FirstTokenSampleCount            int64               `json:"firstTokenSampleCount"`
	AverageFirstResponseMS           float64             `json:"averageFirstResponseMs"`
	FirstResponseSampleCount         int64               `json:"firstResponseSampleCount"`
	AverageLatencyMS                 float64             `json:"averageLatencyMs"`
	LatencySampleCount               int64               `json:"latencySampleCount"`
	AverageDurationMS                float64             `json:"averageDurationMs"`
	DurationSampleCount              int64               `json:"durationSampleCount"`
	AverageTokensPerSecond           float64             `json:"averageTokensPerSecond"`
	TokensPerSecondSampleCount       int64               `json:"tokensPerSecondSampleCount"`
	FirstSeenAt                      time.Time           `json:"firstSeenAt"`
	LastSeenAt                       time.Time           `json:"lastSeenAt"`
	CurrentChannel                   *SessionChannelView `gorm:"-" json:"currentChannel"`
	Resources                        []ResourceSummary   `gorm:"-" json:"resources"`
	SensitiveResourceLeak            bool                `gorm:"-" json:"sensitiveResourceLeak"`
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
	Resources    []ResourceSummary     `json:"resources"`
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
	cutoff, hasCutoff := s.store.detailedLogCutoff(time.Now())

	grouped := applySessionLogFilters(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}), query, cutoff, hasCutoff).
		Select("token_id, " + sessionGroupExpression + " AS group_id").
		Group("token_id, " + sessionGroupExpression)
	var total int64
	if err := s.store.db.WithContext(ctx).Table("(?) AS session_groups", grouped).Count(&total).Error; err != nil {
		return nil, err
	}
	summary, err := aggregateLogSummary(applySessionLogFilters(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}), query, cutoff, hasCutoff))
	if err != nil {
		return nil, err
	}

	items := make([]SessionLogSummary, 0, query.PageSize)
	averageTPS, tpsSampleCount := throughputSQL("", "outcome = 'success'")
	selectSQL := sessionGroupExpression + " AS group_id, " +
		"CASE WHEN codex_session_id <> '' THEN codex_session_id ELSE '' END AS session_id, " +
		"CASE WHEN codex_session_id <> '' THEN 1 ELSE 0 END AS identified, " +
		"CASE WHEN codex_session_id = '' THEN id ELSE '' END AS fallback_request_id, token_id, " +
		"COUNT(*) AS request_count, SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) AS success_count, " +
		"SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END) AS canceled_count, " +
		"SUM(CASE WHEN outcome = 'processing' THEN 1 ELSE 0 END) AS processing_count, " +
		"COALESCE(SUM(attempt_count), 0) AS attempt_count, COALESCE(SUM(CASE WHEN outcome = 'success' THEN input_tokens ELSE 0 END), 0) AS input_tokens, " +
		"COALESCE(SUM(CASE WHEN outcome = 'success' THEN normal_input_tokens ELSE 0 END), 0) AS normal_input_tokens, " +
		"COALESCE(SUM(CASE WHEN outcome = 'success' THEN output_tokens ELSE 0 END), 0) AS output_tokens, COALESCE(SUM(CASE WHEN outcome = 'success' THEN cached_tokens ELSE 0 END), 0) AS cached_tokens, " +
		"COALESCE(SUM(CASE WHEN outcome = 'success' THEN cache_write_tokens ELSE 0 END), 0) AS cache_write_tokens, " +
		"COALESCE(SUM(CASE WHEN outcome = 'success' THEN sent_tokens ELSE 0 END), 0) AS sent_tokens, " +
		"COALESCE(SUM(estimated_cost), 0) AS estimated_cost, COALESCE(SUM(upstream_cost), 0) AS upstream_cost, " +
		"COALESCE(SUM(first_token_ms), 0) AS total_first_token_ms, SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END) AS first_token_sample_count, " +
		"COALESCE(SUM(first_response_ms), 0) AS total_first_response_ms, SUM(CASE WHEN first_response_ms > 0 THEN 1 ELSE 0 END) AS first_response_sample_count, " +
		"COALESCE(SUM(latency_ms), 0) AS total_latency_ms, SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END) AS latency_sample_count, " +
		"COALESCE(SUM(duration_ms), 0) AS total_duration_ms, COUNT(*) AS duration_sample_count, " +
		averageTPS + " AS average_tokens_per_second, " +
		tpsSampleCount + " AS tokens_per_second_sample_count, " +
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
	if err := applySessionLogFilters(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}), query, cutoff, hasCutoff).
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
		sensitive, err := hasSensitiveSessionResources(s.store.db.WithContext(ctx), summary.TokenID, summary.SessionID, summary.FallbackRequestID, summary.Identified)
		if err != nil {
			return nil, err
		}
		summary.SensitiveResourceLeak = sensitive
		items = append(items, summary)
	}
	if err := s.populateSessionRelations(ctx, items); err != nil {
		return nil, err
	}
	return &SessionLogPage{Items: items, Summary: summary, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func applySessionLogFilters(db *gorm.DB, query SessionLogQuery, cutoff time.Time, hasCutoff bool) *gorm.DB {
	if hasCutoff {
		db = db.Where("created_at >= ?", cutoff)
	}
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
	} else if len(query.TokenIDs) > 0 {
		db = db.Where("token_id IN ?", query.TokenIDs)
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
	if (query.SessionID == "" && query.RequestID == "") || (query.SessionID != "" && query.TokenID == 0 && !strings.HasPrefix(query.SessionID, "verification:")) {
		return nil, errors.New("sessionId with tokenId or requestId is required")
	}
	cutoff, hasCutoff := s.store.detailedLogCutoff(time.Now())
	base := applySessionDetailStatus(applySessionIdentity(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}), query), query.Status)
	if hasCutoff {
		base = base.Where("created_at >= ?", cutoff)
	}
	var requestTotal int64
	if err := base.Count(&requestTotal).Error; err != nil {
		return nil, err
	}

	type detailAggregate struct {
		RequestCount               int64
		CompactionCount            int64
		SuccessCount               int64
		CanceledCount              int64
		ProcessingCount            int64
		AttemptCount               int64
		InputTokens                int64
		NormalInputTokens          int64
		OutputTokens               int64
		CachedTokens               int64
		CacheWriteTokens           int64
		SentTokens                 int64
		EstimatedCost              int64
		UpstreamCost               int64
		TotalFirstTokenMS          int64
		FirstTokenSamples          int64
		TotalFirstResponseMS       int64
		FirstResponseSamples       int64
		TotalLatencyMS             int64
		LatencySamples             int64
		TotalDurationMS            int64
		AverageTokensPerSecond     float64
		TokensPerSecondSampleCount int64
		FirstSeenUnix              int64
		LastSeenUnix               int64
	}
	var aggregate detailAggregate
	detailBase := applySessionIdentity(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}), query)
	if hasCutoff {
		detailBase = detailBase.Where("created_at >= ?", cutoff)
	}
	averageTPS, tpsSampleCount := throughputSQL("", "outcome = 'success'")
	if err := detailBase.
		Select("COUNT(*) AS request_count, SUM(CASE WHEN is_compaction AND (outcome = 'success' OR (outcome = '' AND status_code >= 200 AND status_code < 300)) THEN 1 ELSE 0 END) AS compaction_count, " +
			"SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) AS success_count, " +
			"SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END) AS canceled_count, " +
			"SUM(CASE WHEN outcome = 'processing' THEN 1 ELSE 0 END) AS processing_count, " +
			"COALESCE(SUM(attempt_count), 0) AS attempt_count, COALESCE(SUM(CASE WHEN outcome = 'success' THEN input_tokens ELSE 0 END), 0) AS input_tokens, " +
			"COALESCE(SUM(CASE WHEN outcome = 'success' THEN normal_input_tokens ELSE 0 END), 0) AS normal_input_tokens, " +
			"COALESCE(SUM(CASE WHEN outcome = 'success' THEN output_tokens ELSE 0 END), 0) AS output_tokens, COALESCE(SUM(CASE WHEN outcome = 'success' THEN cached_tokens ELSE 0 END), 0) AS cached_tokens, " +
			"COALESCE(SUM(CASE WHEN outcome = 'success' THEN cache_write_tokens ELSE 0 END), 0) AS cache_write_tokens, " +
			"COALESCE(SUM(CASE WHEN outcome = 'success' THEN sent_tokens ELSE 0 END), 0) AS sent_tokens, " +
			"COALESCE(SUM(estimated_cost), 0) AS estimated_cost, COALESCE(SUM(upstream_cost), 0) AS upstream_cost, " +
			"COALESCE(SUM(first_token_ms), 0) AS total_first_token_ms, SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END) AS first_token_samples, " +
			"COALESCE(SUM(first_response_ms), 0) AS total_first_response_ms, SUM(CASE WHEN first_response_ms > 0 THEN 1 ELSE 0 END) AS first_response_samples, " +
			"COALESCE(SUM(latency_ms), 0) AS total_latency_ms, SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END) AS latency_samples, " +
			"COALESCE(SUM(duration_ms), 0) AS total_duration_ms, " +
			averageTPS + " AS average_tokens_per_second, " +
			tpsSampleCount + " AS tokens_per_second_sample_count, " +
			"MIN(unixepoch(created_at)) AS first_seen_unix, MAX(unixepoch(created_at)) AS last_seen_unix").Scan(&aggregate).Error; err != nil {
		return nil, err
	}
	if aggregate.RequestCount == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	summary := SessionLogSummary{
		SessionID:                  query.SessionID,
		Identified:                 query.SessionID != "",
		FallbackRequestID:          query.RequestID,
		TokenID:                    query.TokenID,
		RequestCount:               aggregate.RequestCount,
		SuccessCount:               aggregate.SuccessCount,
		CanceledCount:              aggregate.CanceledCount,
		ProcessingCount:            aggregate.ProcessingCount,
		AttemptCount:               aggregate.AttemptCount,
		InputTokens:                aggregate.InputTokens,
		NormalInputTokens:          aggregate.NormalInputTokens,
		OutputTokens:               aggregate.OutputTokens,
		CachedTokens:               aggregate.CachedTokens,
		CacheWriteTokens:           aggregate.CacheWriteTokens,
		SentTokens:                 aggregate.SentTokens,
		EstimatedCost:              aggregate.EstimatedCost,
		UpstreamCost:               aggregate.UpstreamCost,
		FirstTokenSampleCount:      aggregate.FirstTokenSamples,
		FirstResponseSampleCount:   aggregate.FirstResponseSamples,
		LatencySampleCount:         aggregate.LatencySamples,
		DurationSampleCount:        aggregate.RequestCount,
		AverageTokensPerSecond:     aggregate.AverageTokensPerSecond,
		TokensPerSecondSampleCount: aggregate.TokensPerSecondSampleCount,
		FirstSeenAt:                time.Unix(aggregate.FirstSeenUnix, 0).UTC(),
		LastSeenAt:                 time.Unix(aggregate.LastSeenUnix, 0).UTC(),
	}
	finishSessionSummary(&summary, aggregate.TotalFirstTokenMS, aggregate.TotalLatencyMS, aggregate.TotalDurationMS)
	if summary.FirstResponseSampleCount > 0 {
		summary.AverageFirstResponseMS = float64(aggregate.TotalFirstResponseMS) / float64(summary.FirstResponseSampleCount)
	}
	if err := s.populateSessionSummary(ctx, &summary, cutoff); err != nil {
		return nil, err
	}
	relationSummary := []SessionLogSummary{summary}
	if err := s.populateSessionRelations(ctx, relationSummary); err != nil {
		return nil, err
	}
	summary = relationSummary[0]
	summary.CompactionCount = aggregate.CompactionCount
	resources, err := loadSessionResources(s.store.db.WithContext(ctx), query.TokenID, query.SessionID, query.RequestID, query.SessionID != "", false)
	if err != nil {
		return nil, err
	}
	summary.Resources = resources
	sensitive, err := hasSensitiveSessionResources(s.store.db.WithContext(ctx), query.TokenID, query.SessionID, query.RequestID, query.SessionID != "")
	if err != nil {
		return nil, err
	}
	summary.SensitiveResourceLeak = sensitive
	channels, err := s.sessionChannelUsage(ctx, query, cutoff)
	if err != nil {
		return nil, err
	}

	var logs []RelayRequestLog
	requestBase := applySessionDetailStatus(applySessionIdentity(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}), query), query.Status)
	if hasCutoff {
		requestBase = requestBase.Where("created_at >= ?", cutoff)
	}
	if err := requestBase.
		Omit("request_body", "response_body").
		Order("created_at DESC, id DESC").Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).
		Find(&logs).Error; err != nil {
		return nil, err
	}
	requests, err := s.relayRequestViews(ctx, logs, false, true)
	if err != nil {
		return nil, err
	}
	return &SessionLogDetail{Summary: summary, Channels: channels, Requests: requests, RequestTotal: requestTotal, Page: query.Page, PageSize: query.PageSize, Resources: resources}, nil
}

func (s *ManagementService) sessionChannelUsage(ctx context.Context, query SessionDetailQuery, cutoff time.Time) ([]SessionChannelUsage, error) {
	requestDB := applySessionIdentity(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).Select("id"), query)
	if !cutoff.IsZero() {
		requestDB = requestDB.Where("created_at >= ?", cutoff)
	}
	requestIDs := requestDB
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
	if query.TokenID > 0 {
		return db.Where("token_id = ? AND id = ? AND codex_session_id = ''", query.TokenID, query.RequestID)
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
	latestDB := s.store.db.WithContext(ctx).Model(&RelayRequestLog{})
	firstDB := s.store.db.WithContext(ctx).Model(&RelayRequestLog{})
	if !cutoff.IsZero() {
		latestDB = latestDB.Where("created_at >= ?", cutoff)
		firstDB = firstDB.Where("created_at >= ?", cutoff)
	}
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
			summary.ParentSessionID = state.ParentSessionID
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
	if err := s.populateVerificationAttemptSummary(ctx, summary); err != nil {
		return err
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

func (s *ManagementService) populateVerificationAttemptSummary(ctx context.Context, summary *SessionLogSummary) error {
	if summary == nil || summary.ClientKind != "verification" || !strings.HasPrefix(summary.SessionID, "verification:") {
		return nil
	}
	if !s.store.db.Migrator().HasTable(&VerificationAttempt{}) {
		return nil
	}
	sessionID := strings.TrimPrefix(summary.SessionID, "verification:")
	type verificationTotals struct {
		AttemptCount           int64
		SuccessCount           int64
		FailureCount           int64
		CanceledCount          int64
		TotalTokensPerSecond   float64
		TokensPerSecondSamples int64
	}
	var totals verificationTotals
	if err := s.store.db.WithContext(ctx).Model(&VerificationAttempt{}).Select(
		"COUNT(*) AS attempt_count, "+
			"SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) AS success_count, "+
			"SUM(CASE WHEN outcome = 'failed' THEN 1 ELSE 0 END) AS failure_count, "+
			"SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END) AS canceled_count, "+
			"COALESCE(SUM(CASE WHEN outcome = 'success' AND output_tokens > 0 AND latency_ms > 0 THEN output_tokens * 1000.0 / latency_ms ELSE 0 END), 0) AS total_tokens_per_second, "+
			"SUM(CASE WHEN outcome = 'success' AND output_tokens > 0 AND latency_ms > 0 THEN 1 ELSE 0 END) AS tokens_per_second_samples").
		Where("session_id = ?", sessionID).Scan(&totals).Error; err != nil {
		return err
	}
	if totals.AttemptCount == 0 {
		return nil
	}
	summary.VerificationAttemptSuccessCount = totals.SuccessCount
	summary.VerificationAttemptFailureCount = totals.FailureCount
	summary.VerificationAttemptCanceledCount = totals.CanceledCount
	if summary.RequestCount > 0 {
		summary.SuccessRate = float64(summary.SuccessCount) / float64(summary.RequestCount)
	}
	if totals.TokensPerSecondSamples > 0 {
		summary.AverageTokensPerSecond = totals.TotalTokensPerSecond / float64(totals.TokensPerSecondSamples)
		summary.TokensPerSecondSampleCount = totals.TokensPerSecondSamples
	}
	return nil
}

type sessionRelationKey struct {
	TokenID   uint64
	SessionID string
}

func (s *ManagementService) populateSessionRelations(ctx context.Context, summaries []SessionLogSummary) error {
	conditions := make([]string, 0, len(summaries))
	args := make([]any, 0, len(summaries)*2)
	seen := make(map[sessionRelationKey]struct{}, len(summaries))
	for _, summary := range summaries {
		if !summary.Identified || summary.TokenID == 0 || summary.SessionID == "" {
			continue
		}
		key := sessionRelationKey{TokenID: summary.TokenID, SessionID: summary.SessionID}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		conditions = append(conditions, "(token_id = ? AND parent_session_id = ?)")
		args = append(args, summary.TokenID, summary.SessionID)
	}
	if len(conditions) == 0 {
		return nil
	}
	type childCountRow struct {
		TokenID         uint64
		ParentSessionID string
		ChildCount      int64
	}
	var rows []childCountRow
	if err := s.store.db.WithContext(ctx).Model(&RelaySessionState{}).
		Select("token_id, parent_session_id, COUNT(*) AS child_count").
		Where("("+strings.Join(conditions, " OR ")+")", args...).
		Group("token_id, parent_session_id").Scan(&rows).Error; err != nil {
		return err
	}
	counts := make(map[sessionRelationKey]int64, len(rows))
	for _, row := range rows {
		counts[sessionRelationKey{TokenID: row.TokenID, SessionID: row.ParentSessionID}] = row.ChildCount
	}
	for index := range summaries {
		summaries[index].ChildCount = counts[sessionRelationKey{TokenID: summaries[index].TokenID, SessionID: summaries[index].SessionID}]
	}
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
	if (input.SessionID == "" && input.RequestID == "") || (input.SessionID != "" && input.TokenID == 0 && !strings.HasPrefix(input.SessionID, "verification:")) {
		return errors.New("sessionId with tokenId or requestId is required")
	}
	cutoff, hasCutoff := s.store.detailedLogCutoff(time.Now())
	return s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if input.SessionID == "" {
			result := db.Model(&RelayRequestLog{}).
				Where("id = ? AND codex_session_id = ''", input.RequestID)
			if input.TokenID > 0 {
				result = result.Where("token_id = ?", input.TokenID)
			}
			if hasCutoff {
				result = result.Where("created_at >= ?", cutoff)
			}
			result = result.Update("session_name", input.Title)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
			return nil
		}

		var count int64
		countDB := db.Model(&RelayRequestLog{}).
			Where("token_id = ? AND codex_session_id = ?", input.TokenID, input.SessionID)
		if hasCutoff {
			countDB = countDB.Where("created_at >= ?", cutoff)
		}
		if err := countDB.
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
		Where("r.codex_session_source NOT IN ?", codexAuxiliarySessionSources)
	if !cutoff.IsZero() {
		attemptDB = attemptDB.Where("r.created_at >= ?", cutoff)
	}
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
		Select("a.request_id, a.channel_id, a.channel_name, a.previous_channel_id, a.previous_channel_name, a.selection_reason, a.selection_detail, a.created_at").
		Joins("JOIN relay_request_logs AS r ON r.id = a.request_id").
		Where("r.codex_session_source NOT IN ?", codexAuxiliarySessionSources)
	if !cutoff.IsZero() {
		attemptDB = attemptDB.Where("r.created_at >= ?", cutoff)
	}
	if summary.Identified {
		attemptDB = attemptDB.Where("r.token_id = ? AND r.codex_session_id = ?", summary.TokenID, summary.SessionID)
	} else {
		attemptDB = attemptDB.Where("r.id = ? AND r.codex_session_id = ''", summary.FallbackRequestID)
	}
	var attempts []RelayAttemptLog
	if err := attemptDB.Order("r.created_at ASC, a.created_at ASC, a.id ASC").Scan(&attempts).Error; err != nil {
		return nil, err
	}
	history := make([]SessionChannelMigration, 0)
	var previous RelayAttemptLog
	for _, current := range attempts {
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
	views, err := s.relayRequestViews(ctx, []RelayRequestLog{log}, includePayloads, includeSteps)
	if err != nil {
		return RelayRequestView{}, err
	}
	if len(views) == 0 {
		return RelayRequestView{}, gorm.ErrRecordNotFound
	}
	return views[0], nil
}

func (s *ManagementService) relayRequestViews(ctx context.Context, logs []RelayRequestLog, includePayloads bool, includeSteps bool) ([]RelayRequestView, error) {
	if len(logs) == 0 {
		return []RelayRequestView{}, nil
	}
	db := s.store.db.WithContext(ctx)
	if err := hydrateRelayRequestPayloads(db, logs, includePayloads); err != nil {
		return nil, err
	}
	requestIDs := make([]string, 0, len(logs))
	for _, log := range logs {
		requestIDs = append(requestIDs, log.ID)
	}
	attemptDB := db.Where("request_id IN ?", requestIDs).Omit("request_body", "response_body")
	var allAttempts []RelayAttemptLog
	if err := attemptDB.Order("created_at ASC, id ASC").Find(&allAttempts).Error; err != nil {
		return nil, err
	}
	if includePayloads {
		if err := hydrateRelayAttemptPayloads(db, allAttempts); err != nil {
			return nil, err
		}
	}
	attemptsByRequest := make(map[string][]RelayAttemptLog, len(logs))
	channelIDs := make(map[uint64]struct{})
	for _, attempt := range allAttempts {
		attemptsByRequest[attempt.RequestID] = append(attemptsByRequest[attempt.RequestID], attempt)
		if attempt.ChannelName == "" || attempt.ChannelBaseURL == "" {
			channelIDs[attempt.ChannelID] = struct{}{}
		}
	}
	channels := make(map[uint64]Channel, len(channelIDs))
	if len(channelIDs) > 0 {
		ids := make([]uint64, 0, len(channelIDs))
		for id := range channelIDs {
			ids = append(ids, id)
		}
		var rows []Channel
		if err := db.Where("id IN ?", ids).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, channel := range rows {
			channels[channel.ID] = channel
		}
	}
	stepsByRequest := make(map[string][]RelayStepLog, len(logs))
	if includeSteps {
		var steps []RelayStepLog
		if err := db.Where("request_id IN ?", requestIDs).Order("started_offset_us ASC, id ASC").Find(&steps).Error; err != nil {
			return nil, err
		}
		for _, step := range steps {
			stepsByRequest[step.RequestID] = append(stepsByRequest[step.RequestID], step)
		}
	}
	resourcesByRequest, err := loadRequestResourcesBatch(db, requestIDs, includePayloads)
	if err != nil {
		return nil, err
	}
	sensitiveByRequest := make(map[string]bool)
	var sensitiveRows []struct{ RequestID string }
	if err := db.Table("request_resources rr").Select("DISTINCT rr.request_id").
		Joins("JOIN gateway_resources r ON r.id = rr.resource_id").
		Where("rr.request_id IN ? AND r.sensitive = ?", requestIDs, true).
		Where("r.kind <> ? OR r.sha256 <> ''", "file").Scan(&sensitiveRows).Error; err != nil {
		return nil, err
	}
	for _, row := range sensitiveRows {
		sensitiveByRequest[row.RequestID] = true
	}
	views := make([]RelayRequestView, 0, len(logs))
	for index := range logs {
		log := logs[index]
		if log.Outcome == RelayOutcomeSuccess || log.Outcome == "" && log.StatusCode >= 200 && log.StatusCode < 300 {
			log.TokensPerSecond, log.GenerationDurationMS = normalizedTokensPerSecond(
				log.OutputTokens, log.DurationMS, log.LatencyMS, log.FirstTokenMS, log.TokensPerSecond,
			)
		}
		if includePayloads {
			log.RequestBody = decompressStoredPayload(log.RequestBody)
			log.ResponseBody = decompressStoredPayload(log.ResponseBody)
		}
		attempts := attemptsByRequest[log.ID]
		if attempts == nil {
			attempts = make([]RelayAttemptLog, 0)
		}
		for attemptIndex := range attempts {
			if attempts[attemptIndex].Success {
				attempts[attemptIndex].TokensPerSecond, attempts[attemptIndex].GenerationDurationMS = normalizedTokensPerSecond(
					attempts[attemptIndex].OutputTokens,
					attempts[attemptIndex].DurationMS,
					attempts[attemptIndex].LatencyMS,
					attempts[attemptIndex].FirstTokenMS,
					attempts[attemptIndex].TokensPerSecond,
				)
			}
			if includePayloads {
				attempts[attemptIndex].RequestBody = decompressStoredPayload(attempts[attemptIndex].RequestBody)
				attempts[attemptIndex].ResponseBody = decompressStoredPayload(attempts[attemptIndex].ResponseBody)
			}
			if attempts[attemptIndex].RouteDecisionJSON != "" {
				var decision RouteDecision
				if json.Unmarshal([]byte(attempts[attemptIndex].RouteDecisionJSON), &decision) == nil {
					attempts[attemptIndex].RouteDecision = &decision
				}
			}
			if channel, ok := channels[attempts[attemptIndex].ChannelID]; ok {
				if attempts[attemptIndex].ChannelName == "" {
					attempts[attemptIndex].ChannelName = channel.Name
				}
				if attempts[attemptIndex].ChannelBaseURL == "" {
					attempts[attemptIndex].ChannelBaseURL = channel.BaseURL
				}
			}
			attempts[attemptIndex].APIPath = relayAPIPath(attempts[attemptIndex].ChannelBaseURL, log.Endpoint)
		}
		parameters := decodeRequestParameters(log.RequestParametersJSON)
		apiPath := relayAPIPath("", log.Endpoint)
		if len(attempts) > 0 {
			apiPath = attempts[0].APIPath
		}
		steps := stepsByRequest[log.ID]
		if steps == nil {
			steps = make([]RelayStepLog, 0)
		}
		views = append(views, RelayRequestView{
			RelayRequestLog:       log,
			RequestParameters:     parameters,
			APIPath:               apiPath,
			ReasoningEffort:       requestReasoningEffort(parameters),
			Attempts:              attempts,
			Steps:                 steps,
			Resources:             resourcesByRequest[log.ID],
			SensitiveResourceLeak: sensitiveByRequest[log.ID],
		})
	}
	return views, nil
}

// RedactTokenScopedLogView removes operational configuration that is unrelated
// to the access token's own request result.
func RedactTokenScopedLogView(view *RelayRequestView) {
	if view == nil {
		return
	}
	view.APIPath = ""
	view.Resources = []ResourceView{}
	view.SensitiveResourceLeak = false
	view.Steps = []RelayStepLog{}
	for index := range view.Attempts {
		attempt := &view.Attempts[index]
		attempt.ChannelID = 0
		attempt.ChannelModelID = 0
		attempt.ChannelBaseURL = ""
		attempt.APIPath = ""
		attempt.PreviousChannelID = 0
		attempt.PreviousChannelName = ""
		attempt.SelectionReason = ""
		attempt.SelectionDetail = ""
		attempt.RouteDecisionJSON = ""
		attempt.RouteDecision = nil
	}
}

func RedactTokenScopedLogPage(page *LogPage) {
	if page == nil {
		return
	}
	for index := range page.Items {
		RedactTokenScopedLogView(&page.Items[index])
	}
}

// RedactTokenScopedSessionSummary removes global resource and live routing state
// while retaining the actual model, channel name, usage, and result for the session.
func RedactTokenScopedSessionSummary(summary *SessionLogSummary) {
	if summary == nil {
		return
	}
	summary.Resources = []ResourceSummary{}
	summary.SensitiveResourceLeak = false
	if summary.CurrentChannel != nil {
		channel := summary.CurrentChannel
		channel.ChannelID = 0
		channel.ChannelBaseURL = ""
		channel.ChannelModelID = 0
		channel.AssignmentSource = ""
		channel.Enabled = false
		channel.MappingEnabled = false
		channel.CircuitOpenUntil = nil
		channel.MigrationHistory = []SessionChannelMigration{}
	}
}

func RedactTokenScopedSessionPage(page *SessionLogPage) {
	if page == nil {
		return
	}
	for index := range page.Items {
		RedactTokenScopedSessionSummary(&page.Items[index])
	}
}

func RedactTokenScopedActiveSessionPage(page *ActiveSessionPage) {
	if page == nil {
		return
	}
	for index := range page.Items {
		RedactTokenScopedSessionSummary(&page.Items[index])
	}
}

func RedactTokenScopedSessionDetail(detail *SessionLogDetail) {
	if detail == nil {
		return
	}
	RedactTokenScopedSessionSummary(&detail.Summary)
	detail.Resources = []ResourceSummary{}
	for index := range detail.Channels {
		detail.Channels[index].ChannelID = 0
	}
	for index := range detail.Requests {
		RedactTokenScopedLogView(&detail.Requests[index])
	}
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
