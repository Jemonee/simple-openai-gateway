package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Jemonee/simple-openai-gateway/internal/config"

	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type disconnectedStreamWriter struct {
	header http.Header
	status int
}

type failAfterWritesStreamWriter struct {
	header http.Header
	status int
	writes int
	failAt int
}

type notifyingStreamWriter struct {
	header     http.Header
	status     int
	body       bytes.Buffer
	mu         sync.Mutex
	firstWrite chan []byte
	once       sync.Once
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

type errorReadCloser struct{}

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func (errorReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("response body unavailable")
}

func (errorReadCloser) Close() error {
	return nil
}

func (w *disconnectedStreamWriter) Header() http.Header {
	return w.header
}

func (w *disconnectedStreamWriter) WriteHeader(status int) {
	w.status = status
}

func (w *disconnectedStreamWriter) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}

func (w *disconnectedStreamWriter) Flush() {}

func (w *failAfterWritesStreamWriter) Header() http.Header {
	return w.header
}

func (w *failAfterWritesStreamWriter) WriteHeader(status int) {
	w.status = status
}

func (w *failAfterWritesStreamWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writes >= w.failAt {
		return 0, errors.New("client disconnected")
	}
	return len(data), nil
}

func (w *failAfterWritesStreamWriter) Flush() {}

func (w *notifyingStreamWriter) Header() http.Header {
	return w.header
}

func (w *notifyingStreamWriter) WriteHeader(status int) {
	w.status = status
}

func (w *notifyingStreamWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.once.Do(func() { w.firstWrite <- bytes.Clone(data) })
	return w.body.Write(data)
}

func (w *notifyingStreamWriter) Flush() {}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&AdminUser{}, &AdminSession{}, &Channel{}, &GatewayModel{}, &ChannelModel{},
		&CircuitRecord{},
		&ClientToken{}, &ClientTokenModel{}, &RelayRequestLog{}, &RelaySessionState{}, &RelayChatSessionClaim{}, &RelayAttemptLog{}, &RelayStepLog{}, &TokenDailyStat{}, &GatewayMigration{},
		&ResponseAffinity{}, &SessionAffinity{},
	); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	box, err := NewSecretBox(key)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{db: db, secretBox: box, configManager: &config.ApplicationConfigManager{}}
}

func createRouteFixture(t *testing.T, store *Store, strategy string, channelURLs ...string) (*ClientToken, GatewayModel, []Channel, []ChannelModel) {
	t.Helper()
	model := GatewayModel{Name: "public-model", RoutingStrategy: strategy, Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	token := ClientToken{Name: "test", KeyHash: hashSecret("sk-test"), KeyPrefix: "sk-test", Enabled: true, AllowAllModels: true, RPM: 60, MaxConcurrency: 10}
	if err := store.db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	secret, err := store.secretBox.Encrypt("upstream-secret")
	if err != nil {
		t.Fatal(err)
	}
	channels := make([]Channel, 0, len(channelURLs))
	mappings := make([]ChannelModel, 0, len(channelURLs))
	for index, channelURL := range channelURLs {
		channel := Channel{Name: fmt.Sprintf("channel-%d", index+1), BaseURL: channelURL + "/v1", APIKeyCipher: secret, Enabled: true, SupportsStreamUsage: true}
		if err := store.db.Create(&channel).Error; err != nil {
			t.Fatal(err)
		}
		mapping := ChannelModel{
			ChannelID: channel.ID, ModelID: model.ID, UpstreamModel: fmt.Sprintf("upstream-%d", index+1),
			Priority: 100 - index, Weight: 100, InputPriceMicros: 1_000_000, OutputPriceMicros: 1_000_000, Enabled: true,
		}
		if err := store.db.Create(&mapping).Error; err != nil {
			t.Fatal(err)
		}
		channels = append(channels, channel)
		mappings = append(mappings, mapping)
	}
	return &token, model, channels, mappings
}

func newTestRelay(store *Store) *RelayService {
	access := NewClientAccessService(store)
	router := NewRouter(store, access, nil)
	return NewRelayService(store, router, NewTokenEstimator(), &config.ApplicationConfigManager{})
}

func TestSecretBoxAndHashing(t *testing.T) {
	store := newTestStore(t)
	ciphertext, err := store.secretBox.Encrypt("sk-upstream-secret")
	if err != nil {
		t.Fatal(err)
	}
	if ciphertext == "sk-upstream-secret" || strings.Contains(ciphertext, "upstream") {
		t.Fatal("ciphertext contains plaintext")
	}
	plaintext, err := store.secretBox.Decrypt(ciphertext)
	if err != nil || plaintext != "sk-upstream-secret" {
		t.Fatalf("Decrypt() = %q, %v", plaintext, err)
	}
	if hashSecret("sk-client") == "sk-client" || hashSecret("sk-client") != hashSecret("sk-client") {
		t.Fatal("secret hash is not deterministic and one-way")
	}
}

func TestTokenEstimatorUsesParsedPayload(t *testing.T) {
	body := []byte(`{"model":"public-model","input":"hello world"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	estimator := NewTokenEstimator()
	if parsed, encoded := estimator.EstimateValue(payload.values), estimator.EstimateJSON(body); parsed == 0 || parsed != encoded {
		t.Fatalf("parsed estimate = %d, JSON estimate = %d", parsed, encoded)
	}
	if count := estimator.EstimateText("hello world"); count != 2 {
		t.Fatalf("text estimate = %d, want 2", count)
	}
}

func TestDurationAfterLatency(t *testing.T) {
	tests := []struct {
		total   int64
		latency int64
		want    int64
	}{
		{total: 350, latency: 100, want: 250},
		{total: 100, latency: 100, want: 0},
		{total: 0, latency: 100, want: 0},
	}
	for _, test := range tests {
		if got := durationAfterLatency(test.total, test.latency); got != test.want {
			t.Fatalf("durationAfterLatency(%d, %d) = %d, want %d", test.total, test.latency, got, test.want)
		}
	}
}

func TestFirstTokenAfterLatencyPreservesObservedSample(t *testing.T) {
	tests := []struct {
		firstToken int64
		latency    int64
		want       int64
	}{
		{firstToken: 350, latency: 100, want: 250},
		{firstToken: 100, latency: 100, want: 1},
		{firstToken: 0, latency: 100, want: 0},
	}
	for _, test := range tests {
		if got := firstTokenAfterLatency(test.firstToken, test.latency); got != test.want {
			t.Fatalf("firstTokenAfterLatency(%d, %d) = %d, want %d", test.firstToken, test.latency, got, test.want)
		}
	}
}

func TestRelayRequestElapsedDurationIncludesLatency(t *testing.T) {
	log := RelayRequestLog{LatencyMS: 100, DurationMS: 800}
	if got := relayRequestElapsedDuration(log); got != 900*time.Millisecond {
		t.Fatalf("relayRequestElapsedDuration() = %s, want 900ms", got)
	}
}

func TestBootstrapAdminFromEnvironment(t *testing.T) {
	store := newTestStore(t)
	t.Setenv("GATEWAY_ADMIN_USERNAME", "bootstrap-admin")
	t.Setenv("GATEWAY_ADMIN_PASSWORD", "bootstrap-password")
	if err := store.bootstrapAdmin(); err != nil {
		t.Fatal(err)
	}
	var user AdminUser
	if err := store.db.Where("username = ?", "bootstrap-admin").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("bootstrap-password")) != nil {
		t.Fatal("bootstrap password was not stored with bcrypt")
	}
}

func TestCalculateCostMicrosSeparatesCacheReadAndWrite(t *testing.T) {
	cacheReadPrice := int64(500_000)
	cacheWritePrice := int64(3_000_000)
	mapping := ChannelModel{
		InputPriceMicros:       2_000_000,
		OutputPriceMicros:      8_000_000,
		CachedInputPriceMicros: &cacheReadPrice,
		CacheWritePriceMicros:  &cacheWritePrice,
	}
	usage := Usage{InputTokens: 1000, CachedTokens: 200, CacheWriteTokens: 100, OutputTokens: 500}
	if got, want := CalculateCostMicros(mapping, usage), int64(5800); got != want {
		t.Fatalf("CalculateCostMicros() = %d, want %d", got, want)
	}
	if got, want := normalInputTokens(usage), int64(700); got != want {
		t.Fatalf("normalInputTokens() = %d, want %d", got, want)
	}
	if got := normalInputTokens(Usage{InputTokens: 10, CachedTokens: 8, CacheWriteTokens: 7}); got != 0 {
		t.Fatalf("normalInputTokens() should clamp invalid cache details, got %d", got)
	}
	mapping.CachedInputPriceMicros = nil
	mapping.CacheWritePriceMicros = nil
	if got, want := CalculateCostMicros(mapping, usage), int64(6000); got != want {
		t.Fatalf("CalculateCostMicros() fallback = %d, want %d", got, want)
	}
}

func TestParseUsageReadsCacheReadAndWriteTokens(t *testing.T) {
	openAIUsage, ok := ParseUsage([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2,"cache_creation_tokens":3}}}`))
	if !ok || openAIUsage.InputTokens != 10 || openAIUsage.OutputTokens != 5 || openAIUsage.CachedTokens != 2 || openAIUsage.CacheWriteTokens != 3 {
		t.Fatalf("OpenAI usage = %+v, parsed = %v", openAIUsage, ok)
	}
	anthropicUsage, ok := ParseUsage([]byte(`{"usage":{"input_tokens":5,"output_tokens":2,"cache_read_input_tokens":7,"cache_creation_input_tokens":3}}`))
	if !ok || anthropicUsage.InputTokens != 15 || anthropicUsage.OutputTokens != 2 || anthropicUsage.CachedTokens != 7 || anthropicUsage.CacheWriteTokens != 3 {
		t.Fatalf("Anthropic usage = %+v, parsed = %v", anthropicUsage, ok)
	}
}

