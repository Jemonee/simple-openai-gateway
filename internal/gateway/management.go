package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxUpstreamModelsResponseBytes = 4 << 20
	channelLatencyPointLimit       = 48
)

type ChannelInput struct {
	Name                       string `json:"name"`
	BaseURL                    string `json:"baseUrl"`
	APIKey                     string `json:"apiKey"`
	Enabled                    bool   `json:"enabled"`
	SupportsStreamUsage        bool   `json:"supportsStreamUsage"`
	PriceMultiplierBasisPoints *int64 `json:"priceMultiplierBasisPoints"`
}

type ChannelConfigurationInput struct {
	ID      uint64              `json:"id"`
	Channel ChannelInput        `json:"channel"`
	Models  []ChannelModelInput `json:"models"`
}

type ChannelModelDiscoveryInput struct {
	ChannelID uint64 `json:"channelId"`
	BaseURL   string `json:"baseUrl"`
	APIKey    string `json:"apiKey"`
}

type UpstreamModelView struct {
	ID                 string              `json:"id"`
	OwnedBy            string              `json:"ownedBy"`
	Created            int64               `json:"created"`
	PublicModelID      uint64              `json:"publicModelId"`
	PublicModelCreated bool                `json:"publicModelCreated"`
	OfficialPrice      *OfficialModelPrice `json:"officialPrice"`
}

type ChannelModelDiscovery struct {
	Models    []UpstreamModelView `json:"models"`
	LatencyMS int64               `json:"latencyMs"`
	Status    int                 `json:"status"`
	FetchedAt time.Time           `json:"fetchedAt"`
}

type ChannelView struct {
	Channel
	APIKeyConfigured bool           `json:"apiKeyConfigured"`
	Models           []ChannelModel `json:"models"`
	Metrics          ChannelMetrics `json:"metrics"`
}

type GatewayModelView struct {
	GatewayModel
	RequestCount int64 `json:"requestCount"`
}

type ChannelLatencyPoint struct {
	RecordedAt time.Time `json:"recordedAt"`
	LatencyMS  int64     `json:"latencyMs"`
}

type ChannelMetrics struct {
	LatencySeries         []ChannelLatencyPoint `json:"latencySeries"`
	LatestLatencyMS       int64                 `json:"latestLatencyMs"`
	AverageFirstTokenMS   float64               `json:"averageFirstTokenMs"`
	FirstTokenSampleCount int64                 `json:"firstTokenSampleCount"`
	AverageLatencyMS      float64               `json:"averageLatencyMs"`
	LatencySampleCount    int64                 `json:"latencySampleCount"`
	AverageDurationMS     float64               `json:"averageDurationMs"`
	DurationSampleCount   int64                 `json:"durationSampleCount"`
	InputTokens           int64                 `json:"inputTokens"`
	CachedTokens          int64                 `json:"cachedTokens"`
	CacheHitRate          float64               `json:"cacheHitRate"`
	RecentSuccessRate     float64               `json:"recentSuccessRate"`
	RecentSuccessCount    int64                 `json:"recentSuccessCount"`
	RecentAttemptCount    int64                 `json:"recentAttemptCount"`
}

type GatewayModelInput struct {
	Name            string `json:"name"`
	RoutingStrategy string `json:"routingStrategy"`
	Enabled         bool   `json:"enabled"`
}

type ChannelModelInput struct {
	ModelID                    uint64 `json:"modelId"`
	UpstreamModel              string `json:"upstreamModel"`
	Priority                   int    `json:"priority"`
	Weight                     int    `json:"weight"`
	InputPriceMicros           int64  `json:"inputPriceMicros"`
	OutputPriceMicros          int64  `json:"outputPriceMicros"`
	CachedInputPriceMicros     *int64 `json:"cachedInputPriceMicros"`
	CacheWritePriceMicros      *int64 `json:"cacheWritePriceMicros"`
	PriceMultiplierBasisPoints *int64 `json:"priceMultiplierBasisPoints"`
	Enabled                    bool   `json:"enabled"`
}

type ClientTokenInput struct {
	Name           string   `json:"name"`
	Enabled        bool     `json:"enabled"`
	AllowAllModels bool     `json:"allowAllModels"`
	RPM            int      `json:"rpm"`
	MaxConcurrency int      `json:"maxConcurrency"`
	ModelIDs       []uint64 `json:"modelIds"`
}

type ClientTokenView struct {
	ClientToken
	ModelIDs   []uint64        `json:"modelIds"`
	Statistics TokenStatistics `json:"statistics"`
}

type TokenStatistics struct {
	Requests              int64   `json:"requests"`
	Successes             int64   `json:"successes"`
	InputTokens           int64   `json:"inputTokens"`
	NormalInputTokens     int64   `json:"normalInputTokens"`
	OutputTokens          int64   `json:"outputTokens"`
	CachedTokens          int64   `json:"cachedTokens"`
	CacheWriteTokens      int64   `json:"cacheWriteTokens"`
	SentTokens            int64   `json:"sentTokens"`
	EstimatedCost         int64   `json:"estimatedCostMicros"`
	UpstreamCost          int64   `json:"upstreamCostMicros"`
	AverageFirstTokenMS   float64 `json:"averageFirstTokenMs"`
	FirstTokenSampleCount int64   `json:"firstTokenSampleCount"`
	AverageLatency        float64 `json:"averageLatencyMs"`
	LatencySampleCount    int64   `json:"latencySampleCount"`
	AverageDurationMS     float64 `json:"averageDurationMs"`
	DurationSampleCount   int64   `json:"durationSampleCount"`
	Attempts              int64   `json:"attempts"`
}

type IssuedClientToken struct {
	Token  ClientTokenView `json:"token"`
	Secret string          `json:"secret"`
}

type DashboardSummary struct {
	Requests              int64                `json:"requests"`
	CanceledCount         int64                `json:"canceledCount"`
	SuccessRate           float64              `json:"successRate"`
	InputTokens           int64                `json:"inputTokens"`
	NormalInputTokens     int64                `json:"normalInputTokens"`
	OutputTokens          int64                `json:"outputTokens"`
	CachedTokens          int64                `json:"cachedTokens"`
	CacheWriteTokens      int64                `json:"cacheWriteTokens"`
	CacheHitRate          float64              `json:"cacheHitRate"`
	EstimatedCost         int64                `json:"estimatedCostMicros"`
	UpstreamCost          int64                `json:"upstreamCostMicros"`
	OfficialCost          int64                `json:"officialCostMicros"`
	EstimatedCostRatio    float64              `json:"estimatedCostRatio"`
	UpstreamCostRatio     float64              `json:"upstreamCostRatio"`
	AverageFirstTokenMS   float64              `json:"averageFirstTokenMs"`
	FirstTokenSampleCount int64                `json:"firstTokenSampleCount"`
	AverageLatency        float64              `json:"averageLatencyMs"`
	LatencySampleCount    int64                `json:"latencySampleCount"`
	AverageDurationMS     float64              `json:"averageDurationMs"`
	DurationSampleCount   int64                `json:"durationSampleCount"`
	Daily                 []DashboardDaily     `json:"daily"`
	Hourly                []DashboardHourly    `json:"hourly"`
	CostRatios            []DashboardCostRatio `json:"costRatios"`
	Channels              []DashboardBreakdown `json:"channels"`
	Models                []DashboardBreakdown `json:"models"`
}

type DashboardDaily struct {
	Date                  string  `json:"date"`
	Requests              int64   `json:"requests"`
	Successes             int64   `json:"successes"`
	CanceledCount         int64   `json:"canceledCount"`
	InputTokens           int64   `json:"inputTokens"`
	OutputTokens          int64   `json:"outputTokens"`
	EstimatedCost         int64   `json:"estimatedCostMicros"`
	UpstreamCost          int64   `json:"upstreamCostMicros"`
	AverageFirstTokenMS   float64 `json:"averageFirstTokenMs"`
	FirstTokenSampleCount int64   `json:"firstTokenSampleCount"`
	AverageLatencyMS      float64 `json:"averageLatencyMs"`
	LatencySampleCount    int64   `json:"latencySampleCount"`
	AverageDurationMS     float64 `json:"averageDurationMs"`
	DurationSampleCount   int64   `json:"durationSampleCount"`
}

