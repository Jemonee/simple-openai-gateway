package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestParseRelayPayloadDetectsCodexCompactionRequests(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		want        bool
		wantTrigger string
	}{
		{
			name: "string metadata",
			body: `{"model":"gpt-5.6-sol","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"compaction\",\"compaction\":{\"trigger\":\"auto\"}}"}}`,
			want: true, wantTrigger: "auto",
		},
		{
			name: "object metadata",
			body: `{"model":"gpt-5.6-sol","client_metadata":{"x-codex-turn-metadata":{"request_kind":"compaction","compaction":{"trigger":"manual"}}}}`,
			want: true, wantTrigger: "manual",
		},
		{
			name: "normal turn",
			body: `{"model":"gpt-5.6-sol","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\"}"}}`,
		},
		{
			name: "malformed metadata",
			body: `{"model":"gpt-5.6-sol","client_metadata":{"x-codex-turn-metadata":"not-json"}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := ParseRelayPayload([]byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if payload.IsCompactionRequest != test.want {
				t.Fatalf("IsCompactionRequest = %v, want %v", payload.IsCompactionRequest, test.want)
			}
			if payload.CompactionTrigger != test.wantTrigger {
				t.Fatalf("CompactionTrigger = %q, want %q", payload.CompactionTrigger, test.wantTrigger)
			}
		})
	}
}

func TestRecordRequestPersistsAndInheritsModelAgentContextWindow(t *testing.T) {
	store := newTestStore(t)
	token := ClientToken{
		Name: "codex", KeyHash: hashSecret("sk-context-window"), KeyPrefix: "sk-context-window",
		Enabled: true, AllowAllModels: true, RPM: 60, MaxConcurrency: 10,
	}
	if err := store.db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	relay := newTestRelay(store)
	fingerprint := "codex-agent-profile"

	record := func(requestID string, sessionID string, model string, requestKind string, trigger string, inputTokens int64) {
		t.Helper()
		compaction := ""
		if trigger != "" {
			compaction = `,"compaction":{"trigger":"` + trigger + `"}`
		}
		body := []byte(`{"model":"` + model + `","prompt_cache_key":"` + sessionID + `","client_metadata":{"x-codex-turn-metadata":{"request_kind":"` + requestKind + `"` + compaction + `}},"input":"test"}`)
		payload, err := ParseRelayPayload(body)
		if err != nil {
			t.Fatal(err)
		}
		payload.ClientKind = agentClientKindCodex
		payload.ClientFingerprint = fingerprint
		execution := &relayExecution{
			requestID: requestID, token: &token, endpoint: "responses", payload: payload, rawBody: body,
			startedAt: time.Now().UTC().Add(-time.Second), usage: Usage{InputTokens: inputTokens, Source: "upstream"},
		}
		relay.recordRequestStarted(context.Background(), execution)
		relay.recordRequest(context.Background(), execution, http.StatusOK, "")
	}

	record("session-sample", "session-with-sample", "gpt-5.6-sol", "compaction", "auto", 237680)
	var sampled RelaySessionState
	if err := store.db.First(&sampled, "token_id = ? AND session_id = ?", token.ID, "session-with-sample").Error; err != nil {
		t.Fatal(err)
	}
	if sampled.PrimaryModel != "gpt-5.6-sol" || sampled.ContextWindowTokens != 258000 || sampled.ContextWindowSource != contextWindowSourceSession || sampled.ContextWindowSamples != 1 {
		t.Fatalf("sampled session state = %+v", sampled)
	}

	record("inherited-main", "session-without-sample", "gpt-5.6-sol", "turn", "", 1200)
	record("inherited-review", "session-without-sample", contextWindowIgnoredModel, "turn", "", 800)
	var inherited RelaySessionState
	if err := store.db.First(&inherited, "token_id = ? AND session_id = ?", token.ID, "session-without-sample").Error; err != nil {
		t.Fatal(err)
	}
	if inherited.PrimaryModel != "gpt-5.6-sol" || inherited.ContextWindowTokens != 258000 || inherited.ContextWindowSource != contextWindowSourceAgentModel || inherited.ContextWindowSamples != 1 {
		t.Fatalf("inherited session state = %+v", inherited)
	}
	management := NewManagementService(store)
	page, err := management.SessionLogs(context.Background(), SessionLogQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	var inheritedSummary *SessionLogSummary
	for index := range page.Items {
		if page.Items[index].SessionID == inherited.SessionID {
			inheritedSummary = &page.Items[index]
			break
		}
	}
	if inheritedSummary == nil || inheritedSummary.PrimaryModel != "gpt-5.6-sol" || inheritedSummary.ContextWindowTokens != 258000 {
		t.Fatalf("inherited session summary = %+v", inheritedSummary)
	}

	record("manual-sample", "manual-only-session", "gpt-5.6-terra", "compaction", "manual", 237680)
	var manual RelaySessionState
	if err := store.db.First(&manual, "token_id = ? AND session_id = ?", token.ID, "manual-only-session").Error; err != nil {
		t.Fatal(err)
	}
	if manual.ContextWindowTokens != 0 || manual.ContextWindowSource != "" {
		t.Fatalf("manual compaction inferred a context window: %+v", manual)
	}
}

func TestContextWindowEstimateUsesUpperQuartileOfRecentSamples(t *testing.T) {
	var samplesJSON string
	var sampleCount int64
	var window int64
	var threshold int64
	for _, sample := range []int64{237680, 233352, 240000, 219485, 235464} {
		window, threshold, samplesJSON, sampleCount = appendContextWindowSample(samplesJSON, sampleCount, sample)
	}
	if window != 258000 || threshold != 237680 || sampleCount != 5 {
		t.Fatalf("window = %d, threshold = %d, samples = %d", window, threshold, sampleCount)
	}
}

func TestBackfillModelAgentContextWindowsIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	state := RelaySessionState{
		TokenID: 7, SessionID: "backfill-context-session", ClientKind: agentClientKindCodex,
		ClientFingerprint: "backfill-agent", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.db.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	body := `{"model":"gpt-5.6-sol","prompt_cache_key":"backfill-context-session","client_metadata":{"x-codex-turn-metadata":{"request_kind":"compaction","compaction":{"trigger":"auto","reason":"context_limit"}}}}`
	log := RelayRequestLog{
		ID: "backfill-context-request", TokenID: 7, Endpoint: "responses", RequestedModel: "gpt-5.6-sol",
		CodexSessionID: state.SessionID, CodexSessionSource: "prompt_cache_key", IsCompaction: true,
		RequestBody: body, StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, InputTokens: 237680, CreatedAt: now,
	}
	if err := store.db.Create(&log).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.backfillModelAgentContextWindows(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillModelAgentContextWindows(); err != nil {
		t.Fatal(err)
	}
	var profile ModelAgentContextWindow
	if err := store.db.First(&profile, "token_id = ? AND client_fingerprint = ? AND model = ?", 7, state.ClientFingerprint, log.RequestedModel).Error; err != nil {
		t.Fatal(err)
	}
	if profile.SampleCount != 1 || profile.ContextWindowTokens != 258000 {
		t.Fatalf("backfilled profile = %+v", profile)
	}
	var updated RelaySessionState
	if err := store.db.First(&updated, "token_id = ? AND session_id = ?", 7, state.SessionID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.PrimaryModel != log.RequestedModel || updated.ContextWindowSource != contextWindowSourceSession || updated.ContextWindowTokens != 258000 {
		t.Fatalf("backfilled session = %+v", updated)
	}
}

func TestRecordRequestTracksSessionCompactionsOnce(t *testing.T) {
	store := newTestStore(t)
	token := ClientToken{
		Name: "codex", KeyHash: hashSecret("sk-compaction"), KeyPrefix: "sk-compaction",
		Enabled: true, AllowAllModels: true, RPM: 60, MaxConcurrency: 10,
	}
	if err := store.db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	relay := newTestRelay(store)
	startedAt := time.Now().UTC().Add(-time.Second)
	compactionBody := []byte(`{
		"model":"gpt-5.6-sol",
		"prompt_cache_key":"compaction-session",
		"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"compaction\",\"thread_source\":\"user\",\"compaction\":{\"reason\":\"context_limit\"}}"},
		"input":"compact retained context"
	}`)
	compactionPayload, err := ParseRelayPayload(compactionBody)
	if err != nil {
		t.Fatal(err)
	}
	execution := &relayExecution{
		requestID: "compaction-request", token: &token, endpoint: "responses",
		payload: compactionPayload, rawBody: compactionBody, startedAt: startedAt,
	}
	relay.recordRequestStarted(context.Background(), execution)
	var startedLog RelayRequestLog
	if err := store.db.First(&startedLog, "id = ?", execution.requestID).Error; err != nil {
		t.Fatal(err)
	}
	if startedLog.IsCompaction {
		t.Fatal("processing request was marked as compaction before finalization")
	}

	relay.recordRequest(context.Background(), execution, http.StatusOK, "")
	var finalLog RelayRequestLog
	if err := store.db.First(&finalLog, "id = ?", execution.requestID).Error; err != nil {
		t.Fatal(err)
	}
	if !finalLog.IsCompaction {
		t.Fatalf("final log = %+v", finalLog)
	}

	normalBody := []byte(`{
		"model":"gpt-5.6-sol",
		"prompt_cache_key":"compaction-session",
		"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\",\"thread_source\":\"user\"}"},
		"input":"continue the task"
	}`)
	normalPayload, err := ParseRelayPayload(normalBody)
	if err != nil {
		t.Fatal(err)
	}
	relay.recordRequest(context.Background(), &relayExecution{
		requestID: "normal-request", token: &token, endpoint: "responses",
		payload: normalPayload, rawBody: normalBody, startedAt: time.Now().UTC(),
	}, http.StatusOK, "")

	// Retrying final persistence for the same request must not count it twice.
	relay.recordRequest(context.Background(), execution, http.StatusOK, "")
	var state RelaySessionState
	if err := store.db.First(&state, "token_id = ? AND session_id = ?", token.ID, "compaction-session").Error; err != nil {
		t.Fatal(err)
	}
	if state.CompactionCount != 1 {
		t.Fatalf("CompactionCount = %d, want 1", state.CompactionCount)
	}

	management := NewManagementService(store)
	page, err := management.SessionLogs(context.Background(), SessionLogQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].CompactionCount != 1 {
		t.Fatalf("session page = %+v", page)
	}
}

func TestFailedCompactionDoesNotIncrementSessionCount(t *testing.T) {
	store := newTestStore(t)
	token := ClientToken{
		Name: "codex", KeyHash: hashSecret("sk-failed-compaction"), KeyPrefix: "sk-failed-compaction",
		Enabled: true, AllowAllModels: true, RPM: 60, MaxConcurrency: 10,
	}
	if err := store.db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"prompt_cache_key":"failed-compaction-session",
		"client_metadata":{"x-codex-turn-metadata":{"request_kind":"compaction"}},
		"input":"compact retained context"
	}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	execution := &relayExecution{
		requestID: "failed-compaction-request", token: &token, endpoint: "responses",
		payload: payload, rawBody: body, startedAt: time.Now().UTC().Add(-time.Second),
	}
	relay := newTestRelay(store)
	relay.recordRequestStarted(context.Background(), execution)
	relay.recordRequest(context.Background(), execution, http.StatusBadGateway, "upstream failed")

	var log RelayRequestLog
	if err := store.db.First(&log, "id = ?", execution.requestID).Error; err != nil {
		t.Fatal(err)
	}
	if !log.IsCompaction || log.Outcome != RelayOutcomeFailed {
		t.Fatalf("failed compaction log = %+v", log)
	}
	var state RelaySessionState
	if err := store.db.First(&state, "token_id = ? AND session_id = ?", token.ID, "failed-compaction-session").Error; err != nil {
		t.Fatal(err)
	}
	if state.CompactionCount != 0 {
		t.Fatalf("CompactionCount = %d, want 0", state.CompactionCount)
	}
}

func TestBackfillCodexCompactionTracking(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	stringMetadataBody := []byte(`{
		"model":"gpt-5.6-sol",
		"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"compaction\"}"}
	}`)
	deltaBody := []byte(`{
		"_gatewayLog":{"version":1,"mode":"session","baseRequestId":"historical-compaction-1"},
		"payload":{"client_metadata":{"x-codex-turn-metadata":{"request_kind":"compaction"}}}
	}`)
	normalBody := []byte(`{
		"model":"gpt-5.6-sol",
		"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"turn\"}"}
	}`)
	logs := []RelayRequestLog{
		{ID: "historical-compaction-1", TokenID: 7, Endpoint: "responses", RequestedModel: "gpt-5.6-sol", CodexSessionID: "historical-session", RequestBody: compressStoredPayload(stringMetadataBody), StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now},
		{ID: "historical-compaction-2", TokenID: 7, Endpoint: "responses", RequestedModel: "gpt-5.6-sol", CodexSessionID: "historical-session", RequestBody: compressStoredPayload(deltaBody), StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now.Add(time.Second)},
		{ID: "historical-compaction-failed", TokenID: 7, Endpoint: "responses", RequestedModel: "gpt-5.6-sol", CodexSessionID: "historical-session", RequestBody: compressStoredPayload(stringMetadataBody), StatusCode: http.StatusBadGateway, Outcome: RelayOutcomeFailed, CreatedAt: now.Add(1500 * time.Millisecond)},
		{ID: "historical-turn", TokenID: 7, Endpoint: "responses", RequestedModel: "gpt-5.6-sol", CodexSessionID: "historical-session", RequestBody: compressStoredPayload(normalBody), StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now.Add(2 * time.Second)},
		{ID: "historical-payload-removed", TokenID: 7, Endpoint: "responses", RequestedModel: "gpt-5.6-sol", CodexSessionID: "historical-session", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now.Add(3 * time.Second)},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelaySessionState{
		TokenID: 7, SessionID: "historical-session", CompactionCount: 99,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	if err := store.backfillCodexCompactionTracking(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillCodexCompactionTracking(); err != nil {
		t.Fatal(err)
	}
	var state RelaySessionState
	if err := store.db.First(&state, "token_id = ? AND session_id = ?", 7, "historical-session").Error; err != nil {
		t.Fatal(err)
	}
	if state.CompactionCount != 3 {
		t.Fatalf("legacy CompactionCount = %d, want 3", state.CompactionCount)
	}
	if err := store.correctSuccessfulCodexCompactionCounts(); err != nil {
		t.Fatal(err)
	}
	var marked int64
	if err := store.db.Model(&RelayRequestLog{}).Where("is_compaction = ?", true).Count(&marked).Error; err != nil {
		t.Fatal(err)
	}
	if marked != 3 {
		t.Fatalf("marked compaction requests = %d, want 3", marked)
	}
	if err := store.db.First(&state, "token_id = ? AND session_id = ?", 7, "historical-session").Error; err != nil {
		t.Fatal(err)
	}
	if state.CompactionCount != 2 {
		t.Fatalf("CompactionCount = %d, want 2", state.CompactionCount)
	}
	var removed RelayRequestLog
	if err := store.db.First(&removed, "id = ?", "historical-payload-removed").Error; err != nil {
		t.Fatal(err)
	}
	if removed.IsCompaction {
		t.Fatal("request without retained payload must not be guessed as a compaction")
	}
}

func TestCorrectSuccessfulCodexCompactionCountsRepairsExistingState(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	logs := []RelayRequestLog{
		{ID: "successful-compaction", TokenID: 7, Endpoint: "responses", CodexSessionID: "repair-session", IsCompaction: true, StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now},
		{ID: "failed-compaction", TokenID: 7, Endpoint: "responses", CodexSessionID: "repair-session", IsCompaction: true, StatusCode: http.StatusBadGateway, Outcome: RelayOutcomeFailed, CreatedAt: now.Add(time.Second)},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelaySessionState{
		TokenID: 7, SessionID: "repair-session", CompactionCount: 3, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.correctSuccessfulCodexCompactionCounts(); err != nil {
		t.Fatal(err)
	}
	if err := store.correctSuccessfulCodexCompactionCounts(); err != nil {
		t.Fatal(err)
	}
	var state RelaySessionState
	if err := store.db.First(&state, "token_id = ? AND session_id = ?", 7, "repair-session").Error; err != nil {
		t.Fatal(err)
	}
	if state.CompactionCount != 2 {
		t.Fatalf("CompactionCount = %d, want 2", state.CompactionCount)
	}
}
