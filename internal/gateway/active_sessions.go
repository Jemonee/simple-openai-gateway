package gateway

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

type ActiveSessionQuery struct {
	Session     string
	ActiveSince time.Time
	Page        int
	PageSize    int
}

type ActiveSessionPage struct {
	Items    []SessionLogSummary `json:"items"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
}

type activeSessionKey struct {
	TokenID   uint64
	SessionID string
}

type activeSessionAggregate struct {
	SessionLogSummary
	TotalFirstTokenMS int64
	TotalLatencyMS    int64
	TotalDurationMS   int64
	FirstSeenUnix     int64
	LastSeenUnix      int64
}

func activeSessionRows(db *gorm.DB, query ActiveSessionQuery) *gorm.DB {
	db = db.Table("relay_session_states AS session_state").
		Joins("JOIN relay_request_logs AS latest_request ON latest_request.id = session_state.latest_request_id").
		Joins("LEFT JOIN client_tokens AS client_token ON client_token.id = session_state.token_id").
		Where("session_state.thread_source = ?", codexThreadSourceUser).
		Where("session_state.last_activity_at >= ?", utcQueryTime(query.ActiveSince))
	if value := strings.TrimSpace(query.Session); value != "" {
		pattern := "%" + value + "%"
		db = db.Where(
			"(session_state.title LIKE ? OR session_state.session_id LIKE ? OR latest_request.id LIKE ?)",
			pattern, pattern, pattern,
		)
	}
	return db
}

func (s *ManagementService) ActiveSessions(ctx context.Context, query ActiveSessionQuery) (*ActiveSessionPage, error) {
	// Page from materialized session state first, then aggregate only the visible sessions.
	normalizeSessionPage(&query.Page, &query.PageSize)
	if query.ActiveSince.IsZero() {
		query.ActiveSince = time.Now().UTC().Add(-30 * time.Minute)
	}
	detailCutoff := time.Now().UTC().Add(-DetailedLogRetentionDays * 24 * time.Hour)

	var total int64
	if err := activeSessionRows(s.store.db.WithContext(ctx), query).Count(&total).Error; err != nil {
		return nil, err
	}

	items := make([]SessionLogSummary, 0, query.PageSize)
	if err := activeSessionRows(s.store.db.WithContext(ctx), query).
		Select(
			"session_state.session_id AS session_id, " +
				"COALESCE(NULLIF(session_state.title, ''), latest_request.session_name) AS session_name, " +
				"COALESCE(NULLIF(session_state.session_source, ''), NULLIF(latest_request.codex_session_source, ''), 'unavailable') AS session_source, " +
				"COALESCE(NULLIF(session_state.client_kind, ''), latest_request.client_kind) AS client_kind, " +
				"session_state.thread_source AS thread_source, 1 AS identified, session_state.token_id AS token_id, " +
				"session_state.compaction_count AS compaction_count, " +
				"COALESCE(NULLIF(latest_request.token_name, ''), client_token.name) AS token_name, " +
				"COALESCE(NULLIF(latest_request.token_key_prefix, ''), client_token.key_prefix) AS token_key_prefix, " +
				"latest_request.requested_model AS latest_model, latest_request.endpoint AS latest_endpoint, " +
				"session_state.created_at AS first_seen_at, session_state.last_activity_at AS last_seen_at",
		).
		Order("session_state.last_activity_at DESC, session_state.session_id ASC").
		Offset((query.Page - 1) * query.PageSize).
		Limit(query.PageSize).
		Scan(&items).Error; err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return &ActiveSessionPage{Items: items, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
	}

	conditions := make([]string, 0, len(items))
	conditionArgs := make([]any, 0, len(items)*2)
	for _, item := range items {
		conditions = append(conditions, "(token_id = ? AND codex_session_id = ?)")
		conditionArgs = append(conditionArgs, item.TokenID, item.SessionID)
	}

	aggregates := make([]activeSessionAggregate, 0, len(items))
	if err := s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).
		Where("created_at >= ?", detailCutoff).
		Where("("+strings.Join(conditions, " OR ")+")", conditionArgs...).
		Select("token_id, codex_session_id AS session_id, COUNT(*) AS request_count, " +
			"SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) AS success_count, " +
			"SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END) AS canceled_count, " +
			"SUM(CASE WHEN outcome = 'processing' THEN 1 ELSE 0 END) AS processing_count, " +
			"COALESCE(SUM(attempt_count), 0) AS attempt_count, COALESCE(SUM(input_tokens), 0) AS input_tokens, " +
			"COALESCE(SUM(normal_input_tokens), 0) AS normal_input_tokens, " +
			"COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(cached_tokens), 0) AS cached_tokens, " +
			"COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens, COALESCE(SUM(sent_tokens), 0) AS sent_tokens, " +
			"COALESCE(SUM(estimated_cost), 0) AS estimated_cost, COALESCE(SUM(upstream_cost), 0) AS upstream_cost, " +
			"COALESCE(SUM(first_token_ms), 0) AS total_first_token_ms, SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END) AS first_token_sample_count, " +
			"COALESCE(SUM(latency_ms), 0) AS total_latency_ms, SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END) AS latency_sample_count, " +
			"COALESCE(SUM(duration_ms), 0) AS total_duration_ms, " +
			"MIN(unixepoch(created_at)) AS first_seen_unix, MAX(unixepoch(created_at)) AS last_seen_unix").
		Group("token_id, codex_session_id").Scan(&aggregates).Error; err != nil {
		return nil, err
	}

	aggregatesBySession := make(map[activeSessionKey]activeSessionAggregate, len(aggregates))
	for _, aggregate := range aggregates {
		aggregatesBySession[activeSessionKey{TokenID: aggregate.TokenID, SessionID: aggregate.SessionID}] = aggregate
	}
	for index := range items {
		aggregate, exists := aggregatesBySession[activeSessionKey{TokenID: items[index].TokenID, SessionID: items[index].SessionID}]
		if !exists {
			continue
		}
		items[index].RequestCount = aggregate.RequestCount
		items[index].SuccessCount = aggregate.SuccessCount
		items[index].CanceledCount = aggregate.CanceledCount
		items[index].ProcessingCount = aggregate.ProcessingCount
		items[index].AttemptCount = aggregate.AttemptCount
		items[index].InputTokens = aggregate.InputTokens
		items[index].NormalInputTokens = aggregate.NormalInputTokens
		items[index].OutputTokens = aggregate.OutputTokens
		items[index].CachedTokens = aggregate.CachedTokens
		items[index].CacheWriteTokens = aggregate.CacheWriteTokens
		items[index].SentTokens = aggregate.SentTokens
		items[index].EstimatedCost = aggregate.EstimatedCost
		items[index].UpstreamCost = aggregate.UpstreamCost
		items[index].FirstTokenSampleCount = aggregate.FirstTokenSampleCount
		items[index].LatencySampleCount = aggregate.LatencySampleCount
		items[index].FirstSeenAt = time.Unix(aggregate.FirstSeenUnix, 0).UTC()
		items[index].LastSeenAt = time.Unix(aggregate.LastSeenUnix, 0).UTC()
		finishSessionSummary(&items[index], aggregate.TotalFirstTokenMS, aggregate.TotalLatencyMS, aggregate.TotalDurationMS)
		current, err := s.currentSessionChannel(ctx, items[index], items[index].LatestModel, detailCutoff)
		if err != nil {
			return nil, err
		}
		items[index].CurrentChannel = current
	}
	return &ActiveSessionPage{Items: items, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}