type DashboardHourly struct {
	Hour      string `json:"hour"`
	Requests  int64  `json:"requests"`
	Successes int64  `json:"successes"`
}

type DashboardCostRatio struct {
	Ratio    float64 `json:"ratio"`
	Requests int64   `json:"requests"`
	Share    float64 `json:"share"`
}

type DashboardBreakdown struct {
	Name          string  `json:"name"`
	Requests      int64   `json:"requests"`
	Successes     int64   `json:"successes"`
	CanceledCount int64   `json:"canceledCount"`
	SuccessRate   float64 `json:"successRate"`
	InputTokens   int64   `json:"inputTokens"`
	CachedTokens  int64   `json:"cachedTokens"`
	CacheHitRate  float64 `json:"cacheHitRate"`
	OutputTokens  int64   `json:"outputTokens"`
	EstimatedCost int64   `json:"estimatedCostMicros"`
	UpstreamCost  int64   `json:"upstreamCostMicros"`
}

type LogQuery struct {
	Model      string
	Outcome    string
	StatusCode int
	TokenID    uint64
	ChannelID  uint64
	From       time.Time
	To         time.Time
	Page       int
	PageSize   int
}

type LogAggregateSummary struct {
	RequestCount          int64   `json:"requestCount"`
	SuccessCount          int64   `json:"successCount"`
	CanceledCount         int64   `json:"canceledCount"`
	SuccessRate           float64 `json:"successRate"`
	AttemptCount          int64   `json:"attemptCount"`
	InputTokens           int64   `json:"inputTokens"`
	NormalInputTokens     int64   `json:"normalInputTokens"`
	OutputTokens          int64   `json:"outputTokens"`
	CachedTokens          int64   `json:"cachedTokens"`
	CacheWriteTokens      int64   `json:"cacheWriteTokens"`
	SentTokens            int64   `json:"sentTokens"`
	EstimatedCost         int64   `json:"estimatedCostMicros"`
	UpstreamCost          int64   `json:"upstreamCostMicros"`
	AverageFirstTokenMS   float64 `json:"averageFirstTokenMs"`
	FirstTokenSampleCount int64   `json:"firstTokenSampleCount"`
	AverageLatencyMS      float64 `json:"averageLatencyMs"`
	LatencySampleCount    int64   `json:"latencySampleCount"`
	AverageDurationMS     float64 `json:"averageDurationMs"`
	DurationSampleCount   int64   `json:"durationSampleCount"`
}

type RelayRequestView struct {
	RelayRequestLog
	RequestParameters map[string]any    `json:"requestParameters"`
	APIPath           string            `json:"apiPath"`
	ReasoningEffort   string            `json:"reasoningEffort"`
	Attempts          []RelayAttemptLog `json:"attempts"`
	Steps             []RelayStepLog    `json:"steps"`
}