func TestParseUpstreamCostMicrosUsesWhitelistedPriority(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int64
		ok   bool
	}{
		{name: "usage cost", body: `{"usage":{"cost":0.0012345,"total_cost":0.9},"cost":0.8}`, want: 1235, ok: true},
		{name: "usage total cost", body: `{"usage":{"total_cost":"0.000002"}}`, want: 2, ok: true},
		{name: "top level cost", body: `{"cost":"0"}`, want: 0, ok: true},
		{name: "invalid falls back", body: `{"usage":{"cost":"not-a-number","total_cost":"0.4"}}`, want: 400000, ok: true},
		{name: "absent", body: `{"usage":{"input_tokens":1}}`, want: 0, ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ParseUpstreamCostMicros([]byte(test.body))
			if got != test.want || ok != test.ok {
				t.Fatalf("ParseUpstreamCostMicros() = %d, %v; want %d, %v", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestOpenAIOfficialPriceExactMatch(t *testing.T) {
	tests := []struct {
		modelID                                string
		input, cachedInput, cacheWrite, output int64
	}{
		{modelID: "gpt-5.6-sol", input: 5_000_000, cachedInput: 500_000, cacheWrite: 6_250_000, output: 30_000_000},
		{modelID: "gpt-5.6-terra", input: 2_500_000, cachedInput: 250_000, cacheWrite: 3_125_000, output: 15_000_000},
		{modelID: "gpt-5.6-luna", input: 1_000_000, cachedInput: 100_000, cacheWrite: 1_250_000, output: 6_000_000},
		{modelID: "gpt-5.5", input: 5_000_000, cachedInput: 500_000, output: 30_000_000},
		{modelID: "gpt-5.5-pro", input: 30_000_000, output: 180_000_000},
		{modelID: "gpt-5.4", input: 2_500_000, cachedInput: 250_000, output: 15_000_000},
		{modelID: "gpt-5.4-mini", input: 750_000, cachedInput: 75_000, output: 4_500_000},
		{modelID: "gpt-5.4-nano", input: 200_000, cachedInput: 20_000, output: 1_250_000},
		{modelID: "gpt-5.4-pro", input: 30_000_000, output: 180_000_000},
		{modelID: "gpt-5.2", input: 1_750_000, cachedInput: 175_000, output: 14_000_000},
		{modelID: "gpt-5.2-pro", input: 21_000_000, output: 168_000_000},
		{modelID: "gpt-5.1", input: 1_250_000, cachedInput: 125_000, output: 10_000_000},
		{modelID: "gpt-5", input: 1_250_000, cachedInput: 125_000, output: 10_000_000},
		{modelID: "gpt-5-mini", input: 250_000, cachedInput: 25_000, output: 2_000_000},
		{modelID: "gpt-5-nano", input: 50_000, cachedInput: 5_000, output: 400_000},
		{modelID: "gpt-5-pro", input: 15_000_000, output: 120_000_000},
		{modelID: "codex-auto-review", input: 5_000_000, cachedInput: 500_000, output: 3_000_000},
		{modelID: "gpt-5.3-codex-spark", input: 3_150_000, cachedInput: 315_000, output: 25_200_000},
		{modelID: "gpt-4.1", input: 2_000_000, cachedInput: 500_000, output: 8_000_000},
		{modelID: "gpt-4.1-mini", input: 400_000, cachedInput: 100_000, output: 1_600_000},
		{modelID: "gpt-4.1-nano", input: 100_000, cachedInput: 25_000, output: 400_000},
		{modelID: "gpt-4o", input: 2_500_000, cachedInput: 1_250_000, output: 10_000_000},
		{modelID: "gpt-4o-mini", input: 150_000, cachedInput: 75_000, output: 600_000},
		{modelID: "o4-mini", input: 1_100_000, cachedInput: 275_000, output: 4_400_000},
		{modelID: "o3", input: 2_000_000, cachedInput: 500_000, output: 8_000_000},
		{modelID: "o3-mini", input: 1_100_000, cachedInput: 550_000, output: 4_400_000},
		{modelID: "o3-pro", input: 20_000_000, output: 80_000_000},
	}
	optionalMicros := func(value int64) *int64 {
		if value == 0 {
			return nil
		}
		return &value
	}
	equalOptional := func(got, want *int64) bool {
		return got == nil && want == nil || got != nil && want != nil && *got == *want
	}
	for _, test := range tests {
		t.Run(test.modelID, func(t *testing.T) {
			price := OpenAIOfficialPrice(test.modelID)
			if price == nil || price.ContextTier != OpenAIPriceCatalogTier || price.InputPriceMicros != test.input || price.OutputPriceMicros != test.output || !equalOptional(price.CachedInputPriceMicros, optionalMicros(test.cachedInput)) || !equalOptional(price.CacheWritePriceMicros, optionalMicros(test.cacheWrite)) {
				t.Fatalf("official price = %+v", price)
			}
		})
	}
	if OpenAIOfficialPrice("gpt-4.1-custom") != nil {
		t.Fatal("unknown model received a guessed official price")
	}
}

func TestReplaceChannelModelsRejectsNegativeCacheWritePrice(t *testing.T) {
	store := newTestStore(t)
	_, model, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	negativePrice := int64(-1)
	_, err := NewManagementService(store).ReplaceChannelModels(context.Background(), channels[0].ID, []ChannelModelInput{{
		ModelID:               model.ID,
		UpstreamModel:         "upstream-model",
		Weight:                100,
		CacheWritePriceMicros: &negativePrice,
		Enabled:               true,
	}})
	if err == nil {
		t.Fatal("negative cache-write price was accepted")
	}
}

func TestReplaceChannelModelsPersistsDisabledMapping(t *testing.T) {
	store := newTestStore(t)
	_, model, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	priceMultiplierBasisPoints := int64(1_700)

	mappings, err := NewManagementService(store).ReplaceChannelModels(context.Background(), channels[0].ID, []ChannelModelInput{{
		ModelID:                    model.ID,
		UpstreamModel:              "upstream-model",
		Weight:                     100,
		PriceMultiplierBasisPoints: &priceMultiplierBasisPoints,
		Enabled:                    false,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 || mappings[0].Enabled || mappings[0].PriceMultiplierBasisPoints != priceMultiplierBasisPoints {
		t.Fatalf("ReplaceChannelModels() = %+v; want one disabled mapping", mappings)
	}

	var stored ChannelModel
	if err := store.db.Where("channel_id = ? AND model_id = ?", channels[0].ID, model.ID).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enabled || stored.PriceMultiplierBasisPoints != priceMultiplierBasisPoints {
		t.Fatalf("stored mapping = %+v", stored)
	}
}

func TestReplaceChannelModelsManuallyReopensCircuitDisabledMapping(t *testing.T) {
	store := newTestStore(t)
	_, model, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	if err := store.db.Model(&ChannelModel{}).Where("id = ?", mappings[0].ID).Updates(map[string]any{
		"enabled":          false,
		"circuit_disabled": true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	record := CircuitRecord{
		ChannelID: channels[0].ID, ChannelModelID: mappings[0].ID, ModelID: model.ID,
		ChannelName: channels[0].Name, ModelName: model.Name, UpstreamModel: mappings[0].UpstreamModel,
		Level: CircuitLevelManual, FailureCount: circuitFailureThreshold, Message: "terminal failure", CreatedAt: time.Now(),
	}
	if err := store.db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}

	updated, err := NewManagementService(store).ReplaceChannelModels(context.Background(), channels[0].ID, []ChannelModelInput{{
		ModelID: model.ID, UpstreamModel: mappings[0].UpstreamModel, Weight: 100, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].ID != mappings[0].ID || !updated[0].Enabled || updated[0].CircuitDisabled {
		t.Fatalf("manually reopened mapping = %+v", updated)
	}
	if err := store.db.First(&record, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.ResolvedAt == nil || record.Resolution != CircuitResolutionManualReopen {
		t.Fatalf("resolved circuit record = %+v", record)
	}
}

func TestReplaceChannelModelsDefaultsAndValidatesPriceMultiplier(t *testing.T) {
	store := newTestStore(t)
	_, model, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	management := NewManagementService(store)
	mappings, err := management.ReplaceChannelModels(context.Background(), channels[0].ID, []ChannelModelInput{{
		ModelID: model.ID, UpstreamModel: "upstream-model", Weight: 100, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 || mappings[0].PriceMultiplierBasisPoints != DefaultPriceMultiplierBasisPoints {
		t.Fatalf("default multiplier mappings = %+v", mappings)
	}
	invalid := MaxPriceMultiplierBasisPoints + 1
	if _, err := management.ReplaceChannelModels(context.Background(), channels[0].ID, []ChannelModelInput{{
		ModelID: model.ID, UpstreamModel: "upstream-model", Weight: 100, PriceMultiplierBasisPoints: &invalid, Enabled: true,
	}}); err == nil {
		t.Fatal("invalid price multiplier was accepted")
	}
}

func TestChannelPriceMultiplierPersistsDefaultsAndValidates(t *testing.T) {
	store := newTestStore(t)
	management := NewManagementService(store)
	created, err := management.CreateChannel(context.Background(), ChannelInput{
		Name: "priced channel", BaseURL: "http://upstream.invalid/v1", APIKey: "secret", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.PriceMultiplierBasisPoints != DefaultPriceMultiplierBasisPoints {
		t.Fatalf("default channel multiplier = %d, want %d", created.PriceMultiplierBasisPoints, DefaultPriceMultiplierBasisPoints)
	}

	custom := int64(1_700)
	updated, err := management.UpdateChannel(context.Background(), created.ID, ChannelInput{
		Name: "priced channel", BaseURL: "http://upstream.invalid/v1", Enabled: true, PriceMultiplierBasisPoints: &custom,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.PriceMultiplierBasisPoints != custom {
		t.Fatalf("updated channel multiplier = %d, want %d", updated.PriceMultiplierBasisPoints, custom)
	}
	var stored Channel
	if err := store.db.First(&stored, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.PriceMultiplierBasisPoints != custom {
		t.Fatalf("stored channel multiplier = %d, want %d", stored.PriceMultiplierBasisPoints, custom)
	}
	preserved, err := management.UpdateChannel(context.Background(), created.ID, ChannelInput{
		Name: "priced channel", BaseURL: "http://upstream.invalid/v1", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preserved.PriceMultiplierBasisPoints != custom {
		t.Fatalf("channel multiplier after omitted update = %d, want %d", preserved.PriceMultiplierBasisPoints, custom)
	}

	invalid := MaxPriceMultiplierBasisPoints + 1
	if _, err := management.UpdateChannel(context.Background(), created.ID, ChannelInput{
		Name: "priced channel", BaseURL: "http://upstream.invalid/v1", Enabled: true, PriceMultiplierBasisPoints: &invalid,
	}); err == nil {
		t.Fatal("invalid channel price multiplier was accepted")
	}
}

func TestListModelsIncludesGlobalRequestCounts(t *testing.T) {
	store := newTestStore(t)
	models := []GatewayModel{
		{Name: "model-a", RoutingStrategy: RoutingPriorityWeighted, Enabled: true},
		{Name: "model-b", RoutingStrategy: RoutingPriorityWeighted, Enabled: true},
	}
	if err := store.db.Create(&models).Error; err != nil {
		t.Fatal(err)
	}
	logs := []RelayRequestLog{
		{ID: "model-count-a-1", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a", CreatedAt: time.Now()},
		{ID: "model-count-a-2", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a", CreatedAt: time.Now()},
		{ID: "model-count-b-1", TokenID: 1, Endpoint: "responses", RequestedModel: "model-b", CreatedAt: time.Now()},
		{ID: "model-count-removed", TokenID: 1, Endpoint: "responses", RequestedModel: "removed-model", CreatedAt: time.Now()},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}

	views, err := NewManagementService(store).ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 || views[0].Name != "model-a" || views[0].RequestCount != 2 || views[1].Name != "model-b" || views[1].RequestCount != 1 {
		t.Fatalf("model views = %+v", views)
	}
}

func TestBackfillTokenStatsAndLogFieldsRunOnce(t *testing.T) {
	store := newTestStore(t)
	createdAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	logs := []RelayRequestLog{
		{ID: "request-1", TokenID: 7, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusOK, InputTokens: 10, OutputTokens: 4, CachedTokens: 3, CacheWriteTokens: 2, EstimatedCost: 12, AttemptCount: 1, FirstTokenMS: 50, LatencyMS: 30, DurationMS: 100, CreatedAt: createdAt},
		{ID: "request-2", TokenID: 7, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusBadGateway, InputTokens: 6, OutputTokens: 0, EstimatedCost: 5, AttemptCount: 2, LatencyMS: 60, DurationMS: 300, CreatedAt: createdAt.Add(time.Hour)},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	attempt := RelayAttemptLog{RequestID: logs[0].ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", StatusCode: http.StatusOK, InputTokens: 10, CachedTokens: 3, CacheWriteTokens: 2, CreatedAt: createdAt}
	if err := store.db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.backfillTokenDailyStats(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillTokenDailyStats(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillTokenLogFields(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillTokenLogFields(); err != nil {
		t.Fatal(err)
	}
	var stat TokenDailyStat
	if err := store.db.First(&stat, "date = ? AND token_id = ?", "2026-07-20", 7).Error; err != nil {
		t.Fatal(err)
	}
	if stat.RequestCount != 2 || stat.SuccessCount != 1 || stat.InputTokens != 16 || stat.NormalInputTokens != 11 || stat.OutputTokens != 4 || stat.CachedTokens != 3 || stat.CacheWriteTokens != 2 || stat.SentTokens != 16 || stat.EstimatedCost != 17 || stat.UpstreamCost != 17 || stat.FirstTokenMS != 50 || stat.FirstTokenSamples != 1 || stat.LatencyMS != 90 || stat.LatencySamples != 2 || stat.DurationMS != 400 || stat.AttemptCount != 3 {
		t.Fatalf("backfilled stat = %+v", stat)
	}
	var backfilledRequest RelayRequestLog
	if err := store.db.First(&backfilledRequest, "id = ?", logs[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	var backfilledAttempt RelayAttemptLog
	if err := store.db.First(&backfilledAttempt, attempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if backfilledRequest.NormalInputTokens != 5 || backfilledRequest.SentTokens != 10 || backfilledAttempt.NormalInputTokens != 5 || backfilledAttempt.SentTokens != 10 {
		t.Fatalf("backfilled token details = request %+v attempt %+v", backfilledRequest, backfilledAttempt)
	}
	statistics, err := NewManagementService(store).tokenStatistics(context.Background(), 7)
	if err != nil || statistics.NormalInputTokens != 11 || statistics.CacheWriteTokens != 2 || statistics.SentTokens != 16 || statistics.UpstreamCost != 17 || statistics.AverageFirstTokenMS != 50 || statistics.AverageLatency != 45 || statistics.AverageDurationMS != 200 {
		t.Fatalf("token statistics = %+v, error = %v", statistics, err)
	}
}

func TestBackfillResponsePhaseTimingsRunsOnce(t *testing.T) {
	store := newTestStore(t)
	createdAt := time.Date(2026, 7, 20, 4, 0, 0, 0, time.UTC)
	request := RelayRequestLog{
		ID: "timing-request", TokenID: 7, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusOK,
		Outcome: RelayOutcomeSuccess, LatencyMS: 100, FirstTokenMS: 350, DurationMS: 900, AttemptCount: 1, CreatedAt: createdAt,
	}
	if err := store.db.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	attempt := RelayAttemptLog{
		RequestID: request.ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", StatusCode: http.StatusOK,
		Outcome: RelayOutcomeSuccess, Success: true, LatencyMS: 80, FirstTokenMS: 300, DurationMS: 800, CreatedAt: createdAt,
	}
	if err := store.db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&TokenDailyStat{Date: "2026-07-20", TokenID: 7, RequestCount: 1, FirstTokenMS: 350, FirstTokenSamples: 1, LatencyMS: 100, LatencySamples: 1, DurationMS: 900}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&TokenDailyStat{Date: "2026-07-19", TokenID: 7, RequestCount: 2, FirstTokenMS: 700, FirstTokenSamples: 2, LatencyMS: 200, LatencySamples: 2, DurationMS: 1800}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.backfillResponsePhaseTimings(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillResponsePhaseTimings(); err != nil {
		t.Fatal(err)
	}
	if err := store.db.First(&request, "id = ?", request.ID).Error; err != nil {
		t.Fatal(err)
	}
	if request.FirstTokenMS != 250 || request.DurationMS != 800 || request.LatencyMS != 100 {
		t.Fatalf("request timings = %+v", request)
	}
	if err := store.db.First(&attempt, attempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.FirstTokenMS != 220 || attempt.DurationMS != 720 || attempt.LatencyMS != 80 {
		t.Fatalf("attempt timings = %+v", attempt)
	}
	var stat TokenDailyStat
	if err := store.db.First(&stat, "date = ? AND token_id = ?", "2026-07-20", 7).Error; err != nil {
		t.Fatal(err)
	}
	if stat.FirstTokenMS != 250 || stat.FirstTokenSamples != 1 || stat.DurationMS != 800 || stat.LatencyMS != 100 {
		t.Fatalf("daily timings = %+v", stat)
	}
	var aggregateOnlyStat TokenDailyStat
	if err := store.db.First(&aggregateOnlyStat, "date = ? AND token_id = ?", "2026-07-19", 7).Error; err != nil {
		t.Fatal(err)
	}
	if aggregateOnlyStat.FirstTokenMS != 700 || aggregateOnlyStat.DurationMS != 1800 {
		t.Fatalf("aggregate-only daily timings changed = %+v", aggregateOnlyStat)
	}
}

func TestBackfillCostFieldsZerosRecentFailuresAndPreservesOlderAggregates(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	recentDate := now.Format(time.DateOnly)
	oldDate := now.Add(-(DetailedLogRetentionDays*24*time.Hour + 48*time.Hour)).Format(time.DateOnly)
	logs := []RelayRequestLog{
		{ID: "recent-success", TokenID: 9, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusOK, InputTokens: 10, EstimatedCost: 11, CreatedAt: now.Add(-time.Hour)},
		{ID: "recent-failure", TokenID: 9, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusBadGateway, InputTokens: 20, EstimatedCost: 22, CreatedAt: now.Add(-30 * time.Minute)},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	attempts := []RelayAttemptLog{
		{RequestID: logs[0].ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", StatusCode: http.StatusOK, EstimatedCost: 11, Success: true, CreatedAt: logs[0].CreatedAt},
		{RequestID: logs[1].ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", StatusCode: http.StatusBadGateway, EstimatedCost: 22, Success: false, CreatedAt: logs[1].CreatedAt},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	stats := []TokenDailyStat{
		{Date: recentDate, TokenID: 9, RequestCount: 2, SuccessCount: 1, EstimatedCost: 33},
		{Date: oldDate, TokenID: 9, RequestCount: 4, SuccessCount: 3, EstimatedCost: 77},
	}
	if err := store.db.Create(&stats).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.backfillCostFields(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillCostFields(); err != nil {
		t.Fatal(err)
	}
	var recentFailure RelayRequestLog
	if err := store.db.First(&recentFailure, "id = ?", "recent-failure").Error; err != nil {
		t.Fatal(err)
	}
	if recentFailure.EstimatedCost != 0 || recentFailure.UpstreamCost != 0 || recentFailure.CostSource != CostSourceFailedZero {
		t.Fatalf("recent failed request = %+v", recentFailure)
	}
	var recentSuccess RelayRequestLog
	if err := store.db.First(&recentSuccess, "id = ?", "recent-success").Error; err != nil {
		t.Fatal(err)
	}
	if recentSuccess.EstimatedCost != 11 || recentSuccess.UpstreamCost != 11 || recentSuccess.CostSource != CostSourceFallback {
		t.Fatalf("recent successful request = %+v", recentSuccess)
	}
	var recentStat TokenDailyStat
	if err := store.db.First(&recentStat, "date = ? AND token_id = ?", recentDate, 9).Error; err != nil {
		t.Fatal(err)
	}
	if recentStat.RequestCount != 2 || recentStat.SuccessCount != 1 || recentStat.EstimatedCost != 11 || recentStat.UpstreamCost != 11 {
		t.Fatalf("rebuilt recent stat = %+v", recentStat)
	}
	var oldStat TokenDailyStat
	if err := store.db.First(&oldStat, "date = ? AND token_id = ?", oldDate, 9).Error; err != nil {
		t.Fatal(err)
	}
	if oldStat.EstimatedCost != 77 || oldStat.UpstreamCost != 77 {
		t.Fatalf("preserved old stat = %+v", oldStat)
	}
}

func TestBackfillRelayOutcomesSeparatesCanceledRequests(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	requests := []RelayRequestLog{
		{ID: "outcome-success", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusOK, CreatedAt: now},
		{ID: "outcome-canceled", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a", StatusCode: statusClientClosedRequest, ErrorCode: "request_canceled", CreatedAt: now},
		{ID: "outcome-failed", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusBadGateway, CreatedAt: now},
	}
	if err := store.db.Create(&requests).Error; err != nil {
		t.Fatal(err)
	}
	attempts := []RelayAttemptLog{
		{RequestID: requests[0].ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", StatusCode: http.StatusOK, Success: true, CreatedAt: now},
		{RequestID: requests[1].ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", StatusCode: http.StatusOK, Success: false, CreatedAt: now},
		{RequestID: requests[2].ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", StatusCode: http.StatusBadGateway, Success: false, CreatedAt: now},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&RelayRequestLog{}).Where("id IN ?", []string{requests[0].ID, requests[1].ID}).Update("outcome", RelayOutcomeFailed).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&RelayAttemptLog{}).Where("request_id IN ?", []string{requests[0].ID, requests[1].ID}).Updates(map[string]any{"outcome": RelayOutcomeFailed}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&TokenDailyStat{Date: now.Format(time.DateOnly), TokenID: 1, RequestCount: 3, SuccessCount: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Delete(&GatewayMigration{}, "name = ?", "relay_outcomes_v1").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.backfillRelayOutcomes(); err != nil {
		t.Fatal(err)
	}

	var logs []RelayRequestLog
	if err := store.db.Order("id").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	outcomes := make(map[string]string, len(logs))
	for _, log := range logs {
		outcomes[log.ID] = log.Outcome
	}
	if outcomes["outcome-success"] != RelayOutcomeSuccess || outcomes["outcome-canceled"] != RelayOutcomeCanceled || outcomes["outcome-failed"] != RelayOutcomeFailed {
		t.Fatalf("request outcomes = %+v", outcomes)
	}
	var canceledAttempt RelayAttemptLog
	if err := store.db.Where("request_id = ?", "outcome-canceled").First(&canceledAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if canceledAttempt.Outcome != RelayOutcomeCanceled || canceledAttempt.Success {
		t.Fatalf("canceled attempt = %+v", canceledAttempt)
	}
	var stat TokenDailyStat
	if err := store.db.Where("date = ? AND token_id = ?", now.Format(time.DateOnly), 1).First(&stat).Error; err != nil {
		t.Fatal(err)
	}
	if stat.CanceledCount != 1 {
		t.Fatalf("canceled daily count = %d", stat.CanceledCount)
	}
	summary, err := aggregateLogSummary(store.db.Model(&RelayRequestLog{}))
	if err != nil {
		t.Fatal(err)
	}
	if summary.RequestCount != 3 || summary.SuccessCount != 1 || summary.CanceledCount != 1 || summary.SuccessRate != 0.5 {
		t.Fatalf("outcome summary = %+v", summary)
	}
}

func TestCleanupExpiredKeepsOnlyFiveDaysOfDetails(t *testing.T) {
	store := newTestStore(t)
	oldCreatedAt := time.Now().Add(-(DetailedLogRetentionDays*24*time.Hour + time.Hour))
	recentCreatedAt := time.Now().Add(-(DetailedLogRetentionDays*24*time.Hour - time.Hour))
	logs := []RelayRequestLog{
		{ID: "old-request", TokenID: 1, Endpoint: "chat", RequestedModel: "model-a", StatusCode: http.StatusOK, CreatedAt: oldCreatedAt},
		{ID: "recent-request", TokenID: 1, Endpoint: "chat", RequestedModel: "model-a", StatusCode: http.StatusOK, CreatedAt: recentCreatedAt},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	attempts := []RelayAttemptLog{
		{RequestID: "old-request", ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", CreatedAt: oldCreatedAt},
		{RequestID: "recent-request", ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", CreatedAt: recentCreatedAt},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	stat := TokenDailyStat{Date: oldCreatedAt.UTC().Format(time.DateOnly), TokenID: 1, RequestCount: 1}
	if err := store.db.Create(&stat).Error; err != nil {
		t.Fatal(err)
	}
	store.cleanupExpired()
	var requestCount int64
	if err := store.db.Model(&RelayRequestLog{}).Count(&requestCount).Error; err != nil || requestCount != 1 {
		t.Fatalf("request count = %d, err = %v", requestCount, err)
	}
	var attemptCount int64
	if err := store.db.Model(&RelayAttemptLog{}).Count(&attemptCount).Error; err != nil || attemptCount != 1 {
		t.Fatalf("attempt count = %d, err = %v", attemptCount, err)
	}
	var statCount int64
	if err := store.db.Model(&TokenDailyStat{}).Count(&statCount).Error; err != nil || statCount != 1 {
		t.Fatalf("stat count = %d, err = %v", statCount, err)
	}
}

func TestExplicitFailedOutcomeSurvivesHTTP200AndOutcomeFiltering(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	logs := []RelayRequestLog{
		{ID: "semantic-success", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now},
		{ID: "semantic-failure", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusOK, Outcome: RelayOutcomeFailed, CreatedAt: now},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	var failed RelayRequestLog
	if err := store.db.First(&failed, "id = ?", "semantic-failure").Error; err != nil {
		t.Fatal(err)
	}
	if failed.Outcome != RelayOutcomeFailed {
		t.Fatalf("explicit outcome = %q", failed.Outcome)
	}
	page, err := NewManagementService(store).Logs(context.Background(), LogQuery{Outcome: RelayOutcomeFailed, Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != failed.ID || page.Summary.SuccessCount != 0 {
		t.Fatalf("filtered page = %+v", page)
	}
}

func TestDashboardUsesTokenStatsWithoutDoubleCountingDetails(t *testing.T) {
	store := newTestStore(t)
	_, _, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	now := time.Now().UTC()
	stat := TokenDailyStat{
		Date: now.Format(time.DateOnly), TokenID: 1, RequestCount: 8, SuccessCount: 6,
		InputTokens: 100, OutputTokens: 40, EstimatedCost: 900, UpstreamCost: 700,
		FirstTokenMS: 300, FirstTokenSamples: 3, LatencyMS: 400, LatencySamples: 4, DurationMS: 1600,
	}
	if err := store.db.Create(&stat).Error; err != nil {
		t.Fatal(err)
	}
	requestLog := RelayRequestLog{
		ID: "detail-request", TokenID: 1, Endpoint: "responses", RequestedModel: "public-model",
		StatusCode: http.StatusOK, InputTokens: 10, OutputTokens: 2, EstimatedCost: 50, UpstreamCost: 30, CreatedAt: now,
	}
	if err := store.db.Create(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	attemptLog := RelayAttemptLog{
		RequestID: requestLog.ID, ChannelID: channels[0].ID, ChannelModelID: mappings[0].ID,
		UpstreamModel: mappings[0].UpstreamModel, StatusCode: http.StatusOK, EstimatedCost: 50, UpstreamCost: 30, Success: true, CreatedAt: now,
	}
	if err := store.db.Create(&attemptLog).Error; err != nil {
		t.Fatal(err)
	}
	summary, err := NewManagementService(store).Dashboard(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 1 || summary.InputTokens != 10 || summary.OutputTokens != 2 || summary.EstimatedCost != 50 || summary.UpstreamCost != 30 || summary.AverageFirstTokenMS != 0 || summary.AverageLatency != 0 || summary.AverageDurationMS != 0 {
		t.Fatalf("dashboard totals = %+v", summary)
	}
	if len(summary.Channels) != 1 || summary.Channels[0].Requests != 1 || summary.Channels[0].Successes != 1 || summary.Channels[0].SuccessRate != 1 || summary.Channels[0].InputTokens != 10 || summary.Channels[0].OutputTokens != 2 || summary.Channels[0].UpstreamCost != 30 || len(summary.Models) != 1 || summary.Models[0].Requests != 1 || summary.Models[0].Successes != 1 || summary.Models[0].SuccessRate != 1 || summary.Models[0].InputTokens != 10 || summary.Models[0].OutputTokens != 2 || summary.Models[0].UpstreamCost != 30 {
		t.Fatalf("dashboard breakdowns = channels %+v models %+v", summary.Channels, summary.Models)
	}
	if len(summary.Daily) != 1 || summary.Daily[0].Date != eastEightDate(now) || summary.Daily[0].Requests != 1 {
		t.Fatalf("dashboard daily = %+v", summary.Daily)
	}
}

func TestDashboardIncludesHourlyTrendAndTopCostRatios(t *testing.T) {
	store := newTestStore(t)
	currentHour := time.Now().In(eastEightLocation).Truncate(time.Hour)
	requests := []RelayRequestLog{
		{ID: "ratio-two-a", TokenID: 1, Endpoint: "responses", RequestedModel: "gpt-5", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, InputTokens: 1_000_000, NormalInputTokens: 1_000_000, UpstreamCost: 2_500_000, CreatedAt: currentHour.Add(time.Minute).UTC()},
		{ID: "ratio-two-b", TokenID: 1, Endpoint: "responses", RequestedModel: "gpt-5", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, InputTokens: 1_000_000, NormalInputTokens: 1_000_000, UpstreamCost: 2_500_000, CreatedAt: currentHour.Add(2 * time.Minute).UTC()},
		{ID: "ratio-one-half", TokenID: 1, Endpoint: "responses", RequestedModel: "gpt-5", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, InputTokens: 1_000_000, NormalInputTokens: 1_000_000, UpstreamCost: 1_875_000, CreatedAt: currentHour.Add(3 * time.Minute).UTC()},
	}
	if err := store.db.Create(&requests).Error; err != nil {
		t.Fatal(err)
	}

	summary, err := NewManagementService(store).Dashboard(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Hourly) == 0 {
		t.Fatal("dashboard hourly trend is empty")
	}
	latest := summary.Hourly[len(summary.Hourly)-1]
	if latest.Hour != currentHour.Format("2006-01-02T15:00:00-07:00") || latest.Requests != 3 || latest.Successes != 3 {
		t.Fatalf("latest hourly bucket = %+v", latest)
	}
	if len(summary.CostRatios) != 2 {
		t.Fatalf("cost ratio distribution = %+v", summary.CostRatios)
	}
	if first := summary.CostRatios[0]; first.Ratio != 2 || first.Requests != 2 || math.Abs(first.Share-2.0/3.0) > 0.0001 {
		t.Fatalf("top cost ratio = %+v", first)
	}
	if second := summary.CostRatios[1]; second.Ratio != 1.5 || second.Requests != 1 || math.Abs(second.Share-1.0/3.0) > 0.0001 {
		t.Fatalf("second cost ratio = %+v", second)
	}
}

func TestDashboardChannelBreakdownCountsEachRequestOnce(t *testing.T) {
	store := newTestStore(t)
	_, model, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid", "http://two.invalid")
	now := time.Now().UTC()
	requests := []RelayRequestLog{
		{ID: "retried-request", TokenID: 1, Endpoint: "responses", RequestedModel: model.Name, StatusCode: http.StatusOK, InputTokens: 100, CachedTokens: 40, OutputTokens: 20, EstimatedCost: 70, UpstreamCost: 60, AttemptCount: 2, CreatedAt: now},
		{ID: "unrouted-request", TokenID: 1, Endpoint: "responses", RequestedModel: model.Name, StatusCode: http.StatusServiceUnavailable, AttemptCount: 0, CreatedAt: now},
	}
	if err := store.db.Create(&requests).Error; err != nil {
		t.Fatal(err)
	}
	attempts := []RelayAttemptLog{
		{RequestID: requests[0].ID, ChannelID: channels[0].ID, ChannelName: channels[0].Name, ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel, StatusCode: http.StatusBadGateway, EstimatedCost: 999, UpstreamCost: 999, Success: false, CreatedAt: now.Add(-time.Second)},
		{RequestID: requests[0].ID, ChannelID: channels[1].ID, ChannelName: channels[1].Name, ChannelModelID: mappings[1].ID, UpstreamModel: mappings[1].UpstreamModel, StatusCode: http.StatusOK, EstimatedCost: 70, UpstreamCost: 60, Success: true, CreatedAt: now},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}

	summary, err := NewManagementService(store).Dashboard(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 2 || summary.UpstreamCost != 60 || summary.EstimatedCost != 70 {
		t.Fatalf("dashboard totals = %+v", summary)
	}
	if len(summary.Channels) != 2 {
		t.Fatalf("channel breakdown = %+v", summary.Channels)
	}
	byName := make(map[string]DashboardBreakdown, len(summary.Channels))
	for _, item := range summary.Channels {
		byName[item.Name] = item
	}
	if final := byName[channels[1].Name]; final.Requests != 1 || final.Successes != 1 || final.SuccessRate != 1 || final.CachedTokens != 40 || final.CacheHitRate != 0.4 || final.UpstreamCost != 60 || final.EstimatedCost != 70 {
		t.Fatalf("final channel breakdown = %+v", final)
	}
	if unrouted := byName["未归属渠道"]; unrouted.Requests != 1 || unrouted.Successes != 0 || unrouted.SuccessRate != 0 || unrouted.UpstreamCost != 0 || unrouted.EstimatedCost != 0 {
		t.Fatalf("unrouted breakdown = %+v", unrouted)
	}
}

func TestDashboardBreakdownSuccessRateExcludesCanceledRequests(t *testing.T) {
	store := newTestStore(t)
	_, model, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	now := time.Now().UTC()
	requests := []RelayRequestLog{
		{ID: "breakdown-success", TokenID: 1, Endpoint: "responses", RequestedModel: model.Name, StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now},
		{ID: "breakdown-failed", TokenID: 1, Endpoint: "responses", RequestedModel: model.Name, StatusCode: http.StatusBadGateway, Outcome: RelayOutcomeFailed, CreatedAt: now},
		{ID: "breakdown-canceled", TokenID: 1, Endpoint: "responses", RequestedModel: model.Name, StatusCode: 499, Outcome: RelayOutcomeCanceled, CreatedAt: now},
	}
	if err := store.db.Create(&requests).Error; err != nil {
		t.Fatal(err)
	}
	for index, request := range requests {
		if err := store.db.Create(&RelayAttemptLog{
			RequestID: request.ID, ChannelID: channels[0].ID, ChannelName: channels[0].Name,
			ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel,
			StatusCode: request.StatusCode, Success: request.Outcome == RelayOutcomeSuccess, CreatedAt: now.Add(time.Duration(index) * time.Millisecond),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	summary, err := NewManagementService(store).Dashboard(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Channels) != 1 || len(summary.Models) != 1 {
		t.Fatalf("dashboard breakdowns = channels %+v models %+v", summary.Channels, summary.Models)
	}
	for _, item := range []DashboardBreakdown{summary.Channels[0], summary.Models[0]} {
		if item.Requests != 3 || item.Successes != 1 || item.CanceledCount != 1 || item.SuccessRate != 0.5 {
			t.Fatalf("breakdown success summary = %+v", item)
		}
	}
}

func TestRetryFailureUsageIsNotAddedToRequestTotals(t *testing.T) {
	store := newTestStore(t)
	relay := newTestRelay(store)
	execution := &relayExecution{usageSources: map[string]struct{}{}, costSources: map[string]struct{}{}}
	failed := &attemptResult{usage: Usage{InputTokens: 100, OutputTokens: 20, Source: "upstream"}, outcome: RelayOutcomeFailed}
	relay.addCanceledAttemptUsage(execution, failed)
	if execution.usage.InputTokens != 0 || execution.usage.OutputTokens != 0 || execution.estimatedCost != 0 || execution.upstreamCost != 0 || len(execution.usageSources) != 0 {
		t.Fatalf("failed retry usage leaked into request totals: %+v", execution)
	}
	canceled := &attemptResult{usage: Usage{InputTokens: 10, OutputTokens: 2, Source: "estimated_tiktoken"}, estimatedCost: 5, upstreamCost: 5, costSource: CostSourceFallback, outcome: RelayOutcomeCanceled}
	relay.addCanceledAttemptUsage(execution, canceled)
	if execution.usage.InputTokens != 10 || execution.usage.OutputTokens != 2 || execution.estimatedCost != 5 || execution.upstreamCost != 5 {
		t.Fatalf("canceled final attempt usage was not retained: %+v", execution)
	}
}

func TestBackfillRequestStatisticsUsesFinalAttemptAndRebuildsTokenStats(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	requestLog := RelayRequestLog{
		ID: "historical-retry", TokenID: 7, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess,
		InputTokens: 110, NormalInputTokens: 107, OutputTokens: 25, CachedTokens: 2, CacheWriteTokens: 1, SentTokens: 30,
		EstimatedCost: 15, UpstreamCost: 42, CostSource: CostSourceUpstream, UsageSource: "mixed", AttemptCount: 2, CreatedAt: now,
	}
	if err := store.db.Create(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	attempts := []RelayAttemptLog{
		{RequestID: requestLog.ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a", InputTokens: 100, NormalInputTokens: 100, OutputTokens: 20, Outcome: RelayOutcomeFailed, CreatedAt: now.Add(-time.Second)},
		{RequestID: requestLog.ID, ChannelID: 2, ChannelModelID: 2, UpstreamModel: "model-a", InputTokens: 10, NormalInputTokens: 7, OutputTokens: 5, CachedTokens: 2, CacheWriteTokens: 1, EstimatedCost: 15, UpstreamCost: 42, CostSource: CostSourceUpstream, UsageSource: "upstream", Success: true, Outcome: RelayOutcomeSuccess, CreatedAt: now},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&TokenDailyStat{Date: now.Format(time.DateOnly), TokenID: 7, RequestCount: 1, InputTokens: 110, OutputTokens: 25, EstimatedCost: 15, UpstreamCost: 42}).Error; err != nil {
		t.Fatal(err)
	}

	if err := store.backfillRequestStatisticsFromFinalAttempts(); err != nil {
		t.Fatal(err)
	}
	var corrected RelayRequestLog
	if err := store.db.First(&corrected, "id = ?", requestLog.ID).Error; err != nil {
		t.Fatal(err)
	}
	if corrected.InputTokens != 10 || corrected.NormalInputTokens != 7 || corrected.OutputTokens != 5 || corrected.CachedTokens != 2 || corrected.CacheWriteTokens != 1 || corrected.EstimatedCost != 15 || corrected.UpstreamCost != 42 || corrected.UsageSource != "upstream" {
		t.Fatalf("corrected request = %+v", corrected)
	}
	var stat TokenDailyStat
	if err := store.db.First(&stat, "date = ? AND token_id = ?", now.Format(time.DateOnly), 7).Error; err != nil {
		t.Fatal(err)
	}
	if stat.RequestCount != 1 || stat.InputTokens != 10 || stat.OutputTokens != 5 || stat.EstimatedCost != 15 || stat.UpstreamCost != 42 || stat.AttemptCount != 2 {
		t.Fatalf("rebuilt token statistics = %+v", stat)
	}
	if err := store.backfillRequestStatisticsFromFinalAttempts(); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardFiltersSelectedDayRange(t *testing.T) {
	store := newTestStore(t)
	_, _, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	today := eastEightStartOfDay(time.Now())
	now := today.Add(12 * time.Hour).UTC()
	yesterday := today.AddDate(0, 0, -1).Add(12 * time.Hour).UTC()
	stats := []TokenDailyStat{
		{Date: eastEightDate(now), TokenID: 1, RequestCount: 3, SuccessCount: 3},
		{Date: eastEightDate(yesterday), TokenID: 1, RequestCount: 2, SuccessCount: 1},
	}
	if err := store.db.Create(&stats).Error; err != nil {
		t.Fatal(err)
	}
	requests := []RelayRequestLog{
		{ID: "today", TokenID: 1, Endpoint: "responses", RequestedModel: "public-model", StatusCode: http.StatusOK, CreatedAt: now},
		{ID: "yesterday", TokenID: 1, Endpoint: "responses", RequestedModel: "public-model", StatusCode: http.StatusOK, CreatedAt: yesterday},
	}
	if err := store.db.Create(&requests).Error; err != nil {
		t.Fatal(err)
	}
	attempts := []RelayAttemptLog{
		{RequestID: requests[0].ID, ChannelID: channels[0].ID, ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel, StatusCode: http.StatusOK, Success: true, CreatedAt: now},
		{RequestID: requests[1].ID, ChannelID: channels[0].ID, ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel, StatusCode: http.StatusOK, Success: true, CreatedAt: yesterday},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}

	current, err := NewManagementService(store).Dashboard(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if current.Requests != 1 || len(current.Daily) != 1 || len(current.Channels) != 1 || current.Channels[0].Requests != 1 || len(current.Models) != 1 || current.Models[0].Requests != 1 {
		t.Fatalf("current dashboard = %+v", current)
	}
	recent, err := NewManagementService(store).Dashboard(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if recent.Requests != 2 || recent.SuccessRate != 1 || len(recent.Daily) != 2 || len(recent.Channels) != 1 || recent.Channels[0].Requests != 2 || len(recent.Models) != 1 || recent.Models[0].Requests != 2 {
		t.Fatalf("two-day dashboard = %+v", recent)
	}
	if _, err := NewManagementService(store).Dashboard(context.Background(), 4); err == nil {
		t.Fatal("expected unsupported dashboard range to fail")
	}
}

func TestEastEightDayQueriesStayAlignedAcrossViews(t *testing.T) {
	store := newTestStore(t)
	token, model, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	today := eastEightStartOfDay(time.Now())
	startUTC := today.UTC()
	endUTC := today.AddDate(0, 0, 1).UTC()
	logs := []RelayRequestLog{
		{ID: "before-east-eight-day", TokenID: token.ID, Endpoint: "responses", RequestedModel: model.Name, CodexSessionID: "before", StatusCode: http.StatusOK, CreatedAt: startUTC.Add(-time.Second)},
		{ID: "east-eight-day-start", TokenID: token.ID, Endpoint: "responses", RequestedModel: model.Name, CodexSessionID: "start", StatusCode: http.StatusOK, CreatedAt: startUTC},
		{ID: "east-eight-day-end", TokenID: token.ID, Endpoint: "responses", RequestedModel: model.Name, CodexSessionID: "end", StatusCode: http.StatusOK, CreatedAt: endUTC.Add(-time.Microsecond)},
		{ID: "after-east-eight-day", TokenID: token.ID, Endpoint: "responses", RequestedModel: model.Name, CodexSessionID: "after", StatusCode: http.StatusOK, CreatedAt: endUTC},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}

	management := NewManagementService(store)
	dashboard, err := management.Dashboard(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	queryFrom := today
	queryTo := today.AddDate(0, 0, 1).Add(-time.Millisecond)
	logPage, err := management.Logs(context.Background(), LogQuery{From: queryFrom, To: queryTo, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	sessionPage, err := management.SessionLogs(context.Background(), SessionLogQuery{From: queryFrom, To: queryTo, Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}

	if dashboard.Requests != 2 || logPage.Summary.RequestCount != 2 || sessionPage.Summary.RequestCount != 2 {
		t.Fatalf("east-eight request counts: dashboard=%d logs=%d sessions=%d", dashboard.Requests, logPage.Summary.RequestCount, sessionPage.Summary.RequestCount)
	}
	if len(dashboard.Daily) != 1 || dashboard.Daily[0].Date != today.Format(time.DateOnly) || dashboard.Daily[0].Requests != 2 {
		t.Fatalf("east-eight dashboard daily = %+v", dashboard.Daily)
	}
}

func TestRebuildTokenDailyStatsGroupsByEastEightDate(t *testing.T) {
	store := newTestStore(t)
	logs := []RelayRequestLog{
		{ID: "before-east-eight-midnight", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusOK, CreatedAt: time.Date(2026, 7, 27, 15, 59, 59, 0, time.UTC)},
		{ID: "after-east-eight-midnight", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a", StatusCode: http.StatusOK, CreatedAt: time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if err := rebuildTokenDailyStats(store.db); err != nil {
		t.Fatal(err)
	}
	var stats []TokenDailyStat
	if err := store.db.Order("date ASC").Find(&stats).Error; err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 || stats[0].Date != "2026-07-27" || stats[0].RequestCount != 1 || stats[1].Date != "2026-07-28" || stats[1].RequestCount != 1 {
		t.Fatalf("east-eight daily stats = %+v", stats)
	}
}

func TestListChannelsIncludesRecentLatencyAndCacheMetrics(t *testing.T) {
	store := newTestStore(t)
	secret, err := store.secretBox.Encrypt("upstream-secret")
	if err != nil {
		t.Fatal(err)
	}
	channels := []Channel{
		{Name: "measured", BaseURL: "https://measured.example/v1", APIKeyCipher: secret, Enabled: true},
		{Name: "empty", BaseURL: "https://empty.example/v1", APIKeyCipher: secret, Enabled: true},
	}
	if err := store.db.Create(&channels).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	latencyAttempts := make([]RelayAttemptLog, 0, 50)
	for index := 0; index < 50; index++ {
		latencyAttempts = append(latencyAttempts, RelayAttemptLog{
			RequestID:      fmt.Sprintf("latency-%d", index),
			ChannelID:      channels[0].ID,
			ChannelModelID: 1,
			UpstreamModel:  "upstream-model",
			StatusCode:     http.StatusOK,
			InputTokens:    10,
			CachedTokens:   5,
			UsageSource:    "upstream",
			FirstTokenMS:   int64(200 + index),
			LatencyMS:      int64(100 + index),
			DurationMS:     int64(300 + index),
			Success:        true,
			CreatedAt:      now.Add(-4*time.Hour + time.Duration(index)*time.Minute),
		})
	}
	if err := store.db.Create(&latencyAttempts).Error; err != nil {
		t.Fatal(err)
	}
	extraAttempts := []RelayAttemptLog{
		{RequestID: "usage-without-latency", ChannelID: channels[0].ID, ChannelModelID: 1, UpstreamModel: "upstream-model", StatusCode: http.StatusOK, InputTokens: 100, CachedTokens: 25, UsageSource: "upstream", Success: true, CreatedAt: now.Add(-30 * time.Minute)},
		{RequestID: "estimated-usage", ChannelID: channels[0].ID, ChannelModelID: 1, UpstreamModel: "upstream-model", StatusCode: http.StatusOK, InputTokens: 400, UsageSource: "estimated_tiktoken", FirstTokenMS: 175, LatencyMS: 75, DurationMS: 275, Success: true, CreatedAt: now.Add(-25 * time.Minute)},
		{RequestID: "failed", ChannelID: channels[0].ID, ChannelModelID: 1, UpstreamModel: "upstream-model", StatusCode: http.StatusBadGateway, InputTokens: 1000, CachedTokens: 1000, UsageSource: "upstream", LatencyMS: 888, Success: false, CreatedAt: now.Add(-20 * time.Minute)},
		{RequestID: "expired", ChannelID: channels[0].ID, ChannelModelID: 1, UpstreamModel: "upstream-model", StatusCode: http.StatusOK, InputTokens: 1000, CachedTokens: 1000, UsageSource: "upstream", LatencyMS: 999, Success: true, CreatedAt: now.Add(-(DetailedLogRetentionDays*24*time.Hour + time.Hour))},
	}
	if err := store.db.Create(&extraAttempts).Error; err != nil {
		t.Fatal(err)
	}

	views, err := NewManagementService(store).ListChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var measured, empty *ChannelView
	for index := range views {
		switch views[index].ID {
		case channels[0].ID:
			measured = &views[index]
		case channels[1].ID:
			empty = &views[index]
		}
	}
	if measured == nil || empty == nil {
		t.Fatalf("channel views = %+v", views)
	}
	if measured.Metrics.LatencySampleCount != 51 || len(measured.Metrics.LatencySeries) != channelLatencyPointLimit {
		t.Fatalf("latency metrics = %+v", measured.Metrics)
	}
	if measured.Metrics.LatencySeries[0].LatencyMS != 103 || measured.Metrics.LatencySeries[channelLatencyPointLimit-2].LatencyMS != 149 || measured.Metrics.LatencySeries[channelLatencyPointLimit-1].LatencyMS != 75 || measured.Metrics.LatestLatencyMS != 75 {
		t.Fatalf("latency series is not the latest chronological window: %+v", measured.Metrics.LatencySeries)
	}
	if measured.Metrics.FirstTokenSampleCount != 51 || measured.Metrics.LatencySampleCount != 51 || measured.Metrics.DurationSampleCount != 51 || math.Abs(measured.Metrics.AverageFirstTokenMS-223.5294117647) > 0.000001 || math.Abs(measured.Metrics.AverageLatencyMS-123.5294117647) > 0.000001 || math.Abs(measured.Metrics.AverageDurationMS-323.5294117647) > 0.000001 {
		t.Fatalf("timing metrics = %+v", measured.Metrics)
	}
	for index := 1; index < len(measured.Metrics.LatencySeries); index++ {
		if measured.Metrics.LatencySeries[index-1].RecordedAt.After(measured.Metrics.LatencySeries[index].RecordedAt) {
			t.Fatalf("latency series is not chronological: %+v", measured.Metrics.LatencySeries)
		}
	}
	if measured.Metrics.InputTokens != 600 || measured.Metrics.CachedTokens != 275 || math.Abs(measured.Metrics.CacheHitRate-275.0/600.0) > 0.000001 {
		t.Fatalf("cache metrics = %+v", measured.Metrics)
	}
	if measured.Metrics.RecentSuccessCount != 1 || measured.Metrics.RecentAttemptCount != 2 || measured.Metrics.RecentSuccessRate != 0.5 {
		t.Fatalf("recent channel success metrics = %+v", measured.Metrics)
	}
	if empty.Metrics.LatencySeries == nil || len(empty.Metrics.LatencySeries) != 0 || empty.Metrics.InputTokens != 0 || empty.Metrics.CacheHitRate != 0 || empty.Metrics.RecentSuccessRate != 1 || empty.Metrics.RecentAttemptCount != 0 {
		t.Fatalf("empty channel metrics = %+v", empty.Metrics)
	}
}

func TestRecentSuccessMetricsUseThirtyMinuteWindowAndAggregateChannelModels(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	attempts := []RelayAttemptLog{
		{RequestID: "model-a-success-1", ChannelID: 10, ChannelModelID: 101, UpstreamModel: "model-a", Success: true, CreatedAt: now.Add(-29 * time.Minute)},
		{RequestID: "model-a-success-2", ChannelID: 10, ChannelModelID: 101, UpstreamModel: "model-a", Success: true, CreatedAt: now.Add(-20 * time.Minute)},
		{RequestID: "model-a-failure", ChannelID: 10, ChannelModelID: 101, UpstreamModel: "model-a", Success: false, CreatedAt: now.Add(-10 * time.Minute)},
		{RequestID: "model-b-success", ChannelID: 10, ChannelModelID: 102, UpstreamModel: "model-b", Success: true, CreatedAt: now.Add(-5 * time.Minute)},
		{RequestID: "model-b-failure", ChannelID: 10, ChannelModelID: 102, UpstreamModel: "model-b", Success: false, CreatedAt: now.Add(-time.Minute)},
		{RequestID: "other-channel-failure", ChannelID: 20, ChannelModelID: 201, UpstreamModel: "model-c", Success: false, CreatedAt: now.Add(-2 * time.Minute)},
		{RequestID: "expired-failure", ChannelID: 10, ChannelModelID: 101, UpstreamModel: "model-a", Success: false, CreatedAt: now.Add(-31 * time.Minute)},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}

	metrics, err := loadRecentSuccessMetrics(context.Background(), store.db, []uint64{10, 20, 30}, []uint64{101, 102, 201, 301}, now)
	if err != nil {
		t.Fatal(err)
	}
	if metric := metrics.ByChannelModel[101]; metric.Successes != 2 || metric.Attempts != 3 || math.Abs(metric.rate()-2.0/3.0) > 0.000001 {
		t.Fatalf("model A metric = %+v, rate %v", metric, metric.rate())
	}
	if metric := metrics.ByChannel[10]; metric.Successes != 3 || metric.Attempts != 5 || metric.rate() != 0.6 {
		t.Fatalf("channel metric = %+v, rate %v", metric, metric.rate())
	}
	if metric := metrics.ByChannel[20]; metric.Successes != 0 || metric.Attempts != 1 || metric.rate() != 0 {
		t.Fatalf("failed channel metric = %+v, rate %v", metric, metric.rate())
	}
	if metric := metrics.ByChannel[30]; metric.Attempts != 0 || metric.rate() != 1 {
		t.Fatalf("empty channel metric = %+v, rate %v", metric, metric.rate())
	}
	if metric := metrics.ByChannelModel[301]; metric.Attempts != 0 || metric.rate() != 1 {
		t.Fatalf("empty model metric = %+v, rate %v", metric, metric.rate())
	}
}

func TestListChannelsIncludesRecentModelSuccessMetrics(t *testing.T) {
	store := newTestStore(t)
	_, _, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid", "http://two.invalid")
	now := time.Now()
	attempts := []RelayAttemptLog{
		{RequestID: "recent-model-success", ChannelID: channels[0].ID, ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel, Success: true, CreatedAt: now.Add(-10 * time.Minute)},
		{RequestID: "recent-model-failure", ChannelID: channels[0].ID, ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel, Success: false, CreatedAt: now.Add(-5 * time.Minute)},
		{RequestID: "expired-other-model-failure", ChannelID: channels[1].ID, ChannelModelID: mappings[1].ID, UpstreamModel: mappings[1].UpstreamModel, Success: false, CreatedAt: now.Add(-31 * time.Minute)},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}

	views, err := NewManagementService(store).ListChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	modelsByID := make(map[uint64]ChannelModel, len(mappings))
	metricsByChannelID := make(map[uint64]ChannelMetrics, len(channels))
	for _, view := range views {
		metricsByChannelID[view.ID] = view.Metrics
		for _, mapping := range view.Models {
			modelsByID[mapping.ID] = mapping
		}
	}
	measured := modelsByID[mappings[0].ID]
	if measured.RecentSuccessRate != 0.5 || measured.RecentSuccessCount != 1 || measured.RecentAttemptCount != 2 {
		t.Fatalf("measured mapping = %+v", measured)
	}
	empty := modelsByID[mappings[1].ID]
	if empty.RecentSuccessRate != 1 || empty.RecentSuccessCount != 0 || empty.RecentAttemptCount != 0 {
		t.Fatalf("empty mapping = %+v", empty)
	}
	if metrics := metricsByChannelID[channels[0].ID]; metrics.RecentSuccessRate != 0.5 || metrics.RecentSuccessCount != 1 || metrics.RecentAttemptCount != 2 {
		t.Fatalf("measured channel = %+v", metrics)
	}
	if metrics := metricsByChannelID[channels[1].ID]; metrics.RecentSuccessRate != 1 || metrics.RecentAttemptCount != 0 {
		t.Fatalf("empty channel = %+v", metrics)
	}
}

func TestDiscoverChannelModelsUsesStoredSecretAndNormalizesList(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" {
			t.Errorf("request path = %q", request.URL.Path)
		}
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer upstream-secret" {
			t.Errorf("Authorization = %q", authorization)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"object":"list","data":[{"id":"model-z","owned_by":"provider-z","created":22},{"id":"model-a","owned_by":"provider-a","created":11},{"id":"model-z"},{"id":"  "}]}`))
	}))
	defer upstream.Close()
	ciphertext, err := store.secretBox.Encrypt("upstream-secret")
	if err != nil {
		t.Fatal(err)
	}
	channel := Channel{Name: "discover", BaseURL: upstream.URL + "/v1", APIKeyCipher: ciphertext, Enabled: true}
	if err := store.db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	management := NewManagementService(store)
	latency, status, err := management.TestChannel(context.Background(), channel.ID)
	if err != nil || latency < 0 || status != http.StatusOK {
		t.Fatalf("TestChannel() = latency %d, status %d, error %v", latency, status, err)
	}
	var modelCount int64
	if err := store.db.Model(&GatewayModel{}).Count(&modelCount).Error; err != nil {
		t.Fatal(err)
	}
	if modelCount != 0 {
		t.Fatalf("channel test created %d public models", modelCount)
	}
	discovery, err := management.DiscoverChannelModels(context.Background(), ChannelModelDiscoveryInput{ChannelID: channel.ID})
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Status != http.StatusOK || len(discovery.Models) != 2 || discovery.Models[0].ID != "model-a" || discovery.Models[1].ID != "model-z" {
		t.Fatalf("discovery = %+v", discovery)
	}
	if discovery.Models[0].OwnedBy != "provider-a" || discovery.Models[0].Created != 11 {
		t.Fatalf("first model = %+v", discovery.Models[0])
	}
	if !discovery.Models[0].PublicModelCreated || !discovery.Models[1].PublicModelCreated || discovery.Models[0].PublicModelID == 0 || discovery.Models[1].PublicModelID == 0 {
		t.Fatalf("discovered public models = %+v", discovery.Models)
	}
	var publicModels []GatewayModel
	if err := store.db.Order("name asc").Find(&publicModels).Error; err != nil {
		t.Fatal(err)
	}
	if len(publicModels) != 2 || publicModels[0].Name != "model-a" || publicModels[1].Name != "model-z" {
		t.Fatalf("public models = %+v", publicModels)
	}
	for _, publicModel := range publicModels {
		if !publicModel.Enabled || publicModel.RoutingStrategy != RoutingPriorityWeighted {
			t.Fatalf("public model defaults = %+v", publicModel)
		}
	}
	var refreshed Channel
	if err := store.db.First(&refreshed, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.LastHealthAt == nil || refreshed.LastError != "" {
		t.Fatalf("channel health = %+v", refreshed)
	}

	unsaved, err := management.DiscoverChannelModels(context.Background(), ChannelModelDiscoveryInput{
		BaseURL: upstream.URL + "/v1",
		APIKey:  "upstream-secret",
	})
	if err != nil || len(unsaved.Models) != 2 {
		t.Fatalf("unsaved discovery = %+v, %v", unsaved, err)
	}
	if unsaved.Models[0].PublicModelCreated || unsaved.Models[1].PublicModelCreated || unsaved.Models[0].PublicModelID != discovery.Models[0].PublicModelID || unsaved.Models[1].PublicModelID != discovery.Models[1].PublicModelID {
		t.Fatalf("idempotent public model discovery = %+v", unsaved.Models)
	}
}

func TestDiscoverChannelModelsIncludesExactOfficialPrice(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"id":"gpt-4.1"},{"id":"gpt-4.1-custom"}]}`))
	}))
	defer upstream.Close()
	discovery, err := NewManagementService(store).DiscoverChannelModels(context.Background(), ChannelModelDiscoveryInput{
		BaseURL: upstream.URL,
		APIKey:  "upstream-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Models) != 2 || discovery.Models[0].OfficialPrice == nil || discovery.Models[0].OfficialPrice.Currency != "USD" || discovery.Models[0].OfficialPrice.Unit != OpenAIPriceCatalogUnit || discovery.Models[1].OfficialPrice != nil {
		t.Fatalf("discovery prices = %+v", discovery.Models)
	}
}

func TestClientAccessLimitsAndModelPermission(t *testing.T) {
	store := newTestStore(t)
	access := NewClientAccessService(store)
	token := &ClientToken{ID: 9, Enabled: true, RPM: 10, MaxConcurrency: 1}
	release, err := access.Acquire(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.Acquire(token); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("Acquire() error = %v, want concurrency error", err)
	}
	release()
	secondRelease, err := access.Acquire(token)
	if err != nil {
		t.Fatal(err)
	}
	secondRelease()

	token.RPM = 2
	if _, err := access.Acquire(token); !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("Acquire() error = %v, want RPM error", err)
	}

	model := GatewayModel{Name: "restricted", RoutingStrategy: RoutingPriorityWeighted, Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	if err := access.AuthorizeModel(context.Background(), token, model.ID); !errors.Is(err, ErrModelNotAllowed) {
		t.Fatalf("AuthorizeModel() error = %v", err)
	}
	if err := store.db.Create(&ClientTokenModel{TokenID: token.ID, ModelID: model.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := access.AuthorizeModel(context.Background(), token, model.ID); err != nil {
		t.Fatal(err)
	}
}

func TestClientAccessListModelsReturnsOnlyRoutableModels(t *testing.T) {
	store := newTestStore(t)
	access := NewClientAccessService(store)
	now := time.Now()

	type routeState struct {
		modelEnabled   bool
		mappingEnabled bool
		channelEnabled bool
		circuitUntil   *time.Time
	}
	createModel := func(name string, state routeState) GatewayModel {
		t.Helper()
		model := GatewayModel{Name: name, RoutingStrategy: RoutingPriorityWeighted, Enabled: true}
		if err := store.db.Create(&model).Error; err != nil {
			t.Fatal(err)
		}
		channel := Channel{
			Name: name + "-channel", BaseURL: "http://upstream.invalid/v1", APIKeyCipher: "cipher",
			Enabled: true, CircuitOpenUntil: state.circuitUntil,
		}
		if err := store.db.Create(&channel).Error; err != nil {
			t.Fatal(err)
		}
		mapping := ChannelModel{
			ChannelID: channel.ID, ModelID: model.ID, UpstreamModel: name,
			Priority: 1, Weight: 1, Enabled: true,
		}
		if err := store.db.Create(&mapping).Error; err != nil {
			t.Fatal(err)
		}
		if !state.modelEnabled {
			if err := store.db.Model(&model).Update("enabled", false).Error; err != nil {
				t.Fatal(err)
			}
		}
		if !state.channelEnabled {
			if err := store.db.Model(&channel).Update("enabled", false).Error; err != nil {
				t.Fatal(err)
			}
		}
		if !state.mappingEnabled {
			if err := store.db.Model(&mapping).Update("enabled", false).Error; err != nil {
				t.Fatal(err)
			}
		}
		return model
	}

	available := createModel("available", routeState{modelEnabled: true, mappingEnabled: true, channelEnabled: true})
	expiredCircuit := now.Add(-time.Minute)
	expired := createModel("expired-circuit", routeState{modelEnabled: true, mappingEnabled: true, channelEnabled: true, circuitUntil: &expiredCircuit})
	createModel("disabled-model", routeState{mappingEnabled: true, channelEnabled: true})
	createModel("disabled-mapping", routeState{modelEnabled: true, channelEnabled: true})
	createModel("disabled-channel", routeState{modelEnabled: true, mappingEnabled: true})
	openCircuit := now.Add(time.Minute)
	createModel("open-circuit", routeState{modelEnabled: true, mappingEnabled: true, channelEnabled: true, circuitUntil: &openCircuit})
	if err := store.db.Create(&GatewayModel{Name: "no-mapping", RoutingStrategy: RoutingPriorityWeighted, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	allModels, err := access.ListModels(context.Background(), &ClientToken{ID: 1, Enabled: true, AllowAllModels: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(allModels) != 2 || allModels[0].ID != available.ID || allModels[1].ID != expired.ID {
		t.Fatalf("all-model token models = %+v", allModels)
	}

	restrictedToken := &ClientToken{ID: 2, Enabled: true}
	if err := store.db.Create(&ClientTokenModel{TokenID: restrictedToken.ID, ModelID: expired.ID}).Error; err != nil {
		t.Fatal(err)
	}
	restrictedModels, err := access.ListModels(context.Background(), restrictedToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(restrictedModels) != 1 || restrictedModels[0].ID != expired.ID {
		t.Fatalf("restricted token models = %+v", restrictedModels)
	}
}

func TestRouterOrdersStrategies(t *testing.T) {
	router := &Router{random: func(int) int { return 0 }}
	candidates := []RouteCandidate{
		{Channel: Channel{ID: 1, LatencyEWMA: 80}, Mapping: ChannelModel{Priority: 1, Weight: 1}, Cost: 30},
		{Channel: Channel{ID: 2, LatencyEWMA: 0}, Mapping: ChannelModel{Priority: 2, Weight: 1}, Cost: 20},
		{Channel: Channel{ID: 3, LatencyEWMA: 40}, Mapping: ChannelModel{Priority: 2, Weight: 10}, Cost: 10},
	}
	router.orderCandidates(RoutingPriorityWeighted, candidates)
	if candidates[0].Mapping.Priority != 2 || candidates[2].Mapping.Priority != 1 {
		t.Fatalf("priority order = %+v", candidates)
	}
	router.orderCandidates(RoutingLowestCost, candidates)
	if candidates[0].Cost != 10 || candidates[2].Cost != 30 {
		t.Fatalf("cost order = %+v", candidates)
	}
	router.orderCandidates(RoutingLowestLatency, candidates)
	if candidates[0].Channel.ID != 2 || candidates[1].Channel.ID != 3 {
		t.Fatalf("latency order = %+v", candidates)
	}
}

func TestRouterSmoothWeightedPriorityBalancesAllocations(t *testing.T) {
	tests := []struct {
		name        string
		weights     []int
		allocations int
		want        []int
	}{
		{name: "equal weights", weights: []int{1, 1}, allocations: 20, want: []int{10, 10}},
		{name: "four to one", weights: []int{4, 1}, allocations: 50, want: []int{40, 10}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := &Router{random: func(int) int { return 0 }}
			base := make([]RouteCandidate, len(test.weights))
			for index, weight := range test.weights {
				id := uint64(index + 1)
				base[index] = RouteCandidate{
					Channel: Channel{ID: id},
					Mapping: ChannelModel{ID: id, ModelID: 1, Priority: 10, Weight: weight},
				}
			}
			counts := make([]int, len(test.weights))
			for range test.allocations {
				candidates := append([]RouteCandidate(nil), base...)
				router.orderCandidates(RoutingPriorityWeighted, candidates)
				counts[int(candidates[0].Mapping.ID)-1]++
			}
			for index, want := range test.want {
				if counts[index] != want {
					t.Fatalf("allocation counts = %v, want %v", counts, test.want)
				}
			}
		})
	}
}

func TestRouterRecentSuccessRateInfluencesEveryStrategy(t *testing.T) {
	base := []RouteCandidate{
		{
			Channel:            Channel{ID: 1, LatencyEWMA: 50},
			Mapping:            ChannelModel{ID: 1, ModelID: 1, Priority: 10, Weight: 1},
			Cost:               100,
			RecentSuccessRate:  1,
			RecentAttemptCount: 10,
		},
		{
			Channel:            Channel{ID: 2, LatencyEWMA: 50},
			Mapping:            ChannelModel{ID: 2, ModelID: 1, Priority: 10, Weight: 1},
			Cost:               100,
			RecentSuccessRate:  0.5,
			RecentAttemptCount: 10,
		},
	}

	priorityRouter := &Router{random: func(int) int { return 0 }}
	priorityCounts := [2]int{}
	for range 30 {
		candidates := append([]RouteCandidate(nil), base...)
		priorityRouter.orderCandidates(RoutingPriorityWeighted, candidates)
		priorityCounts[candidates[0].Channel.ID-1]++
	}
	if priorityCounts != [2]int{20, 10} {
		t.Fatalf("priority allocation counts = %v, want [20 10]", priorityCounts)
	}

	for _, strategy := range []string{RoutingLowestCost, RoutingLowestLatency} {
		t.Run(strategy, func(t *testing.T) {
			call := 0
			router := &Router{random: func(limit int) int {
				point := call * limit / 300
				call++
				return point
			}}
			counts := [2]int{}
			for range 300 {
				candidates := append([]RouteCandidate(nil), base...)
				router.orderCandidates(strategy, candidates)
				counts[candidates[0].Channel.ID-1]++
			}
			if counts != [2]int{200, 100} {
				t.Fatalf("allocation counts = %v, want [200 100]", counts)
			}
		})
	}
}

func TestRouterPlanLoadsRecentModelSuccessRates(t *testing.T) {
	store := newTestStore(t)
	token, model, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid", "http://two.invalid")
	now := time.Now()
	attempts := []RelayAttemptLog{
		{RequestID: "recent-success", ChannelID: channels[0].ID, ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel, Success: true, CreatedAt: now.Add(-20 * time.Minute)},
		{RequestID: "recent-failure", ChannelID: channels[0].ID, ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel, Success: false, CreatedAt: now.Add(-10 * time.Minute)},
		{RequestID: "expired-failure", ChannelID: channels[0].ID, ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel, Success: false, CreatedAt: now.Add(-31 * time.Minute)},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	router := NewRouter(store, NewClientAccessService(store), nil)
	plan, err := router.Plan(context.Background(), token, model.Name, 10, 10, "", "")
	if err != nil {
		t.Fatal(err)
	}
	byMapping := make(map[uint64]RouteCandidate, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		byMapping[candidate.Mapping.ID] = candidate
	}
	measured := byMapping[mappings[0].ID]
	if measured.RecentSuccessRate != 0.5 || measured.RecentSuccessCount != 1 || measured.RecentAttemptCount != 2 {
		t.Fatalf("measured route candidate = %+v", measured)
	}
	empty := byMapping[mappings[1].ID]
	if empty.RecentSuccessRate != 1 || empty.RecentSuccessCount != 0 || empty.RecentAttemptCount != 0 {
		t.Fatalf("empty route candidate = %+v", empty)
	}
}

func TestRouterExpectationProbabilityIncludesEveryRoutingSignal(t *testing.T) {
	router := &Router{random: func(int) int { return 0 }}
	candidates := []RouteCandidate{
		{Channel: Channel{ID: 1}, Mapping: ChannelModel{ID: 1, Priority: 10, Weight: 100}, Cost: 50, RecentSuccessRate: 1, RecentAttemptCount: 10, RecentLatencyMS: 40, RecentCacheHitRate: 0.8, RecentCacheSamples: 10, RecentCacheRate: 0.6, RecentCacheTokens: 1000, RecentRouteCount: 20, RecentRouteShare: 0.2, RouteSampleSize: 100, MetricsLoaded: true},
		{Channel: Channel{ID: 2}, Mapping: ChannelModel{ID: 2, Priority: 10, Weight: 100}, Cost: 100, RecentSuccessRate: 0.8, RecentAttemptCount: 10, RecentLatencyMS: 80, RecentCacheHitRate: 0.2, RecentCacheSamples: 10, RecentCacheRate: 0.1, RecentCacheTokens: 1000, RecentRouteCount: 80, RecentRouteShare: 0.8, RouteSampleSize: 100, MetricsLoaded: true},
	}
	decision := router.orderCandidates(RoutingPriorityWeighted, candidates)
	if decision == nil || decision.Mode != "probability" || len(decision.Candidates) != 2 {
		t.Fatalf("decision = %+v", decision)
	}
	first, second := decision.Candidates[0], decision.Candidates[1]
	if !first.Selected || first.Probability <= second.Probability || first.Expectation <= second.Expectation {
		t.Fatalf("candidate expectations = %+v", decision.Candidates)
	}
	if math.Abs(first.Probability+second.Probability-1) > 0.000001 {
		t.Fatalf("probability sum = %f", first.Probability+second.Probability)
	}
	if second.RecentRouteCount != 80 || second.RecentRouteShare != 0.8 || second.RouteSampleSize != 100 || second.CacheHitRate != 0.2 || second.CacheSampleCount != 10 || second.CacheRate != 0.1 || second.CacheTokenCount != 1000 || second.SuccessRate != 0.8 {
		t.Fatalf("decision inputs = %+v", second)
	}
}

func TestRecentRoutingMetricsUseCandidateScopedDynamicSample(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	attempts := []RelayAttemptLog{
		{RequestID: "target-a-cache-hit", ChannelID: 1, ChannelModelID: 11, InputTokens: 100, CachedTokens: 50, OutputTokens: 20, LatencyMS: 20, FirstTokenMS: 50, DurationMS: 250, Success: true, CreatedAt: now.Add(-time.Minute)},
		{RequestID: "target-b-cache-miss", ChannelID: 2, ChannelModelID: 22, InputTokens: 200, CachedTokens: 0, OutputTokens: 10, LatencyMS: 40, FirstTokenMS: 100, DurationMS: 300, Success: true, CreatedAt: now.Add(-2 * time.Minute)},
		{RequestID: "other-model", ChannelID: 3, ChannelModelID: 33, InputTokens: 100, CachedTokens: 100, OutputTokens: 5, LatencyMS: 10, FirstTokenMS: 20, DurationMS: 120, Success: true, CreatedAt: now.Add(-3 * time.Minute)},
		{RequestID: "target-a-expired-cache-hit", ChannelID: 1, ChannelModelID: 11, InputTokens: 300, CachedTokens: 300, OutputTokens: 30, LatencyMS: 90, FirstTokenMS: 200, DurationMS: 500, Success: true, CreatedAt: now.Add(-31 * time.Minute)},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}

	metrics, err := loadRecentRoutingMetrics(context.Background(), store.db, []uint64{11, 22, 44}, now)
	if err != nil {
		t.Fatal(err)
	}
	first := metrics[11]
	if first.RouteCount != 2 || first.RouteSampleSize != 3 || first.RouteShare != 2.0/3.0 || first.CacheHitRate != 1 || first.CacheSampleCount != 1 || first.CacheRate != 0.5 || first.CacheTokenCount != 100 || first.LatencyMS != 20 || first.FirstTokenMS != 50 || first.TokensPerSecond != 100 {
		t.Fatalf("first routing metric = %+v", first)
	}
	second := metrics[22]
	if second.RouteCount != 1 || second.RouteSampleSize != 3 || second.RouteShare != 1.0/3.0 || second.CacheHitRate != 0 || second.CacheSampleCount != 1 || second.CacheRate != 0 || second.CacheTokenCount != 200 || second.LatencyMS != 40 || second.FirstTokenMS != 100 || second.TokensPerSecond != 50 {
		t.Fatalf("second routing metric = %+v", second)
	}
	empty := metrics[44]
	if empty.RouteCount != 0 || empty.RouteSampleSize != 3 || empty.RouteShare != 0 {
		t.Fatalf("empty routing metric = %+v", empty)
	}
}

func TestRouterConfiguredPriceWeightOutweighsCacheAndRouteShare(t *testing.T) {
	router := &Router{random: func(int) int { return 999_999 }}
	candidates := []RouteCandidate{
		{Channel: Channel{ID: 1}, Mapping: ChannelModel{ID: 1, Priority: 10, Weight: 100}, Cost: 10, RecentSuccessRate: 1, RecentAttemptCount: 10, RecentCacheHitRate: 0.1, RecentCacheSamples: 10, RecentCacheRate: 0.1, RecentCacheTokens: 1000, RecentRouteShare: 0.9, RouteSampleSize: 100, MetricsLoaded: true},
		{Channel: Channel{ID: 2}, Mapping: ChannelModel{ID: 2, Priority: 10, Weight: 100}, Cost: 1_000, RecentSuccessRate: 1, RecentAttemptCount: 10, RecentCacheHitRate: 0.9, RecentCacheSamples: 10, RecentCacheRate: 0.9, RecentCacheTokens: 1000, RecentRouteShare: 0.1, RouteSampleSize: 100, MetricsLoaded: true},
	}

	decision := router.orderCandidates(RoutingPriorityWeighted, candidates)
	probabilityByMapping := make(map[uint64]float64, len(decision.Candidates))
	for _, candidate := range decision.Candidates {
		probabilityByMapping[candidate.ChannelModelID] = candidate.Probability
	}
	if probabilityByMapping[1] <= probabilityByMapping[2] {
		t.Fatalf("configured price weight was not applied: candidates = %+v, decision = %+v", candidates, decision.Candidates)
	}
}

func TestRouterBalanceUsesRelativeTargetAcrossManyCandidates(t *testing.T) {
	configProvider := func(balance int) func() *config.ApplicationConfig {
		return func() *config.ApplicationConfig {
			return &config.ApplicationConfig{GatewayConfig: config.GatewayConfig{
				RoutingPriceWeightPercent:      35,
				RoutingEfficiencyWeightPercent: 30,
				RoutingQualityWeightPercent:    35 - balance,
				RoutingBalanceWeightPercent:    balance,
			}}
		}
	}
	base := make([]RouteCandidate, 20)
	for index := range base {
		count := int64(18)
		if index == 0 {
			count = 58
		} else if index == 1 {
			count = 0
		}
		id := uint64(index + 1)
		base[index] = RouteCandidate{
			Channel: Channel{ID: id}, Mapping: ChannelModel{ID: id, Priority: 10, Weight: 100}, Cost: 100,
			RecentSuccessRate: 1, RecentAttemptCount: 10, RecentLatencyMS: 20, RecentFirstTokenMS: 50, RecentTokensPerSecond: 50,
			RecentRouteCount: count, RouteSampleSize: 400, MetricsLoaded: true,
		}
	}
	balancedRouter := &Router{random: func(int) int { return 0 }, configProvider: configProvider(20)}
	balanced := balancedRouter.orderCandidates(RoutingPriorityWeighted, append([]RouteCandidate(nil), base...))
	byID := make(map[uint64]RouteDecisionCandidate, len(balanced.Candidates))
	for _, candidate := range balanced.Candidates {
		byID[candidate.ChannelModelID] = candidate
	}
	if byID[1].RecentRouteShare <= byID[1].TargetRouteShare || byID[1].BalanceMultiplier >= 1 || byID[1].Probability >= byID[2].Probability || byID[2].BalanceMultiplier <= 1 {
		t.Fatalf("relative balance did not suppress the overloaded candidate: overloaded=%+v idle=%+v", byID[1], byID[2])
	}

	unbalancedRouter := &Router{random: func(int) int { return 0 }, configProvider: configProvider(0)}
	unbalanced := unbalancedRouter.orderCandidates(RoutingPriorityWeighted, append([]RouteCandidate(nil), base...))
	for _, candidate := range unbalanced.Candidates {
		if math.Abs(candidate.Probability-0.05) > 0.000001 {
			t.Fatalf("zero balance weight changed base probability: %+v", candidate)
		}
	}
}

func TestRoutingHistorySampleSizeScalesWithCandidateCount(t *testing.T) {
	tests := map[int]int{0: 100, 2: 100, 20: 400, 80: 1000}
	for candidates, want := range tests {
		if got := routingHistorySampleSize(candidates); got != want {
			t.Fatalf("routingHistorySampleSize(%d) = %d, want %d", candidates, got, want)
		}
	}
}

func TestRouterRoutingWeightsUpdateAtRuntime(t *testing.T) {
	currentConfig := &config.ApplicationConfig{GatewayConfig: config.GatewayConfig{
		RoutingPriceWeightPercent:      80,
		RoutingEfficiencyWeightPercent: 10,
		RoutingQualityWeightPercent:    5,
		RoutingBalanceWeightPercent:    5,
	}}
	router := &Router{
		random: func(int) int { return 0 },
		configProvider: func() *config.ApplicationConfig {
			return currentConfig
		},
	}
	base := []RouteCandidate{
		{Channel: Channel{ID: 1}, Mapping: ChannelModel{ID: 1, Priority: 10, Weight: 100}, Cost: 10, RecentSuccessRate: 1, RecentAttemptCount: 10, RecentLatencyMS: 20, RecentFirstTokenMS: 100, RecentTokensPerSecond: 10, MetricsLoaded: true},
		{Channel: Channel{ID: 2}, Mapping: ChannelModel{ID: 2, Priority: 10, Weight: 100}, Cost: 100, RecentSuccessRate: 1, RecentAttemptCount: 10, RecentLatencyMS: 10, RecentFirstTokenMS: 10, RecentTokensPerSecond: 100, MetricsLoaded: true},
	}

	priceCandidates := append([]RouteCandidate(nil), base...)
	priceDecision := router.orderCandidates(RoutingPriorityWeighted, priceCandidates)
	if priceDecision.Candidates[0].Probability <= priceDecision.Candidates[1].Probability {
		t.Fatalf("price-weighted decision = %+v", priceDecision.Candidates)
	}

	currentConfig = &config.ApplicationConfig{GatewayConfig: config.GatewayConfig{
		RoutingPriceWeightPercent:      10,
		RoutingEfficiencyWeightPercent: 80,
		RoutingQualityWeightPercent:    5,
		RoutingBalanceWeightPercent:    5,
	}}
	efficiencyCandidates := append([]RouteCandidate(nil), base...)
	efficiencyDecision := router.orderCandidates(RoutingPriorityWeighted, efficiencyCandidates)
	if efficiencyDecision.Candidates[1].Probability <= efficiencyDecision.Candidates[0].Probability {
		t.Fatalf("efficiency-weighted decision = %+v", efficiencyDecision.Candidates)
	}
	if efficiencyDecision.Weights.Price != 0.1 || efficiencyDecision.Weights.Efficiency != 0.8 || efficiencyDecision.Weights.Quality != 0.05 || efficiencyDecision.Weights.Balance != 0.05 {
		t.Fatalf("updated decision weights = %+v", efficiencyDecision.Weights)
	}
}

func TestRouterReservesExplorationProbabilityForColdStartCandidates(t *testing.T) {
	router := &Router{random: func(int) int { return 999_999 }}
	candidates := []RouteCandidate{
		{Channel: Channel{ID: 1}, Mapping: ChannelModel{ID: 1, Priority: 10, Weight: 100}, Cost: 10, RecentSuccessRate: 1, RecentAttemptCount: 100, RecentLatencyMS: 10, RecentCacheHitRate: 1, RecentCacheSamples: 100, RecentCacheRate: 1, RecentCacheTokens: 10_000, RouteSampleSize: 100, MetricsLoaded: true},
		{Channel: Channel{ID: 2}, Mapping: ChannelModel{ID: 2, Priority: 10, Weight: 1}, Cost: 10, RouteSampleSize: 100, MetricsLoaded: true},
		{Channel: Channel{ID: 3}, Mapping: ChannelModel{ID: 3, Priority: 10, Weight: 1}, Cost: 10, RouteSampleSize: 100, MetricsLoaded: true},
	}

	decision := router.orderCandidates(RoutingPriorityWeighted, candidates)
	probabilityByMapping := make(map[uint64]float64, len(decision.Candidates))
	for _, candidate := range decision.Candidates {
		probabilityByMapping[candidate.ChannelModelID] = candidate.Probability
	}
	if probabilityByMapping[2] < 0.10 || probabilityByMapping[3] < 0.10 {
		t.Fatalf("cold-start probabilities = %+v", probabilityByMapping)
	}
	if math.Abs(probabilityByMapping[1]+probabilityByMapping[2]+probabilityByMapping[3]-1) > 0.000001 {
		t.Fatalf("probability sum = %+v", probabilityByMapping)
	}
	if candidates[0].Mapping.ID != 3 {
		t.Fatalf("cold-start exploration probability was not used for selection: %+v", candidates)
	}
}

func TestRouterDoesNotExploreLowerPriorityColdStartCandidates(t *testing.T) {
	router := &Router{random: func(int) int { return 0 }}
	candidates := []RouteCandidate{
		{Channel: Channel{ID: 1}, Mapping: ChannelModel{ID: 1, Priority: 10, Weight: 100}, Cost: 10, RecentSuccessRate: 1, RecentAttemptCount: 10, MetricsLoaded: true},
		{Channel: Channel{ID: 2}, Mapping: ChannelModel{ID: 2, Priority: 9, Weight: 100}, Cost: 10, MetricsLoaded: true},
	}

	decision := router.orderCandidates(RoutingPriorityWeighted, candidates)
	for _, candidate := range decision.Candidates {
		if candidate.ChannelModelID == 2 && candidate.Probability != 0 {
			t.Fatalf("lower-priority cold-start candidate received probability: %+v", decision.Candidates)
		}
	}
}

func TestRouterSessionAffinityDecisionRemainsDeterministic(t *testing.T) {
	candidates := []RouteCandidate{
		{Channel: Channel{ID: 2, Name: "sticky"}, Mapping: ChannelModel{ID: 2, Weight: 100}},
		{Channel: Channel{ID: 1, Name: "other"}, Mapping: ChannelModel{ID: 1, Weight: 100}},
	}
	decision := deterministicRouteDecision(RoutingPriorityWeighted, "session_affinity", candidates)
	if decision.Mode != "session_affinity" || !decision.Candidates[0].Selected || decision.Candidates[0].Probability != 1 || decision.Candidates[1].Probability != 0 {
		t.Fatalf("affinity decision = %+v", decision)
	}
}

func TestRouterSmoothWeightedPriorityBalancesConcurrentAllocations(t *testing.T) {
	router := &Router{random: func(int) int { return 0 }}
	base := []RouteCandidate{
		{Channel: Channel{ID: 1}, Mapping: ChannelModel{ID: 1, ModelID: 1, Priority: 10, Weight: 1}},
		{Channel: Channel{ID: 2}, Mapping: ChannelModel{ID: 2, ModelID: 1, Priority: 10, Weight: 1}},
	}
	const allocations = 100
	results := make(chan uint64, allocations)
	for range allocations {
		go func() {
			candidates := append([]RouteCandidate(nil), base...)
			router.orderCandidates(RoutingPriorityWeighted, candidates)
			results <- candidates[0].Mapping.ID
		}()
	}
	counts := [2]int{}
	for range allocations {
		counts[<-results-1]++
	}
	if counts != [2]int{50, 50} {
		t.Fatalf("concurrent allocation counts = %v, want [50 50]", counts)
	}
}

func TestRouterSessionAffinityDoesNotConsumeWeightedAllocation(t *testing.T) {
	store := newTestStore(t)
	token, model, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid", "http://two.invalid")
	if err := store.db.Model(&ChannelModel{}).Where("model_id = ?", model.ID).Update("priority", 100).Error; err != nil {
		t.Fatal(err)
	}
	router := NewRouter(store, NewClientAccessService(store), nil)
	router.random = func(int) int { return 0 }

	firstPlan, err := router.Plan(context.Background(), token, model.Name, 10, 10, "", "new-session-a")
	if err != nil {
		t.Fatal(err)
	}
	firstMappingID := firstPlan.Candidates[0].Mapping.ID
	router.RecordSessionAffinity(context.Background(), token.ID, model.ID, "new-session-a", firstMappingID)
	for range 5 {
		stickyPlan, stickyErr := router.Plan(context.Background(), token, model.Name, 10, 10, "", "new-session-a")
		if stickyErr != nil {
			t.Fatal(stickyErr)
		}
		if stickyPlan.Candidates[0].Mapping.ID != firstMappingID {
			t.Fatalf("sticky mapping = %d, want %d", stickyPlan.Candidates[0].Mapping.ID, firstMappingID)
		}
	}

	secondPlan, err := router.Plan(context.Background(), token, model.Name, 10, 10, "", "new-session-b")
	if err != nil {
		t.Fatal(err)
	}
	if secondPlan.Candidates[0].Mapping.ID != firstMappingID {
		t.Fatalf("deterministic probability draw changed after affinity reads: got %d, want %d", secondPlan.Candidates[0].Mapping.ID, firstMappingID)
	}
}

func TestParseRelayPayloadExtractsCodexSessionKey(t *testing.T) {
	payload, err := ParseRelayPayload([]byte(`{
		"model":"public-model",
		"prompt_cache_key":" codex-session ",
		"client_metadata":{"session_id":"fallback-session","x-codex-turn-metadata":"{\"thread_source\":\"user\"}","private":"do-not-log"},
		"messages":[{"role":"user","content":"private prompt"}],
		"temperature":0.2,
		"reasoning":{"effort":"high","private":"do-not-log"},
		"tools":[{"type":"function","function":{"name":"lookup","description":"private tool description"}}],
		"unknown_field":"private unknown value"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.SessionKey != "codex-session" || payload.SessionSource != "prompt_cache_key" {
		t.Fatalf("session = %q from %q", payload.SessionKey, payload.SessionSource)
	}
	if payload.ThreadSource != codexThreadSourceUser {
		t.Fatalf("thread source = %q", payload.ThreadSource)
	}
	parameters := decodeRequestParameters(payload.RequestParametersJSON)
	if parameters["model"] != "public-model" || parameters["temperature"] != json.Number("0.2") || parameters["message_count"] != json.Number("1") || parameters["tool_count"] != json.Number("1") {
		t.Fatalf("request parameters = %#v", parameters)
	}
	for _, blocked := range []string{"prompt_cache_key", "client_metadata", "messages", "unknown_field"} {
		if _, exists := parameters[blocked]; exists {
			t.Fatalf("sensitive parameter %q was retained: %#v", blocked, parameters)
		}
	}
	if strings.Contains(payload.RequestParametersJSON, "private") {
		t.Fatalf("parameter snapshot contains request content: %s", payload.RequestParametersJSON)
	}
	fallback, err := ParseRelayPayload([]byte(`{"model":"public-model","client_metadata":{"session_id":"session-from-metadata","thread_id":"thread-from-metadata"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if fallback.SessionKey != "session-from-metadata" || fallback.SessionSource != "client_metadata.session_id" {
		t.Fatalf("fallback session = %q from %q", fallback.SessionKey, fallback.SessionSource)
	}
	unavailable, err := ParseRelayPayload([]byte(`{"model":"public-model","input":"private prompt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.SessionKey != "" || unavailable.SessionSource != "unavailable" || strings.Contains(unavailable.RequestParametersJSON, "private prompt") {
		t.Fatalf("unavailable session payload = %+v", unavailable)
	}
	ambient, err := ParseRelayPayload([]byte(`{"model":"public-model","prompt_cache_key":"ambient-session","client_metadata":{"x-codex-turn-metadata":"{\"thread_source\":\"ambient_suggestions\"}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ambient.ThreadSource != codexThreadSourceAmbient {
		t.Fatalf("ambient thread source = %q", ambient.ThreadSource)
	}
}

func TestSessionCandidateIndexesAreCreated(t *testing.T) {
	store := newTestStore(t)
	indexes := []struct {
		model any
		name  string
	}{
		{model: &RelaySessionState{}, name: "idx_relay_session_client_recent"},
		{model: &RelayChatSessionClaim{}, name: "idx_relay_claim_client_recent"},
	}
	for _, index := range indexes {
		if err := store.db.Migrator().DropIndex(index.model, index.name); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ensureSessionCandidateIndexes(); err != nil {
		t.Fatal(err)
	}
	for _, index := range indexes {
		if !store.db.Migrator().HasIndex(index.model, index.name) {
			t.Fatalf("missing session candidate index %s", index.name)
		}
	}
}

func TestCopilotRequiresFixedIntegrationHeader(t *testing.T) {
	store := newTestStore(t)
	body := []byte(`{"model":"public-model","messages":[{"role":"system","content":"You are the GitHub Copilot CLI.\n<copilot_tauri_workspace>\nproject_session_id: abda4fa3-2495-49d6-a71c-5d147c5148db\n</copilot_tauri_workspace>"},{"role":"user","content":"hello"}]}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := store.resolveAgentSession(context.Background(), agentSessionRequest{
		TokenID: 7, Endpoint: "chat", Headers: http.Header{"User-Agent": []string{"OpenAI/JS 5.20.1"}},
		Payload: payload, Body: body, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	applyAgentSessionResolution(payload, resolution)
	if payload.ClientKind == copilotClientKind || payload.SessionKey != "" {
		t.Fatalf("payload-only Copilot detection must be disabled: %+v", payload)
	}
	dynamicOnlyPayload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	dynamicOnly, err := store.resolveAgentSession(context.Background(), agentSessionRequest{
		TokenID: 7, Endpoint: "chat",
		Headers: http.Header{"X-Copilot-Session-Id": []string{"must-not-identify-client"}},
		Payload: dynamicOnlyPayload, Body: body, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	applyAgentSessionResolution(dynamicOnlyPayload, dynamicOnly)
	if dynamicOnlyPayload.ClientKind == copilotClientKind || dynamicOnlyPayload.SessionKey != "" {
		t.Fatalf("dynamic data header identified Copilot without the fixed marker: %+v", dynamicOnlyPayload)
	}

	wrongHeaders := http.Header{copilotIntegrationHeader: []string{"another-client"}}
	if (copilotAgentSessionResolver{}).Match(agentSessionRequest{Headers: wrongHeaders}) {
		t.Fatal("unexpected Copilot match for a different fixed header value")
	}
	if !requestHeaderBlocked(copilotIntegrationHeader) {
		t.Fatal("internal Copilot marker must not be forwarded upstream")
	}
}

func TestCopilotChatUsesProjectSessionIDBeforeHistory(t *testing.T) {
	store := newTestStore(t)
	headers := http.Header{copilotIntegrationHeader: []string{copilotIntegrationID}, "User-Agent": []string{"OpenAI/JS 5.20.1"}}
	now := time.Now().UTC()
	const sessionID = "abda4fa3-2495-49d6-a71c-5d147c5148db"
	firstBody := []byte(`{"model":"public-model","messages":[{"role":"system","content":"rules\n<copilot_tauri_workspace>\nproject_session_id: abda4fa3-2495-49d6-a71c-5d147c5148db\nproject_id: b232c12a-5240-46d0-a7d8-4639a05c022f\n</copilot_tauri_workspace>"},{"role":"user","content":"<current_datetime>2026-07-30 12:36:00</current_datetime>\n苏卡"}]}`)
	compressedBody := []byte(`{"model":"public-model","messages":[{"role":"system","content":"rules\n<copilot_tauri_workspace>\nproject_session_id: abda4fa3-2495-49d6-a71c-5d147c5148db\n</copilot_tauri_workspace>"},{"role":"user","content":"summary replaced the old history"}]}`)

	for index, body := range [][]byte{firstBody, compressedBody} {
		payload, err := ParseRelayPayload(body)
		if err != nil {
			t.Fatal(err)
		}
		resolution, err := store.resolveAgentSession(context.Background(), agentSessionRequest{
			TokenID: 7, Endpoint: "chat", Headers: headers, Payload: payload, Body: body, Now: now.Add(time.Duration(index) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		applyAgentSessionResolution(payload, resolution)
		if payload.SessionKey != sessionID || payload.SessionSource != copilotChatProjectSource || payload.ClientKind != copilotClientKind {
			t.Fatalf("Copilot Chat identity = %+v", payload)
		}
	}
	if title, renamed := agentRequestSessionTitle("chat", copilotClientKind, firstBody); title != "苏卡" || renamed {
		t.Fatalf("initial Chat title = %q, renamed=%v", title, renamed)
	}
	var claims int64
	if err := store.db.Model(&RelayChatSessionClaim{}).Count(&claims).Error; err != nil || claims != 0 {
		t.Fatalf("stable project session should bypass history claims: count=%d err=%v", claims, err)
	}
}

func TestCopilotChatHistoryFallbackRemainsStrict(t *testing.T) {
	store := newTestStore(t)
	headers := http.Header{copilotIntegrationHeader: []string{copilotIntegrationID}}
	now := time.Now().UTC()
	firstBody := []byte(`{"model":"public-model","messages":[{"role":"system","content":"rules"},{"role":"user","content":"first"}]}`)
	secondBody := []byte(`{"model":"public-model","messages":[{"role":"system","content":"rules"},{"role":"user","content":"first"},{"role":"assistant","content":"answer"},{"role":"user","content":"second"}]}`)

	resolve := func(body []byte, at time.Time) agentSessionIdentity {
		payload, err := ParseRelayPayload(body)
		if err != nil {
			t.Fatal(err)
		}
		resolution, err := store.resolveAgentSession(context.Background(), agentSessionRequest{
			TokenID: 7, Endpoint: "chat", Headers: headers, Payload: payload, Body: body, Now: at,
		})
		if err != nil {
			t.Fatal(err)
		}
		return resolution.Identity
	}
	first := resolve(firstBody, now)
	if !strings.HasPrefix(first.ID, "chat_") || first.Source != copilotChatHistorySource {
		t.Fatalf("history fallback identity = %+v", first)
	}
	compactSessionPayload(store.db, 7, first.ID, "chat-request-1", "first", "", firstBody, []byte(`{"choices":[{"message":{"role":"assistant","content":"answer"}}]}`), now)
	second := resolve(secondBody, now.Add(time.Minute))
	if second.ID != first.ID || second.Source != copilotChatHistorySource {
		t.Fatalf("continued history identity = %+v, want %s", second, first.ID)
	}
}

func TestCopilotResponsesUsesPromptCacheKeyAndOwnTitleParser(t *testing.T) {
	store := newTestStore(t)
	headers := http.Header{copilotIntegrationHeader: []string{copilotIntegrationID}}
	body := []byte(`{"model":"public-model","prompt_cache_key":"40c6ed6f-d1c0-4372-829a-e968a386416c","instructions":"<copilot_tauri_workspace>\nproject_session_id: da25c2e9-f42f-4255-b315-b3b189d1e0fa\n</copilot_tauri_workspace>","input":[{"role":"user","content":"<current_datetime>2026-07-30 12:36:00</current_datetime>\n认识 Copilot"}]}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := store.resolveAgentSession(context.Background(), agentSessionRequest{
		TokenID: 7, Endpoint: "responses", Headers: headers, Payload: payload, Body: body, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	applyAgentSessionResolution(payload, resolution)
	if payload.SessionKey != "40c6ed6f-d1c0-4372-829a-e968a386416c" || payload.SessionSource != copilotResponsesPromptCacheSource || payload.ClientKind != copilotClientKind {
		t.Fatalf("Copilot Responses identity = %+v", payload)
	}
	if title, renamed := agentRequestSessionTitle("responses", copilotClientKind, body); title != "认识 Copilot" || renamed {
		t.Fatalf("initial Responses title = %q, renamed=%v", title, renamed)
	}
	renameBody := []byte(`{"model":"public-model","prompt_cache_key":"40c6ed6f-d1c0-4372-829a-e968a386416c","input":[{"type":"function_call_output","call_id":"rename-1","output":"Renamed session to \"深入了解 Copilot\"."}]}`)
	if title, renamed := agentRequestSessionTitle("responses", copilotClientKind, renameBody); title != "深入了解 Copilot" || !renamed {
		t.Fatalf("renamed Responses title = %q, renamed=%v", title, renamed)
	}
}

func TestRelayPersistsCopilotResponsesIdentityAndRenamedTitle(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_copilot","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer upstream.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	relay := newTestRelay(store)
	headers := http.Header{copilotIntegrationHeader: []string{copilotIntegrationID}}
	const sessionID = "40c6ed6f-d1c0-4372-829a-e968a386416c"
	bodies := [][]byte{
		[]byte(`{"model":"public-model","prompt_cache_key":"40c6ed6f-d1c0-4372-829a-e968a386416c","input":[{"role":"user","content":"<current_datetime>2026-07-30 12:36:00</current_datetime>\n认识 Copilot"}]}`),
		[]byte(`{"model":"public-model","prompt_cache_key":"40c6ed6f-d1c0-4372-829a-e968a386416c","input":[{"type":"function_call_output","call_id":"rename-1","output":"Renamed session to \"深入了解 Copilot\"."}]}`),
	}
	for _, body := range bodies {
		payload, err := ParseRelayPayload(body)
		if err != nil {
			t.Fatal(err)
		}
		if publicErr := relay.Relay(context.Background(), httptest.NewRecorder(), headers, "", "responses", token, payload, body); publicErr != nil {
			t.Fatal(publicErr)
		}
	}

	var logs []RelayRequestLog
	if err := store.db.Order("created_at ASC, id ASC").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].CodexSessionID != sessionID || logs[1].CodexSessionID != sessionID {
		t.Fatalf("Copilot Responses logs = %+v", logs)
	}
	if logs[0].CodexSessionSource != copilotResponsesPromptCacheSource || logs[0].SessionName != "认识 Copilot" || logs[1].SessionName != "深入了解 Copilot" {
		t.Fatalf("Copilot Responses log metadata = %+v", logs)
	}
	var state RelaySessionState
	if err := store.db.First(&state, "token_id = ? AND session_id = ?", token.ID, sessionID).Error; err != nil {
		t.Fatal(err)
	}
	if state.Title != "深入了解 Copilot" || state.SessionSource != copilotResponsesPromptCacheSource || state.ClientKind != copilotClientKind {
		t.Fatalf("Copilot Responses state = %+v", state)
	}
}

func TestCodexTitleDetectionAcceptsSchemaVariantsAndFencedJSON(t *testing.T) {
	titleBody := []byte(`{
		"model":"title-model",
		"text":{"format":{"type":"json_schema","name":"conversation_title_v2","schema":{"type":"object","properties":{"title":{"type":"string"},"summary":{"type":"string"},"language":{"type":"string"}}}}},
		"input":[{"role":"user","content":"Create a concise title for this request: improve routing dashboard"}]
	}`)
	prompt, isTitle := codexTitleRequestPrompt(titleBody)
	if !isTitle || !strings.Contains(prompt, "improve routing dashboard") {
		t.Fatalf("title request = %t, prompt = %q", isTitle, prompt)
	}
	if name := requestSessionName(titleBody); name != "" {
		t.Fatalf("title helper request name = %q", name)
	}
	mainBody := []byte(`{"model":"main-model","input":[{"role":"user","content":"improve routing dashboard"}]}`)
	if !codexTitleMatchesMain(titleBody, mainBody, hashCodexPrompt(prompt), hashCodexPrompt(codexRequestPrompt(mainBody))) {
		t.Fatal("schema-variant title request did not match its main request")
	}
	response := []byte("data: {\"type\":\"response.output_text.done\",\"text\":\"```json\\n{\\\"title\\\":\\\"Routing dashboard improvements\\\"}\\n```\"}\n\n")
	if title := codexGeneratedTitle(response); title != "Routing dashboard improvements" {
		t.Fatalf("generated title = %q", title)
	}
}

func TestRelayLogMetadata(t *testing.T) {
	if path := relayAPIPath("https://provider.example/api/v1", "responses"); path != "/v1/responses" {
		t.Fatalf("responses API path = %q", path)
	}
	if path := relayAPIPath("", "chat"); path != "/v1/chat/completions" {
		t.Fatalf("chat API path = %q", path)
	}
	if effort := requestReasoningEffort(map[string]any{"reasoning": map[string]any{"effort": "high"}}); effort != "high" {
		t.Fatalf("nested reasoning effort = %q", effort)
	}
	if effort := requestReasoningEffort(map[string]any{"reasoning_effort": "medium"}); effort != "medium" {
		t.Fatalf("top-level reasoning effort = %q", effort)
	}
}

func TestCodexAuxiliaryRequestsMergeIntoMainSession(t *testing.T) {
	store := newTestStore(t)
	token := ClientToken{Name: "codex", KeyHash: hashSecret("sk-codex"), KeyPrefix: "sk-codex", Enabled: true, AllowAllModels: true, RPM: 60, MaxConcurrency: 10}
	if err := store.db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	relay := newTestRelay(store)
	requestStart := time.Now().Add(-12 * time.Second)
	titleBody := []byte(`{
		"model":"title-model","prompt_cache_key":"title-session","stream":true,
		"text":{"format":{"type":"json_schema","name":"codex_output_schema","schema":{"type":"object","properties":{"title":{"type":"string"},"description":{"type":"string"}}}}},
		"input":[{"role":"user","type":"message","content":[{"type":"input_text","text":"Generate a title. User prompt: investigate session grouping"}]}]
	}`)
	titlePayload, err := ParseRelayPayload(titleBody)
	if err != nil {
		t.Fatal(err)
	}
	titleResponse := []byte("data: {\"type\":\"response.output_text.done\",\"text\":\"{\\\"title\\\":\\\"Investigate session grouping\\\",\\\"description\\\":\\\"Merge Codex helper calls\\\"}\"}\n\n")
	relay.recordRequest(context.Background(), &relayExecution{
		requestID: "title-request", token: &token, endpoint: "responses", payload: titlePayload,
		rawBody: titleBody, responseBody: titleResponse, startedAt: requestStart,
	}, http.StatusOK, "")

	mainBody := []byte(`{"model":"main-model","prompt_cache_key":"main-session","client_metadata":{"x-codex-turn-metadata":"{\"thread_source\":\"user\"}"},"stream":true,"input":[{"role":"user","type":"message","content":[{"type":"input_text","text":"# Files mentioned by the user:\n\n## screenshot.png: /tmp/screenshot.png\n\n## My request for Codex:\ninvestigate session grouping"},{"type":"input_text","text":"<image name=[Image #1] path=\"/tmp/screenshot.png\">"},{"type":"input_image","image_url":"data:image/png;base64,AA=="},{"type":"input_text","text":"</image>"}]}]}`)
	mainPayload, err := ParseRelayPayload(mainBody)
	if err != nil {
		t.Fatal(err)
	}
	relay.recordRequest(context.Background(), &relayExecution{
		requestID: "main-request", token: &token, endpoint: "responses", payload: mainPayload,
		rawBody: mainBody, startedAt: requestStart.Add(time.Second),
	}, http.StatusOK, "")

	guardianBody := []byte(`{"model":"guard-model","prompt_cache_key":"guardian:main-session","input":"authorize action"}`)
	guardianPayload, err := ParseRelayPayload(guardianBody)
	if err != nil {
		t.Fatal(err)
	}
	relay.recordRequest(context.Background(), &relayExecution{
		requestID: "guardian-request", token: &token, endpoint: "responses", payload: guardianPayload,
		rawBody: guardianBody, startedAt: time.Now(),
	}, http.StatusOK, "")

	var logs []RelayRequestLog
	if err := store.db.Order("created_at ASC, id ASC").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 {
		t.Fatalf("logs = %+v", logs)
	}
	for _, log := range logs {
		if log.CodexSessionID != "main-session" {
			t.Fatalf("log session identity = %+v", log)
		}
	}
	if logs[0].CodexSessionSource != codexTitleSessionSource || logs[0].SessionName != "Investigate session grouping" {
		t.Fatalf("title log = %+v", logs[0])
	}
	if logs[2].CodexSessionSource != codexGuardianSessionSource {
		t.Fatalf("guardian log = %+v", logs[2])
	}

	var mainState RelaySessionState
	if err := store.db.Where("token_id = ? AND session_id = ?", token.ID, "main-session").First(&mainState).Error; err != nil {
		t.Fatal(err)
	}
	if mainState.Title != "Investigate session grouping" || mainState.ThreadSource != codexThreadSourceUser {
		t.Fatalf("main state = %+v", mainState)
	}
	var oldStateCount int64
	if err := store.db.Model(&RelaySessionState{}).Where("token_id = ? AND session_id = ?", token.ID, "title-session").Count(&oldStateCount).Error; err != nil || oldStateCount != 0 {
		t.Fatalf("title state count = %d, %v", oldStateCount, err)
	}
	var guardianStateCount int64
	if err := store.db.Model(&RelaySessionState{}).Where("token_id = ? AND session_id = ?", token.ID, "guardian:main-session").Count(&guardianStateCount).Error; err != nil || guardianStateCount != 0 {
		t.Fatalf("guardian state count = %d, %v", guardianStateCount, err)
	}

	page, err := NewManagementService(store).SessionLogs(context.Background(), SessionLogQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].SessionID != "main-session" || page.Items[0].SessionName != "Investigate session grouping" || page.Items[0].RequestCount != 3 || page.Items[0].LatestModel != "main-model" {
		t.Fatalf("merged session page = %+v", page)
	}
}

func TestCodexTitleRequestDoesNotMergeWithDifferentPrompt(t *testing.T) {
	store := newTestStore(t)
	token := ClientToken{Name: "codex", KeyHash: hashSecret("sk-codex-negative"), KeyPrefix: "sk-codex", Enabled: true, AllowAllModels: true, RPM: 60, MaxConcurrency: 10}
	if err := store.db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	relay := newTestRelay(store)
	titleBody := []byte(`{
		"model":"title-model","prompt_cache_key":"title-session","stream":true,
		"text":{"format":{"type":"json_schema","name":"codex_output_schema","schema":{"properties":{"title":{},"description":{}}}}},
		"input":[{"role":"user","content":"Generate a title. User prompt: first user request"}]
	}`)
	titlePayload, err := ParseRelayPayload(titleBody)
	if err != nil {
		t.Fatal(err)
	}
	relay.recordRequest(context.Background(), &relayExecution{
		requestID: "different-title-request", token: &token, endpoint: "responses", payload: titlePayload,
		rawBody: titleBody, responseBody: []byte(`{"output_text":"{\"title\":\"First title\"}"}`), startedAt: time.Now().Add(-time.Second),
	}, http.StatusOK, "")

	mainBody := []byte(`{"model":"main-model","prompt_cache_key":"main-session","input":[{"role":"user","content":"second user request"}]}`)
	mainPayload, err := ParseRelayPayload(mainBody)
	if err != nil {
		t.Fatal(err)
	}
	relay.recordRequest(context.Background(), &relayExecution{
		requestID: "different-main-request", token: &token, endpoint: "responses", payload: mainPayload,
		rawBody: mainBody, startedAt: time.Now(),
	}, http.StatusOK, "")

	page, err := NewManagementService(store).SessionLogs(context.Background(), SessionLogQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("different prompts must remain separate: %+v", page)
	}
}

func TestCodexTitleRequestMergesAfterAttachedMainRequestCompletes(t *testing.T) {
	store := newTestStore(t)
	token := ClientToken{Name: "codex", KeyHash: hashSecret("sk-codex-attached"), KeyPrefix: "sk-codex", Enabled: true, AllowAllModels: true, RPM: 60, MaxConcurrency: 10}
	if err := store.db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	relay := newTestRelay(store)
	startedAt := time.Now().Add(-2 * time.Second)
	mainBody := []byte(`{
		"model":"main-model","prompt_cache_key":"main-session","input":[{"role":"user","content":[
			{"type":"input_text","text":"# Files mentioned by the user:\n\n## screenshot.png: /tmp/screenshot.png\n\n## My request for Codex:\nfix duplicate sessions"},
			{"type":"input_text","text":"<image name=[Image #1] path=\"/tmp/screenshot.png\">"},
			{"type":"input_image","image_url":"data:image/png;base64,AA=="},
			{"type":"input_text","text":"</image>"}
		]}]}`)
	mainPayload, err := ParseRelayPayload(mainBody)
	if err != nil {
		t.Fatal(err)
	}
	relay.recordRequest(context.Background(), &relayExecution{
		requestID: "attached-main-request", token: &token, endpoint: "responses", payload: mainPayload,
		rawBody: mainBody, startedAt: startedAt,
	}, http.StatusOK, "")

	titleBody := []byte(`{
		"model":"title-model","prompt_cache_key":"title-session","stream":true,
		"text":{"format":{"type":"json_schema","name":"codex_output_schema","schema":{"properties":{"title":{},"description":{}}}}},
		"input":[{"role":"user","content":"Generate a title. User prompt:\nfix duplicate sessions"}]
	}`)
	titlePayload, err := ParseRelayPayload(titleBody)
	if err != nil {
		t.Fatal(err)
	}
	relay.recordRequest(context.Background(), &relayExecution{
		requestID: "following-title-request", token: &token, endpoint: "responses", payload: titlePayload,
		rawBody: titleBody, responseBody: []byte(`{"output_text":"{\"title\":\"Fix duplicate sessions\"}"}`), startedAt: startedAt,
	}, http.StatusOK, "")

	page, err := NewManagementService(store).SessionLogs(context.Background(), SessionLogQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].SessionID != "main-session" || page.Items[0].SessionName != "Fix duplicate sessions" || page.Items[0].RequestCount != 2 {
		t.Fatalf("merged attached session page = %+v", page)
	}
	var titleLog RelayRequestLog
	if err := store.db.First(&titleLog, "id = ?", "following-title-request").Error; err != nil {
		t.Fatal(err)
	}
	if titleLog.CodexSessionID != "main-session" || titleLog.CodexSessionSource != codexTitleSessionSource {
		t.Fatalf("following title log = %+v", titleLog)
	}
}

func TestBackfillCodexAuxiliarySessions(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	titleBody := []byte(`{"model":"title-model","prompt_cache_key":"old-title-session","text":{"format":{"type":"json_schema","name":"codex_output_schema","schema":{"properties":{"title":{},"description":{}}}}},"input":[{"role":"user","content":"User prompt: historical prompt"}]}`)
	titleResponse := []byte("data: {\"type\":\"response.output_text.done\",\"text\":\"{\\\"title\\\":\\\"Historical generated title\\\"}\"}\n\n")
	mainBody := []byte(`{"model":"main-model","prompt_cache_key":"old-main-session","input":[{"role":"user","content":"# Files mentioned by the user:\n\n## screenshot.png: /tmp/screenshot.png\n\n## My request for Codex:\nhistorical prompt <image name=[Image #1] path=\"/tmp/screenshot.png\"> </image>"}]}`)
	logs := []RelayRequestLog{
		{ID: "old-title-request", TokenID: 7, Endpoint: "responses", RequestedModel: "title-model", CodexSessionID: "old-title-session", CodexSessionSource: "prompt_cache_key", SessionName: "Generate a", RequestParametersJSON: `{"text_format":"json_schema"}`, RequestBody: compressStoredPayload(titleBody), ResponseBody: compressStoredPayload(titleResponse), StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now},
		{ID: "old-main-request", TokenID: 7, Endpoint: "responses", RequestedModel: "main-model", CodexSessionID: "old-main-session", CodexSessionSource: "prompt_cache_key", SessionName: "historical", RequestBody: compressStoredPayload(mainBody), StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, DurationMS: 1000, CreatedAt: now.Add(10 * time.Second)},
		{ID: "old-guardian-request", TokenID: 7, Endpoint: "responses", RequestedModel: "guard-model", CodexSessionID: "guardian:old-main-session", CodexSessionSource: "prompt_cache_key", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now.Add(20 * time.Second)},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	states := []RelaySessionState{
		{TokenID: 7, SessionID: "old-title-session", Title: "Generate a", CreatedAt: now, UpdatedAt: now},
		{TokenID: 7, SessionID: "old-main-session", Title: "historical", CreatedAt: now, UpdatedAt: now},
	}
	if err := store.db.Create(&states).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&GatewayMigration{Name: "codex_auxiliary_sessions_v1", AppliedAt: now.Add(-time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.backfillCodexAuxiliarySessions(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillCodexAuxiliarySessions(); err != nil {
		t.Fatal(err)
	}
	page, err := NewManagementService(store).SessionLogs(context.Background(), SessionLogQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].SessionID != "old-main-session" || page.Items[0].SessionName != "Historical generated title" || page.Items[0].RequestCount != 3 || page.Items[0].LatestModel != "main-model" {
		t.Fatalf("backfilled sessions = %+v", page)
	}
}

func TestBackfillCodexThreadSourcesRunsOnce(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	ambientBody := []byte(`{"model":"gpt-5.6-terra","prompt_cache_key":"ambient-session","client_metadata":{"x-codex-turn-metadata":"{\"thread_source\":\"ambient_suggestions\"}"},"input":"# Overview"}`)
	userBody := []byte(`{"model":"gpt-5.6-sol","prompt_cache_key":"user-session","client_metadata":{"x-codex-turn-metadata":"{\"thread_source\":\"user\"}"},"input":"normal request"}`)
	logs := []RelayRequestLog{
		{ID: "ambient-request", TokenID: 7, Endpoint: "responses", RequestedModel: "gpt-5.6-terra", CodexSessionID: "ambient-session", RequestBody: compressStoredPayload(ambientBody), StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now},
		{ID: "user-request", TokenID: 7, Endpoint: "responses", RequestedModel: "gpt-5.6-sol", CodexSessionID: "user-session", RequestBody: compressStoredPayload(userBody), StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelaySessionState{TokenID: 7, SessionID: "ambient-session", Title: "# Overview", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.backfillCodexThreadSources(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillCodexThreadSources(); err != nil {
		t.Fatal(err)
	}
	var ambientState RelaySessionState
	if err := store.db.First(&ambientState, "token_id = ? AND session_id = ?", 7, "ambient-session").Error; err != nil {
		t.Fatal(err)
	}
	if ambientState.ThreadSource != codexThreadSourceAmbient {
		t.Fatalf("ambient state = %+v", ambientState)
	}
	var userState RelaySessionState
	if err := store.db.First(&userState, "token_id = ? AND session_id = ?", 7, "user-session").Error; err != nil {
		t.Fatal(err)
	}
	if userState.ThreadSource != codexThreadSourceUser {
		t.Fatalf("user state = %+v", userState)
	}
	page, err := NewManagementService(store).SessionLogs(context.Background(), SessionLogQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("session page = %+v", page)
	}
	for _, item := range page.Items {
		if item.SessionID == "ambient-session" && item.ThreadSource != codexThreadSourceAmbient {
			t.Fatalf("ambient summary = %+v", item)
		}
		if item.SessionID == "user-session" && item.ThreadSource != codexThreadSourceUser {
			t.Fatalf("user summary = %+v", item)
		}
	}
}

func TestSessionLogsAggregateExistingFiveDayDetails(t *testing.T) {
	store := newTestStore(t)
	token, model, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid", "http://two.invalid")
	now := time.Now()
	logs := []RelayRequestLog{
		{ID: "session-request-1", TokenID: token.ID, TokenName: token.Name, TokenKeyPrefix: token.KeyPrefix, Endpoint: "responses", RequestedModel: model.Name, CodexSessionID: "codex-session", CodexSessionSource: "prompt_cache_key", RequestParametersJSON: `{"temperature":0.2}`, StatusCode: http.StatusOK, InputTokens: 100, NormalInputTokens: 60, OutputTokens: 20, CachedTokens: 30, CacheWriteTokens: 10, SentTokens: 100, EstimatedCost: 70, UpstreamCost: 60, AttemptCount: 1, FirstTokenMS: 50, LatencyMS: 20, DurationMS: 100, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "session-request-2", TokenID: token.ID, TokenName: token.Name, TokenKeyPrefix: token.KeyPrefix, Endpoint: "responses", RequestedModel: model.Name, CodexSessionID: "codex-session", CodexSessionSource: "prompt_cache_key", RequestParametersJSON: `{"reasoning":{"effort":"high"}}`, StatusCode: http.StatusBadGateway, InputTokens: 10, NormalInputTokens: 10, SentTokens: 20, EstimatedCost: 0, UpstreamCost: 0, AttemptCount: 2, LatencyMS: 40, DurationMS: 300, CreatedAt: now.Add(-time.Hour)},
		{ID: "unknown-request", TokenID: token.ID, TokenName: token.Name, TokenKeyPrefix: token.KeyPrefix, Endpoint: "chat", RequestedModel: model.Name, CodexSessionSource: "unavailable", RequestParametersJSON: `{}`, StatusCode: http.StatusOK, InputTokens: 50, OutputTokens: 10, CachedTokens: 5, EstimatedCost: 20, UpstreamCost: 15, AttemptCount: 0, FirstTokenMS: 30, LatencyMS: 10, DurationMS: 80, CreatedAt: now.Add(-30 * time.Minute)},
		{ID: "expired-request", TokenID: token.ID, Endpoint: "responses", RequestedModel: model.Name, CodexSessionID: "codex-session", StatusCode: http.StatusOK, InputTokens: 1000, CreatedAt: now.Add(-(DetailedLogRetentionDays*24*time.Hour + time.Hour))},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	attempts := []RelayAttemptLog{
		{RequestID: logs[0].ID, ChannelID: channels[0].ID, ChannelName: channels[0].Name, ChannelBaseURL: channels[0].BaseURL, ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel, SelectionReason: SelectionReasonInitialRoute, RouteDecisionJSON: `{"strategy":"priority_weighted","mode":"probability","candidates":[{"channelId":1,"channelName":"channel-1","channelModelId":1,"upstreamModel":"upstream-1","expectation":0.8,"probability":0.8,"selected":true}]}`, StatusCode: http.StatusOK, InputTokens: 100, NormalInputTokens: 60, OutputTokens: 20, CachedTokens: 30, CacheWriteTokens: 10, SentTokens: 100, Success: true, CreatedAt: logs[0].CreatedAt},
		{RequestID: logs[1].ID, ChannelID: channels[0].ID, ChannelName: channels[0].Name, ChannelBaseURL: channels[0].BaseURL, ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel, StatusCode: http.StatusInternalServerError, SentTokens: 10, Success: false, CreatedAt: logs[1].CreatedAt},
		{RequestID: logs[1].ID, ChannelID: channels[1].ID, ChannelName: channels[1].Name, ChannelBaseURL: channels[1].BaseURL, ChannelModelID: mappings[1].ID, UpstreamModel: mappings[1].UpstreamModel, PreviousChannelID: channels[0].ID, PreviousChannelName: channels[0].Name, SelectionReason: SelectionReasonRetryableStatus, SelectionDetail: "HTTP 500", StatusCode: http.StatusBadGateway, SentTokens: 10, Success: false, CreatedAt: logs[1].CreatedAt.Add(time.Second)},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	NewRouter(store, NewClientAccessService(store), nil).RecordSessionAffinity(context.Background(), token.ID, model.ID, "codex-session", mappings[1].ID)

	management := NewManagementService(store)
	page, err := management.SessionLogs(context.Background(), SessionLogQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("session page = %+v", page)
	}
	if page.Summary.RequestCount != 3 || page.Summary.SuccessCount != 2 || page.Summary.AttemptCount != 3 || page.Summary.InputTokens != 160 || page.Summary.OutputTokens != 30 {
		t.Fatalf("session page summary = %+v", page.Summary)
	}
	var identified, unknown *SessionLogSummary
	for index := range page.Items {
		if page.Items[index].Identified {
			identified = &page.Items[index]
		} else {
			unknown = &page.Items[index]
		}
	}
	if identified == nil || unknown == nil {
		t.Fatalf("session summaries = %+v", page.Items)
	}
	if identified.RequestCount != 2 || identified.SuccessCount != 1 || identified.AttemptCount != 3 || identified.InputTokens != 110 || identified.NormalInputTokens != 70 || identified.OutputTokens != 20 || identified.CachedTokens != 30 || identified.CacheWriteTokens != 10 || identified.SentTokens != 120 || identified.EstimatedCost != 70 || identified.UpstreamCost != 60 || identified.AverageFirstTokenMS != 50 || identified.FirstTokenSampleCount != 1 || identified.AverageLatencyMS != 30 || identified.LatencySampleCount != 2 || identified.AverageDurationMS != 200 || identified.DurationSampleCount != 2 {
		t.Fatalf("identified summary = %+v", identified)
	}
	if identified.CurrentChannel == nil || identified.CurrentChannel.ChannelID != channels[1].ID || identified.CurrentChannel.AssignmentSource != "session_affinity" {
		t.Fatalf("current channel = %+v", identified.CurrentChannel)
	}
	if len(identified.CurrentChannel.MigrationHistory) != 1 || identified.CurrentChannel.MigrationHistory[0].FromChannelID != channels[0].ID || identified.CurrentChannel.MigrationHistory[0].ToChannelID != channels[1].ID {
		t.Fatalf("channel migration history = %+v", identified.CurrentChannel.MigrationHistory)
	}
	if unknown.FallbackRequestID != "unknown-request" || unknown.RequestCount != 1 || unknown.CurrentChannel != nil {
		t.Fatalf("unknown summary = %+v", unknown)
	}
	filtered, err := management.SessionLogs(context.Background(), SessionLogQuery{ChannelID: channels[1].ID, Page: 1, PageSize: 25})
	if err != nil || filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].SessionID != "codex-session" {
		t.Fatalf("channel-filtered sessions = %+v, %v", filtered, err)
	}

	detail, err := management.SessionLogDetail(context.Background(), SessionDetailQuery{SessionID: "codex-session", TokenID: token.ID, Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if detail.RequestTotal != 2 || len(detail.Requests) != 2 || detail.Requests[0].ID != "session-request-2" || len(detail.Requests[0].Attempts) != 2 || detail.Requests[1].ID != "session-request-1" {
		t.Fatalf("session detail = %+v", detail)
	}
	if detail.Summary.NormalInputTokens != 70 || detail.Summary.CacheWriteTokens != 10 || detail.Summary.SentTokens != 120 || detail.Summary.UpstreamCost != 60 || detail.Summary.AverageFirstTokenMS != 50 || detail.Summary.AverageLatencyMS != 30 || detail.Summary.AverageDurationMS != 200 || detail.Requests[1].CacheWriteTokens != 10 || detail.Requests[1].Attempts[0].CacheWriteTokens != 10 || detail.Requests[1].Attempts[0].SentTokens != 100 {
		t.Fatalf("session cache-write detail = %+v", detail)
	}
	if detail.Requests[1].Attempts[0].SelectionReason != SelectionReasonInitialRoute || detail.Requests[0].Attempts[0].SelectionReason != "" || detail.Requests[0].Attempts[1].SelectionReason != SelectionReasonRetryableStatus || detail.Requests[0].Attempts[1].PreviousChannelID != channels[0].ID {
		t.Fatalf("session selection metadata = %+v", detail.Requests)
	}
	if decision := detail.Requests[1].Attempts[0].RouteDecision; decision == nil || decision.Mode != "probability" || len(decision.Candidates) != 1 || !decision.Candidates[0].Selected || decision.Candidates[0].Probability != 0.8 {
		t.Fatalf("session route decision = %+v", decision)
	}
	secondPage, err := management.SessionLogDetail(context.Background(), SessionDetailQuery{SessionID: "codex-session", TokenID: token.ID, Page: 2, PageSize: 1})
	if err != nil || len(secondPage.Requests) != 1 || secondPage.Requests[0].ID != "session-request-1" || secondPage.RequestTotal != 2 {
		t.Fatalf("session detail second page = %+v, %v", secondPage, err)
	}
	reasoning, ok := detail.Requests[0].RequestParameters["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("detail parameters = %#v", detail.Requests[0].RequestParameters)
	}
	if detail.Requests[0].ReasoningEffort != "high" || detail.Requests[0].APIPath != "/v1/responses" || detail.Requests[0].Attempts[0].APIPath != "/v1/responses" {
		t.Fatalf("detail request metadata = %+v", detail.Requests[0])
	}
	successDetail, err := management.SessionLogDetail(context.Background(), SessionDetailQuery{SessionID: "codex-session", TokenID: token.ID, Status: "success", Page: 1, PageSize: 25})
	if err != nil || successDetail.RequestTotal != 1 || len(successDetail.Requests) != 1 || successDetail.Requests[0].ID != "session-request-1" || successDetail.Summary.RequestCount != 2 {
		t.Fatalf("successful session detail = %+v, %v", successDetail, err)
	}
	failureDetail, err := management.SessionLogDetail(context.Background(), SessionDetailQuery{SessionID: "codex-session", TokenID: token.ID, Status: "failure", Page: 1, PageSize: 25})
	if err != nil || failureDetail.RequestTotal != 1 || len(failureDetail.Requests) != 1 || failureDetail.Requests[0].ID != "session-request-2" || failureDetail.Summary.RequestCount != 2 {
		t.Fatalf("failed session detail = %+v, %v", failureDetail, err)
	}
	unknownDetail, err := management.SessionLogDetail(context.Background(), SessionDetailQuery{RequestID: "unknown-request", Page: 1, PageSize: 25})
	if err != nil || unknownDetail.RequestTotal != 1 || unknownDetail.Summary.Identified {
		t.Fatalf("unknown detail = %+v, %v", unknownDetail, err)
	}
	emptyFailureDetail, err := management.SessionLogDetail(context.Background(), SessionDetailQuery{RequestID: "unknown-request", Status: "failure", Page: 1, PageSize: 25})
	if err != nil || emptyFailureDetail.RequestTotal != 0 || len(emptyFailureDetail.Requests) != 0 || emptyFailureDetail.Summary.RequestCount != 1 {
		t.Fatalf("empty failed detail = %+v, %v", emptyFailureDetail, err)
	}
}

func TestSessionLogsSortByFirstCallAndUseFirstRequestName(t *testing.T) {
	store := newTestStore(t)
	token, model, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	now := time.Now()
	logs := []RelayRequestLog{
		{ID: "older-first", TokenID: token.ID, Endpoint: "responses", RequestedModel: model.Name, CodexSessionID: "older-session", SessionName: "最早的问题名称", StatusCode: http.StatusOK, CreatedAt: now.Add(-4 * time.Hour)},
		{ID: "older-latest", TokenID: token.ID, Endpoint: "responses", RequestedModel: model.Name, CodexSessionID: "older-session", SessionName: "不应覆盖名称", StatusCode: http.StatusOK, CreatedAt: now.Add(-10 * time.Minute)},
		{ID: "newer-first", TokenID: token.ID, Endpoint: "responses", RequestedModel: model.Name, CodexSessionID: "newer-session", SessionName: "较新的会话名", StatusCode: http.StatusOK, CreatedAt: now.Add(-2 * time.Hour)},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	management := NewManagementService(store)
	page, err := management.SessionLogs(context.Background(), SessionLogQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].SessionID != "newer-session" || page.Items[1].SessionID != "older-session" || page.Items[1].SessionName != "最早的问题名称" {
		t.Fatalf("sessions = %+v", page.Items)
	}
	filtered, err := management.SessionLogs(context.Background(), SessionLogQuery{Session: "最早的问题", Page: 1, PageSize: 25})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].SessionID != "older-session" {
		t.Fatalf("name-filtered sessions = %+v, %v", filtered, err)
	}
}

func TestResponsesAffinityPinsOriginalMapping(t *testing.T) {
	store := newTestStore(t)
	token, model, channels, mappings := createRouteFixture(t, store, RoutingLowestCost, "http://one.invalid", "http://two.invalid")
	access := NewClientAccessService(store)
	router := NewRouter(store, access, nil)
	router.RecordAffinity(context.Background(), "resp_123", mappings[1].ID)
	plan, err := router.Plan(context.Background(), token, model.Name, 10, 10, "resp_123", "")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Affinity || len(plan.Candidates) != 1 || plan.Candidates[0].Mapping.ID != mappings[1].ID {
		t.Fatalf("affinity plan = %+v", plan)
	}
	if plan.InitialSelection.Reason != SelectionReasonResponseAffinity {
		t.Fatalf("response affinity selection = %+v", plan.InitialSelection)
	}
	openUntil := time.Now().Add(time.Minute)
	if err := store.db.Model(&channels[1]).Update("circuit_open_until", openUntil).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := router.Plan(context.Background(), token, model.Name, 10, 10, "resp_123", ""); !errors.Is(err, ErrAffinityUnavailable) {
		t.Fatalf("Plan() error = %v, want affinity unavailable", err)
	}
}

func TestCodexSessionAffinityPinsMappingAndAllowsCircuitFailover(t *testing.T) {
	store := newTestStore(t)
	token, model, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid", "http://two.invalid")
	router := NewRouter(store, NewClientAccessService(store), nil)
	router.RecordSessionAffinity(context.Background(), token.ID, model.ID, "codex-session-a", mappings[1].ID)

	plan, err := router.Plan(context.Background(), token, model.Name, 10, 10, "", "codex-session-a")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.SessionAffinity || plan.Affinity || len(plan.Candidates) != 2 || plan.Candidates[0].Mapping.ID != mappings[1].ID {
		t.Fatalf("session affinity plan = %+v", plan)
	}
	if plan.InitialSelection.Reason != SelectionReasonSessionAffinity {
		t.Fatalf("session affinity selection = %+v", plan.InitialSelection)
	}

	otherPlan, err := router.Plan(context.Background(), token, model.Name, 10, 10, "", "codex-session-b")
	if err != nil {
		t.Fatal(err)
	}
	if otherPlan.SessionAffinity || otherPlan.Candidates[0].Mapping.ID != mappings[0].ID {
		t.Fatalf("independent session plan = %+v", otherPlan)
	}

	openUntil := time.Now().Add(time.Minute)
	if err := store.db.Model(&channels[1]).Update("circuit_open_until", openUntil).Error; err != nil {
		t.Fatal(err)
	}
	failoverPlan, err := router.Plan(context.Background(), token, model.Name, 10, 10, "", "codex-session-a")
	if err != nil {
		t.Fatal(err)
	}
	if !failoverPlan.SessionAffinity || len(failoverPlan.Candidates) != 1 || failoverPlan.Candidates[0].Mapping.ID != mappings[0].ID {
		t.Fatalf("circuit failover plan = %+v", failoverPlan)
	}
	if failoverPlan.InitialSelection.Reason != SelectionReasonCircuitOpen || failoverPlan.InitialSelection.PreviousChannelID != channels[1].ID || failoverPlan.InitialSelection.PreviousChannelName != channels[1].Name || failoverPlan.InitialSelection.Detail == "" {
		t.Fatalf("circuit failover selection = %+v", failoverPlan.InitialSelection)
	}
}

func TestCodexSessionModelSwitchRecordsPreviousChannel(t *testing.T) {
	store := newTestStore(t)
	token, firstModel, channels, firstMappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid", "http://two.invalid")
	secondModel := GatewayModel{Name: "second-public-model", RoutingStrategy: RoutingPriorityWeighted, Enabled: true}
	if err := store.db.Create(&secondModel).Error; err != nil {
		t.Fatal(err)
	}
	secondMapping := ChannelModel{
		ChannelID: channels[1].ID, ModelID: secondModel.ID, UpstreamModel: "second-upstream-model",
		Priority: 100, Weight: 100, InputPriceMicros: 1_000_000, OutputPriceMicros: 1_000_000, Enabled: true,
	}
	if err := store.db.Create(&secondMapping).Error; err != nil {
		t.Fatal(err)
	}
	stickyMapping := ChannelModel{
		ChannelID: channels[0].ID, ModelID: secondModel.ID, UpstreamModel: "second-upstream-model-sticky",
		Priority: 1, Weight: 1, InputPriceMicros: 1_000_000, OutputPriceMicros: 1_000_000, Enabled: true,
	}
	if err := store.db.Create(&stickyMapping).Error; err != nil {
		t.Fatal(err)
	}
	previousRequest := RelayRequestLog{
		ID: "model-switch-previous", TokenID: token.ID, RequestedModel: firstModel.Name,
		CodexSessionID: "model-switch-session", CreatedAt: time.Now().Add(-time.Minute),
	}
	if err := store.db.Create(&previousRequest).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelaySessionState{
		TokenID: token.ID, SessionID: previousRequest.CodexSessionID, LatestRequestID: previousRequest.ID,
		CreatedAt: previousRequest.CreatedAt, UpdatedAt: previousRequest.CreatedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelayAttemptLog{
		RequestID: previousRequest.ID, ChannelID: channels[0].ID, ChannelName: channels[0].Name,
		ChannelModelID: firstMappings[0].ID, UpstreamModel: firstMappings[0].UpstreamModel,
		Success: true, CreatedAt: previousRequest.CreatedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}

	router := NewRouter(store, NewClientAccessService(store), nil)
	router.RecordSessionAffinity(context.Background(), token.ID, secondModel.ID, "model-switch-session", secondMapping.ID)
	router.RecordAffinity(context.Background(), "resp_before_model_switch", firstMappings[0].ID)
	plan, err := router.Plan(context.Background(), token, secondModel.Name, 10, 10, "resp_before_model_switch", "model-switch-session")
	if err != nil {
		t.Fatal(err)
	}
	selection := plan.InitialSelection
	if len(plan.Candidates) != 2 || plan.Candidates[0].Mapping.ID != stickyMapping.ID {
		t.Fatalf("model-switch candidates = %+v", plan.Candidates)
	}
	if !plan.SessionAffinity || plan.Affinity || !plan.RefreshSessionAffinity || plan.SessionAffinityMappingID != 0 {
		t.Fatalf("model-switch affinity flags = %+v", plan)
	}
	if selection.Reason != SelectionReasonModelSwitch || selection.PreviousChannelID != channels[0].ID || selection.PreviousChannelName != channels[0].Name || selection.Detail != firstModel.Name+" -> "+secondModel.Name {
		t.Fatalf("model-switch selection = %+v", selection)
	}
	if selection.Decision == nil || selection.Decision.Mode != "session_affinity" || !selection.Decision.Candidates[0].Selected {
		t.Fatalf("model-switch route decision = %+v", selection.Decision)
	}

	execution := &relayExecution{
		token: token, modelID: secondModel.ID, payload: &RelayPayload{SessionKey: "model-switch-session"}, refreshSessionAffinity: true,
	}
	(&RelayService{router: router}).recordSessionAffinityAfterSuccess(context.Background(), execution, stickyMapping.ID)
	var refreshedAffinity SessionAffinity
	if err := store.db.Where("token_id = ? AND model_id = ? AND session_hash = ?", token.ID, secondModel.ID, hashSecret("model-switch-session")).First(&refreshedAffinity).Error; err != nil {
		t.Fatal(err)
	}
	if refreshedAffinity.ChannelModelID != stickyMapping.ID {
		t.Fatalf("refreshed model-switch affinity = %+v", refreshedAffinity)
	}

	if err := store.db.Create(&RelayRequestLog{
		ID: "same-model-previous", TokenID: token.ID, RequestedModel: secondModel.Name,
		CodexSessionID: "same-model-session", CreatedAt: time.Now().Add(-time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelayAttemptLog{
		RequestID: "same-model-previous", ChannelID: channels[1].ID, ChannelName: channels[1].Name,
		ChannelModelID: secondMapping.ID, UpstreamModel: secondMapping.UpstreamModel,
		Success: true, CreatedAt: time.Now().Add(-time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	sameModelPlan, err := router.Plan(context.Background(), token, secondModel.Name, 10, 10, "", "same-model-session")
	if err != nil {
		t.Fatal(err)
	}
	if sameModelPlan.InitialSelection.Reason != SelectionReasonInitialRoute || sameModelPlan.InitialSelection.PreviousChannelID != 0 {
		t.Fatalf("same-model selection = %+v", sameModelPlan.InitialSelection)
	}
}

func TestCodexSessionModelSwitchFallsBackWhenPreviousChannelDoesNotSupportModel(t *testing.T) {
	store := newTestStore(t)
	token, firstModel, channels, firstMappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid", "http://two.invalid")
	secondModel := GatewayModel{Name: "fallback-public-model", RoutingStrategy: RoutingPriorityWeighted, Enabled: true}
	if err := store.db.Create(&secondModel).Error; err != nil {
		t.Fatal(err)
	}
	secondMapping := ChannelModel{
		ChannelID: channels[1].ID, ModelID: secondModel.ID, UpstreamModel: "fallback-upstream-model",
		Priority: 100, Weight: 100, Enabled: true,
	}
	if err := store.db.Create(&secondMapping).Error; err != nil {
		t.Fatal(err)
	}
	previousRequest := RelayRequestLog{
		ID: "model-switch-fallback", TokenID: token.ID, RequestedModel: firstModel.Name,
		CodexSessionID: "model-switch-fallback-session", CreatedAt: time.Now().Add(-time.Minute),
	}
	if err := store.db.Create(&previousRequest).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelayAttemptLog{
		RequestID: previousRequest.ID, ChannelID: channels[0].ID, ChannelName: channels[0].Name,
		ChannelModelID: firstMappings[0].ID, UpstreamModel: firstMappings[0].UpstreamModel,
		Success: true, CreatedAt: previousRequest.CreatedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}

	router := NewRouter(store, NewClientAccessService(store), nil)
	plan, err := router.Plan(context.Background(), token, secondModel.Name, 10, 10, "", previousRequest.CodexSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Mapping.ID != secondMapping.ID || plan.RefreshSessionAffinity {
		t.Fatalf("model-switch fallback plan = %+v", plan)
	}
	if plan.InitialSelection.Reason != SelectionReasonModelSwitch || plan.InitialSelection.Decision == nil || plan.InitialSelection.Decision.Mode != "probability" {
		t.Fatalf("model-switch fallback selection = %+v", plan.InitialSelection)
	}
}

func TestCodexSessionAffinityClassifiesUnavailableTarget(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*testing.T, *Store, Channel, ChannelModel)
		wantReason string
	}{
		{
			name: "channel disabled",
			mutate: func(t *testing.T, store *Store, channel Channel, _ ChannelModel) {
				t.Helper()
				if err := store.db.Model(&channel).Update("enabled", false).Error; err != nil {
					t.Fatal(err)
				}
			},
			wantReason: SelectionReasonChannelDisabled,
		},
		{
			name: "mapping disabled",
			mutate: func(t *testing.T, store *Store, _ Channel, mapping ChannelModel) {
				t.Helper()
				if err := store.db.Model(&mapping).Update("enabled", false).Error; err != nil {
					t.Fatal(err)
				}
			},
			wantReason: SelectionReasonMappingDisabled,
		},
		{
			name: "target deleted",
			mutate: func(t *testing.T, store *Store, _ Channel, mapping ChannelModel) {
				t.Helper()
				if err := store.db.Delete(&mapping).Error; err != nil {
					t.Fatal(err)
				}
			},
			wantReason: SelectionReasonAffinityTargetMissing,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			token, model, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid", "http://two.invalid")
			router := NewRouter(store, NewClientAccessService(store), nil)
			router.RecordSessionAffinity(context.Background(), token.ID, model.ID, "codex-session", mappings[0].ID)
			if err := store.db.Create(&RelayRequestLog{
				ID: "historical-request", TokenID: token.ID, RequestedModel: model.Name,
				CodexSessionID: "codex-session", CreatedAt: time.Now().Add(-time.Minute),
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := store.db.Create(&RelayAttemptLog{
				RequestID: "historical-request", ChannelID: channels[0].ID, ChannelName: channels[0].Name,
				ChannelModelID: mappings[0].ID, UpstreamModel: mappings[0].UpstreamModel, CreatedAt: time.Now().Add(-time.Minute),
			}).Error; err != nil {
				t.Fatal(err)
			}

			test.mutate(t, store, channels[0], mappings[0])
			plan, err := router.Plan(context.Background(), token, model.Name, 10, 10, "", "codex-session")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Candidates) != 1 || plan.Candidates[0].Channel.ID != channels[1].ID {
				t.Fatalf("failover candidates = %+v", plan.Candidates)
			}
			selection := plan.InitialSelection
			if selection.Reason != test.wantReason || selection.PreviousChannelID != channels[0].ID || selection.PreviousChannelName != channels[0].Name {
				t.Fatalf("selection = %+v, want reason %q and previous channel %d", selection, test.wantReason, channels[0].ID)
			}
		})
	}
}

func TestCircuitOpensAfterThreeFailuresAndRecovers(t *testing.T) {
	store := newTestStore(t)
	_, _, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	relay := newTestRelay(store)
	for range 3 {
		relay.recordChannelFailure(context.Background(), channels[0].ID, mappings[0].ID, "retryable failure")
	}
	var channel Channel
	if err := store.db.First(&channel, channels[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ConsecutiveFailures != 0 || channel.CircuitLevel != CircuitLevelTemporary || channel.CircuitOpenUntil == nil || !channel.CircuitOpenUntil.After(time.Now()) {
		t.Fatalf("channel circuit state = %+v", channel)
	}
	channelID := channel.ID
	relay.recordChannelSuccess(context.Background(), channelID, 42)
	channel = Channel{}
	if err := store.db.First(&channel, channelID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ConsecutiveFailures != 0 || channel.CircuitLevel != CircuitLevelClosed || channel.CircuitOpenUntil != nil || channel.LatencyEWMA != 42 {
		t.Fatalf("recovered channel = %+v", channel)
	}
}

func TestBackfillCircuitLevelsPreservesExistingOpenCircuits(t *testing.T) {
	store := newTestStore(t)
	_, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	if err := store.db.Model(&Channel{}).Where("id = ?", channels[0].ID).Update("circuit_open_until", time.Now().Add(time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.backfillCircuitLevels(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillCircuitLevels(); err != nil {
		t.Fatal(err)
	}
	var channel Channel
	if err := store.db.First(&channel, channels[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.CircuitLevel != CircuitLevelTemporary {
		t.Fatalf("backfilled circuit level = %d", channel.CircuitLevel)
	}
}

func TestCircuitEscalatesAndRecoversOneLevelAtATime(t *testing.T) {
	store := newTestStore(t)
	_, _, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	relay := newTestRelay(store)
	channelID := channels[0].ID

	for range circuitFailureThreshold {
		relay.recordChannelFailure(context.Background(), channelID, mappings[0].ID, "temporary failure")
	}
	if err := store.db.Model(&Channel{}).Where("id = ?", channelID).Update("circuit_open_until", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	for range circuitFailureThreshold {
		relay.recordChannelFailure(context.Background(), channelID, mappings[0].ID, "extended failure")
	}

	var channel Channel
	if err := store.db.First(&channel, channelID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.CircuitLevel != CircuitLevelExtended || channel.CircuitOpenUntil == nil || !channel.CircuitOpenUntil.After(time.Now().Add(4*time.Minute)) {
		t.Fatalf("extended circuit = %+v", channel)
	}

	relay.recordChannelSuccess(context.Background(), channelID, 25)
	channel = Channel{}
	if err := store.db.First(&channel, channelID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.CircuitLevel != CircuitLevelTemporary || channel.CircuitOpenUntil != nil || channel.LastError == "" {
		t.Fatalf("first recovery = %+v", channel)
	}
	relay.recordChannelSuccess(context.Background(), channelID, 20)
	channel = Channel{}
	if err := store.db.First(&channel, channelID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.CircuitLevel != CircuitLevelClosed || channel.CircuitOpenUntil != nil || channel.LastError != "" {
		t.Fatalf("full recovery = %+v", channel)
	}
	var records []CircuitRecord
	if err := store.db.Order("level asc").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Resolution != CircuitResolutionEscalated || records[1].Resolution != CircuitResolutionAutomaticRecovery {
		t.Fatalf("circuit records = %+v", records)
	}
}

func TestHighestCircuitLevelDisablesOnlyFailingMapping(t *testing.T) {
	store := newTestStore(t)
	token, _, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	secondModel := GatewayModel{Name: "second-public-model", RoutingStrategy: RoutingPriorityWeighted, Enabled: true}
	if err := store.db.Create(&secondModel).Error; err != nil {
		t.Fatal(err)
	}
	secondMapping := ChannelModel{ChannelID: channels[0].ID, ModelID: secondModel.ID, UpstreamModel: "second-upstream-model", Priority: 10, Weight: 100, Enabled: true}
	if err := store.db.Create(&secondMapping).Error; err != nil {
		t.Fatal(err)
	}
	relay := newTestRelay(store)
	channelID := channels[0].ID
	if err := store.db.Model(&Channel{}).Where("id = ?", channelID).Updates(map[string]any{
		"circuit_level":        CircuitLevelExtended,
		"consecutive_failures": circuitFailureThreshold - 1,
		"circuit_open_until":   time.Now().Add(-time.Second),
	}).Error; err != nil {
		t.Fatal(err)
	}

	relay.recordChannelFailure(context.Background(), channelID, mappings[0].ID, "terminal failure")
	var channel Channel
	if err := store.db.First(&channel, channelID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.CircuitLevel != CircuitLevelClosed || !channel.Enabled || channel.CircuitOpenUntil != nil || channel.ConsecutiveFailures != 0 {
		t.Fatalf("channel should remain available after terminal mapping circuit = %+v", channel)
	}
	var failedMapping ChannelModel
	if err := store.db.First(&failedMapping, mappings[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if failedMapping.Enabled || !failedMapping.CircuitDisabled {
		t.Fatalf("failed mapping = %+v", failedMapping)
	}
	var availableMapping ChannelModel
	if err := store.db.First(&availableMapping, secondMapping.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !availableMapping.Enabled || availableMapping.CircuitDisabled {
		t.Fatalf("unrelated mapping = %+v", availableMapping)
	}
	plan, err := relay.router.Plan(context.Background(), token, secondModel.Name, 10, 10, "", "")
	if err != nil || len(plan.Candidates) != 1 || plan.Candidates[0].Mapping.ID != secondMapping.ID {
		t.Fatalf("remaining route plan = %+v, error = %v", plan, err)
	}

	relay.recordChannelSuccess(context.Background(), channelID, 10)
	failedMapping = ChannelModel{}
	if err := store.db.First(&failedMapping, mappings[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if failedMapping.Enabled || !failedMapping.CircuitDisabled {
		t.Fatalf("automatic channel recovery changed terminal mapping = %+v", failedMapping)
	}

	management := NewManagementService(store)
	page, err := management.CircuitRecords(context.Background(), CircuitRecordQuery{Level: CircuitLevelManual})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.PendingManual != 1 || len(page.Items) != 1 || !page.Items[0].MappingCircuitDisabled {
		t.Fatalf("terminal circuit page = %+v", page)
	}
	if err := management.ReopenCircuitMapping(context.Background(), page.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	failedMapping = ChannelModel{}
	if err := store.db.First(&failedMapping, mappings[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if !failedMapping.Enabled || failedMapping.CircuitDisabled {
		t.Fatalf("manually reopened mapping = %+v", failedMapping)
	}
	var record CircuitRecord
	if err := store.db.First(&record, page.Items[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.ResolvedAt == nil || record.Resolution != CircuitResolutionManualReopen {
		t.Fatalf("resolved terminal record = %+v", record)
	}
}

func TestResetChannelCircuitClearsFailureState(t *testing.T) {
	store := newTestStore(t)
	_, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid")
	openUntil := time.Now().Add(time.Minute)
	if err := store.db.Model(&Channel{}).Where("id = ?", channels[0].ID).Updates(map[string]any{
		"enabled":              false,
		"consecutive_failures": 4,
		"circuit_level":        CircuitLevelManual,
		"circuit_open_until":   openUntil,
		"last_error":           "insufficient balance",
	}).Error; err != nil {
		t.Fatal(err)
	}
	management := NewManagementService(store)
	if err := management.ResetChannelCircuit(context.Background(), channels[0].ID); err != nil {
		t.Fatal(err)
	}
	var channel Channel
	if err := store.db.First(&channel, channels[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if !channel.Enabled || channel.ConsecutiveFailures != 0 || channel.CircuitLevel != CircuitLevelClosed || channel.CircuitOpenUntil != nil || channel.LastError != "" {
		t.Fatalf("reset channel = %+v", channel)
	}
	if err := management.ResetChannelCircuit(context.Background(), channel.ID+1000); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing channel error = %v", err)
	}
}

func TestRelayRetriesJSONAndRewritesAuthorizationAndModel(t *testing.T) {
	store := newTestStore(t)
	var firstCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		firstCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":{"message":"temporary","type":"api_error","code":"temporary"},"usage":{"prompt_tokens":100,"completion_tokens":20,"cost":"9.99"}}`))
	}))
	defer first.Close()
	var secondCalls atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		secondCalls.Add(1)
		if request.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "upstream-2" || body["unknown_field"] != "preserved" {
			t.Errorf("upstream body = %+v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Set-Cookie", "gateway_admin_session=poisoned; Path=/api")
		_, _ = writer.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2,"cache_creation_tokens":1},"cost":"0.000042"}}`))
	}))
	defer second.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	payloadBody := []byte(`{"model":"public-model","prompt_cache_key":"codex-session-log","temperature":0.4,"messages":[{"role":"user","content":"hello"}],"unknown_field":"preserved"}`)
	payload, err := ParseRelayPayload(payloadBody)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	relay := newTestRelay(store)
	if publicErr := relay.Relay(context.Background(), recorder, http.Header{"Authorization": []string{"Bearer client-secret"}}, "", "chat", token, payload, payloadBody); publicErr != nil {
		t.Fatal(publicErr)
	}
	if recorder.Code != http.StatusOK || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("status=%d calls=%d/%d body=%s", recorder.Code, firstCalls.Load(), secondCalls.Load(), recorder.Body.String())
	}
	if cookie := recorder.Header().Get("Set-Cookie"); cookie != "" {
		t.Fatalf("upstream Set-Cookie was forwarded: %q", cookie)
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.AttemptCount != 2 || requestLog.InputTokens != 10 || requestLog.NormalInputTokens != 7 || requestLog.OutputTokens != 5 || requestLog.CachedTokens != 2 || requestLog.CacheWriteTokens != 1 || requestLog.SentTokens <= 0 || requestLog.EstimatedCost != 15 || requestLog.UpstreamCost != 42 || requestLog.CostSource != CostSourceUpstream {
		t.Fatalf("request log = %+v", requestLog)
	}
	if requestLog.CodexSessionID != "codex-session-log" || requestLog.CodexSessionSource != "prompt_cache_key" || requestLog.TokenName != token.Name || requestLog.TokenKeyPrefix != token.KeyPrefix {
		t.Fatalf("request log identity snapshot = %+v", requestLog)
	}
	if requestLog.SessionName != "hello" || requestLog.RequestBody != string(payloadBody) || requestLog.RequestBodyTruncated || requestLog.ResponseBodyTruncated || !strings.Contains(requestLog.ResponseBody, `"id":"chatcmpl_1"`) {
		t.Fatalf("request payload snapshot = %+v", requestLog)
	}
	if !strings.Contains(requestLog.RequestParametersJSON, `"temperature":0.4`) || strings.Contains(requestLog.RequestParametersJSON, "hello") || strings.Contains(requestLog.RequestParametersJSON, "unknown_field") {
		t.Fatalf("request parameter snapshot = %s", requestLog.RequestParametersJSON)
	}
	var attemptLogs []RelayAttemptLog
	if err := store.db.Order("id ASC").Find(&attemptLogs).Error; err != nil {
		t.Fatal(err)
	}
	if len(attemptLogs) != 2 || attemptLogs[0].ChannelName == "" || attemptLogs[0].ChannelBaseURL == "" || attemptLogs[1].ChannelName == "" || attemptLogs[1].NormalInputTokens != 7 || attemptLogs[1].CacheWriteTokens != 1 || attemptLogs[0].SentTokens <= 0 || attemptLogs[1].SentTokens <= 0 || attemptLogs[0].EstimatedCost != 0 || attemptLogs[0].UpstreamCost != 0 || attemptLogs[0].CostSource != CostSourceFailedZero || attemptLogs[1].UpstreamCost != 42 || requestLog.SentTokens != attemptLogs[0].SentTokens+attemptLogs[1].SentTokens {
		t.Fatalf("attempt channel snapshots = %+v", attemptLogs)
	}
	if attemptLogs[0].InputTokens != 100 || attemptLogs[0].OutputTokens != 20 || requestLog.InputTokens != attemptLogs[1].InputTokens || requestLog.OutputTokens != attemptLogs[1].OutputTokens {
		t.Fatalf("request statistics must use final attempt only: request = %+v, attempts = %+v", requestLog, attemptLogs)
	}
	if attemptLogs[0].SelectionReason != SelectionReasonInitialRoute || attemptLogs[1].SelectionReason != SelectionReasonRetryableStatus || attemptLogs[1].SelectionDetail != "HTTP 500" || attemptLogs[1].PreviousChannelID != attemptLogs[0].ChannelID || attemptLogs[1].PreviousChannelName != attemptLogs[0].ChannelName {
		t.Fatalf("attempt selection metadata = %+v", attemptLogs)
	}
	if !strings.Contains(attemptLogs[0].RequestBody, `"model":"upstream-1"`) || !strings.Contains(attemptLogs[0].ResponseBody, `"code":"temporary"`) || !strings.Contains(attemptLogs[1].RequestBody, `"model":"upstream-2"`) || !strings.Contains(attemptLogs[1].ResponseBody, `"id":"chatcmpl_1"`) {
		t.Fatalf("attempt payload snapshots = %+v", attemptLogs)
	}
	management := NewManagementService(store)
	page, err := management.Logs(context.Background(), LogQuery{Page: 1, PageSize: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].RequestBody != "" || page.Items[0].ResponseBody != "" || page.Items[0].Attempts[0].RequestBody != "" {
		t.Fatalf("lightweight log page = %+v, %v", page, err)
	}
	if page.Summary.RequestCount != 1 || page.Summary.SuccessCount != 1 || page.Summary.AttemptCount != 2 || page.Summary.InputTokens != 10 || page.Summary.OutputTokens != 5 || page.Summary.UpstreamCost != 42 {
		t.Fatalf("log page summary = %+v", page.Summary)
	}
	detail, err := management.LogDetail(context.Background(), requestLog.ID)
	if err != nil || detail.RequestBody != string(payloadBody) || !strings.Contains(detail.ResponseBody, `"id":"chatcmpl_1"`) || !strings.Contains(detail.Attempts[0].ResponseBody, `"code":"temporary"`) {
		t.Fatalf("log detail = %+v, %v", detail, err)
	}
	stageCounts := make(map[string]int)
	for _, step := range detail.Steps {
		if step.RequestID != requestLog.ID || step.DurationUS < 0 || step.StartedOffsetUS < 0 {
			t.Fatalf("invalid relay step = %+v", step)
		}
		stageCounts[step.Stage]++
	}
	for _, stage := range []string{
		RelayStageRequestLogStart, RelayStageSessionResolution, RelayStageTokenEstimation,
		RelayStageRoutePlanning, RelayStagePayloadTransform, RelayStageCredentialDecrypt,
		RelayStageUpstreamRequestBuild, RelayStageUpstreamWaitHeaders, RelayStageResponseBodyRead,
		RelayStageResponseAnalysis, RelayStageAttemptLogPrepare, RelayStageResponseWrite,
		RelayStageRequestFinalize, RelayStageRequestLogPersist,
	} {
		if stageCounts[stage] == 0 {
			t.Fatalf("missing relay timing stage %q in %+v", stage, detail.Steps)
		}
	}
	if stageCounts[RelayStageUpstreamWaitHeaders] != 2 || stageCounts[RelayStagePayloadTransform] != 2 {
		t.Fatalf("attempt timing counts = %+v", stageCounts)
	}
	var stat TokenDailyStat
	if err := store.db.First(&stat).Error; err != nil {
		t.Fatal(err)
	}
	if stat.RequestCount != 1 || stat.SuccessCount != 1 || stat.InputTokens != 10 || stat.NormalInputTokens != 7 || stat.OutputTokens != 5 || stat.CachedTokens != 2 || stat.CacheWriteTokens != 1 || stat.SentTokens != requestLog.SentTokens || stat.EstimatedCost != 15 || stat.UpstreamCost != 42 || stat.AttemptCount != 2 {
		t.Fatalf("daily stat = %+v", stat)
	}
}

func TestRelayZerosCostsForNonSuccessAndNetworkFailure(t *testing.T) {
	t.Run("non success response", func(t *testing.T) {
		store := newTestStore(t)
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"code":"bad_request"},"usage":{"input_tokens":20,"output_tokens":4,"cost":"1.25"}}`))
		}))
		defer upstream.Close()
		token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
		body := []byte(`{"model":"public-model","input":"hello"}`)
		payload, err := ParseRelayPayload(body)
		if err != nil {
			t.Fatal(err)
		}
		if publicErr := newTestRelay(store).Relay(context.Background(), httptest.NewRecorder(), nil, "", "responses", token, payload, body); publicErr != nil {
			t.Fatal(publicErr)
		}
		var requestLog RelayRequestLog
		if err := store.db.First(&requestLog).Error; err != nil {
			t.Fatal(err)
		}
		var attemptLog RelayAttemptLog
		if err := store.db.First(&attemptLog).Error; err != nil {
			t.Fatal(err)
		}
		if requestLog.InputTokens != 20 || requestLog.OutputTokens != 4 || requestLog.EstimatedCost != 0 || requestLog.UpstreamCost != 0 || requestLog.CostSource != CostSourceFailedZero || attemptLog.EstimatedCost != 0 || attemptLog.UpstreamCost != 0 || attemptLog.CostSource != CostSourceFailedZero {
			t.Fatalf("request = %+v, attempt = %+v", requestLog, attemptLog)
		}
	})

	t.Run("network failure", func(t *testing.T) {
		store := newTestStore(t)
		token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://network.invalid")
		body := []byte(`{"model":"public-model","input":"hello"}`)
		payload, err := ParseRelayPayload(body)
		if err != nil {
			t.Fatal(err)
		}
		relay := newTestRelay(store)
		relay.client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network unavailable")
		})
		publicErr := relay.Relay(context.Background(), httptest.NewRecorder(), nil, "", "responses", token, payload, body)
		if publicErr == nil || publicErr.Status != http.StatusBadGateway {
			t.Fatalf("Relay() error = %+v", publicErr)
		}
		var requestLog RelayRequestLog
		if err := store.db.First(&requestLog).Error; err != nil {
			t.Fatal(err)
		}
		var attemptLog RelayAttemptLog
		if err := store.db.First(&attemptLog).Error; err != nil {
			t.Fatal(err)
		}
		if requestLog.EstimatedCost != 0 || requestLog.UpstreamCost != 0 || requestLog.CostSource != CostSourceFailedZero || attemptLog.EstimatedCost != 0 || attemptLog.UpstreamCost != 0 || attemptLog.CostSource != CostSourceFailedZero {
			t.Fatalf("request = %+v, attempt = %+v", requestLog, attemptLog)
		}
	})
}

func TestRelayStreamUsesLastValidCostSnapshot(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"usage\":{\"input_tokens\":10,\"output_tokens\":1,\"cost\":\"0.000100\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"total_cost\":\"0.000250\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"usage\":{\"cost\":\"invalid\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_cost\",\"status\":\"completed\"}}\n\n"))
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
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.InputTokens != 10 || requestLog.OutputTokens != 2 || requestLog.EstimatedCost != 12 || requestLog.UpstreamCost != 250 || requestLog.CostSource != CostSourceUpstream {
		t.Fatalf("stream request = %+v", requestLog)
	}
	var attemptLog RelayAttemptLog
	if err := store.db.First(&attemptLog).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requestLog.ResponseBody, "response.completed") || !strings.Contains(attemptLog.ResponseBody, `"total_cost":"0.000250"`) {
		t.Fatalf("stream payload snapshots request=%q attempt=%q", requestLog.ResponseBody, attemptLog.ResponseBody)
	}
}

func TestSSEEventHasOutputToken(t *testing.T) {
	tests := []struct {
		name  string
		event string
		want  bool
	}{
		{name: "responses metadata", event: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n", want: false},
		{name: "responses text delta", event: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n", want: true},
		{name: "chat role delta", event: "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n", want: false},
		{name: "chat content delta", event: "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n", want: true},
		{name: "chat tool delta", event: "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"arguments\":\"{\"}}]}}]}\n\n", want: true},
		{name: "done", event: "data: [DONE]\n\n", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sseEventHasOutputToken([]byte(test.event)); got != test.want {
				t.Fatalf("sseEventHasOutputToken() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRequestSessionNameUsesLatestUserText(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "chat string", body: `{"messages":[{"role":"assistant","content":"ignored"},{"role":"user","content":"  first   question here  "}]}`, want: "first ques"},
		{name: "chat accumulated context", body: `{"messages":[{"role":"user","content":"old context"},{"role":"assistant","content":"old answer"},{"role":"user","content":"current user request"}]}`, want: "current us"},
		{name: "chat parts", body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"private"}},{"type":"text","text":"inspect this image"}]}]}`, want: "inspect th"},
		{name: "chat Codex image attachment", body: `{"messages":[{"role":"user","content":"# Files mentioned by the user:\n\n## screenshot.png: /tmp/screenshot.png\n\n## My request for Codex:\n修复一下这个问题\n<image name=[Image #1] path=\"/tmp/screenshot.png\">\n</image>"}]}`, want: "修复一下这个问题"},
		{name: "responses string", body: `{"input":"生成一份设备运行日报表"}`, want: "生成一份设备运行日报"},
		{name: "responses messages", body: `{"input":[{"role":"developer","content":"ignored"},{"role":"user","content":[{"type":"input_text","text":"line one"},{"type":"input_text","text":"line two"}]}]}`, want: "line one l"},
		{name: "responses accumulated context", body: `{"input":[{"role":"user","content":"older prompt"},{"role":"assistant","content":"older answer"},{"role":"user","content":"latest prompt"}]}`, want: "latest pro"},
		{name: "responses Codex image attachment", body: `{"input":[{"role":"user","content":[{"type":"input_text","text":"# Files mentioned by the user:\n\n## screenshot.png: /tmp/screenshot.png\n\n## My request for Codex:\ninspect session naming"},{"type":"input_text","text":"<image name=[Image #1] path=\"/tmp/screenshot.png\">"},{"type":"input_image","image_url":"data:image/png;base64,AA=="},{"type":"input_text","text":"</image>"}]}]}`, want: "inspect se"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := requestSessionName([]byte(test.body)); got != test.want {
				t.Fatalf("requestSessionName() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSessionPayloadCompactionRetainsOnlyIncrement(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	sharedContext := strings.Repeat("shared system context ", 80)
	first, _ := json.Marshal(map[string]any{
		"model": "model-a", "instructions": sharedContext,
		"messages": []any{
			map[string]any{"role": "system", "content": "rules"},
			map[string]any{"role": "user", "content": "first prompt"},
		},
	})
	second, _ := json.Marshal(map[string]any{
		"model": "model-a", "instructions": sharedContext,
		"messages": []any{
			map[string]any{"role": "system", "content": "rules"},
			map[string]any{"role": "user", "content": "first prompt"},
			map[string]any{"role": "assistant", "content": "first answer"},
			map[string]any{"role": "user", "content": "second prompt"},
		},
	})

	firstResponse := []byte(`{"choices":[{"message":{"role":"assistant","content":"first answer"}}]}`)
	if got := compactSessionPayload(store.db, 7, "session-a", "request-1", "first prom", codexThreadSourceUser, first, firstResponse, now); string(got) != string(first) {
		t.Fatalf("first payload = %s", got)
	}
	compacted := compactSessionPayload(store.db, 7, "session-a", "request-2", "second pro", codexThreadSourceUser, second, nil, now.Add(time.Minute))
	var envelope payloadDeltaEnvelope
	if err := json.Unmarshal(compacted, &envelope); err != nil {
		t.Fatalf("decode compacted payload: %v (%s)", err, compacted)
	}
	messages, ok := envelope.Payload["messages"].([]any)
	if envelope.Metadata.Mode != "session" || envelope.Metadata.BaseRequestID != "request-1" || envelope.Metadata.OmittedItems["messages"] != 3 || !ok || len(messages) != 1 {
		t.Fatalf("compacted payload = %+v", envelope)
	}
	if _, exists := envelope.Payload["instructions"]; exists {
		t.Fatalf("unchanged instructions were retained: %+v", envelope.Payload)
	}
	var state RelaySessionState
	if err := store.db.Where("token_id = ? AND session_id = ?", 7, "session-a").First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.Title != "first prom" || state.ThreadSource != codexThreadSourceUser || state.LatestRequestID != "request-2" {
		t.Fatalf("session state = %+v", state)
	}
}

func TestRenameSessionPersistsCustomTitle(t *testing.T) {
	store := newTestStore(t)
	log := RelayRequestLog{
		ID: "request-title", TokenID: 9, Endpoint: "responses", RequestedModel: "model-a",
		CodexSessionID: "session-title", SessionName: "自动标题", StatusCode: http.StatusOK, CreatedAt: time.Now(),
	}
	if err := store.db.Create(&log).Error; err != nil {
		t.Fatal(err)
	}
	management := NewManagementService(store)
	if err := management.RenameSession(context.Background(), SessionTitleInput{
		TokenID: 9, SessionID: "session-title", Title: "  我的 自定义标题  ",
	}); err != nil {
		t.Fatal(err)
	}
	page, err := management.SessionLogs(context.Background(), SessionLogQuery{Session: "自定义标题", Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].SessionName != "我的 自定义标题" {
		t.Fatalf("renamed session page = %+v", page.Items)
	}
	var state RelaySessionState
	if err := store.db.Where("token_id = ? AND session_id = ?", 9, "session-title").First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if !state.TitleCustomized || state.Title != "我的 自定义标题" {
		t.Fatalf("custom title state = %+v", state)
	}
}

func TestStoredPayloadIsBoundedAndValidUTF8(t *testing.T) {
	payload := append(bytes.Repeat([]byte("a"), maxDetailedPayloadBytes-1), []byte("你b")...)
	stored, truncated := storedPayload(payload, false)
	if !truncated || !utf8.ValidString(stored) || len(stored) > maxDetailedPayloadBytes {
		t.Fatalf("stored payload length=%d truncated=%v valid=%v", len(stored), truncated, utf8.ValidString(stored))
	}
}

func TestStoredPayloadCompressesLargeBodiesAndKeepsLegacyTextReadable(t *testing.T) {
	payload := bytes.Repeat([]byte(`{"message":"repeated upstream context"}`), 200)
	stored, truncated := storedPayload(payload, false)
	if truncated || !strings.HasPrefix(stored, compressedPayloadPrefix) || len(stored) >= len(payload) {
		t.Fatalf("stored payload length=%d original=%d truncated=%v prefix=%v", len(stored), len(payload), truncated, strings.HasPrefix(stored, compressedPayloadPrefix))
	}
	if restored := decompressStoredPayload(stored); restored != string(payload) {
		t.Fatalf("restored payload length=%d want=%d", len(restored), len(payload))
	}
	legacy := `{"status":"completed"}`
	if decompressStoredPayload(legacy) != legacy {
		t.Fatalf("legacy payload changed")
	}
	if decoded, ok := decodeStoredPayload(compressedPayloadPrefix + "invalid"); ok || decoded != "" {
		t.Fatalf("invalid compressed payload decoded as %q", decoded)
	}
}

func TestCompressDetailedPayloadsMigratesLegacyRowsAndKeepsDetailsReadable(t *testing.T) {
	store := newTestStore(t)
	requestBody := string(bytes.Repeat([]byte(`{"input":"legacy request context"}`), 160))
	responseBody := string(bytes.Repeat([]byte(`{"output":"legacy response context"}`), 160))
	requestLog := RelayRequestLog{
		ID: "legacy-compression", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a",
		RequestBody: requestBody, ResponseBody: responseBody, StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: time.Now().UTC(),
	}
	if err := store.db.Create(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	attempt := RelayAttemptLog{
		RequestID: requestLog.ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a",
		RequestBody: requestBody, ResponseBody: responseBody, StatusCode: http.StatusOK, Success: true, Outcome: RelayOutcomeSuccess, CreatedAt: requestLog.CreatedAt,
	}
	if err := store.db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.compressDetailedPayloads(); err != nil {
		t.Fatal(err)
	}
	var storedRequest RelayRequestLog
	if err := store.db.First(&storedRequest, "id = ?", requestLog.ID).Error; err != nil {
		t.Fatal(err)
	}
	var storedAttempt RelayAttemptLog
	if err := store.db.First(&storedAttempt, attempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"request request":  storedRequest.RequestBody,
		"request response": storedRequest.ResponseBody,
		"attempt request":  storedAttempt.RequestBody,
		"attempt response": storedAttempt.ResponseBody,
	} {
		if !strings.HasPrefix(value, compressedPayloadPrefix) {
			t.Fatalf("%s was not compressed", name)
		}
	}
	detail, err := NewManagementService(store).LogDetail(context.Background(), requestLog.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.RequestBody != requestBody || detail.ResponseBody != responseBody || len(detail.Attempts) != 1 || detail.Attempts[0].RequestBody != requestBody || detail.Attempts[0].ResponseBody != responseBody {
		t.Fatalf("decompressed detail = %+v", detail)
	}
	if err := store.compressDetailedPayloads(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteSpaceReclaimMigrationIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	if err := store.reclaimSQLiteSpaceOnce(); err != nil {
		t.Fatal(err)
	}
	if err := store.reclaimSQLiteSpaceOnce(); err != nil {
		t.Fatal(err)
	}
	var migrations int64
	if err := store.db.Model(&GatewayMigration{}).Where("name = ?", "sqlite_space_reclaim_v1").Count(&migrations).Error; err != nil {
		t.Fatal(err)
	}
	if migrations != 1 {
		t.Fatalf("space reclaim migrations = %d", migrations)
	}
	store.checkpointSQLiteWAL()
}

func TestBackfillApplicationOutcomesCorrectsCompressedHistoricalSuccess(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	model := GatewayModel{Name: "model-a", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	responseBody := strings.Repeat("event: codex.response.metadata\ndata: {\"type\":\"codex.response.metadata\"}\n\n", 40) +
		"event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_historical_incomplete\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"upstream_interrupted\"},\"output\":[]}}\n\n" +
		"data: [DONE]\n\n"
	requestLog := RelayRequestLog{
		ID: "historical-incomplete", TokenID: 9, Endpoint: "responses", RequestedModel: model.Name, CodexSessionID: "historical-session", Stream: true,
		RequestBody: strings.Repeat("request-context-", 200), ResponseBody: responseBody,
		StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, EstimatedCost: 120, UpstreamCost: 90, CostSource: CostSourceUpstream, AttemptCount: 1, CreatedAt: now,
	}
	if err := store.db.Create(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	attempt := RelayAttemptLog{
		RequestID: requestLog.ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a",
		RequestBody: requestLog.RequestBody, ResponseBody: responseBody, StatusCode: http.StatusOK,
		Success: true, Outcome: RelayOutcomeSuccess, EstimatedCost: 120, UpstreamCost: 90, CostSource: CostSourceUpstream, CreatedAt: now,
	}
	if err := store.db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	stat := TokenDailyStat{Date: now.Format(time.DateOnly), TokenID: requestLog.TokenID, RequestCount: 1, SuccessCount: 1, EstimatedCost: 120, UpstreamCost: 90}
	if err := store.db.Create(&stat).Error; err != nil {
		t.Fatal(err)
	}
	affinity := ResponseAffinity{ResponseHash: hashSecret("resp_historical_incomplete"), ChannelModelID: 1, ExpiresAt: now.Add(time.Hour)}
	if err := store.db.Create(&affinity).Error; err != nil {
		t.Fatal(err)
	}
	sessionAffinity := SessionAffinity{
		TokenID: requestLog.TokenID, ModelID: model.ID, SessionHash: hashSecret(requestLog.CodexSessionID), ChannelModelID: 1,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now.Add(time.Millisecond),
	}
	if err := store.db.Create(&sessionAffinity).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.compressDetailedPayloads(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillApplicationOutcomes(); err != nil {
		t.Fatal(err)
	}

	var correctedRequest RelayRequestLog
	if err := store.db.First(&correctedRequest, "id = ?", requestLog.ID).Error; err != nil {
		t.Fatal(err)
	}
	if correctedRequest.Outcome != RelayOutcomeFailed || correctedRequest.StatusCode != http.StatusBadGateway || correctedRequest.ErrorCode != "upstream_interrupted" || correctedRequest.EstimatedCost != 0 || correctedRequest.UpstreamCost != 0 || correctedRequest.CostSource != CostSourceFailedZero || !strings.HasPrefix(correctedRequest.ResponseBody, compressedPayloadPrefix) {
		t.Fatalf("corrected request = %+v", correctedRequest)
	}
	var correctedAttempt RelayAttemptLog
	if err := store.db.First(&correctedAttempt, attempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if correctedAttempt.Success || correctedAttempt.Outcome != RelayOutcomeFailed || correctedAttempt.EstimatedCost != 0 || correctedAttempt.UpstreamCost != 0 || correctedAttempt.CostSource != CostSourceFailedZero || !strings.Contains(correctedAttempt.ErrorMessage, "upstream_interrupted") {
		t.Fatalf("corrected attempt = %+v", correctedAttempt)
	}
	var correctedStat TokenDailyStat
	if err := store.db.First(&correctedStat, "date = ? AND token_id = ?", stat.Date, stat.TokenID).Error; err != nil {
		t.Fatal(err)
	}
	if correctedStat.SuccessCount != 0 || correctedStat.EstimatedCost != 0 || correctedStat.UpstreamCost != 0 {
		t.Fatalf("corrected stat = %+v", correctedStat)
	}
	var affinityCount int64
	if err := store.db.Model(&ResponseAffinity{}).Where("response_hash = ?", affinity.ResponseHash).Count(&affinityCount).Error; err != nil || affinityCount != 0 {
		t.Fatalf("response affinity count=%d error=%v", affinityCount, err)
	}
	var sessionAffinityCount int64
	if err := store.db.Model(&SessionAffinity{}).Where("token_id = ? AND model_id = ? AND session_hash = ?", sessionAffinity.TokenID, sessionAffinity.ModelID, sessionAffinity.SessionHash).Count(&sessionAffinityCount).Error; err != nil || sessionAffinityCount != 0 {
		t.Fatalf("session affinity count=%d error=%v", sessionAffinityCount, err)
	}
	detail, err := NewManagementService(store).LogDetail(context.Background(), requestLog.ID)
	if err != nil || !strings.Contains(detail.ResponseBody, "response.incomplete") || len(detail.Attempts) != 1 || !strings.Contains(detail.Attempts[0].ResponseBody, "response.incomplete") {
		t.Fatalf("corrected detail = %+v, %v", detail, err)
	}
	if err := store.backfillApplicationOutcomes(); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalOutcomeClassifierSkipsUncertainPayloads(t *testing.T) {
	if failure, _, inspected := classifyHistoricalResponse("responses", true, compressedPayloadPrefix+"invalid", false); inspected || failure != nil {
		t.Fatalf("corrupt payload was inspected: %+v", failure)
	}
	truncated := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	if failure, _, inspected := classifyHistoricalResponse("responses", true, truncated, true); inspected || failure != nil {
		t.Fatalf("truncated payload was reclassified: %+v", failure)
	}
	completed := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"
	if failure, responseID, inspected := classifyHistoricalResponse("responses", true, completed, false); !inspected || failure != nil || responseID != "resp_ok" {
		t.Fatalf("completed payload failure=%+v responseID=%q inspected=%v", failure, responseID, inspected)
	}
	incomplete := `{"id":"resp_non_stream","status":"incomplete","incomplete_details":{"reason":"upstream_interrupted"},"output":[]}`
	if failure, responseID, inspected := classifyHistoricalResponse("responses", false, incomplete, false); !inspected || failure == nil || failure.Code != "upstream_interrupted" || responseID != "resp_non_stream" {
		t.Fatalf("non-stream failure=%+v responseID=%q inspected=%v", failure, responseID, inspected)
	}
}

func TestRelayRecordsDistinctStreamingTimings(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		flusher := writer.(http.Flusher)
		flusher.Flush()
		time.Sleep(20 * time.Millisecond)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_timing\"}}\n\n"))
		flusher.Flush()
		time.Sleep(20 * time.Millisecond)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
		time.Sleep(20 * time.Millisecond)
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
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

	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	var attemptLog RelayAttemptLog
	if err := store.db.First(&attemptLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.LatencyMS <= 0 || requestLog.FirstTokenMS <= 0 || requestLog.DurationMS <= requestLog.FirstTokenMS {
		t.Fatalf("request timings = latency %d, first token %d, duration %d", requestLog.LatencyMS, requestLog.FirstTokenMS, requestLog.DurationMS)
	}
	if attemptLog.LatencyMS <= 0 || attemptLog.FirstTokenMS <= 0 || attemptLog.DurationMS <= attemptLog.FirstTokenMS {
		t.Fatalf("attempt timings = latency %d, first token %d, duration %d", attemptLog.LatencyMS, attemptLog.FirstTokenMS, attemptLog.DurationMS)
	}
	var stat TokenDailyStat
	if err := store.db.First(&stat).Error; err != nil {
		t.Fatal(err)
	}
	if stat.FirstTokenSamples != 1 || stat.LatencySamples != 1 || stat.FirstTokenMS != requestLog.FirstTokenMS || stat.LatencyMS != requestLog.LatencyMS || stat.DurationMS != requestLog.DurationMS {
		t.Fatalf("daily timing stats = %+v", stat)
	}
}

func TestRelayForwardsFirstSSEEventBeforeOutputToken(t *testing.T) {
	store := newTestStore(t)
	releaseOutput := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fast\"}}\n\n"))
		flusher.Flush()
		<-releaseOutput
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fast\",\"status\":\"completed\"}}\n\n"))
	}))
	defer upstream.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","stream":true,"input":"hello"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	writer := &notifyingStreamWriter{header: make(http.Header), firstWrite: make(chan []byte, 1)}
	done := make(chan *PublicError, 1)
	go func() {
		done <- newTestRelay(store).Relay(context.Background(), writer, nil, "", "responses", token, payload, body)
	}()
	select {
	case firstWrite := <-writer.firstWrite:
		if !bytes.Contains(firstWrite, []byte("response.created")) {
			t.Fatalf("first downstream event = %s", firstWrite)
		}
	case <-time.After(time.Second):
		t.Fatal("first SSE event was buffered until output")
	}
	close(releaseOutput)
	if publicErr := <-done; publicErr != nil {
		t.Fatal(publicErr)
	}
	var refreshedToken ClientToken
	if err := store.db.First(&refreshedToken, token.ID).Error; err != nil || refreshedToken.LastUsedAt == nil {
		t.Fatalf("last used token = %+v, error = %v", refreshedToken, err)
	}
}

func TestRelayPersistsProcessingLogBeforeUpstreamCompletes(t *testing.T) {
	store := newTestStore(t)
	upstreamStarted := make(chan struct{})
	releaseUpstream := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(upstreamStarted)
		<-releaseUpstream
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_two_phase","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`))
	}))
	defer upstream.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","input":"hello"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	done := make(chan *PublicError, 1)
	go func() {
		done <- newTestRelay(store).Relay(context.Background(), recorder, nil, "", "responses", token, payload, body)
	}()

	select {
	case <-upstreamStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	var processing RelayRequestLog
	if err := store.db.First(&processing).Error; err != nil {
		t.Fatal(err)
	}
	if processing.Outcome != RelayOutcomeProcessing || processing.StatusCode != 0 || processing.RequestedModel != "public-model" {
		t.Fatalf("processing log = %+v", processing)
	}

	close(releaseUpstream)
	if publicErr := <-done; publicErr != nil {
		t.Fatal(publicErr)
	}
	var completed RelayRequestLog
	if err := store.db.First(&completed, "id = ?", processing.ID).Error; err != nil {
		t.Fatal(err)
	}
	if completed.Outcome != RelayOutcomeSuccess || completed.StatusCode != http.StatusOK || completed.AttemptCount != 1 {
		t.Fatalf("completed log = %+v", completed)
	}
	var count int64
	if err := store.db.Model(&RelayRequestLog{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("request log count = %d, error = %v", count, err)
	}
}

func TestRequestCostSourceBecomesMixedAcrossSuccessfulAttempts(t *testing.T) {
	store := newTestStore(t)
	token := ClientToken{ID: 99, Name: "mixed", KeyPrefix: "sk-mixed"}
	execution := &relayExecution{
		requestID: "mixed-request", token: &token, endpoint: "responses", payload: &RelayPayload{Model: "model-a"}, startedAt: time.Now(),
		usageSources: map[string]struct{}{}, costSources: map[string]struct{}{},
	}
	relay := newTestRelay(store)
	relay.addUsage(execution, Usage{InputTokens: 1, Source: "upstream"}, 10, 11, CostSourceUpstream, true)
	relay.addUsage(execution, Usage{OutputTokens: 1, Source: "estimated_tiktoken"}, 20, 20, CostSourceFallback, true)
	relay.recordRequest(context.Background(), execution, http.StatusOK, "")
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog, "id = ?", execution.requestID).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.EstimatedCost != 30 || requestLog.UpstreamCost != 31 || requestLog.CostSource != CostSourceMixed || requestLog.UsageSource != "mixed" {
		t.Fatalf("mixed request = %+v", requestLog)
	}
}

func TestRelayDoesNotCountSentTokensBeforeNetworkAttempt(t *testing.T) {
	store := newTestStore(t)
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://never-called.invalid")
	if err := store.db.Model(&Channel{}).Where("id = ?", channels[0].ID).Update("api_key_cipher", "invalid-ciphertext").Error; err != nil {
		t.Fatal(err)
	}
	payloadBody := []byte(`{"model":"public-model","input":"hello"}`)
	payload, err := ParseRelayPayload(payloadBody)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	publicErr := newTestRelay(store).Relay(context.Background(), recorder, nil, "", "responses", token, payload, payloadBody)
	if publicErr == nil || publicErr.Status != http.StatusBadGateway {
		t.Fatalf("Relay() error = %+v, want 502", publicErr)
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.SentTokens != 0 {
		t.Fatalf("sent tokens = %d, want 0 before an upstream network call", requestLog.SentTokens)
	}
	var attempts []RelayAttemptLog
	if err := store.db.Order("id ASC").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].SentTokens != 0 || attempts[0].SelectionReason != SelectionReasonInitialRoute || attempts[0].ErrorMessage != "gateway preparation failed: credential_decrypt" {
		t.Fatalf("preparation attempt logs = %+v", attempts)
	}
}

func TestRelayRecordsRetrySelectionReasons(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			store := newTestStore(t)
			first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(status)
				_, _ = writer.Write([]byte(`{"error":{"code":"retry"}}`))
			}))
			defer first.Close()
			second := successfulResponseServer()
			defer second.Close()
			token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)

			relayTestRequest(t, newTestRelay(store), token)
			attempts := relayAttempts(t, store)
			if len(attempts) != 2 || attempts[1].SelectionReason != SelectionReasonRetryableStatus || attempts[1].SelectionDetail != fmt.Sprintf("HTTP %d", status) || attempts[1].PreviousChannelID != attempts[0].ChannelID || attempts[1].PreviousChannelName != attempts[0].ChannelName {
				t.Fatalf("attempts = %+v", attempts)
			}
		})
	}

	t.Run("transport error", func(t *testing.T) {
		store := newTestStore(t)
		second := successfulResponseServer()
		defer second.Close()
		token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://network.invalid", second.URL)
		relay := newTestRelay(store)
		transportCalls := 0
		relay.client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			transportCalls++
			if transportCalls == 1 {
				return nil, errors.New("network unavailable")
			}
			return http.DefaultTransport.RoundTrip(request)
		})

		relayTestRequest(t, relay, token)
		attempts := relayAttempts(t, store)
		if len(attempts) != 2 || attempts[1].SelectionReason != SelectionReasonTransportError || attempts[1].SelectionDetail != "upstream_request" || attempts[1].PreviousChannelID != attempts[0].ChannelID {
			t.Fatalf("attempts = %+v", attempts)
		}
	})

	t.Run("response read error", func(t *testing.T) {
		store := newTestStore(t)
		second := successfulResponseServer()
		defer second.Close()
		token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://response.invalid", second.URL)
		relay := newTestRelay(store)
		transportCalls := 0
		relay.client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			transportCalls++
			if transportCalls == 1 {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       errorReadCloser{},
					Request:    request,
				}, nil
			}
			return http.DefaultTransport.RoundTrip(request)
		})

		relayTestRequest(t, relay, token)
		attempts := relayAttempts(t, store)
		if len(attempts) != 2 || attempts[1].SelectionReason != SelectionReasonResponseError || attempts[1].SelectionDetail != "response_body_read" || attempts[1].PreviousChannelID != attempts[0].ChannelID {
			t.Fatalf("attempts = %+v", attempts)
		}
	})

	t.Run("gateway preparation error", func(t *testing.T) {
		store := newTestStore(t)
		second := successfulResponseServer()
		defer second.Close()
		token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://never-called.invalid", second.URL)
		if err := store.db.Model(&Channel{}).Where("id = ?", channels[0].ID).Update("api_key_cipher", "invalid-ciphertext").Error; err != nil {
			t.Fatal(err)
		}

		relayTestRequest(t, newTestRelay(store), token)
		attempts := relayAttempts(t, store)
		if len(attempts) != 2 || attempts[0].SentTokens != 0 || attempts[1].SelectionReason != SelectionReasonGatewayPreparationError || attempts[1].SelectionDetail != "credential_decrypt" || attempts[1].PreviousChannelID != attempts[0].ChannelID {
			t.Fatalf("attempts = %+v", attempts)
		}
	})

	t.Run("new circuit", func(t *testing.T) {
		store := newTestStore(t)
		first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(`{"error":{"code":"retry"}}`))
		}))
		defer first.Close()
		second := successfulResponseServer()
		defer second.Close()
		token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
		if err := store.db.Model(&Channel{}).Where("id = ?", channels[0].ID).Update("consecutive_failures", 2).Error; err != nil {
			t.Fatal(err)
		}

		relayTestRequest(t, newTestRelay(store), token)
		attempts := relayAttempts(t, store)
		if len(attempts) != 2 || attempts[1].SelectionReason != SelectionReasonCircuitOpened || attempts[1].SelectionDetail == "" || attempts[1].PreviousChannelID != attempts[0].ChannelID {
			t.Fatalf("attempts = %+v", attempts)
		}
	})
}

func successfulResponseServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_retry","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":5,"output_tokens":1}}`))
	}))
}

func relayTestRequest(t *testing.T, relay *RelayService, token *ClientToken) {
	t.Helper()
	body := []byte(`{"model":"public-model","input":"hello"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	if publicErr := relay.Relay(context.Background(), httptest.NewRecorder(), nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
}

func relayAttempts(t *testing.T, store *Store) []RelayAttemptLog {
	t.Helper()
	var attempts []RelayAttemptLog
	if err := store.db.Order("id ASC").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	return attempts
}

func TestRelayMovesCodexSessionAffinityAfterRetryableFailure(t *testing.T) {
	store := newTestStore(t)
	var firstCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":{"message":"temporary","type":"api_error","code":"temporary"}}`))
	}))
	defer first.Close()
	var secondCalls atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_session","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":4}}}`))
	}))
	defer second.Close()
	token, model, _, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	relay := newTestRelay(store)

	relayRequest := func(sessionKey string) {
		body := []byte(fmt.Sprintf(`{"model":"public-model","prompt_cache_key":%q,"input":"hello"}`, sessionKey))
		payload, err := ParseRelayPayload(body)
		if err != nil {
			t.Fatal(err)
		}
		if publicErr := relay.Relay(context.Background(), httptest.NewRecorder(), http.Header{}, "", "responses", token, payload, body); publicErr != nil {
			t.Fatal(publicErr)
		}
	}

	relayRequest("codex-session-a")
	relayRequest("codex-session-a")
	if firstCalls.Load() != 1 || secondCalls.Load() != 2 {
		t.Fatalf("same-session calls = %d/%d, want 1/2", firstCalls.Load(), secondCalls.Load())
	}
	var affinity SessionAffinity
	if err := store.db.Where("token_id = ? AND model_id = ? AND session_hash = ?", token.ID, model.ID, hashSecret("codex-session-a")).First(&affinity).Error; err != nil {
		t.Fatal(err)
	}
	if affinity.ChannelModelID != mappings[1].ID {
		t.Fatalf("affinity mapping = %d, want %d", affinity.ChannelModelID, mappings[1].ID)
	}

	relayRequest("codex-session-b")
	if firstCalls.Load() != 2 || secondCalls.Load() != 3 {
		t.Fatalf("different-session calls = %d/%d, want 2/3", firstCalls.Load(), secondCalls.Load())
	}
}

func TestRelayUsesStableCopilotSessionForRoutingAffinity(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get(copilotIntegrationHeader) != "" {
			t.Error("internal Copilot integration header was forwarded upstream")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chatcmpl-copilot","choices":[{"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	defer upstream.Close()
	token, model, _, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	relay := newTestRelay(store)
	headers := http.Header{copilotIntegrationHeader: []string{copilotIntegrationID}, "User-Agent": []string{"OpenAI/JS 5.20.1"}}
	firstBody := []byte(`{"model":"public-model","messages":[{"role":"system","content":"<copilot_tauri_workspace>\nproject_session_id: abda4fa3-2495-49d6-a71c-5d147c5148db\n</copilot_tauri_workspace>"},{"role":"user","content":"你好"}]}`)
	secondBody := []byte(`{"model":"public-model","messages":[{"role":"system","content":"<copilot_tauri_workspace>\nproject_session_id: abda4fa3-2495-49d6-a71c-5d147c5148db\n</copilot_tauri_workspace>"},{"role":"user","content":"压缩后的上下文"}]}`)

	for _, body := range [][]byte{firstBody, secondBody} {
		payload, err := ParseRelayPayload(body)
		if err != nil {
			t.Fatal(err)
		}
		if publicErr := relay.Relay(context.Background(), httptest.NewRecorder(), headers, "", "chat", token, payload, body); publicErr != nil {
			t.Fatal(publicErr)
		}
		if payload.SessionKey != "abda4fa3-2495-49d6-a71c-5d147c5148db" || payload.SessionKey != payload.LogSessionKey || payload.SessionSource != copilotChatProjectSource {
			t.Fatalf("inferred routing identity = %+v", payload)
		}
	}

	attempts := relayAttempts(t, store)
	if len(attempts) != 2 || attempts[0].SelectionReason != SelectionReasonInitialRoute || attempts[1].SelectionReason != SelectionReasonSessionAffinity {
		t.Fatalf("attempt selections = %+v", attempts)
	}
	var logs []RelayRequestLog
	if err := store.db.Order("created_at ASC").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].CodexSessionID == "" || logs[1].CodexSessionID != logs[0].CodexSessionID {
		t.Fatalf("Copilot request logs = %+v", logs)
	}
	var affinity SessionAffinity
	if err := store.db.Where("token_id = ? AND model_id = ? AND session_hash = ?", token.ID, model.ID, hashSecret(logs[0].CodexSessionID)).First(&affinity).Error; err != nil {
		t.Fatal(err)
	}
	if affinity.ChannelModelID != mappings[0].ID {
		t.Fatalf("affinity mapping = %d, want %d", affinity.ChannelModelID, mappings[0].ID)
	}
}

func TestRelayKeepsCodexSessionAffinityUntilOriginalChannelIsUnavailable(t *testing.T) {
	store := newTestStore(t)
	var firstCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		call := firstCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		if call == 1 {
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(`{"error":{"message":"temporary","type":"api_error","code":"temporary"}}`))
			return
		}
		_, _ = writer.Write([]byte(`{"id":"resp_original","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"original"}]}],"usage":{"input_tokens":4,"output_tokens":1}}`))
	}))
	defer first.Close()
	var secondCalls atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_fallback","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fallback"}]}],"usage":{"input_tokens":4,"output_tokens":1}}`))
	}))
	defer second.Close()
	token, model, _, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	router := NewRouter(store, NewClientAccessService(store), nil)
	router.RecordSessionAffinity(context.Background(), token.ID, model.ID, "codex-session-sticky", mappings[0].ID)
	relay := NewRelayService(store, router, NewTokenEstimator(), &config.ApplicationConfigManager{})

	for range 2 {
		body := []byte(`{"model":"public-model","prompt_cache_key":"codex-session-sticky","input":"hello"}`)
		payload, err := ParseRelayPayload(body)
		if err != nil {
			t.Fatal(err)
		}
		if publicErr := relay.Relay(context.Background(), httptest.NewRecorder(), http.Header{}, "", "responses", token, payload, body); publicErr != nil {
			t.Fatal(publicErr)
		}
	}
	if firstCalls.Load() != 2 || secondCalls.Load() != 1 {
		t.Fatalf("calls = %d/%d, want 2/1", firstCalls.Load(), secondCalls.Load())
	}
	var affinity SessionAffinity
	if err := store.db.Where("token_id = ? AND model_id = ? AND session_hash = ?", token.ID, model.ID, hashSecret("codex-session-sticky")).First(&affinity).Error; err != nil {
		t.Fatal(err)
	}
	if affinity.ChannelModelID != mappings[0].ID {
		t.Fatalf("affinity mapping = %d, want original %d", affinity.ChannelModelID, mappings[0].ID)
	}
}

func TestRelayStopsAfterClientCancellationWithoutPenalizingChannels(t *testing.T) {
	store := newTestStore(t)
	started := make(chan struct{})
	releaseUpstream := make(chan struct{})
	first := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		<-releaseUpstream
	}))
	defer first.Close()
	defer close(releaseUpstream)
	var secondCalls atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer second.Close()
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	body := []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *PublicError, 1)
	go func() {
		done <- newTestRelay(store).Relay(ctx, httptest.NewRecorder(), http.Header{}, "", "chat", token, payload, body)
	}()
	<-started
	cancel()
	publicErr := <-done
	if publicErr == nil || publicErr.Status != statusClientClosedRequest || publicErr.Code != "request_canceled" {
		t.Fatalf("Relay() error = %+v", publicErr)
	}
	if secondCalls.Load() != 0 {
		t.Fatalf("second channel calls = %d, want 0", secondCalls.Load())
	}
	for _, fixture := range channels {
		var channel Channel
		if err := store.db.First(&channel, fixture.ID).Error; err != nil {
			t.Fatal(err)
		}
		if channel.ConsecutiveFailures != 0 {
			t.Fatalf("channel %d failures = %d", channel.ID, channel.ConsecutiveFailures)
		}
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.StatusCode != statusClientClosedRequest || requestLog.AttemptCount != 1 || requestLog.ErrorCode != "request_canceled" {
		t.Fatalf("request log = %+v", requestLog)
	}
}

func TestSSEClientDisconnectDoesNotOpenCircuit(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"id\":\"chatcmpl_disconnect\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		writer.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	writer := &disconnectedStreamWriter{header: make(http.Header)}
	if publicErr := newTestRelay(store).Relay(context.Background(), writer, http.Header{}, "", "chat", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	var channel Channel
	if err := store.db.First(&channel, channels[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ConsecutiveFailures != 0 || channel.CircuitOpenUntil != nil {
		t.Fatalf("channel health = %+v", channel)
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.StatusCode != statusClientClosedRequest || requestLog.ErrorCode != "request_canceled" {
		t.Fatalf("request log = %+v", requestLog)
	}
	if requestLog.Outcome != RelayOutcomeCanceled || requestLog.CostSource == CostSourceFailedZero {
		t.Fatalf("canceled request outcome/cost = %+v", requestLog)
	}
	var attemptLog RelayAttemptLog
	if err := store.db.First(&attemptLog).Error; err != nil {
		t.Fatal(err)
	}
	if attemptLog.Outcome != RelayOutcomeCanceled || attemptLog.Success || attemptLog.CostSource == CostSourceFailedZero {
		t.Fatalf("canceled attempt = %+v", attemptLog)
	}
}

func TestSSEResponseCompletedRemainsSuccessfulWhenUpstreamKeepsConnectionOpen(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_completed\",\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"))
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer upstream.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","stream":true,"input":"hello"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := newTestRelay(store).Relay(context.Background(), recorder, http.Header{}, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	if !strings.Contains(recorder.Body.String(), "response.completed") {
		t.Fatalf("response body = %s", recorder.Body.String())
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.StatusCode != http.StatusOK || requestLog.Outcome != RelayOutcomeSuccess || requestLog.ErrorCode != "" {
		t.Fatalf("completed request = %+v", requestLog)
	}
	var attemptLog RelayAttemptLog
	if err := store.db.First(&attemptLog).Error; err != nil {
		t.Fatal(err)
	}
	if !attemptLog.Success || attemptLog.Outcome != RelayOutcomeSuccess {
		t.Fatalf("completed attempt = %+v", attemptLog)
	}
}

func TestSSEEOFBeforeTerminalEventIsInterrupted(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
	}))
	defer upstream.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","stream":true,"input":"hello"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	if publicErr := newTestRelay(store).Relay(context.Background(), httptest.NewRecorder(), http.Header{}, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.StatusCode != upstreamApplicationErrorStatus || requestLog.Outcome != RelayOutcomeFailed || requestLog.ErrorCode != "stream_interrupted" {
		t.Fatalf("interrupted request = %+v", requestLog)
	}
}

func TestRelayRetriesBeforeFirstSSEEvent(t *testing.T) {
	store := newTestStore(t)
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
	}))
	defer first.Close()
	var secondCalls atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("data: {\"id\":\"chatcmpl_2\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = writer.Write([]byte("data: {\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer second.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	body := []byte(`{"model":"public-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := newTestRelay(store).Relay(context.Background(), recorder, http.Header{}, "", "chat", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	if secondCalls.Load() != 1 || !strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("second calls=%d body=%s", secondCalls.Load(), recorder.Body.String())
	}
	var attempts int64
	if err := store.db.Model(&RelayAttemptLog{}).Count(&attempts).Error; err != nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestRelayRetriesHTTP200ApplicationError(t *testing.T) {
	store := newTestStore(t)
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"error":{"code":"model_at_capacity","message":"Selected model is at capacity. Please try a different model."}}`))
	}))
	defer first.Close()
	second := successfulResponseServer()
	defer second.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	body := []byte(`{"model":"public-model","input":"capacity retry"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := newTestRelay(store).Relay(context.Background(), recorder, nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "at capacity") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	attempts := relayAttempts(t, store)
	if len(attempts) != 2 || attempts[0].Success || attempts[0].StatusCode != http.StatusOK || attempts[1].SelectionReason != SelectionReasonUpstreamApplicationError || !attempts[1].Success {
		t.Fatalf("attempts = %+v", attempts)
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.StatusCode != http.StatusOK || requestLog.AttemptCount != 2 || !strings.Contains(requestLog.ResponseBody, `"status":"completed"`) {
		t.Fatalf("request log = %+v", requestLog)
	}
}

func TestRelayCircuitsBalanceFailureAndSwitchesChannel(t *testing.T) {
	store := newTestStore(t)
	var firstCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"error":{"message":"预扣费额度失败, 用户剩余额度: ¥0.033256, 需要预扣费额度: ¥0.042656"}}`))
	}))
	defer first.Close()
	var secondCalls atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_switched","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"switched"}]}],"usage":{"input_tokens":5,"output_tokens":1}}`))
	}))
	defer second.Close()
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	body := []byte(`{"model":"public-model","input":"switch depleted channel"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := newTestRelay(store).Relay(context.Background(), recorder, nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 || recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "resp_switched") {
		t.Fatalf("first calls=%d second calls=%d status=%d body=%s", firstCalls.Load(), secondCalls.Load(), recorder.Code, recorder.Body.String())
	}
	attempts := relayAttempts(t, store)
	if len(attempts) != 2 || attempts[0].Success || !strings.Contains(attempts[0].ErrorMessage, "预扣费额度失败") || attempts[1].SelectionReason != SelectionReasonCircuitOpened || !attempts[1].Success {
		t.Fatalf("attempts = %+v", attempts)
	}
	var failedChannel Channel
	if err := store.db.First(&failedChannel, channels[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if failedChannel.ConsecutiveFailures != 0 || failedChannel.CircuitLevel != CircuitLevelTemporary || failedChannel.CircuitOpenUntil == nil || !failedChannel.CircuitOpenUntil.After(time.Now()) || !strings.Contains(failedChannel.LastError, "预扣费额度失败") {
		t.Fatalf("failed channel = %+v", failedChannel)
	}
}

func TestRelayDoesNotSwitchForOrdinaryForbiddenResponse(t *testing.T) {
	store := newTestStore(t)
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"error":{"code":"model_not_allowed","message":"This model is not permitted."}}`))
	}))
	defer first.Close()
	var secondCalls atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer second.Close()
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	body := []byte(`{"model":"public-model","input":"forbidden"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := newTestRelay(store).Relay(context.Background(), recorder, nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	if recorder.Code != http.StatusForbidden || secondCalls.Load() != 0 || !strings.Contains(recorder.Body.String(), "model_not_allowed") {
		t.Fatalf("status=%d second calls=%d body=%s", recorder.Code, secondCalls.Load(), recorder.Body.String())
	}
	var channel Channel
	if err := store.db.First(&channel, channels[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ConsecutiveFailures != 0 || channel.CircuitOpenUntil != nil {
		t.Fatalf("ordinary forbidden channel = %+v", channel)
	}
	if attempts := relayAttempts(t, store); len(attempts) != 1 || attempts[0].StatusCode != http.StatusForbidden {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestRelayCircuitsAuthenticationFailureAndSwitchesChannel(t *testing.T) {
	store := newTestStore(t)
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"Invalid authentication credentials"}}`))
	}))
	defer first.Close()
	second := successfulResponseServer()
	defer second.Close()
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	relayTestRequest(t, newTestRelay(store), token)
	var channel Channel
	if err := store.db.First(&channel, channels[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ConsecutiveFailures != 0 || channel.CircuitLevel != CircuitLevelTemporary || channel.CircuitOpenUntil == nil || !channel.CircuitOpenUntil.After(time.Now()) {
		t.Fatalf("authentication failure channel = %+v", channel)
	}
	if attempts := relayAttempts(t, store); len(attempts) != 2 || attempts[0].StatusCode != http.StatusUnauthorized || !attempts[1].Success {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestResponseAffinityBalanceFailureCircuitsWithoutCrossChannelRetry(t *testing.T) {
	store := newTestStore(t)
	var firstCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"error":{"message":"insufficient balance"}}`))
	}))
	defer first.Close()
	var secondCalls atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer second.Close()
	token, _, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	relay := newTestRelay(store)
	relay.router.RecordAffinity(context.Background(), "resp_original", mappings[0].ID)
	body := []byte(`{"model":"public-model","previous_response_id":"resp_original","input":"continue"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	publicErr := relay.Relay(context.Background(), httptest.NewRecorder(), nil, "", "responses", token, payload, body)
	if publicErr == nil || publicErr.Code != "response_affinity_unavailable" || firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("error=%+v first calls=%d second calls=%d", publicErr, firstCalls.Load(), secondCalls.Load())
	}
	var channel Channel
	if err := store.db.First(&channel, channels[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.CircuitOpenUntil == nil || !channel.CircuitOpenUntil.After(time.Now()) {
		t.Fatalf("affinity channel = %+v", channel)
	}
}

func TestRelayRetriesFirstSSEApplicationErrorWithoutLeakingEvents(t *testing.T) {
	store := newTestStore(t)
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"error\",\"error\":{\"code\":\"model_at_capacity\",\"message\":\"Selected model is at capacity. Please try a different model.\"}}\n\n"))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"switched\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_switched\",\"status\":\"completed\"}}\n\n"))
	}))
	defer second.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	body := []byte(`{"model":"public-model","stream":true,"input":"capacity retry"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := newTestRelay(store).Relay(context.Background(), recorder, nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	if output := recorder.Body.String(); !strings.Contains(output, "switched") || strings.Contains(output, "resp_failed") || strings.Contains(output, "at capacity") {
		t.Fatalf("downstream stream = %s", output)
	}
	attempts := relayAttempts(t, store)
	if len(attempts) != 2 || attempts[0].Success || !strings.Contains(attempts[0].ResponseBody, "at capacity") || attempts[1].SelectionReason != SelectionReasonUpstreamApplicationError || !attempts[1].Success {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestRelayDoesNotRetryApplicationErrorAfterFirstToken(t *testing.T) {
	store := newTestStore(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"model_at_capacity\",\"message\":\"Selected model is at capacity.\"}}}\n\n"))
	}))
	defer upstream.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL, upstream.URL)
	body := []byte(`{"model":"public-model","stream":true,"input":"do not duplicate"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := newTestRelay(store).Relay(context.Background(), recorder, nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	if calls.Load() != 1 || !strings.Contains(recorder.Body.String(), "partial") {
		t.Fatalf("calls=%d body=%s", calls.Load(), recorder.Body.String())
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.StatusCode != http.StatusBadGateway || requestLog.ErrorCode != "model_at_capacity" || requestLog.AttemptCount != 1 {
		t.Fatalf("request log = %+v", requestLog)
	}
	attempts := relayAttempts(t, store)
	if len(attempts) != 1 || attempts[0].Success || attempts[0].FirstTokenMS == 0 {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestRelayRetriesPretokenIncompleteResponseWithoutMarkingItSuccessful(t *testing.T) {
	store := newTestStore(t)
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_interrupted\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"upstream_interrupted\"},\"output\":[]}}\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"recovered\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_recovered\",\"status\":\"completed\"}}\n\n"))
	}))
	defer second.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	body := []byte(`{"model":"public-model","stream":true,"input":"retry interrupted response"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := newTestRelay(store).Relay(context.Background(), recorder, nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	if output := recorder.Body.String(); !strings.Contains(output, "recovered") || strings.Contains(output, "resp_interrupted") {
		t.Fatalf("downstream stream = %s", output)
	}
	attempts := relayAttempts(t, store)
	if len(attempts) != 2 || attempts[0].Success || attempts[0].Outcome != RelayOutcomeFailed || attempts[1].SelectionReason != SelectionReasonUpstreamApplicationError || attempts[1].SelectionDetail != "upstream response incomplete: upstream_interrupted" || !attempts[1].Success {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestRelayMarksIncompleteResponseAfterFirstTokenAsFailed(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"upstream_interrupted\"}}}\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","stream":true,"input":"partial response"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := newTestRelay(store).Relay(context.Background(), recorder, nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	if output := recorder.Body.String(); !strings.Contains(output, "partial") || !strings.Contains(output, "response.incomplete") {
		t.Fatalf("downstream stream = %s", output)
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.StatusCode != http.StatusBadGateway || requestLog.Outcome != RelayOutcomeFailed || requestLog.ErrorCode != "upstream_interrupted" {
		t.Fatalf("request log = %+v", requestLog)
	}
	attempts := relayAttempts(t, store)
	if len(attempts) != 1 || attempts[0].Success || attempts[0].Outcome != RelayOutcomeFailed || attempts[0].ErrorMessage != "upstream application error (upstream_interrupted): upstream response incomplete: upstream_interrupted" {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestRelayRetriesTopLevelNonStreamingIncompleteResponse(t *testing.T) {
	store := newTestStore(t)
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_incomplete","object":"response","status":"incomplete","incomplete_details":{"reason":"upstream_interrupted"},"output":[]}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_completed","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`))
	}))
	defer second.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	body := []byte(`{"model":"public-model","input":"retry non-stream incomplete"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := newTestRelay(store).Relay(context.Background(), recorder, nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	if output := recorder.Body.String(); !strings.Contains(output, `"status":"completed"`) || strings.Contains(output, "resp_incomplete") {
		t.Fatalf("downstream body = %s", output)
	}
	attempts := relayAttempts(t, store)
	if len(attempts) != 2 || attempts[0].Success || attempts[1].SelectionReason != SelectionReasonUpstreamApplicationError || attempts[1].SelectionDetail != "upstream response incomplete: upstream_interrupted" || !attempts[1].Success {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestRelayDoesNotRetryOrPenalizeNonTransientIncompleteResponse(t *testing.T) {
	store := newTestStore(t)
	firstCalls := atomic.Int32{}
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp_limited","object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`))
	}))
	defer first.Close()
	secondCalls := atomic.Int32{}
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer second.Close()
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, first.URL, second.URL)
	body := []byte(`{"model":"public-model","input":"bounded output"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := newTestRelay(store).Relay(context.Background(), recorder, nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 0 || !strings.Contains(recorder.Body.String(), "max_output_tokens") {
		t.Fatalf("calls=%d/%d body=%s", firstCalls.Load(), secondCalls.Load(), recorder.Body.String())
	}
	var channel Channel
	if err := store.db.First(&channel, channels[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ConsecutiveFailures != 0 || channel.CircuitOpenUntil != nil {
		t.Fatalf("channel health = %+v", channel)
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.Outcome != RelayOutcomeFailed || requestLog.ErrorCode != "max_output_tokens" || requestLog.AttemptCount != 1 {
		t.Fatalf("request log = %+v", requestLog)
	}
}

func TestResponsesStreamDoneWithoutCompletedIsNotSuccessful(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_unfinished\",\"status\":\"in_progress\"}}\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","stream":true,"input":"unfinished"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	if publicErr := newTestRelay(store).Relay(context.Background(), httptest.NewRecorder(), nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatalf("Relay() error = %+v", publicErr)
	}
	attempts := relayAttempts(t, store)
	if len(attempts) != 1 || attempts[0].Success || attempts[0].Outcome != RelayOutcomeFailed {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestChatStreamFinishReasonIsSuccessfulWithoutDoneSentinel(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"id\":\"chatcmpl_finished\",\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n"))
	}))
	defer upstream.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	if publicErr := newTestRelay(store).Relay(context.Background(), httptest.NewRecorder(), nil, "", "chat", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.Outcome != RelayOutcomeSuccess || requestLog.StatusCode != http.StatusOK {
		t.Fatalf("request log = %+v", requestLog)
	}
}

func TestTerminalEventDeliveryFailureRemainsCanceled(t *testing.T) {
	store := newTestStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_completed\",\"status\":\"completed\"}}\n\n"))
	}))
	defer upstream.Close()
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL)
	body := []byte(`{"model":"public-model","stream":true,"input":"delivery"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	writer := &failAfterWritesStreamWriter{header: make(http.Header), failAt: 2}
	if publicErr := newTestRelay(store).Relay(context.Background(), writer, nil, "", "responses", token, payload, body); publicErr != nil {
		t.Fatal(publicErr)
	}
	var requestLog RelayRequestLog
	if err := store.db.First(&requestLog).Error; err != nil {
		t.Fatal(err)
	}
	if requestLog.Outcome != RelayOutcomeCanceled || requestLog.ErrorCode != "request_canceled" || requestLog.StatusCode != statusClientClosedRequest {
		t.Fatalf("request log = %+v", requestLog)
	}
	var channel Channel
	if err := store.db.First(&channel, channels[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if channel.ConsecutiveFailures != 0 || channel.CircuitOpenUntil != nil {
		t.Fatalf("channel health = %+v", channel)
	}
}

func TestRelayApplicationErrorAttemptsAreHardCappedAtThree(t *testing.T) {
	store := newTestStore(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"error":{"message":"Selected model is at capacity. Please try a different model."}}`))
	}))
	defer upstream.Close()
	token, _, _, _ := createRouteFixture(t, store, RoutingPriorityWeighted, upstream.URL, upstream.URL, upstream.URL, upstream.URL)
	body := []byte(`{"model":"public-model","input":"bounded retry"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	publicErr := newTestRelay(store).Relay(context.Background(), httptest.NewRecorder(), nil, "", "responses", token, payload, body)
	if publicErr == nil || publicErr.Status != http.StatusBadGateway || calls.Load() != 3 {
		t.Fatalf("error=%+v calls=%d", publicErr, calls.Load())
	}
	if attempts := relayAttempts(t, store); len(attempts) != 3 {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestAdminSessionIsHashedAndPasswordChangeRevokesIt(t *testing.T) {
	store := newTestStore(t)
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("initial-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user := AdminUser{Username: "admin", PasswordHash: string(passwordHash), Enabled: true}
	if err := store.db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	auth := NewAdminAuthService(store, &config.ApplicationConfigManager{})
	rawToken, _, err := auth.Login("admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	var session AdminSession
	if err := store.db.First(&session).Error; err != nil {
		t.Fatal(err)
	}
	if session.TokenHash == rawToken || session.TokenHash != hashSecret(rawToken) {
		t.Fatal("administrator session was not stored as a hash")
	}
	if _, err := auth.Authenticate(rawToken); err != nil {
		t.Fatal(err)
	}
	if err := auth.ChangePassword(user.ID, "initial-password", "replacement-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(rawToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Authenticate() error = %v, want invalid session", err)
	}
}
