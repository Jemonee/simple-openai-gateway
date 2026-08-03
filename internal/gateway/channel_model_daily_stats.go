package gateway

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	channelModelDailyCostsMigration      = "channel_model_daily_stats_v1"
	channelModelDailyAggregatesMigration = "channel_model_daily_stats_v2"
)

var channelModelDailyAggregateColumns = []string{
	"request_count",
	"attempt_count",
	"success_count",
	"failed_count",
	"canceled_count",
	"input_tokens",
	"normal_input_tokens",
	"output_tokens",
	"cached_tokens",
	"cache_write_tokens",
	"sent_tokens",
	"estimated_cost",
	"upstream_cost",
	"upstream_cost_count",
	"fallback_cost_count",
	"mixed_cost_count",
	"failed_zero_cost_count",
	"first_token_ms",
	"first_token_samples",
	"latency_ms",
	"latency_samples",
	"duration_ms",
	"duration_samples",
	"status1xx_count",
	"status2xx_count",
	"status3xx_count",
	"status4xx_count",
	"status5xx_count",
	"no_status_count",
}

func (s *Store) backfillChannelModelDailyStats() error {
	if err := s.backfillChannelModelDailyCosts(); err != nil {
		return err
	}
	return s.backfillChannelModelDailyAggregates()
}

func (s *Store) backfillChannelModelDailyCosts() error {
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", channelModelDailyCostsMigration).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var stats []ChannelModelDailyStat
		if err := db.Model(&RelayAttemptLog{}).
			Select(sqliteEastEightCreatedDate + " AS date, channel_id, channel_model_id, " +
				"COALESCE(SUM(CASE WHEN upstream_cost > 0 THEN upstream_cost ELSE 0 END), 0) AS upstream_cost").
			Where("channel_id > 0 AND channel_model_id > 0").
			Group(sqliteEastEightCreatedDate + ", channel_id, channel_model_id").
			Scan(&stats).Error; err != nil {
			return err
		}
		if len(stats) > 0 {
			if err := db.Create(&stats).Error; err != nil {
				return err
			}
		}
		return db.Create(&GatewayMigration{Name: channelModelDailyCostsMigration, AppliedAt: time.Now()}).Error
	})
}

func (s *Store) backfillChannelModelDailyAggregates() error {
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", channelModelDailyAggregatesMigration).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var stats []ChannelModelDailyStat
		if err := db.Model(&RelayAttemptLog{}).Select(channelModelDailyAggregateSelect()).
			Where("channel_id > 0 AND channel_model_id > 0").
			Group(sqliteEastEightCreatedDate + ", channel_id, channel_model_id").
			Scan(&stats).Error; err != nil {
			return err
		}
		if len(stats) > 0 {
			if err := db.Clauses(clause.OnConflict{
				Columns:   channelModelDailyConflictColumns(),
				DoUpdates: channelModelDailyReplacementAssignments(time.Now()),
			}).Create(&stats).Error; err != nil {
				return err
			}
		}
		return db.Create(&GatewayMigration{Name: channelModelDailyAggregatesMigration, AppliedAt: time.Now()}).Error
	})
}

