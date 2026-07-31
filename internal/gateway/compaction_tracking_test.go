package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestParseRelayPayloadDetectsCodexCompactionRequests(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "string metadata",
			body: `{"model":"gpt-5.6-sol","client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"compaction\",\"compaction\":{\"trigger\":\"auto\"}}"}}`,
			want: true,
		},
		{
			name: "object metadata",
			body: `{"model":"gpt-5.6-sol","client_metadata":{"x-codex-turn-metadata":{"request_kind":"compaction","compaction":{"trigger":"manual"}}}}`,
			want: true,
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
		})
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
	var marked int64
	if err := store.db.Model(&RelayRequestLog{}).Where("is_compaction = ?", true).Count(&marked).Error; err != nil {
		t.Fatal(err)
	}
	if marked != 2 {
		t.Fatalf("marked compaction requests = %d, want 2", marked)
	}
	var state RelaySessionState
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