type LogPage struct {
	Items    []RelayRequestView  `json:"items"`
	Summary  LogAggregateSummary `json:"summary"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
}

type ManagementService struct {
	store *Store
}

func NewManagementService(store *Store) *ManagementService {
	return &ManagementService{store: store}
}

func normalizeBaseURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("base URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("base URL must use HTTP or HTTPS")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("base URL cannot contain user info, query parameters, or fragments")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validateChannelInput(input ChannelInput, requireKey bool) (ChannelInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return input, errors.New("channel name is required")
	}
	baseURL, err := normalizeBaseURL(input.BaseURL)
	if err != nil {
		return input, err
	}
	input.BaseURL = baseURL
	if requireKey && strings.TrimSpace(input.APIKey) == "" {
		return input, errors.New("channel API key is required")
	}
	if input.PriceMultiplierBasisPoints != nil && (*input.PriceMultiplierBasisPoints < 0 || *input.PriceMultiplierBasisPoints > MaxPriceMultiplierBasisPoints) {
		return input, errors.New("price multiplier must be between 0 and 100")
	}
	return input, nil
}

func (s *ManagementService) ListChannels(ctx context.Context) ([]ChannelView, error) {
	var channels []Channel
	if err := s.store.db.WithContext(ctx).Order("id desc").Find(&channels).Error; err != nil {
		return nil, err
	}
	channelIDs := make([]uint64, 0, len(channels))
	for _, channel := range channels {
		channelIDs = append(channelIDs, channel.ID)
	}
	var models []ChannelModel
	if len(channelIDs) > 0 {
		if err := s.store.db.WithContext(ctx).Where("channel_id IN ?", channelIDs).Order("priority desc, id asc").Find(&models).Error; err != nil {
			return nil, err
		}
	}
	channelModelIDs := make([]uint64, 0, len(models))
	modelsByChannel := make(map[uint64][]ChannelModel, len(channels))
	for _, model := range models {
		channelModelIDs = append(channelModelIDs, model.ID)
	}
	recentSuccess, err := loadRecentSuccessMetrics(ctx, s.store.db, channelIDs, channelModelIDs, time.Now())
	if err != nil {
		return nil, err
	}
	for index := range models {
		metric := recentSuccess.ByChannelModel[models[index].ID]
		models[index].RecentSuccessRate = metric.rate()
		models[index].RecentSuccessCount = metric.Successes
		models[index].RecentAttemptCount = metric.Attempts
		modelsByChannel[models[index].ChannelID] = append(modelsByChannel[models[index].ChannelID], models[index])
	}
	metricsByChannel, err := s.channelMetrics(ctx, channelIDs)
	if err != nil {
		return nil, err
	}
	views := make([]ChannelView, 0, len(channels))
	for _, channel := range channels {
		metrics := metricsByChannel[channel.ID]
		recentMetric := recentSuccess.ByChannel[channel.ID]
		metrics.RecentSuccessRate = recentMetric.rate()
		metrics.RecentSuccessCount = recentMetric.Successes
		metrics.RecentAttemptCount = recentMetric.Attempts
		views = append(views, ChannelView{
			Channel:          channel,
			APIKeyConfigured: channel.APIKeyCipher != "",
			Models:           modelsByChannel[channel.ID],
			Metrics:          metrics,
		})
	}
	return views, nil
}

func (s *ManagementService) channelMetrics(ctx context.Context, channelIDs []uint64) (map[uint64]ChannelMetrics, error) {
	metricsByChannel := make(map[uint64]ChannelMetrics, len(channelIDs))
	for _, channelID := range channelIDs {
		metricsByChannel[channelID] = ChannelMetrics{LatencySeries: []ChannelLatencyPoint{}}
	}
	if len(channelIDs) == 0 {
		return metricsByChannel, nil
	}

	cutoff := time.Now().Add(-DetailedLogRetentionDays * 24 * time.Hour)
	type channelAggregate struct {
		ChannelID             uint64
		AverageFirstTokenMS   float64
		FirstTokenSampleCount int64
		AverageLatencyMS      float64
		LatencySampleCount    int64
		AverageDurationMS     float64
		DurationSampleCount   int64
		InputTokens           int64
		CachedTokens          int64
	}
	var aggregates []channelAggregate
	if err := s.store.db.WithContext(ctx).Model(&RelayAttemptLog{}).
		Select("channel_id, COALESCE(AVG(CASE WHEN first_token_ms > 0 THEN first_token_ms END), 0) AS average_first_token_ms, "+
			"SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END) AS first_token_sample_count, "+
			"COALESCE(AVG(CASE WHEN latency_ms > 0 THEN latency_ms END), 0) AS average_latency_ms, "+
			"SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END) AS latency_sample_count, "+
			"COALESCE(AVG(CASE WHEN duration_ms > 0 THEN duration_ms END), 0) AS average_duration_ms, "+
			"SUM(CASE WHEN duration_ms > 0 THEN 1 ELSE 0 END) AS duration_sample_count, "+
			"COALESCE(SUM(CASE WHEN usage_source = 'upstream' THEN input_tokens ELSE 0 END), 0) AS input_tokens, "+
			"COALESCE(SUM(CASE WHEN usage_source = 'upstream' THEN cached_tokens ELSE 0 END), 0) AS cached_tokens").
		Where("channel_id IN ? AND created_at >= ? AND success = ?", channelIDs, cutoff, true).
		Group("channel_id").Scan(&aggregates).Error; err != nil {
		return nil, err
	}
	for _, aggregate := range aggregates {
		metrics := metricsByChannel[aggregate.ChannelID]
		metrics.AverageFirstTokenMS = aggregate.AverageFirstTokenMS
		metrics.FirstTokenSampleCount = aggregate.FirstTokenSampleCount
		metrics.AverageLatencyMS = aggregate.AverageLatencyMS
		metrics.LatencySampleCount = aggregate.LatencySampleCount
		metrics.AverageDurationMS = aggregate.AverageDurationMS
		metrics.DurationSampleCount = aggregate.DurationSampleCount
		metrics.InputTokens = max(aggregate.InputTokens, 0)
		metrics.CachedTokens = min(max(aggregate.CachedTokens, 0), metrics.InputTokens)
		if metrics.InputTokens > 0 {
			metrics.CacheHitRate = float64(metrics.CachedTokens) / float64(metrics.InputTokens)
		}
		metricsByChannel[aggregate.ChannelID] = metrics
	}

	type latencyRow struct {
		ChannelID  uint64
		LatencyMS  int64
		RecordedAt time.Time `gorm:"column:created_at"`
	}
	var rows []latencyRow
	if err := s.store.db.WithContext(ctx).Raw(`
		SELECT channel_id, latency_ms, created_at
		FROM (
			SELECT channel_id, latency_ms, created_at, id,
				ROW_NUMBER() OVER (PARTITION BY channel_id ORDER BY created_at DESC, id DESC) AS row_number
			FROM relay_attempt_logs
			WHERE channel_id IN ? AND created_at >= ? AND success = ? AND latency_ms > 0
		) AS recent_latency
		WHERE row_number <= ?
		ORDER BY channel_id ASC, created_at ASC, id ASC
	`, channelIDs, cutoff, true, channelLatencyPointLimit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		metrics := metricsByChannel[row.ChannelID]
		metrics.LatencySeries = append(metrics.LatencySeries, ChannelLatencyPoint{
			RecordedAt: row.RecordedAt,
			LatencyMS:  row.LatencyMS,
		})
		metrics.LatestLatencyMS = row.LatencyMS
		metricsByChannel[row.ChannelID] = metrics
	}
	return metricsByChannel, nil
}

func (s *ManagementService) CreateChannel(ctx context.Context, input ChannelInput) (*ChannelView, error) {
	channel, err := s.createChannel(s.store.db.WithContext(ctx), input)
	if err != nil {
		return nil, err
	}
	return &ChannelView{
		Channel:          channel,
		APIKeyConfigured: true,
		Models:           []ChannelModel{},
		Metrics:          ChannelMetrics{LatencySeries: []ChannelLatencyPoint{}, RecentSuccessRate: 1},
	}, nil
}

func (s *ManagementService) createChannel(db *gorm.DB, input ChannelInput) (Channel, error) {
	input, err := validateChannelInput(input, true)
	if err != nil {
		return Channel{}, err
	}
	cipherText, err := s.store.secretBox.Encrypt(strings.TrimSpace(input.APIKey))
	if err != nil {
		return Channel{}, err
	}
	priceMultiplierBasisPoints := DefaultPriceMultiplierBasisPoints
	if input.PriceMultiplierBasisPoints != nil {
		priceMultiplierBasisPoints = *input.PriceMultiplierBasisPoints
	}
	channel := Channel{
		Name:                       input.Name,
		BaseURL:                    input.BaseURL,
		APIKeyCipher:               cipherText,
		Enabled:                    input.Enabled,
		SupportsStreamUsage:        input.SupportsStreamUsage,
		PriceMultiplierBasisPoints: priceMultiplierBasisPoints,
	}
	if err := db.Create(&channel).Error; err != nil {
		return Channel{}, err
	}
	return channel, nil
}

func (s *ManagementService) UpdateChannel(ctx context.Context, id uint64, input ChannelInput) (*ChannelView, error) {
	channel, err := s.updateChannel(s.store.db.WithContext(ctx), id, input)
	if err != nil {
		return nil, err
	}
	return s.channelView(ctx, channel)
}

func (s *ManagementService) updateChannel(db *gorm.DB, id uint64, input ChannelInput) (Channel, error) {
	input, err := validateChannelInput(input, false)
	if err != nil {
		return Channel{}, err
	}
	var channel Channel
	if err := db.First(&channel, id).Error; err != nil {
		return Channel{}, err
	}
	channel.Name = input.Name
	channel.BaseURL = input.BaseURL
	if channel.CircuitLevel == CircuitLevelManual && input.Enabled {
		channel.ConsecutiveFailures = 0
		channel.CircuitLevel = CircuitLevelClosed
		channel.CircuitOpenUntil = nil
		channel.LastError = ""
	}
	channel.Enabled = input.Enabled
	channel.SupportsStreamUsage = input.SupportsStreamUsage
	if input.PriceMultiplierBasisPoints != nil {
		channel.PriceMultiplierBasisPoints = *input.PriceMultiplierBasisPoints
	}
	if strings.TrimSpace(input.APIKey) != "" {
		channel.APIKeyCipher, err = s.store.secretBox.Encrypt(strings.TrimSpace(input.APIKey))
		if err != nil {
			return Channel{}, err
		}
	}
	if err := db.Save(&channel).Error; err != nil {
		return Channel{}, err
	}
	return channel, nil
}

func (s *ManagementService) channelView(ctx context.Context, channel Channel) (*ChannelView, error) {
	var models []ChannelModel
	if err := s.store.db.WithContext(ctx).Where("channel_id = ?", channel.ID).Find(&models).Error; err != nil {
		return nil, err
	}
	channelModelIDs := make([]uint64, 0, len(models))
	for _, model := range models {
		channelModelIDs = append(channelModelIDs, model.ID)
	}
	recentSuccess, err := loadRecentSuccessMetrics(ctx, s.store.db, []uint64{channel.ID}, channelModelIDs, time.Now())
	if err != nil {
		return nil, err
	}
	for index := range models {
		metric := recentSuccess.ByChannelModel[models[index].ID]
		models[index].RecentSuccessRate = metric.rate()
		models[index].RecentSuccessCount = metric.Successes
		models[index].RecentAttemptCount = metric.Attempts
	}
	metricsByChannel, err := s.channelMetrics(ctx, []uint64{channel.ID})
	if err != nil {
		return nil, err
	}
	metrics := metricsByChannel[channel.ID]
	recentMetric := recentSuccess.ByChannel[channel.ID]
	metrics.RecentSuccessRate = recentMetric.rate()
	metrics.RecentSuccessCount = recentMetric.Successes
	metrics.RecentAttemptCount = recentMetric.Attempts
	return &ChannelView{
		Channel:          channel,
		APIKeyConfigured: channel.APIKeyCipher != "",
		Models:           models,
		Metrics:          metrics,
	}, nil
}

func (s *ManagementService) SaveChannelConfiguration(ctx context.Context, input ChannelConfigurationInput) (*ChannelView, error) {
	var view ChannelView
	err := s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var channel Channel
		var err error
		if input.ID == 0 {
			channel, err = s.createChannel(db, input.Channel)
		} else {
			channel, err = s.updateChannel(db, input.ID, input.Channel)
		}
		if err != nil {
			return err
		}

		models, err := s.replaceChannelModels(db, channel.ID, input.Models)
		if err != nil {
			return err
		}
		view = ChannelView{
			Channel:          channel,
			APIKeyConfigured: channel.APIKeyCipher != "",
			Models:           models,
			Metrics:          ChannelMetrics{LatencySeries: []ChannelLatencyPoint{}, RecentSuccessRate: 1},
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &view, nil
}

func (s *ManagementService) DeleteChannel(ctx context.Context, id uint64) error {
	return s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if err := resolveCircuitRecords(db, id, 0, 0, CircuitResolutionMappingRemoved, time.Now()); err != nil {
			return err
		}
		if err := db.Where("channel_id = ?", id).Delete(&ChannelModel{}).Error; err != nil {
			return err
		}
		result := db.Delete(&Channel{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (s *ManagementService) ResetChannelCircuit(ctx context.Context, id uint64) error {
	return s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		result := db.Model(&Channel{}).Where("id = ?", id).Updates(map[string]any{
			"enabled":              true,
			"consecutive_failures": 0,
			"circuit_level":        CircuitLevelClosed,
			"circuit_open_until":   nil,
			"last_error":           "",
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return resolveCircuitRecords(db, id, 0, 0, CircuitResolutionManualReset, time.Now())
	})
}

func (s *ManagementService) ReplaceChannelModels(ctx context.Context, channelID uint64, inputs []ChannelModelInput) ([]ChannelModel, error) {
	var models []ChannelModel
	err := s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var err error
		models, err = s.replaceChannelModels(db, channelID, inputs)
		return err
	})
	return models, err
}

func (s *ManagementService) replaceChannelModels(db *gorm.DB, channelID uint64, inputs []ChannelModelInput) ([]ChannelModel, error) {
	var channel Channel
	if err := db.First(&channel, channelID).Error; err != nil {
		return nil, err
	}
	models := make([]ChannelModel, 0, len(inputs))
	seen := make(map[uint64]struct{}, len(inputs))
	for _, input := range inputs {
		if _, ok := seen[input.ModelID]; ok {
			return nil, fmt.Errorf("model %d is duplicated", input.ModelID)
		}
		seen[input.ModelID] = struct{}{}
		if strings.TrimSpace(input.UpstreamModel) == "" {
			return nil, errors.New("upstream model is required")
		}
		if input.Weight < 1 || input.Weight > 10000 {
			return nil, errors.New("weight must be between 1 and 10000")
		}
		if input.InputPriceMicros < 0 || input.OutputPriceMicros < 0 ||
			(input.CachedInputPriceMicros != nil && *input.CachedInputPriceMicros < 0) ||
			(input.CacheWritePriceMicros != nil && *input.CacheWritePriceMicros < 0) {
			return nil, errors.New("model prices cannot be negative")
		}
		priceMultiplierBasisPoints := DefaultPriceMultiplierBasisPoints
		if input.PriceMultiplierBasisPoints != nil {
			if *input.PriceMultiplierBasisPoints < 0 || *input.PriceMultiplierBasisPoints > MaxPriceMultiplierBasisPoints {
				return nil, errors.New("price multiplier must be between 0 and 100")
			}
			priceMultiplierBasisPoints = *input.PriceMultiplierBasisPoints
		}
		var count int64
		if err := db.Model(&GatewayModel{}).Where("id = ?", input.ModelID).Count(&count).Error; err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, fmt.Errorf("model %d does not exist", input.ModelID)
		}
		models = append(models, ChannelModel{
			ChannelID:                  channelID,
			ModelID:                    input.ModelID,
			UpstreamModel:              strings.TrimSpace(input.UpstreamModel),
			Priority:                   input.Priority,
			Weight:                     input.Weight,
			InputPriceMicros:           input.InputPriceMicros,
			OutputPriceMicros:          input.OutputPriceMicros,
			CachedInputPriceMicros:     input.CachedInputPriceMicros,
			CacheWritePriceMicros:      input.CacheWritePriceMicros,
			PriceMultiplierBasisPoints: priceMultiplierBasisPoints,
			Enabled:                    input.Enabled,
		})
	}
	var existing []ChannelModel
	if err := db.Where("channel_id = ?", channelID).Find(&existing).Error; err != nil {
		return nil, err
	}
	existingByModelID := make(map[uint64]ChannelModel, len(existing))
	for _, mapping := range existing {
		existingByModelID[mapping.ModelID] = mapping
	}
	requestedModelIDs := make(map[uint64]struct{}, len(models))
	for index := range models {
		requestedModelIDs[models[index].ModelID] = struct{}{}
		current, exists := existingByModelID[models[index].ModelID]
		if !exists {
			if err := db.Create(&models[index]).Error; err != nil {
				return nil, err
			}
			continue
		}
		models[index].ID = current.ID
		models[index].CreatedAt = current.CreatedAt
		models[index].CircuitDisabled = current.CircuitDisabled && !models[index].Enabled
		if current.CircuitDisabled && models[index].Enabled {
			if err := resolveCircuitRecords(db, channelID, current.ID, CircuitLevelManual, CircuitResolutionManualReopen, time.Now()); err != nil {
				return nil, err
			}
		}
		if err := db.Save(&models[index]).Error; err != nil {
			return nil, err
		}
	}
	for _, current := range existing {
		if _, kept := requestedModelIDs[current.ModelID]; kept {
			continue
		}
		if err := resolveCircuitRecords(db, channelID, current.ID, CircuitLevelManual, CircuitResolutionMappingRemoved, time.Now()); err != nil {
			return nil, err
		}
		if err := db.Delete(&current).Error; err != nil {
			return nil, err
		}
	}
	for index := range models {
		models[index].RecentSuccessRate = 1
	}
	return models, nil
}

func validRoutingStrategy(value string) bool {
	return value == RoutingPriorityWeighted || value == RoutingLowestCost || value == RoutingLowestLatency
}

func (s *ManagementService) ListModels(ctx context.Context) ([]GatewayModelView, error) {
	var models []GatewayModel
	if err := s.store.db.WithContext(ctx).Order("name asc").Find(&models).Error; err != nil {
		return nil, err
	}
	type modelRequestCount struct {
		Name         string
		RequestCount int64
	}
	var counts []modelRequestCount
	if err := s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).
		Select("requested_model AS name, COUNT(*) AS request_count").
		Group("requested_model").Scan(&counts).Error; err != nil {
		return nil, err
	}
	countByName := make(map[string]int64, len(counts))
	for _, count := range counts {
		countByName[count.Name] = count.RequestCount
	}
	views := make([]GatewayModelView, 0, len(models))
	for _, model := range models {
		views = append(views, GatewayModelView{GatewayModel: model, RequestCount: countByName[model.Name]})
	}
	return views, nil
}

func (s *ManagementService) CreateModel(ctx context.Context, input GatewayModelInput) (*GatewayModel, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return nil, errors.New("model name is required")
	}
	if !validRoutingStrategy(input.RoutingStrategy) {
		return nil, errors.New("invalid routing strategy")
	}
	model := GatewayModel{Name: input.Name, RoutingStrategy: input.RoutingStrategy, Enabled: input.Enabled}
	if err := s.store.db.WithContext(ctx).Create(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

func (s *ManagementService) UpdateModel(ctx context.Context, id uint64, input GatewayModelInput) (*GatewayModel, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || !validRoutingStrategy(input.RoutingStrategy) {
		return nil, errors.New("model name and routing strategy are invalid")
	}
	var model GatewayModel
	if err := s.store.db.WithContext(ctx).First(&model, id).Error; err != nil {
		return nil, err
	}
	model.Name = input.Name
	model.RoutingStrategy = input.RoutingStrategy
	model.Enabled = input.Enabled
	if err := s.store.db.WithContext(ctx).Save(&model).Error; err != nil {
		return nil, err
	}
	return &model, nil
}

func (s *ManagementService) DeleteModel(ctx context.Context, id uint64) error {
	return s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if err := db.Where("model_id = ?", id).Delete(&ChannelModel{}).Error; err != nil {
			return err
		}
		if err := db.Where("model_id = ?", id).Delete(&ClientTokenModel{}).Error; err != nil {
			return err
		}
		result := db.Delete(&GatewayModel{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (s *ManagementService) ListTokens(ctx context.Context) ([]ClientTokenView, error) {
	var tokens []ClientToken
	if err := s.store.db.WithContext(ctx).Order("id desc").Find(&tokens).Error; err != nil {
		return nil, err
	}
	views := make([]ClientTokenView, 0, len(tokens))
	for _, token := range tokens {
		var links []ClientTokenModel
		if err := s.store.db.WithContext(ctx).Where("token_id = ?", token.ID).Find(&links).Error; err != nil {
			return nil, err
		}
		ids := make([]uint64, 0, len(links))
		for _, link := range links {
			ids = append(ids, link.ModelID)
		}
		statistics, err := s.tokenStatistics(ctx, token.ID)
		if err != nil {
			return nil, err
		}
		views = append(views, ClientTokenView{ClientToken: token, ModelIDs: ids, Statistics: statistics})
	}
	return views, nil
}

func (s *ManagementService) tokenStatistics(ctx context.Context, tokenID uint64) (TokenStatistics, error) {
	type totals struct {
		TokenStatistics
		FirstTokenMS int64
		LatencyMS    int64
		DurationMS   int64
	}
	var total totals
	err := s.store.db.WithContext(ctx).Model(&TokenDailyStat{}).Select(
		"COALESCE(SUM(request_count),0) AS requests, COALESCE(SUM(success_count),0) AS successes, "+
			"COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(normal_input_tokens),0) AS normal_input_tokens, "+
			"COALESCE(SUM(output_tokens),0) AS output_tokens, "+
			"COALESCE(SUM(cached_tokens),0) AS cached_tokens, COALESCE(SUM(cache_write_tokens),0) AS cache_write_tokens, "+
			"COALESCE(SUM(sent_tokens),0) AS sent_tokens, "+
			"COALESCE(SUM(estimated_cost),0) AS estimated_cost, "+
			"COALESCE(SUM(upstream_cost),0) AS upstream_cost, "+
			"COALESCE(SUM(first_token_ms),0) AS first_token_ms, COALESCE(SUM(first_token_samples),0) AS first_token_sample_count, "+
			"COALESCE(SUM(latency_ms),0) AS latency_ms, COALESCE(SUM(latency_samples),0) AS latency_sample_count, "+
			"COALESCE(SUM(duration_ms),0) AS duration_ms, COALESCE(SUM(attempt_count),0) AS attempts",
	).Where("token_id = ?", tokenID).Scan(&total).Error
	if err != nil {
		return TokenStatistics{}, err
	}
	if total.FirstTokenSampleCount > 0 {
		total.AverageFirstTokenMS = float64(total.FirstTokenMS) / float64(total.FirstTokenSampleCount)
	}
	if total.LatencySampleCount > 0 {
		total.AverageLatency = float64(total.LatencyMS) / float64(total.LatencySampleCount)
	}
	if total.Requests > 0 {
		total.AverageDurationMS = float64(total.DurationMS) / float64(total.Requests)
		total.DurationSampleCount = total.Requests
	}
	return total.TokenStatistics, nil
}

func validateTokenInput(input ClientTokenInput) (ClientTokenInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return input, errors.New("token name is required")
	}
	if input.RPM < 1 || input.RPM > 100000 {
		return input, errors.New("RPM must be between 1 and 100000")
	}
	if input.MaxConcurrency < 1 || input.MaxConcurrency > 10000 {
		return input, errors.New("max concurrency must be between 1 and 10000")
	}
	if !input.AllowAllModels && len(input.ModelIDs) == 0 {
		return input, errors.New("select at least one model or allow all models")
	}
	sort.Slice(input.ModelIDs, func(i, j int) bool { return input.ModelIDs[i] < input.ModelIDs[j] })
	return input, nil
}

func (s *ManagementService) issueToken(ctx context.Context, input ClientTokenInput, existing *ClientToken) (*IssuedClientToken, error) {
	input, err := validateTokenInput(input)
	if err != nil {
		return nil, err
	}
	secret, err := generateSecret("sk-")
	if err != nil {
		return nil, err
	}
	token := ClientToken{
		Name:           input.Name,
		KeyHash:        hashSecret(secret),
		KeyPrefix:      visibleKeyPrefix(secret),
		Enabled:        input.Enabled,
		AllowAllModels: input.AllowAllModels,
		RPM:            input.RPM,
		MaxConcurrency: input.MaxConcurrency,
	}
	if existing != nil {
		token.ID = existing.ID
		token.CreatedAt = existing.CreatedAt
	}
	err = s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if existing == nil {
			if err := db.Create(&token).Error; err != nil {
				return err
			}
		} else if err := db.Save(&token).Error; err != nil {
			return err
		}
		return replaceTokenModels(db, token.ID, input)
	})
	if err != nil {
		return nil, err
	}
	view, err := s.tokenView(ctx, token)
	if err != nil {
		return nil, err
	}
	return &IssuedClientToken{Token: *view, Secret: secret}, nil
}

func replaceTokenModels(db *gorm.DB, tokenID uint64, input ClientTokenInput) error {
	if err := db.Where("token_id = ?", tokenID).Delete(&ClientTokenModel{}).Error; err != nil {
		return err
	}
	if input.AllowAllModels {
		return nil
	}
	links := make([]ClientTokenModel, 0, len(input.ModelIDs))
	for _, modelID := range input.ModelIDs {
		links = append(links, ClientTokenModel{TokenID: tokenID, ModelID: modelID})
	}
	return db.Create(&links).Error
}

func (s *ManagementService) CreateToken(ctx context.Context, input ClientTokenInput) (*IssuedClientToken, error) {
	return s.issueToken(ctx, input, nil)
}

func (s *ManagementService) UpdateToken(ctx context.Context, id uint64, input ClientTokenInput) (*ClientTokenView, error) {
	input, err := validateTokenInput(input)
	if err != nil {
		return nil, err
	}
	var token ClientToken
	if err := s.store.db.WithContext(ctx).First(&token, id).Error; err != nil {
		return nil, err
	}
	token.Name = input.Name
	token.Enabled = input.Enabled
	token.AllowAllModels = input.AllowAllModels
	token.RPM = input.RPM
	token.MaxConcurrency = input.MaxConcurrency
	err = s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if err := db.Save(&token).Error; err != nil {
			return err
		}
		return replaceTokenModels(db, token.ID, input)
	})
	if err != nil {
		return nil, err
	}
	return s.tokenView(ctx, token)
}

func (s *ManagementService) RotateToken(ctx context.Context, id uint64) (*IssuedClientToken, error) {
	var token ClientToken
	if err := s.store.db.WithContext(ctx).First(&token, id).Error; err != nil {
		return nil, err
	}
	view, err := s.tokenView(ctx, token)
	if err != nil {
		return nil, err
	}
	input := ClientTokenInput{
		Name: token.Name, Enabled: token.Enabled, AllowAllModels: token.AllowAllModels,
		RPM: token.RPM, MaxConcurrency: token.MaxConcurrency, ModelIDs: view.ModelIDs,
	}
	return s.issueToken(ctx, input, &token)
}

func (s *ManagementService) tokenView(ctx context.Context, token ClientToken) (*ClientTokenView, error) {
	var links []ClientTokenModel
	if err := s.store.db.WithContext(ctx).Where("token_id = ?", token.ID).Find(&links).Error; err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(links))
	for _, link := range links {
		ids = append(ids, link.ModelID)
	}
	statistics, err := s.tokenStatistics(ctx, token.ID)
	if err != nil {
		return nil, err
	}
	return &ClientTokenView{ClientToken: token, ModelIDs: ids, Statistics: statistics}, nil
}

func (s *ManagementService) DeleteToken(ctx context.Context, id uint64) error {
	return s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if err := db.Where("token_id = ?", id).Delete(&ClientTokenModel{}).Error; err != nil {
			return err
		}
		result := db.Delete(&ClientToken{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (s *ManagementService) TestChannel(ctx context.Context, id uint64) (int64, int, error) {
	discovery, err := s.discoverChannelModels(ctx, ChannelModelDiscoveryInput{ChannelID: id}, false)
	if discovery == nil {
		return 0, 0, err
	}
	return discovery.LatencyMS, discovery.Status, err
}

func (s *ManagementService) DiscoverChannelModels(ctx context.Context, input ChannelModelDiscoveryInput) (*ChannelModelDiscovery, error) {
	return s.discoverChannelModels(ctx, input, true)
}

func (s *ManagementService) discoverChannelModels(ctx context.Context, input ChannelModelDiscoveryInput, registerPublicModels bool) (*ChannelModelDiscovery, error) {
	baseURL := strings.TrimSpace(input.BaseURL)
	apiKey := strings.TrimSpace(input.APIKey)
	var channel *Channel
	if input.ChannelID > 0 {
		stored := &Channel{}
		if err := s.store.db.WithContext(ctx).First(stored, input.ChannelID).Error; err != nil {
			return nil, err
		}
		channel = stored
		if baseURL == "" {
			baseURL = stored.BaseURL
		}
		if apiKey == "" {
			decrypted, err := s.store.secretBox.Decrypt(stored.APIKeyCipher)
			if err != nil {
				return nil, err
			}
			apiKey = decrypted
		}
	}
	normalizedBaseURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if apiKey == "" {
		return nil, errors.New("channel API key is required to discover models")
	}
	discovery, discoverErr := fetchUpstreamModels(ctx, normalizedBaseURL, apiKey)
	if channel != nil {
		s.updateDiscoveryHealth(channel.ID, discovery, discoverErr)
	}
	if discoverErr != nil {
		return discovery, discoverErr
	}
	if registerPublicModels {
		if err := s.registerDiscoveredPublicModels(ctx, discovery); err != nil {
			return discovery, fmt.Errorf("register discovered public models: %w", err)
		}
	}
	return discovery, nil
}

func (s *ManagementService) registerDiscoveredPublicModels(ctx context.Context, discovery *ChannelModelDiscovery) error {
	if discovery == nil || len(discovery.Models) == 0 {
		return nil
	}
	return s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		for index := range discovery.Models {
			modelView := &discovery.Models[index]
			var publicModel GatewayModel
			if err := db.Where("name = ?", modelView.ID).Limit(1).Find(&publicModel).Error; err != nil {
				return err
			}
			if publicModel.ID != 0 {
				modelView.PublicModelID = publicModel.ID
				continue
			}

			publicModel = GatewayModel{
				Name:            modelView.ID,
				RoutingStrategy: RoutingPriorityWeighted,
				Enabled:         true,
			}
			result := db.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "name"}},
				DoNothing: true,
			}).Create(&publicModel)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				modelView.PublicModelID = publicModel.ID
				modelView.PublicModelCreated = true
				continue
			}
			if err := db.Where("name = ?", modelView.ID).First(&publicModel).Error; err != nil {
				return err
			}
			modelView.PublicModelID = publicModel.ID
		}
		return nil
	})
}

func fetchUpstreamModels(ctx context.Context, baseURL string, apiKey string) (*ChannelModelDiscovery, error) {
	discovery := &ChannelModelDiscovery{Models: []UpstreamModelView{}, FetchedAt: time.Now()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return discovery, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	started := time.Now()
	resp, err := client.Do(req)
	discovery.LatencyMS = time.Since(started).Milliseconds()
	discovery.FetchedAt = time.Now()
	if err != nil {
		return discovery, err
	}
	defer resp.Body.Close()
	discovery.Status = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return discovery, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamModelsResponseBytes+1))
	if err != nil {
		return discovery, fmt.Errorf("read upstream models response: %w", err)
	}
	if len(body) > maxUpstreamModelsResponseBytes {
		return discovery, errors.New("upstream models response is too large")
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return discovery, errors.New("upstream models response is not valid JSON")
	}
	if len(envelope.Data) == 0 {
		return discovery, errors.New("upstream models response does not contain data")
	}
	var models []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
		Created int64  `json:"created"`
	}
	if err := json.Unmarshal(envelope.Data, &models); err != nil {
		return discovery, errors.New("upstream models data is not a model list")
	}
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		model.OwnedBy = strings.TrimSpace(model.OwnedBy)
		if model.ID == "" {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		discovery.Models = append(discovery.Models, UpstreamModelView{ID: model.ID, OwnedBy: model.OwnedBy, Created: model.Created, OfficialPrice: OpenAIOfficialPrice(model.ID)})
	}
	sort.Slice(discovery.Models, func(i, j int) bool { return discovery.Models[i].ID < discovery.Models[j].ID })
	return discovery, nil
}

func (s *ManagementService) updateDiscoveryHealth(channelID uint64, discovery *ChannelModelDiscovery, discoverErr error) {
	updates := map[string]any{"last_health_at": time.Now()}
	if discoverErr != nil {
		message := discoverErr.Error()
		if len(message) > 2000 {
			message = message[:2000]
		}
		updates["last_error"] = message
	} else {
		updates["last_error"] = ""
		updates["latency_ewma"] = float64(discovery.LatencyMS)
	}
	_ = s.store.db.Model(&Channel{}).Where("id = ?", channelID).Updates(updates).Error
}

func officialUsageCost(model string, inputTokens, normalInputTokens, outputTokens, cachedTokens, cacheWriteTokens int64) int64 {
	price := OpenAIOfficialPrice(model)
	if price == nil {
		return 0
	}
	inputTokens = max(inputTokens, 0)
	cachedTokens = min(max(cachedTokens, 0), inputTokens)
	cacheWriteTokens = min(max(cacheWriteTokens, 0), inputTokens-cachedTokens)
	normalInputTokens = min(max(normalInputTokens, 0), inputTokens-cachedTokens-cacheWriteTokens)
	unclassifiedInputTokens := inputTokens - normalInputTokens - cachedTokens - cacheWriteTokens
	cachedPrice := price.InputPriceMicros
	if price.CachedInputPriceMicros != nil {
		cachedPrice = *price.CachedInputPriceMicros
	}
	cacheWritePrice := price.InputPriceMicros
	if price.CacheWritePriceMicros != nil {
		cacheWritePrice = *price.CacheWritePriceMicros
	}
	return ((normalInputTokens+unclassifiedInputTokens)*price.InputPriceMicros +
		cachedTokens*cachedPrice + cacheWriteTokens*cacheWritePrice +
		max(outputTokens, 0)*price.OutputPriceMicros) / 1_000_000
}

func (s *ManagementService) Dashboard(ctx context.Context, days int) (*DashboardSummary, error) {
	if days != 1 && days != 2 && days != 3 && days != 5 {
		return nil, errors.New("统计时间范围仅支持 1、2、3 或 5 天")
	}
	now := time.Now()
	today := eastEightStartOfDay(now)
	startTime := today.AddDate(0, 0, -(days - 1)).UTC()
	endTime := today.AddDate(0, 0, 1).UTC()
	summary := &DashboardSummary{
		Daily:      []DashboardDaily{},
		Hourly:     []DashboardHourly{},
		CostRatios: []DashboardCostRatio{},
		Channels:   []DashboardBreakdown{},
		Models:     []DashboardBreakdown{},
	}
	type totals struct {
		Requests              int64
		Successes             int64
		CanceledCount         int64
		InputTokens           int64
		NormalInputTokens     int64
		OutputTokens          int64
		CachedTokens          int64
		CacheWriteTokens      int64
		EstimatedCost         int64
		UpstreamCost          int64
		FirstTokenMS          int64
		FirstTokenSampleCount int64
		LatencyMS             int64
		LatencySampleCount    int64
		DurationMS            int64
	}
	var total totals
	err := s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).Where("created_at >= ? AND created_at < ?", startTime, endTime).Select(
		"COUNT(*) AS requests, COALESCE(SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END),0) AS successes, COALESCE(SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END),0) AS canceled_count, " +
			"COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(normal_input_tokens),0) AS normal_input_tokens, " +
			"COALESCE(SUM(output_tokens),0) AS output_tokens, COALESCE(SUM(cached_tokens),0) AS cached_tokens, COALESCE(SUM(cache_write_tokens),0) AS cache_write_tokens, " +
			"COALESCE(SUM(estimated_cost),0) AS estimated_cost, COALESCE(SUM(upstream_cost),0) AS upstream_cost, " +
			"COALESCE(SUM(first_token_ms),0) AS first_token_ms, COALESCE(SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END),0) AS first_token_sample_count, " +
			"COALESCE(SUM(latency_ms),0) AS latency_ms, COALESCE(SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END),0) AS latency_sample_count, COALESCE(SUM(duration_ms),0) AS duration_ms",
	).Scan(&total).Error
	if err != nil {
		return nil, err
	}
	summary.Requests = total.Requests
	summary.CanceledCount = total.CanceledCount
	summary.InputTokens = total.InputTokens
	summary.NormalInputTokens = total.NormalInputTokens
	summary.OutputTokens = total.OutputTokens
	summary.CachedTokens = total.CachedTokens
	summary.CacheWriteTokens = total.CacheWriteTokens
	summary.EstimatedCost = total.EstimatedCost
	summary.UpstreamCost = total.UpstreamCost
	summary.FirstTokenSampleCount = total.FirstTokenSampleCount
	summary.LatencySampleCount = total.LatencySampleCount
	summary.DurationSampleCount = total.Requests
	if total.InputTokens > 0 {
		summary.CacheHitRate = float64(total.CachedTokens) / float64(total.InputTokens)
	}
	type officialUsageRow struct {
		RequestedModel    string
		InputTokens       int64
		NormalInputTokens int64
		OutputTokens      int64
		CachedTokens      int64
		CacheWriteTokens  int64
		UpstreamCost      int64
	}
	var officialUsage []officialUsageRow
	if err := s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).
		Select("requested_model, input_tokens, normal_input_tokens, output_tokens, cached_tokens, cache_write_tokens, upstream_cost").
		Where("created_at >= ? AND created_at < ?", startTime, endTime).Find(&officialUsage).Error; err != nil {
		return nil, err
	}
	costRatioCounts := make(map[int64]int64)
	var costRatioSamples int64
	for _, usage := range officialUsage {
		officialCost := officialUsageCost(usage.RequestedModel, usage.InputTokens, usage.NormalInputTokens, usage.OutputTokens, usage.CachedTokens, usage.CacheWriteTokens)
		summary.OfficialCost += officialCost
		if officialCost > 0 {
			ratioBasisPoints := int64(math.Round(float64(max(usage.UpstreamCost, 0)) / float64(officialCost) * 100))
			costRatioCounts[ratioBasisPoints]++
			costRatioSamples++
		}
	}
	for ratioBasisPoints, requests := range costRatioCounts {
		summary.CostRatios = append(summary.CostRatios, DashboardCostRatio{
			Ratio:    float64(ratioBasisPoints) / 100,
			Requests: requests,
			Share:    float64(requests) / float64(costRatioSamples),
		})
	}
	sort.Slice(summary.CostRatios, func(left, right int) bool {
		if summary.CostRatios[left].Requests == summary.CostRatios[right].Requests {
			return summary.CostRatios[left].Ratio > summary.CostRatios[right].Ratio
		}
		return summary.CostRatios[left].Requests > summary.CostRatios[right].Requests
	})
	if len(summary.CostRatios) > 5 {
		summary.CostRatios = summary.CostRatios[:5]
	}
	if summary.OfficialCost > 0 {
		summary.EstimatedCostRatio = float64(summary.EstimatedCost) / float64(summary.OfficialCost)
		summary.UpstreamCostRatio = float64(summary.UpstreamCost) / float64(summary.OfficialCost)
	}
	if total.FirstTokenSampleCount > 0 {
		summary.AverageFirstTokenMS = float64(total.FirstTokenMS) / float64(total.FirstTokenSampleCount)
	}
	if total.LatencySampleCount > 0 {
		summary.AverageLatency = float64(total.LatencyMS) / float64(total.LatencySampleCount)
	}
	if completedRequests := total.Requests - total.CanceledCount; completedRequests > 0 {
		summary.SuccessRate = float64(total.Successes) / float64(completedRequests)
	}
	if total.Requests > 0 {
		summary.AverageDurationMS = float64(total.DurationMS) / float64(total.Requests)
	}
	if err := s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).
		Select(sqliteEastEightCreatedDate+" AS date, COUNT(*) AS requests, COALESCE(SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END),0) AS successes, COALESCE(SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END),0) AS canceled_count, "+
			"COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(output_tokens),0) AS output_tokens, "+
			"COALESCE(SUM(estimated_cost),0) AS estimated_cost, COALESCE(SUM(upstream_cost),0) AS upstream_cost, "+
			"COALESCE(1.0 * SUM(first_token_ms) / NULLIF(SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END),0),0) AS average_first_token_ms, "+
			"COALESCE(SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END),0) AS first_token_sample_count, "+
			"COALESCE(1.0 * SUM(latency_ms) / NULLIF(SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END),0),0) AS average_latency_ms, "+
			"COALESCE(SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END),0) AS latency_sample_count, "+
			"COALESCE(1.0 * SUM(duration_ms) / NULLIF(COUNT(*),0),0) AS average_duration_ms, COUNT(*) AS duration_sample_count").
		Where("created_at >= ? AND created_at < ?", startTime, endTime).Group(sqliteEastEightCreatedDate).Order("date asc").Scan(&summary.Daily).Error; err != nil {
		return nil, err
	}
	dailyByDate := make(map[string]DashboardDaily, len(summary.Daily))
	for _, daily := range summary.Daily {
		dailyByDate[daily.Date] = daily
	}
	summary.Daily = make([]DashboardDaily, 0, days)
	for offset := days - 1; offset >= 0; offset-- {
		date := today.AddDate(0, 0, -offset).Format(time.DateOnly)
		daily, ok := dailyByDate[date]
		if !ok {
			daily = DashboardDaily{Date: date}
		}
		summary.Daily = append(summary.Daily, daily)
	}
	if err := s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).
		Select(sqliteEastEightCreatedHour+" AS hour, COUNT(*) AS requests, COALESCE(SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END),0) AS successes").
		Where("created_at >= ? AND created_at < ?", startTime, endTime).Group(sqliteEastEightCreatedHour).Order("hour asc").Scan(&summary.Hourly).Error; err != nil {
		return nil, err
	}
	hourlyByHour := make(map[string]DashboardHourly, len(summary.Hourly))
	for _, hourly := range summary.Hourly {
		hourlyByHour[hourly.Hour] = hourly
	}
	currentHour := now.In(eastEightLocation).Truncate(time.Hour)
	startHour := today.AddDate(0, 0, -(days - 1))
	hourCount := int(currentHour.Sub(startHour)/time.Hour) + 1
	summary.Hourly = make([]DashboardHourly, 0, max(hourCount, 0))
	for hour := startHour; !hour.After(currentHour); hour = hour.Add(time.Hour) {
		key := hour.Format("2006-01-02T15:00:00-07:00")
		hourly, ok := hourlyByHour[key]
		if !ok {
			hourly = DashboardHourly{Hour: key}
		}
		summary.Hourly = append(summary.Hourly, hourly)
	}
	if err := s.store.db.WithContext(ctx).Table("relay_request_logs AS request").
		Select("COALESCE(NULLIF(final_attempt.channel_name, ''), c.name, '未归属渠道') AS name, COUNT(*) AS requests, "+
			"COALESCE(SUM(CASE WHEN request.outcome = 'success' THEN 1 ELSE 0 END),0) AS successes, COALESCE(SUM(CASE WHEN request.outcome = 'canceled' THEN 1 ELSE 0 END),0) AS canceled_count, "+
			"COALESCE(1.0 * SUM(CASE WHEN request.outcome = 'success' THEN 1 ELSE 0 END) / NULLIF(SUM(CASE WHEN request.outcome <> 'canceled' THEN 1 ELSE 0 END),0),0) AS success_rate, "+
			"COALESCE(SUM(request.input_tokens),0) AS input_tokens, COALESCE(SUM(request.cached_tokens),0) AS cached_tokens, "+
			"COALESCE(SUM(request.output_tokens),0) AS output_tokens, COALESCE(SUM(request.estimated_cost),0) AS estimated_cost, COALESCE(SUM(request.upstream_cost),0) AS upstream_cost").
		Joins("LEFT JOIN relay_attempt_logs AS final_attempt ON final_attempt.id = (SELECT a.id FROM relay_attempt_logs AS a WHERE a.request_id = request.id ORDER BY a.id DESC LIMIT 1)").
		Joins("LEFT JOIN channels AS c ON c.id = final_attempt.channel_id").
		Where("request.created_at >= ? AND request.created_at < ?", startTime, endTime).
		Group("COALESCE(NULLIF(final_attempt.channel_name, ''), c.name, '未归属渠道')").Order("upstream_cost desc").Scan(&summary.Channels).Error; err != nil {
		return nil, err
	}
	for index := range summary.Channels {
		if summary.Channels[index].InputTokens > 0 {
			summary.Channels[index].CacheHitRate = float64(summary.Channels[index].CachedTokens) / float64(summary.Channels[index].InputTokens)
		}
	}
	if err := s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).
		Select("requested_model AS name, COUNT(*) AS requests, "+
			"COALESCE(SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END),0) AS successes, COALESCE(SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END),0) AS canceled_count, "+
			"COALESCE(1.0 * SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) / NULLIF(SUM(CASE WHEN outcome <> 'canceled' THEN 1 ELSE 0 END),0),0) AS success_rate, "+
			"COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(cached_tokens),0) AS cached_tokens, "+
			"COALESCE(SUM(output_tokens),0) AS output_tokens, COALESCE(SUM(estimated_cost),0) AS estimated_cost, COALESCE(SUM(upstream_cost),0) AS upstream_cost").
		Where("created_at >= ? AND created_at < ?", startTime, endTime).Group("requested_model").Order("upstream_cost desc").Scan(&summary.Models).Error; err != nil {
		return nil, err
	}
	for index := range summary.Models {
		if summary.Models[index].InputTokens > 0 {
			summary.Models[index].CacheHitRate = float64(summary.Models[index].CachedTokens) / float64(summary.Models[index].InputTokens)
		}
	}
	return summary, nil
}

func (s *ManagementService) Logs(ctx context.Context, query LogQuery) (*LogPage, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 || query.PageSize > 200 {
		query.PageSize = 50
	}
	detailCutoff := time.Now().UTC().Add(-DetailedLogRetentionDays * 24 * time.Hour)
	filteredLogs := func() *gorm.DB {
		return applyLogFilters(s.store.db.WithContext(ctx).Model(&RelayRequestLog{}), query, detailCutoff)
	}
	summary, err := aggregateLogSummary(filteredLogs())
	if err != nil {
		return nil, err
	}
	var total int64
	if err := filteredLogs().Count(&total).Error; err != nil {
		return nil, err
	}
	var logs []RelayRequestLog
	if err := filteredLogs().Omit("request_body", "response_body").Order("created_at desc, id desc").Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Find(&logs).Error; err != nil {
		return nil, err
	}
	items := make([]RelayRequestView, 0, len(logs))
	for _, log := range logs {
		view, err := s.relayRequestView(ctx, log, false, false)
		if err != nil {
			return nil, err
		}
		items = append(items, view)
	}
	return &LogPage{Items: items, Summary: summary, Total: total, Page: query.Page, PageSize: query.PageSize}, nil
}

func applyLogFilters(db *gorm.DB, query LogQuery, detailCutoff time.Time) *gorm.DB {
	db = db.Where("created_at >= ?", detailCutoff)
	if strings.TrimSpace(query.Model) != "" {
		db = db.Where("requested_model LIKE ?", "%"+strings.TrimSpace(query.Model)+"%")
	}
	if query.StatusCode > 0 {
		db = db.Where("status_code = ?", query.StatusCode)
	}
	switch strings.TrimSpace(query.Outcome) {
	case RelayOutcomeSuccess, RelayOutcomeCanceled, RelayOutcomeFailed:
		db = db.Where("outcome = ?", strings.TrimSpace(query.Outcome))
	}
	if query.TokenID > 0 {
		db = db.Where("token_id = ?", query.TokenID)
	}
	if query.ChannelID > 0 {
		db = db.Where("(SELECT a.channel_id FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id ORDER BY a.id DESC LIMIT 1) = ?", query.ChannelID)
	}
	if !query.From.IsZero() {
		db = db.Where("created_at >= ?", utcQueryTime(query.From))
	}
	if !query.To.IsZero() {
		db = db.Where("created_at <= ?", utcInclusiveMillisecondEnd(query.To))
	}
	return db
}

func aggregateLogSummary(db *gorm.DB) (LogAggregateSummary, error) {
	type aggregateRow struct {
		RequestCount          int64
		SuccessCount          int64
		CanceledCount         int64
		AttemptCount          int64
		InputTokens           int64
		NormalInputTokens     int64
		OutputTokens          int64
		CachedTokens          int64
		CacheWriteTokens      int64
		SentTokens            int64
		EstimatedCost         int64
		UpstreamCost          int64
		TotalFirstTokenMS     int64
		FirstTokenSampleCount int64
		TotalLatencyMS        int64
		LatencySampleCount    int64
		TotalDurationMS       int64
	}
	var row aggregateRow
	if err := db.Select(
		"COUNT(*) AS request_count, " +
			"COALESCE(SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END), 0) AS success_count, " +
			"COALESCE(SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END), 0) AS canceled_count, " +
			"COALESCE(SUM(attempt_count), 0) AS attempt_count, COALESCE(SUM(input_tokens), 0) AS input_tokens, " +
			"COALESCE(SUM(normal_input_tokens), 0) AS normal_input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens, " +
			"COALESCE(SUM(cached_tokens), 0) AS cached_tokens, COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens, " +
			"COALESCE(SUM(sent_tokens), 0) AS sent_tokens, COALESCE(SUM(estimated_cost), 0) AS estimated_cost, " +
			"COALESCE(SUM(upstream_cost), 0) AS upstream_cost, COALESCE(SUM(first_token_ms), 0) AS total_first_token_ms, " +
			"COALESCE(SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END), 0) AS first_token_sample_count, " +
			"COALESCE(SUM(latency_ms), 0) AS total_latency_ms, COALESCE(SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END), 0) AS latency_sample_count, " +
			"COALESCE(SUM(duration_ms), 0) AS total_duration_ms").Scan(&row).Error; err != nil {
		return LogAggregateSummary{}, err
	}
	summary := LogAggregateSummary{
		RequestCount:          row.RequestCount,
		SuccessCount:          row.SuccessCount,
		CanceledCount:         row.CanceledCount,
		AttemptCount:          row.AttemptCount,
		InputTokens:           row.InputTokens,
		NormalInputTokens:     row.NormalInputTokens,
		OutputTokens:          row.OutputTokens,
		CachedTokens:          row.CachedTokens,
		CacheWriteTokens:      row.CacheWriteTokens,
		SentTokens:            row.SentTokens,
		EstimatedCost:         row.EstimatedCost,
		UpstreamCost:          row.UpstreamCost,
		FirstTokenSampleCount: row.FirstTokenSampleCount,
		LatencySampleCount:    row.LatencySampleCount,
		DurationSampleCount:   row.RequestCount,
	}
	if completedRequests := row.RequestCount - row.CanceledCount; completedRequests > 0 {
		summary.SuccessRate = float64(row.SuccessCount) / float64(completedRequests)
	}
	if row.RequestCount > 0 {
		summary.AverageDurationMS = float64(row.TotalDurationMS) / float64(row.RequestCount)
	}
	if row.FirstTokenSampleCount > 0 {
		summary.AverageFirstTokenMS = float64(row.TotalFirstTokenMS) / float64(row.FirstTokenSampleCount)
	}
	if row.LatencySampleCount > 0 {
		summary.AverageLatencyMS = float64(row.TotalLatencyMS) / float64(row.LatencySampleCount)
	}
	return summary, nil
}

func (s *ManagementService) LogDetail(ctx context.Context, requestID string) (*RelayRequestView, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, errors.New("request id is required")
	}
	cutoff := time.Now().UTC().Add(-DetailedLogRetentionDays * 24 * time.Hour)
	var log RelayRequestLog
	if err := s.store.db.WithContext(ctx).Where("id = ? AND created_at >= ?", requestID, cutoff).First(&log).Error; err != nil {
		return nil, err
	}
	view, err := s.relayRequestView(ctx, log, true, true)
	if err != nil {
		return nil, err
	}
	return &view, nil
}
