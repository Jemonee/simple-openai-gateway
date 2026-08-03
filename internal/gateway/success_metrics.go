package gateway

import (
	"context"
	"time"

	"gorm.io/gorm"
)

const recentSuccessWindow = 30 * time.Minute

type recentSuccessMetric struct {
	Successes int64
	Attempts  int64
}

func (metric recentSuccessMetric) rate() float64 {
	if metric.Attempts == 0 {
		return 1
	}
	return float64(metric.Successes) / float64(metric.Attempts)
}

type recentSuccessMetrics struct {
	ByChannel      map[uint64]recentSuccessMetric
	ByChannelModel map[uint64]recentSuccessMetric
}

func loadRecentSuccessMetrics(ctx context.Context, db *gorm.DB, channelIDs []uint64, channelModelIDs []uint64, now time.Time) (recentSuccessMetrics, error) {
	metrics := recentSuccessMetrics{
		ByChannel:      make(map[uint64]recentSuccessMetric, len(channelIDs)),
		ByChannelModel: make(map[uint64]recentSuccessMetric, len(channelModelIDs)),
	}
	for _, channelID := range channelIDs {
		metrics.ByChannel[channelID] = recentSuccessMetric{}
	}
	for _, channelModelID := range channelModelIDs {
		metrics.ByChannelModel[channelModelID] = recentSuccessMetric{}
	}
	if len(channelIDs) == 0 && len(channelModelIDs) == 0 {
		return metrics, nil
	}

	type aggregateRow struct {
		ChannelID      uint64
		ChannelModelID uint64
		Successes      int64
		Attempts       int64
	}
	var rows []aggregateRow
	query := db.WithContext(ctx).Model(&RelayAttemptLog{}).
		Select("channel_id, channel_model_id, SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) AS successes, COUNT(*) AS attempts").
		Where("created_at >= ? AND outcome <> ?", utcQueryTime(now.Add(-recentSuccessWindow)), RelayOutcomeCanceled)
	if len(channelIDs) > 0 {
		query = query.Where("channel_id IN ?", channelIDs)
	} else {
		query = query.Where("channel_model_id IN ?", channelModelIDs)
	}
	if err := query.Group("channel_id, channel_model_id").Scan(&rows).Error; err != nil {
		return recentSuccessMetrics{}, err
	}
	for _, row := range rows {
		channelMetric := metrics.ByChannel[row.ChannelID]
		channelMetric.Successes += row.Successes
		channelMetric.Attempts += row.Attempts
		metrics.ByChannel[row.ChannelID] = channelMetric
		metrics.ByChannelModel[row.ChannelModelID] = recentSuccessMetric{Successes: row.Successes, Attempts: row.Attempts}
	}
	return metrics, nil
}

func loadTodayChannelAttemptCounts(ctx context.Context, db *gorm.DB, channelIDs []uint64, now time.Time) (map[uint64]int64, error) {
	counts := make(map[uint64]int64, len(channelIDs))
	for _, channelID := range channelIDs {
		counts[channelID] = 0
	}
	if len(channelIDs) == 0 {
		return counts, nil
	}

	type aggregateRow struct {
		ChannelID uint64
		Attempts  int64
	}
	var rows []aggregateRow
	if err := db.WithContext(ctx).Model(&RelayAttemptLog{}).
		Select("channel_id, COUNT(*) AS attempts").
		Where("channel_id IN ? AND created_at >= ? AND created_at <= ?", channelIDs, eastEightStartOfDay(now).UTC(), now.UTC()).
		Group("channel_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts[row.ChannelID] = row.Attempts
	}
	return counts, nil
}