func channelModelDailyAggregateSelect() string {
	return sqliteEastEightCreatedDate + " AS date, channel_id, channel_model_id, " +
		"COUNT(DISTINCT request_id) AS request_count, COUNT(*) AS attempt_count, " +
		"SUM(CASE WHEN outcome = 'success' OR success = 1 THEN 1 ELSE 0 END) AS success_count, " +
		"SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END) AS canceled_count, " +
		"SUM(CASE WHEN outcome NOT IN ('success', 'canceled') AND success = 0 THEN 1 ELSE 0 END) AS failed_count, " +
		"COALESCE(SUM(CASE WHEN input_tokens > 0 THEN input_tokens ELSE 0 END), 0) AS input_tokens, " +
		"COALESCE(SUM(CASE WHEN normal_input_tokens > 0 THEN normal_input_tokens ELSE 0 END), 0) AS normal_input_tokens, " +
		"COALESCE(SUM(CASE WHEN output_tokens > 0 THEN output_tokens ELSE 0 END), 0) AS output_tokens, " +
		"COALESCE(SUM(CASE WHEN cached_tokens > 0 THEN cached_tokens ELSE 0 END), 0) AS cached_tokens, " +
		"COALESCE(SUM(CASE WHEN cache_write_tokens > 0 THEN cache_write_tokens ELSE 0 END), 0) AS cache_write_tokens, " +
		"COALESCE(SUM(CASE WHEN sent_tokens > 0 THEN sent_tokens ELSE 0 END), 0) AS sent_tokens, " +
		"COALESCE(SUM(CASE WHEN estimated_cost > 0 THEN estimated_cost ELSE 0 END), 0) AS estimated_cost, " +
		"COALESCE(SUM(CASE WHEN upstream_cost > 0 THEN upstream_cost ELSE 0 END), 0) AS upstream_cost, " +
		"SUM(CASE WHEN cost_source = 'upstream' THEN 1 ELSE 0 END) AS upstream_cost_count, " +
		"SUM(CASE WHEN cost_source = 'estimated_fallback' THEN 1 ELSE 0 END) AS fallback_cost_count, " +
		"SUM(CASE WHEN cost_source = 'mixed' THEN 1 ELSE 0 END) AS mixed_cost_count, " +
		"SUM(CASE WHEN cost_source = 'failed_zero' THEN 1 ELSE 0 END) AS failed_zero_cost_count, " +
		"COALESCE(SUM(CASE WHEN first_token_ms > 0 THEN first_token_ms ELSE 0 END), 0) AS first_token_ms, " +
		"SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END) AS first_token_samples, " +
		"COALESCE(SUM(CASE WHEN latency_ms > 0 THEN latency_ms ELSE 0 END), 0) AS latency_ms, " +
		"SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END) AS latency_samples, " +
		"COALESCE(SUM(CASE WHEN duration_ms > 0 THEN duration_ms ELSE 0 END), 0) AS duration_ms, " +
		"SUM(CASE WHEN duration_ms > 0 THEN 1 ELSE 0 END) AS duration_samples, " +
		"SUM(CASE WHEN status_code BETWEEN 100 AND 199 THEN 1 ELSE 0 END) AS status1xx_count, " +
		"SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) AS status2xx_count, " +
		"SUM(CASE WHEN status_code BETWEEN 300 AND 399 THEN 1 ELSE 0 END) AS status3xx_count, " +
		"SUM(CASE WHEN status_code BETWEEN 400 AND 499 THEN 1 ELSE 0 END) AS status4xx_count, " +
		"SUM(CASE WHEN status_code BETWEEN 500 AND 599 THEN 1 ELSE 0 END) AS status5xx_count, " +
		"SUM(CASE WHEN status_code <= 0 THEN 1 ELSE 0 END) AS no_status_count"
}

func upsertChannelModelDailyStats(db *gorm.DB, attempts []RelayAttemptLog, now time.Time) error {
	type dailyKey struct {
		date           string
		channelID      uint64
		channelModelID uint64
	}
	statsByKey := make(map[dailyKey]*ChannelModelDailyStat)
	requestIDsByKey := make(map[dailyKey]map[string]struct{})
	for _, attempt := range attempts {
		if attempt.ChannelID == 0 || attempt.ChannelModelID == 0 {
			continue
		}
		createdAt := attempt.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		key := dailyKey{date: eastEightDate(createdAt), channelID: attempt.ChannelID, channelModelID: attempt.ChannelModelID}
		stat := statsByKey[key]
		if stat == nil {
			stat = &ChannelModelDailyStat{
				Date: key.date, ChannelID: key.channelID, ChannelModelID: key.channelModelID,
				CreatedAt: now, UpdatedAt: now,
			}
			statsByKey[key] = stat
			requestIDsByKey[key] = make(map[string]struct{})
		}
		requestIDsByKey[key][attempt.RequestID] = struct{}{}
		addAttemptToChannelModelDailyStat(stat, attempt)
	}
	if len(statsByKey) == 0 {
		return nil
	}
	stats := make([]ChannelModelDailyStat, 0, len(statsByKey))
	for key, stat := range statsByKey {
		stat.RequestCount = int64(len(requestIDsByKey[key]))
		stats = append(stats, *stat)
	}
	return db.Clauses(clause.OnConflict{
		Columns:   channelModelDailyConflictColumns(),
		DoUpdates: channelModelDailyIncrementAssignments(now),
	}).Create(&stats).Error
}

