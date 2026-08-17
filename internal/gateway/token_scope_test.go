package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestManagementQueriesEnforceTokenScope(t *testing.T) {
	store := newTestStore(t)
	management := NewManagementService(store)
	ctx := context.Background()
	now := time.Now().UTC()
	tokenA := ClientToken{Name: "token-a", KeyHash: hashSecret("sk-token-a"), KeyPrefix: "sk-token-a", Enabled: true, AllowAllModels: true, RPM: 60, MaxConcurrency: 4}
	tokenB := ClientToken{Name: "token-b", KeyHash: hashSecret("sk-token-b"), KeyPrefix: "sk-token-b", Enabled: true, AllowAllModels: true, RPM: 60, MaxConcurrency: 4}
	if err := store.db.Create(&tokenA).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&tokenB).Error; err != nil {
		t.Fatal(err)
	}
	logs := []RelayRequestLog{
		{ID: "token-a-session-request", TokenID: tokenA.ID, TokenName: tokenA.Name, TokenKeyPrefix: tokenA.KeyPrefix, Endpoint: "responses", RequestedModel: "model-a", CodexSessionID: "session-a", SessionName: "Session A", StatusCode: 200, Outcome: RelayOutcomeSuccess, AttemptCount: 1, InputTokens: 11, OutputTokens: 2, DurationMS: 10, CreatedAt: now.Add(-4 * time.Minute)},
		{ID: "token-a-fallback", TokenID: tokenA.ID, TokenName: tokenA.Name, TokenKeyPrefix: tokenA.KeyPrefix, Endpoint: "chat", RequestedModel: "model-a", SessionName: "Fallback A", StatusCode: 200, Outcome: RelayOutcomeSuccess, AttemptCount: 1, InputTokens: 13, OutputTokens: 3, DurationMS: 12, CreatedAt: now.Add(-3 * time.Minute)},
		{ID: "token-b-session-request", TokenID: tokenB.ID, TokenName: tokenB.Name, TokenKeyPrefix: tokenB.KeyPrefix, Endpoint: "responses", RequestedModel: "model-b", CodexSessionID: "session-b", SessionName: "Session B", StatusCode: 200, Outcome: RelayOutcomeSuccess, AttemptCount: 1, InputTokens: 101, OutputTokens: 20, DurationMS: 30, CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "token-b-fallback", TokenID: tokenB.ID, TokenName: tokenB.Name, TokenKeyPrefix: tokenB.KeyPrefix, Endpoint: "chat", RequestedModel: "model-b", SessionName: "Fallback B", StatusCode: 500, Outcome: RelayOutcomeFailed, AttemptCount: 1, DurationMS: 20, CreatedAt: now.Add(-time.Minute)},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	activeAt := now.Add(-time.Minute)
	states := []RelaySessionState{
		{TokenID: tokenA.ID, SessionID: "session-a", Title: "Session A", ThreadSource: codexThreadSourceUser, LatestRequestID: logs[0].ID, LastActivityAt: &activeAt, PrimaryModel: "model-a", CreatedAt: now.Add(-4 * time.Minute), UpdatedAt: activeAt},
		{TokenID: tokenB.ID, SessionID: "session-b", Title: "Session B", ThreadSource: codexThreadSourceUser, LatestRequestID: logs[2].ID, LastActivityAt: &activeAt, PrimaryModel: "model-b", CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: activeAt},
	}
	if err := store.db.Create(&states).Error; err != nil {
		t.Fatal(err)
	}

	dashboard, err := management.Dashboard(ctx, 1, tokenA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Requests != 2 || dashboard.InputTokens != 24 || dashboard.OutputTokens != 5 {
		t.Fatalf("token-a dashboard leaked another token: %+v", dashboard)
	}
	logPage, err := management.Logs(ctx, LogQuery{TokenID: tokenA.ID, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if logPage.Total != 2 || logPage.Summary.RequestCount != 2 {
		t.Fatalf("token-a log page = %+v", logPage)
	}
	for _, item := range logPage.Items {
		if item.TokenID != tokenA.ID {
			t.Fatalf("log page contains token %d", item.TokenID)
		}
	}
	if _, err := management.LogDetail(ctx, logs[2].ID, tokenA.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-token log detail error = %v", err)
	}

	sessionPage, err := management.SessionLogs(ctx, SessionLogQuery{TokenID: tokenA.ID, Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if sessionPage.Total != 2 || sessionPage.Summary.RequestCount != 2 {
		t.Fatalf("token-a session page = %+v", sessionPage)
	}
	for _, item := range sessionPage.Items {
		if item.TokenID != tokenA.ID {
			t.Fatalf("session page contains token %d", item.TokenID)
		}
	}
	detail, err := management.SessionLogDetail(ctx, SessionDetailQuery{TokenID: tokenA.ID, SessionID: "session-a", Page: 1, PageSize: 25})
	if err != nil || detail.RequestTotal != 1 || detail.Summary.TokenID != tokenA.ID {
		t.Fatalf("token-a session detail = %+v, error = %v", detail, err)
	}
	if _, err := management.SessionLogDetail(ctx, SessionDetailQuery{TokenID: tokenA.ID, SessionID: "session-b", Page: 1, PageSize: 25}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-token session detail error = %v", err)
	}
	if _, err := management.SessionLogDetail(ctx, SessionDetailQuery{TokenID: tokenA.ID, RequestID: logs[3].ID, Page: 1, PageSize: 25}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-token fallback detail error = %v", err)
	}
	if err := management.RenameSession(ctx, SessionTitleInput{TokenID: tokenA.ID, SessionID: "session-b", Title: "stolen"}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-token session rename error = %v", err)
	}
	if err := management.RenameSession(ctx, SessionTitleInput{TokenID: tokenA.ID, RequestID: logs[3].ID, Title: "stolen"}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-token fallback rename error = %v", err)
	}
	var untouched RelayRequestLog
	if err := store.db.First(&untouched, "id = ?", logs[3].ID).Error; err != nil || untouched.SessionName != "Fallback B" {
		t.Fatalf("cross-token rename changed fallback: %+v, error = %v", untouched, err)
	}

	active, err := management.ActiveSessions(ctx, ActiveSessionQuery{TokenID: tokenA.ID, Sources: []string{codexThreadSourceUser}, ActiveSince: now.Add(-30 * time.Minute), Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if active.Total != 1 || len(active.Items) != 1 || active.Items[0].TokenID != tokenA.ID {
		t.Fatalf("token-a active sessions = %+v", active)
	}
}

func TestTokenScopedResponsesRemoveGlobalRoutingAndResources(t *testing.T) {
	view := RelayRequestView{
		APIPath:               "/v1/responses",
		Resources:             []ResourceView{{}},
		SensitiveResourceLeak: true,
		Steps:                 []RelayStepLog{{}},
		Attempts: []RelayAttemptLog{{
			ChannelID: 7, ChannelName: "actual-channel", ChannelBaseURL: "https://private.example/v1",
			ChannelModelID: 9, UpstreamModel: "actual-model", APIPath: "/v1/responses",
			PreviousChannelID: 6, PreviousChannelName: "other-channel", SelectionReason: "retryable_status",
			SelectionDetail: "private detail", RouteDecisionJSON: `{\"strategy\":\"lowest_cost\"}`, RouteDecision: &RouteDecision{},
		}},
	}
	RedactTokenScopedLogView(&view)
	if view.APIPath != "" || len(view.Resources) != 0 || view.SensitiveResourceLeak || len(view.Steps) != 0 {
		t.Fatalf("request redaction incomplete: %+v", view)
	}
	attempt := view.Attempts[0]
	if attempt.ChannelName != "actual-channel" || attempt.UpstreamModel != "actual-model" {
		t.Fatalf("actual request result was removed: %+v", attempt)
	}
	if attempt.ChannelID != 0 || attempt.ChannelModelID != 0 || attempt.ChannelBaseURL != "" || attempt.APIPath != "" || attempt.PreviousChannelID != 0 || attempt.PreviousChannelName != "" || attempt.SelectionReason != "" || attempt.SelectionDetail != "" || attempt.RouteDecisionJSON != "" || attempt.RouteDecision != nil {
		t.Fatalf("routing configuration was not redacted: %+v", attempt)
	}

	summary := SessionLogSummary{
		Resources: []ResourceSummary{{}}, SensitiveResourceLeak: true,
		CurrentChannel: &SessionChannelView{
			ChannelID: 7, ChannelName: "actual-channel", ChannelBaseURL: "https://private.example/v1",
			ChannelModelID: 9, UpstreamModel: "actual-model", AssignmentSource: "session_affinity",
			Enabled: true, MappingEnabled: true, CircuitOpenUntil: func() *time.Time { value := time.Now().Add(time.Hour); return &value }(),
			MigrationHistory: []SessionChannelMigration{{FromChannelID: 6, ToChannelID: 7}},
		},
	}
	RedactTokenScopedSessionSummary(&summary)
	if summary.SensitiveResourceLeak || len(summary.Resources) != 0 || summary.CurrentChannel == nil {
		t.Fatalf("session redaction incomplete: %+v", summary)
	}
	channel := summary.CurrentChannel
	if channel.ChannelName != "actual-channel" || channel.UpstreamModel != "actual-model" {
		t.Fatalf("actual session route was removed: %+v", channel)
	}
	if channel.ChannelID != 0 || channel.ChannelModelID != 0 || channel.ChannelBaseURL != "" || channel.AssignmentSource != "" || channel.Enabled || channel.MappingEnabled || channel.CircuitOpenUntil != nil || len(channel.MigrationHistory) != 0 {
		t.Fatalf("live channel state was not redacted: %+v", channel)
	}
}
