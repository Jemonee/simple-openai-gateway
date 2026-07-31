package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestActiveSessionsOnlyReturnsRecentUserSessions(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	logs := []RelayRequestLog{
		{ID: "active-user-old", TokenID: 7, TokenName: "user-token", TokenKeyPrefix: "sk-user", Endpoint: "responses", RequestedModel: "gpt-active", CodexSessionID: "active-user", CodexSessionSource: "prompt_cache_key", SessionName: "Active user", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, AttemptCount: 1, InputTokens: 100, NormalInputTokens: 60, OutputTokens: 20, CachedTokens: 30, CacheWriteTokens: 10, SentTokens: 70, EstimatedCost: 120, UpstreamCost: 100, FirstTokenMS: 25, LatencyMS: 40, DurationMS: 100, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "active-user-recent", TokenID: 7, TokenName: "user-token", TokenKeyPrefix: "sk-user", Endpoint: "responses", RequestedModel: "gpt-active", CodexSessionID: "active-user", CodexSessionSource: "prompt_cache_key", SessionName: "Active user", StatusCode: http.StatusOK, Outcome: RelayOutcomeProcessing, CreatedAt: now.Add(-5 * time.Minute)},
		{ID: "earlier-active-user", TokenID: 7, TokenName: "user-token", TokenKeyPrefix: "sk-user", Endpoint: "responses", RequestedModel: "gpt-active", CodexSessionID: "earlier-active-user", CodexSessionSource: "prompt_cache_key", SessionName: "Earlier active user", StatusCode: http.StatusBadGateway, Outcome: RelayOutcomeFailed, AttemptCount: 1, CreatedAt: now.Add(-20 * time.Minute)},
		{ID: "inactive-user", TokenID: 7, TokenName: "user-token", TokenKeyPrefix: "sk-user", Endpoint: "responses", RequestedModel: "gpt-active", CodexSessionID: "inactive-user", CodexSessionSource: "prompt_cache_key", SessionName: "Inactive user", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now.Add(-31 * time.Minute)},
		{ID: "active-ambient", TokenID: 7, TokenName: "user-token", TokenKeyPrefix: "sk-user", Endpoint: "responses", RequestedModel: "gpt-active", CodexSessionID: "active-ambient", CodexSessionSource: "prompt_cache_key", SessionName: "Active ambient", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now.Add(-time.Minute)},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	attempts := []RelayAttemptLog{
		{RequestID: "active-user-old", ChannelID: 11, ChannelName: "active-channel", ChannelBaseURL: "https://upstream.example/v1", ChannelModelID: 22, UpstreamModel: "gpt-upstream", StatusCode: http.StatusOK, Success: true, CreatedAt: now.Add(-2*time.Hour + time.Second)},
		{RequestID: "earlier-active-user", ChannelID: 12, ChannelName: "failed-channel", ChannelModelID: 23, UpstreamModel: "gpt-upstream", StatusCode: http.StatusBadGateway, Success: false, CreatedAt: now.Add(-20*time.Minute + time.Second)},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	states := []RelaySessionState{
		{TokenID: 7, SessionID: "active-user", Title: "Active user", ThreadSource: codexThreadSourceUser, LatestRequestID: "active-user-recent", LastActivityAt: timePointer(now.Add(-5 * time.Minute)), CompactionCount: 2, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-5 * time.Minute)},
		{TokenID: 7, SessionID: "earlier-active-user", Title: "Earlier active user", ThreadSource: codexThreadSourceUser, LatestRequestID: "earlier-active-user", LastActivityAt: timePointer(now.Add(-20 * time.Minute)), CreatedAt: now.Add(-20 * time.Minute), UpdatedAt: now.Add(-20 * time.Minute)},
		{TokenID: 7, SessionID: "inactive-user", Title: "Inactive user", ThreadSource: codexThreadSourceUser, LatestRequestID: "inactive-user", LastActivityAt: timePointer(now.Add(-31 * time.Minute)), CreatedAt: now.Add(-31 * time.Minute), UpdatedAt: now.Add(-31 * time.Minute)},
		{TokenID: 7, SessionID: "active-ambient", Title: "Active ambient", ThreadSource: codexThreadSourceAmbient, LatestRequestID: "active-ambient", LastActivityAt: timePointer(now.Add(-time.Minute)), CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)},
	}
	if err := store.db.Create(&states).Error; err != nil {
		t.Fatal(err)
	}

	page, err := NewManagementService(store).ActiveSessions(context.Background(), ActiveSessionQuery{
		ActiveSince: now.Add(-30 * time.Minute),
		Page:        1,
		PageSize:    25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("active user sessions = %+v", page)
	}
	if page.Items[0].SessionID != "active-user" || page.Items[1].SessionID != "earlier-active-user" {
		t.Fatalf("active user order = %+v", page.Items)
	}
	if page.Items[0].ThreadSource != codexThreadSourceUser || page.Items[0].LatestModel != "gpt-active" {
		t.Fatalf("active session identity = %+v", page.Items[0])
	}
	if page.Items[0].RequestCount != 2 || page.Items[1].RequestCount != 1 {
		t.Fatalf("active session request counts = %+v", page.Items)
	}
	active := page.Items[0]
	if active.SuccessCount != 1 || active.ProcessingCount != 1 || active.AttemptCount != 1 || active.SuccessRate != 1 {
		t.Fatalf("active session outcomes = %+v", active)
	}
	if active.CompactionCount != 2 || active.InputTokens != 100 || active.NormalInputTokens != 60 || active.OutputTokens != 20 || active.CachedTokens != 30 || active.CacheWriteTokens != 10 || active.SentTokens != 70 {
		t.Fatalf("active session usage = %+v", active)
	}
	if active.EstimatedCost != 120 || active.UpstreamCost != 100 || active.CacheHitRate != 0.3 {
		t.Fatalf("active session cost and cache metrics = %+v", active)
	}
	if active.FirstTokenSampleCount != 1 || active.AverageFirstTokenMS != 25 || active.LatencySampleCount != 1 || active.AverageLatencyMS != 40 || active.DurationSampleCount != 1 || active.AverageDurationMS != 100 {
		t.Fatalf("active session timing metrics = %+v", active)
	}
	if active.CurrentChannel == nil || active.CurrentChannel.ChannelName != "active-channel" || active.CurrentChannel.AssignmentSource != "latest_successful_attempt" {
		t.Fatalf("active session channel = %+v", active.CurrentChannel)
	}
	if page.Items[1].SuccessCount != 0 || page.Items[1].SuccessRate != 0 || page.Items[1].AttemptCount != 1 {
		t.Fatalf("failed active session outcomes = %+v", page.Items[1])
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func TestBackfillRelaySessionActivity(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.db.Create(&RelayRequestLog{
		ID: "backfill-activity-request", TokenID: 7, Endpoint: "responses", RequestedModel: "model",
		CodexSessionID: "backfill-activity-session", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess,
		CreatedAt: now.Add(-12 * time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelaySessionState{
		TokenID: 7, SessionID: "backfill-activity-session", ThreadSource: codexThreadSourceUser,
		LatestRequestID: "backfill-activity-request", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.backfillRelaySessionActivity(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillRelaySessionActivity(); err != nil {
		t.Fatal(err)
	}
	var state RelaySessionState
	if err := store.db.First(&state, "token_id = ? AND session_id = ?", 7, "backfill-activity-session").Error; err != nil {
		t.Fatal(err)
	}
	if state.LastActivityAt == nil || !state.LastActivityAt.Equal(now.Add(-12*time.Minute)) {
		t.Fatalf("backfilled activity = %v", state.LastActivityAt)
	}
	var indexCount int64
	if err := store.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", "idx_relay_session_active").Scan(&indexCount).Error; err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 {
		t.Fatalf("active session index count = %d", indexCount)
	}
}

func TestActiveSessionQueryUsesActivityIndex(t *testing.T) {
	store := newTestStore(t)
	type queryPlanRow struct {
		Detail string
	}
	var plan []queryPlanRow
	if err := store.db.Raw(`
		EXPLAIN QUERY PLAN
		SELECT session_state.session_id
		FROM relay_session_states AS session_state
		JOIN relay_request_logs AS latest_request ON latest_request.id = session_state.latest_request_id
		WHERE session_state.thread_source = ? AND session_state.last_activity_at >= ?
		ORDER BY session_state.last_activity_at DESC, session_state.session_id ASC
		LIMIT 25
	`, codexThreadSourceUser, time.Now().UTC().Add(-30*time.Minute)).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	details := make([]string, 0, len(plan))
	for _, row := range plan {
		details = append(details, row.Detail)
	}
	if combined := strings.Join(details, "\n"); !strings.Contains(combined, "idx_relay_session_active") {
		t.Fatalf("active session query plan does not use activity index:\n%s", combined)
	}
}
