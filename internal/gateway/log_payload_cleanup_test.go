package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/Jemonee/simple-openai-gateway/internal/config"
)

func TestClearHistoricalLogPayloadsOnlyClearsRowsOlderThanThirtyMinutes(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	oldRequest := RelayRequestLog{
		ID: "old-request", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a",
		RequestParametersJSON: `{"temperature":0.2}`, PayloadLogDetail: config.PayloadLogDetailDefault,
		RequestBody: `{"input":"secret"}`, RequestBodyTruncated: true,
		ResponseBody: `{"output":"secret"}`, ResponseBodyTruncated: true,
		CreatedAt: now.Add(-31 * time.Minute),
	}
	recentRequest := RelayRequestLog{
		ID: "recent-request", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a",
		RequestParametersJSON: `{"temperature":0.4}`, PayloadLogDetail: config.PayloadLogDetailSummary,
		RequestBody: `{"input":"keep"}`, ResponseBody: `{"output":"keep"}`,
		CreatedAt: now.Add(-29 * time.Minute),
	}
	if err := store.db.Create(&[]RelayRequestLog{oldRequest, recentRequest}).Error; err != nil {
		t.Fatal(err)
	}
	attempts := []RelayAttemptLog{
		{RequestID: oldRequest.ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", PayloadLogDetail: config.PayloadLogDetailDefault, RequestBody: "old request", ResponseBody: "old response", RequestBodyTruncated: true, ResponseBodyTruncated: true, CreatedAt: oldRequest.CreatedAt},
		{RequestID: recentRequest.ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", PayloadLogDetail: config.PayloadLogDetailSummary, RequestBody: "recent request", ResponseBody: "recent response", CreatedAt: recentRequest.CreatedAt},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}

	result, err := NewManagementService(store).clearHistoricalLogPayloads(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestLogsCleared != 1 || result.AttemptLogsCleared != 1 {
		t.Fatalf("cleanup result = %+v", result)
	}
	var cleared RelayRequestLog
	if err := store.db.First(&cleared, "id = ?", oldRequest.ID).Error; err != nil {
		t.Fatal(err)
	}
	if cleared.PayloadLogDetail != config.PayloadLogDetailNone || cleared.RequestParametersJSON != "" || cleared.RequestBody != "" || cleared.ResponseBody != "" || cleared.RequestBodyTruncated || cleared.ResponseBodyTruncated {
		t.Fatalf("old request payload fields were not cleared: %+v", cleared)
	}
	var clearedAttempt RelayAttemptLog
	if err := store.db.First(&clearedAttempt, attempts[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if clearedAttempt.PayloadLogDetail != config.PayloadLogDetailNone || clearedAttempt.RequestBody != "" || clearedAttempt.ResponseBody != "" || clearedAttempt.RequestBodyTruncated || clearedAttempt.ResponseBodyTruncated {
		t.Fatalf("old attempt payload fields were not cleared: %+v", clearedAttempt)
	}
	var kept RelayRequestLog
	if err := store.db.First(&kept, "id = ?", recentRequest.ID).Error; err != nil {
		t.Fatal(err)
	}
	if kept.PayloadLogDetail != config.PayloadLogDetailSummary || kept.RequestParametersJSON == "" || kept.RequestBody == "" || kept.ResponseBody == "" {
		t.Fatalf("recent request payload fields changed: %+v", kept)
	}
}
