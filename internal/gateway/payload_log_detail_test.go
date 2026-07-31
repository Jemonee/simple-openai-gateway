package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jemonee/simple-openai-gateway/internal/config"
)

func TestRetainLoggedPayloadModes(t *testing.T) {
	longContent := strings.Repeat("private-context-", 80)
	messages := make([]map[string]any, 0, 12)
	for index := 0; index < 12; index++ {
		messages = append(messages, map[string]any{"role": "user", "content": longContent})
	}
	body, err := json.Marshal(map[string]any{"model": "test-model", "messages": messages})
	if err != nil {
		t.Fatal(err)
	}

	storedDefault, defaultTruncated := retainLoggedPayload(config.PayloadLogDetailDefault, body, false)
	if defaultTruncated || decompressStoredPayload(storedDefault) != string(body) {
		t.Fatalf("default retention changed payload: truncated=%v", defaultTruncated)
	}

	storedSummary, summaryTruncated := retainLoggedPayload(config.PayloadLogDetailSummary, body, false)
	summary := decompressStoredPayload(storedSummary)
	if !summaryTruncated || len(summary) >= len(body) || strings.Contains(summary, longContent) {
		t.Fatalf("summary retention did not reduce content: summary=%d original=%d truncated=%v", len(summary), len(body), summaryTruncated)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(summary), &decoded); err != nil {
		t.Fatalf("summary is not JSON: %v", err)
	}
	metadata, ok := decoded["_gatewayLogRetention"].(map[string]any)
	if !ok || metadata["detail"] != config.PayloadLogDetailSummary {
		t.Fatalf("summary metadata = %#v", decoded["_gatewayLogRetention"])
	}

	storedNone, noneTruncated := retainLoggedPayload(config.PayloadLogDetailNone, body, true)
	if storedNone != "" || noneTruncated {
		t.Fatalf("none retention = %q, truncated=%v", storedNone, noneTruncated)
	}
}

func TestRecordRequestPersistsPayloadLogDetail(t *testing.T) {
	store := newTestStore(t)
	relay := &RelayService{store: store}
	execution := &relayExecution{
		requestID:        "none-detail-request",
		token:            &ClientToken{ID: 91, Name: "test-token", KeyPrefix: "sk-test"},
		endpoint:         "responses",
		payload:          &RelayPayload{Model: "test-model", RequestParametersJSON: `{"temperature":0.2}`},
		rawBody:          []byte(`{"model":"test-model","input":"secret request"}`),
		responseBody:     []byte(`{"status":"completed","output_text":"secret response"}`),
		startedAt:        time.Now().Add(-time.Second),
		payloadLogDetail: config.PayloadLogDetailNone,
		usageSources:     map[string]struct{}{},
		costSources:      map[string]struct{}{},
	}
	relay.recordRequest(context.Background(), execution, http.StatusOK, "")

	var log RelayRequestLog
	if err := store.db.First(&log, "id = ?", execution.requestID).Error; err != nil {
		t.Fatal(err)
	}
	if log.PayloadLogDetail != config.PayloadLogDetailNone || log.RequestBody != "" || log.ResponseBody != "" || log.RequestParametersJSON != "" {
		t.Fatalf("stored none-detail request = %+v", log)
	}
	if log.StatusCode != http.StatusOK || log.DurationMS <= 0 {
		t.Fatalf("core request metrics were not retained: %+v", log)
	}
}

func TestCodexTitleMergeWorksWithoutRetainedPayloadBodies(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	promptHash := hashCodexPrompt("deploy the service")
	titleRequest := RelayRequestLog{
		ID: "title-without-bodies", TokenID: 31, Endpoint: "responses", RequestedModel: "title-model",
		CodexSessionID: "title-session", CodexSessionSource: "prompt_cache_key", CodexPromptHash: promptHash,
		CodexTitleRequest: true, CodexGeneratedTitle: "Deploy service", PayloadLogDetail: config.PayloadLogDetailNone,
		StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now,
	}
	if err := store.db.Create(&titleRequest).Error; err != nil {
		t.Fatal(err)
	}
	mainLog := RelayRequestLog{
		ID: "main-without-bodies", TokenID: titleRequest.TokenID, CodexSessionID: "main-session",
		CodexSessionSource: "prompt_cache_key", CodexPromptHash: promptHash,
	}
	title, err := store.mergePrecedingCodexTitleRequest(store.db, &mainLog, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Deploy service" {
		t.Fatalf("merged title = %q", title)
	}
	var merged RelayRequestLog
	if err := store.db.First(&merged, "id = ?", titleRequest.ID).Error; err != nil {
		t.Fatal(err)
	}
	if merged.CodexSessionID != mainLog.CodexSessionID || merged.CodexSessionSource != codexTitleSessionSource {
		t.Fatalf("merged title request = %+v", merged)
	}
}
