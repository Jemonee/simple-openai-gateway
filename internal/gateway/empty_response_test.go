package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateBufferedResponsesRejectsEmptyCompletedOutput(t *testing.T) {
	empty := []byte(`{"id":"resp_empty","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text"}]}]}`)
	failure := validateBufferedApplicationResponse("responses", empty)
	if failure == nil || failure.Code != "upstream_empty_response" || failure.Message != "upstream response completed without any usable output" {
		t.Fatalf("empty response failure = %+v", failure)
	}

	textResponse := []byte(`{"id":"resp_text","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`)
	if failure := validateBufferedApplicationResponse("responses", textResponse); failure != nil {
		t.Fatalf("text response failure = %+v", failure)
	}

	toolResponse := []byte(`{"id":"resp_tool","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]}`)
	if failure := validateBufferedApplicationResponse("responses", toolResponse); failure != nil {
		t.Fatalf("tool response failure = %+v", failure)
	}
}

func TestRelayMarksEmptyCompletedStreamFailedAndZeroCost(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_empty_stream\",\"status\":\"in_progress\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_empty_stream\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\"}]}],\"usage\":{\"input_tokens\":100,\"output_tokens\":9}}}\n\n"))
	}))
	defer upstream.Close()

	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","stream":true,"input":"hello"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	if publicErr := newTestRelay(store).Relay(context.Background(), httptest.NewRecorder(), nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}

	assertEmptyResponseFailureLogs(t, store, "resp_empty_stream")
}

func TestRelayRejectsEmptyCompletedBufferedResponse(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_empty_buffered","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text"}]}],"usage":{"input_tokens":100,"output_tokens":9}}`))
	}))
	defer upstream.Close()

	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","input":"hello"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	publicErr := newTestRelay(store).Relay(context.Background(), httptest.NewRecorder(), nil, "", "responses", token, payload, body)
	if publicErr == nil || publicErr.Status != http.StatusBadGateway || publicErr.Code != "upstream_empty_response" || publicErr.Message != "upstream response completed without any usable output" {
		t.Fatalf("public error = %+v", publicErr)
	}

	assertEmptyResponseFailureLogs(t, store, "resp_empty_buffered")
}

func assertEmptyResponseFailureLogs(t *testing.T, store *Store, responseID string) {
	t.Helper()
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.StatusCode != http.StatusBadGateway || requestLog.Outcome != RelayOutcomeFailed || requestLog.ErrorCode != "upstream_empty_response" || requestLog.EstimatedCost != 0 || requestLog.UpstreamCost != 0 || requestLog.CostSource != CostSourceFailedZero {
		t.Fatalf("request log = %+v", requestLog)
	}
	var attemptLog RelayAttemptLog
	if err := store.db.First(&attemptLog).Error; err != nil {
		t.Fatal(err)
	}
	if attemptLog.Success || attemptLog.Outcome != RelayOutcomeFailed || attemptLog.EstimatedCost != 0 || attemptLog.UpstreamCost != 0 || attemptLog.CostSource != CostSourceFailedZero || !strings.Contains(attemptLog.ErrorMessage, "upstream response completed without any usable output") {
		t.Fatalf("attempt log = %+v", attemptLog)
	}
	if !strings.Contains(attemptLog.ResponseBody, responseID) {
		t.Fatalf("attempt response body = %q", attemptLog.ResponseBody)
	}
	var stat TokenDailyStat
	if err := store.db.First(&stat).Error; err != nil {
		t.Fatal(err)
	}
	if stat.SuccessCount != 0 || stat.EstimatedCost != 0 || stat.UpstreamCost != 0 {
		t.Fatalf("daily stat = %+v", stat)
	}
}

func TestBackfillApplicationOutcomesCorrectsHistoricalEmptyResponse(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	responseBody := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_historical_empty\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\"}]}]}}\n\n"
	requestLog := RelayRequestLog{
		ID: "historical-empty", TokenID: 17, Endpoint: "responses", RequestedModel: "model-a", Stream: true,
		ResponseBody: compressStoredPayload([]byte(responseBody)), StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess,
		InputTokens: 100, OutputTokens: 9, EstimatedCost: 1_000, UpstreamCost: 1_000, CostSource: CostSourceFallback, AttemptCount: 1, CreatedAt: now,
	}
	if err := store.db.Create(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	attempt := RelayAttemptLog{
		RequestID: requestLog.ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a",
		ResponseBody: requestLog.ResponseBody, StatusCode: http.StatusOK, Success: true, Outcome: RelayOutcomeSuccess,
		InputTokens: 100, OutputTokens: 9, EstimatedCost: 1_000, UpstreamCost: 1_000, CostSource: CostSourceFallback, CreatedAt: now,
	}
	if err := store.db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	stat := TokenDailyStat{Date: eastEightDate(now), TokenID: requestLog.TokenID, RequestCount: 1, SuccessCount: 1, InputTokens: 100, OutputTokens: 9, EstimatedCost: 1_000, UpstreamCost: 1_000}
	if err := store.db.Create(&stat).Error; err != nil {
		t.Fatal(err)
	}

	if err := store.backfillApplicationOutcomes(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillApplicationOutcomes(); err != nil {
		t.Fatal(err)
	}

	var correctedRequest RelayRequestLog
	if err := store.db.First(&correctedRequest, "id = ?", requestLog.ID).Error; err != nil {
		t.Fatal(err)
	}
	if correctedRequest.Outcome != RelayOutcomeFailed || correctedRequest.StatusCode != http.StatusBadGateway || correctedRequest.ErrorCode != "upstream_empty_response" || correctedRequest.EstimatedCost != 0 || correctedRequest.UpstreamCost != 0 || correctedRequest.InputTokens != 100 || correctedRequest.OutputTokens != 9 {
		t.Fatalf("corrected request = %+v", correctedRequest)
	}
	var correctedAttempt RelayAttemptLog
	if err := store.db.First(&correctedAttempt, attempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if correctedAttempt.Success || correctedAttempt.Outcome != RelayOutcomeFailed || correctedAttempt.EstimatedCost != 0 || correctedAttempt.UpstreamCost != 0 || !strings.Contains(correctedAttempt.ErrorMessage, "upstream_empty_response") {
		t.Fatalf("corrected attempt = %+v", correctedAttempt)
	}
	var correctedStat TokenDailyStat
	if err := store.db.First(&correctedStat, "date = ? AND token_id = ?", stat.Date, stat.TokenID).Error; err != nil {
		t.Fatal(err)
	}
	if correctedStat.SuccessCount != 0 || correctedStat.EstimatedCost != 0 || correctedStat.UpstreamCost != 0 || correctedStat.InputTokens != 100 || correctedStat.OutputTokens != 9 {
		t.Fatalf("corrected stat = %+v", correctedStat)
	}
	var migrations int64
	if err := store.db.Model(&GatewayMigration{}).Where("name = ?", historicalApplicationOutcomesMigration).Count(&migrations).Error; err != nil || migrations != 1 {
		t.Fatalf("migration count = %d, error = %v", migrations, err)
	}
}
