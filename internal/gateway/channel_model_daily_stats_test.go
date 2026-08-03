package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChannelCumulativeCostUsesDailyStatsAfterDetailedLogsExpire(t *testing.T) {
	store := newTestStore(t)
	channel := Channel{Name: "one", BaseURL: "https://one.example/v1", APIKeyCipher: "encrypted", Enabled: true}
	if err := store.db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC().Add(-(DetailedLogRetentionDays*24*time.Hour + time.Hour))
	attempt := RelayAttemptLog{
		RequestID: "expired-detail", ChannelID: channel.ID, ChannelModelID: 1, UpstreamModel: "model-a",
		UpstreamCost: 42, Success: true, CreatedAt: createdAt,
	}
	if err := store.db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}

	if err := store.backfillChannelModelDailyStats(); err != nil {
		t.Fatal(err)
	}
	store.cleanupExpired()
	todayAttempt := RelayAttemptLog{
		RequestID: "today-detail", ChannelID: channel.ID, ChannelModelID: 1, UpstreamModel: "model-a",
		UpstreamCost: 8, Success: true, Outcome: RelayOutcomeSuccess, CreatedAt: time.Now().UTC(),
	}
	if err := store.db.Create(&todayAttempt).Error; err != nil {
		t.Fatal(err)
	}

	var attemptCount int64
	if err := store.db.Model(&RelayAttemptLog{}).Count(&attemptCount).Error; err != nil || attemptCount != 1 {
		t.Fatalf("retained attempt count = %d, error = %v", attemptCount, err)
	}
	views, err := NewManagementService(store).ListChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].CumulativeUpstreamCost != 50 {
		t.Fatalf("channel views = %+v", views)
	}
}

func TestBackfillChannelModelDailyStatsStoresCompactAggregates(t *testing.T) {
	store := newTestStore(t)
	createdAt := eastEightStartOfDay(time.Now()).Add(-24 * time.Hour).Add(time.Hour).UTC()
	attempts := []RelayAttemptLog{
		{
			RequestID: "request-one", ChannelID: 7, ChannelModelID: 11, UpstreamModel: "model-a",
			StatusCode: http.StatusOK, InputTokens: 10, NormalInputTokens: 7, OutputTokens: 4,
			CachedTokens: 2, CacheWriteTokens: 1, SentTokens: 12, EstimatedCost: 30, UpstreamCost: 42,
			CostSource: CostSourceUpstream, FirstTokenMS: 100, LatencyMS: 20, DurationMS: 300,
			Success: true, Outcome: RelayOutcomeSuccess, CreatedAt: createdAt,
		},
		{
			RequestID: "request-one", ChannelID: 7, ChannelModelID: 11, UpstreamModel: "model-a",
			StatusCode: http.StatusServiceUnavailable, SentTokens: 5, CostSource: CostSourceFailedZero,
			Outcome: RelayOutcomeFailed, LatencyMS: 40, DurationMS: 80, CreatedAt: createdAt.Add(time.Minute),
		},
		{
			RequestID: "request-two", ChannelID: 7, ChannelModelID: 11, UpstreamModel: "model-a",
			StatusCode: 0, InputTokens: 3, NormalInputTokens: 3, EstimatedCost: 5, UpstreamCost: 5,
			CostSource: CostSourceFallback, Outcome: RelayOutcomeCanceled, CreatedAt: createdAt.Add(2 * time.Minute),
		},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.backfillChannelModelDailyStats(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillChannelModelDailyStats(); err != nil {
		t.Fatal(err)
	}

	var stat ChannelModelDailyStat
	if err := store.db.First(&stat, "date = ? AND channel_id = ? AND channel_model_id = ?", eastEightDate(createdAt), 7, 11).Error; err != nil {
		t.Fatal(err)
	}
	if stat.RequestCount != 2 || stat.AttemptCount != 3 || stat.SuccessCount != 1 || stat.FailedCount != 1 || stat.CanceledCount != 1 {
		t.Fatalf("channel-model daily outcomes = %+v", stat)
	}
	if stat.InputTokens != 13 || stat.NormalInputTokens != 10 || stat.OutputTokens != 4 || stat.CachedTokens != 2 || stat.CacheWriteTokens != 1 || stat.SentTokens != 17 {
		t.Fatalf("channel-model daily usage = %+v", stat)
	}
	if stat.EstimatedCost != 35 || stat.UpstreamCost != 47 || stat.UpstreamCostCount != 1 || stat.FallbackCostCount != 1 || stat.FailedZeroCostCount != 1 {
		t.Fatalf("channel-model daily costs = %+v", stat)
	}
	if stat.FirstTokenMS != 100 || stat.FirstTokenSamples != 1 || stat.LatencyMS != 60 || stat.LatencySamples != 2 || stat.DurationMS != 380 || stat.DurationSamples != 2 {
		t.Fatalf("channel-model daily timings = %+v", stat)
	}
	if stat.Status2xxCount != 1 || stat.Status5xxCount != 1 || stat.NoStatusCount != 1 {
		t.Fatalf("channel-model daily statuses = %+v", stat)
	}
}

func TestRelayAddsUpstreamCostToChannelModelDailyStats(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chatcmpl_cost","choices":[{"message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":"0.000042"}}`))
	}))
	defer upstream.Close()

	token, _, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	if publicErr := newTestRelay(store).Relay(context.Background(), httptest.NewRecorder(), nil, "", "chat", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}

	var stat ChannelModelDailyStat
	if err := store.db.First(&stat, "channel_id = ? AND channel_model_id = ?", channels[0].ID, mappings[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if stat.RequestCount != 1 || stat.AttemptCount != 1 || stat.SuccessCount != 1 || stat.Status2xxCount != 1 || stat.UpstreamCostCount != 1 {
		t.Fatalf("channel-model daily counts = %+v", stat)
	}
	if stat.InputTokens != 10 || stat.NormalInputTokens != 10 || stat.OutputTokens != 5 || stat.EstimatedCost <= 0 || stat.UpstreamCost != 42 {
		t.Fatalf("channel-model daily usage and cost = %+v", stat)
	}
	views, err := NewManagementService(store).ListChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].CumulativeUpstreamCost != 42 {
		t.Fatalf("channel views = %+v", views)
	}
}