func addAttemptToChannelModelDailyStat(stat *ChannelModelDailyStat, attempt RelayAttemptLog) {
	stat.AttemptCount++
	if attempt.Outcome == RelayOutcomeSuccess || attempt.Success {
		stat.SuccessCount++
	} else if attempt.Outcome == RelayOutcomeCanceled {
		stat.CanceledCount++
	} else {
		stat.FailedCount++
	}
	stat.InputTokens += max(attempt.InputTokens, int64(0))
	stat.NormalInputTokens += max(attempt.NormalInputTokens, int64(0))
	stat.OutputTokens += max(attempt.OutputTokens, int64(0))
	stat.CachedTokens += max(attempt.CachedTokens, int64(0))
	stat.CacheWriteTokens += max(attempt.CacheWriteTokens, int64(0))
	stat.SentTokens += max(attempt.SentTokens, int64(0))
	stat.EstimatedCost += max(attempt.EstimatedCost, int64(0))
	stat.UpstreamCost += max(attempt.UpstreamCost, int64(0))
	switch attempt.CostSource {
	case CostSourceUpstream:
		stat.UpstreamCostCount++
	case CostSourceFallback:
		stat.FallbackCostCount++
	case CostSourceMixed:
		stat.MixedCostCount++
	case CostSourceFailedZero:
		stat.FailedZeroCostCount++
	}
	if attempt.FirstTokenMS > 0 {
		stat.FirstTokenMS += attempt.FirstTokenMS
		stat.FirstTokenSamples++
	}
	if attempt.LatencyMS > 0 {
		stat.LatencyMS += attempt.LatencyMS
		stat.LatencySamples++
	}
	if attempt.DurationMS > 0 {
		stat.DurationMS += attempt.DurationMS
		stat.DurationSamples++
	}
	switch {
	case attempt.StatusCode >= 100 && attempt.StatusCode < 200:
		stat.Status1xxCount++
	case attempt.StatusCode >= 200 && attempt.StatusCode < 300:
		stat.Status2xxCount++
	case attempt.StatusCode >= 300 && attempt.StatusCode < 400:
		stat.Status3xxCount++
	case attempt.StatusCode >= 400 && attempt.StatusCode < 500:
		stat.Status4xxCount++
	case attempt.StatusCode >= 500 && attempt.StatusCode < 600:
		stat.Status5xxCount++
	case attempt.StatusCode <= 0:
		stat.NoStatusCount++
	}
}

func channelModelDailyConflictColumns() []clause.Column {
	return []clause.Column{{Name: "date"}, {Name: "channel_id"}, {Name: "channel_model_id"}}
}

func channelModelDailyIncrementAssignments(now time.Time) clause.Set {
	assignments := make(map[string]any, len(channelModelDailyAggregateColumns)+1)
	for _, column := range channelModelDailyAggregateColumns {
		assignments[column] = gorm.Expr(column + " + excluded." + column)
	}
	assignments["updated_at"] = now
	return clause.Assignments(assignments)
}

func channelModelDailyReplacementAssignments(now time.Time) clause.Set {
	assignments := make(map[string]any, len(channelModelDailyAggregateColumns))
	for _, column := range channelModelDailyAggregateColumns {
		if column != "upstream_cost" {
			assignments[column] = gorm.Expr("excluded." + column)
		}
	}
	assignments["updated_at"] = now
	return clause.Assignments(assignments)
}

func loadChannelCumulativeCosts(ctx context.Context, db *gorm.DB, channelIDs []uint64) (map[uint64]int64, error) {
	costs := make(map[uint64]int64, len(channelIDs))
	for _, channelID := range channelIDs {
		costs[channelID] = 0
	}
	if len(channelIDs) == 0 {
		return costs, nil
	}
	type channelCostRow struct {
		ChannelID    uint64
		UpstreamCost int64
	}
	todayStart := eastEightStartOfDay(time.Now()).UTC()
	tomorrowStart := todayStart.Add(24 * time.Hour)
	today := eastEightDate(todayStart)
	var historicalRows []channelCostRow
	if err := db.WithContext(ctx).Model(&ChannelModelDailyStat{}).
		Select("channel_id, COALESCE(SUM(upstream_cost), 0) AS upstream_cost").
		Where("channel_id IN ? AND date < ?", channelIDs, today).
		Group("channel_id").
		Scan(&historicalRows).Error; err != nil {
		return nil, err
	}
	for _, row := range historicalRows {
		costs[row.ChannelID] += max(row.UpstreamCost, int64(0))
	}
	var todayRows []channelCostRow
	if err := db.WithContext(ctx).Model(&RelayAttemptLog{}).
		Select("channel_id, COALESCE(SUM(CASE WHEN upstream_cost > 0 THEN upstream_cost ELSE 0 END), 0) AS upstream_cost").
		Where("channel_id IN ? AND created_at >= ? AND created_at < ?", channelIDs, todayStart, tomorrowStart).
		Group("channel_id").
		Scan(&todayRows).Error; err != nil {
		return nil, err
	}
	for _, row := range todayRows {
		costs[row.ChannelID] += max(row.UpstreamCost, int64(0))
	}
	return costs, nil
}
