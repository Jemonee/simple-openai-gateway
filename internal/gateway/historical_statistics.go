package gateway

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

const finalAttemptStatisticsMigration = "final_attempt_statistics_v1"
const responsePhaseTimingsMigration = "response_phase_timings_v1"

// backfillRequestStatisticsFromFinalAttempts removes retry-only usage from retained request totals.
func (s *Store) backfillRequestStatisticsFromFinalAttempts() error {
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", finalAttemptStatisticsMigration).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		cutoff := time.Now().UTC().Add(-DetailedLogRetentionDays * 24 * time.Hour)
		if err := db.Exec(`
			UPDATE relay_request_logs
			SET input_tokens = COALESCE((SELECT a.input_tokens FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id ORDER BY a.id DESC LIMIT 1), input_tokens),
				normal_input_tokens = COALESCE((SELECT a.normal_input_tokens FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id ORDER BY a.id DESC LIMIT 1), normal_input_tokens),
				output_tokens = COALESCE((SELECT a.output_tokens FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id ORDER BY a.id DESC LIMIT 1), output_tokens),
				cached_tokens = COALESCE((SELECT a.cached_tokens FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id ORDER BY a.id DESC LIMIT 1), cached_tokens),
				cache_write_tokens = COALESCE((SELECT a.cache_write_tokens FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id ORDER BY a.id DESC LIMIT 1), cache_write_tokens),
				estimated_cost = COALESCE((SELECT a.estimated_cost FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id ORDER BY a.id DESC LIMIT 1), estimated_cost),
				upstream_cost = COALESCE((SELECT a.upstream_cost FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id ORDER BY a.id DESC LIMIT 1), upstream_cost),
				cost_source = COALESCE((SELECT a.cost_source FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id ORDER BY a.id DESC LIMIT 1), cost_source),
				usage_source = COALESCE((SELECT a.usage_source FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id ORDER BY a.id DESC LIMIT 1), usage_source)
			WHERE created_at >= ? AND EXISTS (SELECT 1 FROM relay_attempt_logs a WHERE a.request_id = relay_request_logs.id)
		`, cutoff).Error; err != nil {
			return err
		}
		if err := rebuildTokenDailyStats(db); err != nil {
			return err
		}
		return db.Create(&GatewayMigration{Name: finalAttemptStatisticsMigration, AppliedAt: time.Now()}).Error
	})
}

func (s *Store) backfillResponsePhaseTimings() error {
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", responsePhaseTimingsMigration).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var dates []string
		if err := db.Model(&RelayRequestLog{}).Distinct(sqliteEastEightCreatedDate).Pluck(sqliteEastEightCreatedDate, &dates).Error; err != nil {
			return err
		}
		for _, table := range []string{"relay_request_logs", "relay_attempt_logs"} {
			if err := db.Exec(`UPDATE ` + table + `
				SET first_token_ms = CASE
						WHEN first_token_ms <= 0 THEN 0
						WHEN first_token_ms > latency_ms THEN first_token_ms - latency_ms
						ELSE 1
					END,
					duration_ms = CASE WHEN duration_ms > latency_ms THEN duration_ms - latency_ms ELSE 0 END`).Error; err != nil {
				return err
			}
		}
		if err := rebuildTokenDailyStatsForDates(db, dates); err != nil {
			return err
		}
		return db.Create(&GatewayMigration{Name: responsePhaseTimingsMigration, AppliedAt: time.Now()}).Error
	})
}

func rebuildTokenDailyStats(db *gorm.DB) error {
	if err := db.Where("1 = 1").Delete(&TokenDailyStat{}).Error; err != nil {
		return err
	}
	var stats []TokenDailyStat
	if err := db.Model(&RelayRequestLog{}).Select(
		sqliteEastEightCreatedDate + " AS date, token_id, COUNT(*) AS request_count, " +
			"SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) AS success_count, " +
			"SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END) AS canceled_count, " +
			"COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(normal_input_tokens),0) AS normal_input_tokens, " +
			"COALESCE(SUM(output_tokens),0) AS output_tokens, COALESCE(SUM(cached_tokens),0) AS cached_tokens, " +
			"COALESCE(SUM(cache_write_tokens),0) AS cache_write_tokens, COALESCE(SUM(sent_tokens),0) AS sent_tokens, " +
			"COALESCE(SUM(estimated_cost),0) AS estimated_cost, COALESCE(SUM(upstream_cost),0) AS upstream_cost, " +
			"COALESCE(SUM(first_token_ms),0) AS first_token_ms, SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END) AS first_token_samples, " +
			"COALESCE(SUM(latency_ms),0) AS latency_ms, SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END) AS latency_samples, " +
			"COALESCE(SUM(duration_ms),0) AS duration_ms, COALESCE(SUM(attempt_count),0) AS attempt_count",
	).Group(sqliteEastEightCreatedDate + ", token_id").Scan(&stats).Error; err != nil {
		return err
	}
	if len(stats) == 0 {
		return nil
	}
	return db.Create(&stats).Error
}

func rebuildTokenDailyStatsForDates(db *gorm.DB, dates []string) error {
	if len(dates) == 0 {
		return nil
	}
	if err := db.Where("date IN ?", dates).Delete(&TokenDailyStat{}).Error; err != nil {
		return err
	}
	var stats []TokenDailyStat
	if err := db.Model(&RelayRequestLog{}).Select(
		sqliteEastEightCreatedDate+" AS date, token_id, COUNT(*) AS request_count, "+
			"SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) AS success_count, "+
			"SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END) AS canceled_count, "+
			"COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(normal_input_tokens),0) AS normal_input_tokens, "+
			"COALESCE(SUM(output_tokens),0) AS output_tokens, COALESCE(SUM(cached_tokens),0) AS cached_tokens, "+
			"COALESCE(SUM(cache_write_tokens),0) AS cache_write_tokens, COALESCE(SUM(sent_tokens),0) AS sent_tokens, "+
			"COALESCE(SUM(estimated_cost),0) AS estimated_cost, COALESCE(SUM(upstream_cost),0) AS upstream_cost, "+
			"COALESCE(SUM(first_token_ms),0) AS first_token_ms, SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END) AS first_token_samples, "+
			"COALESCE(SUM(latency_ms),0) AS latency_ms, SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END) AS latency_samples, "+
			"COALESCE(SUM(duration_ms),0) AS duration_ms, COALESCE(SUM(attempt_count),0) AS attempt_count",
	).Where(sqliteEastEightCreatedDate+" IN ?", dates).Group(sqliteEastEightCreatedDate + ", token_id").Scan(&stats).Error; err != nil {
		return err
	}
	if len(stats) == 0 {
		return nil
	}
	return db.Create(&stats).Error
}
