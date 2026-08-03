package gateway

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Jemonee/simple-openai-gateway/internal/config"
	"github.com/Jemonee/simple-openai-gateway/pkg/core/tx"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const DetailedLogRetentionDays = 5

type Store struct {
	db            *gorm.DB
	secretBox     *SecretBox
	configManager *config.ApplicationConfigManager
}

func NewStore(dataSource *tx.DataSource, configManager *config.ApplicationConfigManager) *Store {
	secretBox, err := NewSecretBox(os.Getenv("GATEWAY_MASTER_KEY"))
	if err != nil {
		panic(err)
	}
	store := &Store{
		db:            dataSource.Db(),
		secretBox:     secretBox,
		configManager: configManager,
	}
	if err := store.migrate(); err != nil {
		panic(fmt.Sprintf("migrate gateway database: %v", err))
	}
	if err := store.bootstrapAdmin(); err != nil {
		panic(fmt.Sprintf("bootstrap gateway administrator: %v", err))
	}
	store.cleanupExpired()
	go store.runCleanup()
	return store
}

func (s *Store) DB() *gorm.DB {
	return s.db
}

func (s *Store) SecretBox() *SecretBox {
	return s.secretBox
}

func (s *Store) migrate() error {
	if err := s.db.AutoMigrate(
		&AdminUser{},
		&AdminSession{},
		&Channel{},
		&GatewayModel{},
		&ChannelModel{},
		&CircuitRecord{},
		&ClientToken{},
		&ClientTokenModel{},
		&RelayRequestLog{},
		&RelaySessionState{},
		&ModelAgentContextWindow{},
		&RelayChatSessionClaim{},
		&RelayAttemptLog{},
		&RelayStepLog{},
		&TokenDailyStat{},
		&GatewayMigration{},
		&ResponseAffinity{},
		&SessionAffinity{},
	); err != nil {
		return err
	}
	if err := s.ensureSessionCandidateIndexes(); err != nil {
		return err
	}
	if err := s.backfillTokenDailyStats(); err != nil {
		return err
	}
	if err := s.backfillCircuitLevels(); err != nil {
		return err
	}
	if err := s.backfillTokenLogFields(); err != nil {
		return err
	}
	if err := s.backfillRelayOutcomes(); err != nil {
		return err
	}
	if err := s.backfillCostFields(); err != nil {
		return err
	}
	if err := s.backfillApplicationOutcomes(); err != nil {
		return err
	}
	if err := s.backfillRequestStatisticsFromFinalAttempts(); err != nil {
		return err
	}
	if err := s.backfillResponsePhaseTimings(); err != nil {
		return err
	}
	if err := s.compressDetailedPayloads(); err != nil {
		return err
	}
	if err := s.backfillCodexAuxiliarySessions(); err != nil {
		return err
	}
	if err := s.backfillCodexThreadSources(); err != nil {
		return err
	}
	if err := s.backfillCodexCompactionTracking(); err != nil {
		return err
	}
	if err := s.backfillModelAgentContextWindows(); err != nil {
		return err
	}
	if err := s.backfillRelaySessionActivity(); err != nil {
		return err
	}
	return s.reclaimSQLiteSpaceOnce()
}

func (s *Store) backfillRelaySessionActivity() error {
	const migrationName = "relay_session_activity_v1"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := db.Exec(`
			UPDATE relay_session_states
			SET last_activity_at = COALESCE(
				(SELECT latest.created_at FROM relay_request_logs AS latest WHERE latest.id = relay_session_states.latest_request_id),
				(SELECT MAX(session_log.created_at) FROM relay_request_logs AS session_log
				 WHERE session_log.token_id = relay_session_states.token_id
				 AND session_log.codex_session_id = relay_session_states.session_id)
			)
			WHERE last_activity_at IS NULL
		`).Error; err != nil {
			return err
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now().UTC()}).Error
	})
}

func (s *Store) ensureSessionCandidateIndexes() error {
	for _, statement := range []string{
		"CREATE INDEX IF NOT EXISTS idx_relay_session_client_recent ON relay_session_states(token_id, client_fingerprint, updated_at DESC)",
		"CREATE INDEX IF NOT EXISTS idx_relay_claim_client_recent ON relay_chat_session_claims(token_id, client_fingerprint, updated_at DESC)",
	} {
		if err := s.db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) backfillCircuitLevels() error {
	const migrationName = "channel_circuit_levels_v1"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := db.Model(&Channel{}).
			Where("circuit_level = ? AND circuit_open_until IS NOT NULL", CircuitLevelClosed).
			Update("circuit_level", CircuitLevelTemporary).Error; err != nil {
			return err
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now()}).Error
	})
}

func (s *Store) reclaimSQLiteSpaceOnce() error {
	if s.db.Dialector.Name() != "sqlite" {
		return nil
	}
	const migrationName = "sqlite_space_reclaim_v1"
	var migration GatewayMigration
	err := s.db.First(&migration, "name = ?", migrationName).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
		return err
	}
	if err := s.db.Exec("VACUUM").Error; err != nil {
		return err
	}
	if err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
		return err
	}
	return s.db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now()}).Error
}

func (s *Store) checkpointSQLiteWAL() {
	if s.db.Dialector.Name() == "sqlite" {
		_ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error
	}
}

func (s *Store) compressDetailedPayloads() error {
	const migrationName = "compressed_detailed_payloads_v1"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		type requestPayloadRow struct {
			ID           string
			RequestBody  string
			ResponseBody string
		}
		var requestRows []requestPayloadRow
		if err := db.Model(&RelayRequestLog{}).
			Select("id, request_body, response_body").
			Where("length(request_body) >= ? OR length(response_body) >= ?", payloadCompressionThreshold, payloadCompressionThreshold).
			FindInBatches(&requestRows, 100, func(batch *gorm.DB, _ int) error {
				for _, row := range requestRows {
					updates := make(map[string]any, 2)
					if compressed := compressStoredPayload([]byte(row.RequestBody)); compressed != row.RequestBody {
						updates["request_body"] = compressed
					}
					if compressed := compressStoredPayload([]byte(row.ResponseBody)); compressed != row.ResponseBody {
						updates["response_body"] = compressed
					}
					if len(updates) > 0 {
						if err := batch.Model(&RelayRequestLog{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
							return err
						}
					}
				}
				return nil
			}).Error; err != nil {
			return err
		}
		type attemptPayloadRow struct {
			ID           uint64
			RequestBody  string
			ResponseBody string
		}
		var attemptRows []attemptPayloadRow
		if err := db.Model(&RelayAttemptLog{}).
			Select("id, request_body, response_body").
			Where("length(request_body) >= ? OR length(response_body) >= ?", payloadCompressionThreshold, payloadCompressionThreshold).
			FindInBatches(&attemptRows, 100, func(batch *gorm.DB, _ int) error {
				for _, row := range attemptRows {
					updates := make(map[string]any, 2)
					if compressed := compressStoredPayload([]byte(row.RequestBody)); compressed != row.RequestBody {
						updates["request_body"] = compressed
					}
					if compressed := compressStoredPayload([]byte(row.ResponseBody)); compressed != row.ResponseBody {
						updates["response_body"] = compressed
					}
					if len(updates) > 0 {
						if err := batch.Model(&RelayAttemptLog{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
							return err
						}
					}
				}
				return nil
			}).Error; err != nil {
			return err
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now()}).Error
	})
}

func (s *Store) backfillRelayOutcomes() error {
	const migrationName = "relay_outcomes_v1"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := db.Model(&RelayRequestLog{}).Where("status_code BETWEEN 200 AND 299 AND error_code = ''").Update("outcome", RelayOutcomeSuccess).Error; err != nil {
			return err
		}
		if err := db.Model(&RelayRequestLog{}).Where("status_code = ? OR error_code = ?", statusClientClosedRequest, "request_canceled").Update("outcome", RelayOutcomeCanceled).Error; err != nil {
			return err
		}
		if err := db.Model(&RelayRequestLog{}).Where("outcome NOT IN ?", []string{RelayOutcomeSuccess, RelayOutcomeCanceled}).Update("outcome", RelayOutcomeFailed).Error; err != nil {
			return err
		}
		if err := db.Model(&RelayAttemptLog{}).Where("success = ?", true).Update("outcome", RelayOutcomeSuccess).Error; err != nil {
			return err
		}
		if err := db.Model(&RelayAttemptLog{}).Where("success = ? AND request_id IN (?)", false, db.Model(&RelayRequestLog{}).Select("id").Where("outcome = ?", RelayOutcomeCanceled)).Update("outcome", RelayOutcomeCanceled).Error; err != nil {
			return err
		}
		if err := db.Model(&RelayAttemptLog{}).Where("outcome NOT IN ?", []string{RelayOutcomeSuccess, RelayOutcomeCanceled}).Update("outcome", RelayOutcomeFailed).Error; err != nil {
			return err
		}
		type canceledDaily struct {
			Date    string
			TokenID uint64
			Count   int64
		}
		var rows []canceledDaily
		if err := db.Model(&RelayRequestLog{}).Select(sqliteEastEightCreatedDate+" AS date, token_id, COUNT(*) AS count").Where("outcome = ?", RelayOutcomeCanceled).Group(sqliteEastEightCreatedDate + ", token_id").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := db.Model(&TokenDailyStat{}).Where("date = ? AND token_id = ?", row.Date, row.TokenID).Update("canceled_count", row.Count).Error; err != nil {
				return err
			}
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now()}).Error
	})
}

func (s *Store) backfillTokenDailyStats() error {
	const migrationName = "token_daily_stats_v1"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var stats []TokenDailyStat
		if err := db.Model(&RelayRequestLog{}).Select(
			sqliteEastEightCreatedDate + " AS date, token_id, COUNT(*) AS request_count, " +
				"SUM(CASE WHEN outcome = 'success' THEN 1 ELSE 0 END) AS success_count, " +
				"SUM(CASE WHEN outcome = 'canceled' THEN 1 ELSE 0 END) AS canceled_count, " +
				"COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(normal_input_tokens),0) AS normal_input_tokens, " +
				"COALESCE(SUM(output_tokens),0) AS output_tokens, " +
				"COALESCE(SUM(cached_tokens),0) AS cached_tokens, COALESCE(SUM(cache_write_tokens),0) AS cache_write_tokens, " +
				"COALESCE(SUM(sent_tokens),0) AS sent_tokens, " +
				"COALESCE(SUM(estimated_cost),0) AS estimated_cost, COALESCE(SUM(estimated_cost),0) AS upstream_cost, " +
				"COALESCE(SUM(first_token_ms),0) AS first_token_ms, SUM(CASE WHEN first_token_ms > 0 THEN 1 ELSE 0 END) AS first_token_samples, " +
				"COALESCE(SUM(latency_ms),0) AS latency_ms, SUM(CASE WHEN latency_ms > 0 THEN 1 ELSE 0 END) AS latency_samples, " +
				"COALESCE(SUM(duration_ms),0) AS duration_ms, COALESCE(SUM(attempt_count),0) AS attempt_count",
		).Group(sqliteEastEightCreatedDate + ", token_id").Scan(&stats).Error; err != nil {
			return err
		}
		if len(stats) > 0 {
			if err := db.Create(&stats).Error; err != nil {
				return err
			}
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now()}).Error
	})
}

func (s *Store) backfillCostFields() error {
	const migrationName = "upstream_cost_fields_v3"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		if err := db.Model(&TokenDailyStat{}).
			Where("upstream_cost = 0 AND estimated_cost <> 0").
			Update("upstream_cost", gorm.Expr("estimated_cost")).Error; err != nil {
			return err
		}

		requestCutoff := time.Now().UTC().Add(-DetailedLogRetentionDays * 24 * time.Hour)
		attemptCutoff := time.Now().Add(-DetailedLogRetentionDays * 24 * time.Hour)
		if err := db.Model(&RelayRequestLog{}).Where("created_at >= ? AND outcome = ?", requestCutoff, RelayOutcomeFailed).Updates(map[string]any{
			"estimated_cost": 0,
			"upstream_cost":  0,
			"cost_source":    CostSourceFailedZero,
		}).Error; err != nil {
			return err
		}
		if err := db.Model(&RelayRequestLog{}).Where("created_at >= ? AND status_code BETWEEN 200 AND 299 AND cost_source = ''", requestCutoff).Updates(map[string]any{
			"upstream_cost": gorm.Expr("estimated_cost"),
			"cost_source":   CostSourceFallback,
		}).Error; err != nil {
			return err
		}
		if err := db.Model(&RelayAttemptLog{}).Where("created_at >= ? AND outcome = ?", attemptCutoff, RelayOutcomeFailed).Updates(map[string]any{
			"estimated_cost": 0,
			"upstream_cost":  0,
			"cost_source":    CostSourceFailedZero,
		}).Error; err != nil {
			return err
		}
		if err := db.Model(&RelayAttemptLog{}).Where("created_at >= ? AND success = ? AND status_code BETWEEN 200 AND 299 AND cost_source = ''", attemptCutoff, true).Updates(map[string]any{
			"upstream_cost": gorm.Expr("estimated_cost"),
			"cost_source":   CostSourceFallback,
		}).Error; err != nil {
			return err
		}

		var dates []string
		if err := db.Model(&RelayRequestLog{}).Distinct(sqliteEastEightCreatedDate).Where("created_at >= ?", requestCutoff).Pluck(sqliteEastEightCreatedDate, &dates).Error; err != nil {
			return err
		}
		if len(dates) > 0 {
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
			if len(stats) > 0 {
				if err := db.Create(&stats).Error; err != nil {
					return err
				}
			}
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now()}).Error
	})
}

func (s *Store) backfillTokenLogFields() error {
	const migrationName = "token_log_fields_v2"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		normalInputSQL := "CASE WHEN input_tokens > cached_tokens + cache_write_tokens THEN input_tokens - cached_tokens - cache_write_tokens ELSE 0 END"
		for _, table := range []string{"relay_request_logs", "relay_attempt_logs", "token_daily_stats"} {
			if err := db.Table(table).Where("1 = 1").Updates(map[string]any{
				"normal_input_tokens": gorm.Expr(normalInputSQL),
				"sent_tokens":         gorm.Expr("input_tokens"),
			}).Error; err != nil {
				return err
			}
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now()}).Error
	})
}

func (s *Store) bootstrapAdmin() error {
	var count int64
	if err := s.db.Model(&AdminUser{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	username := strings.TrimSpace(os.Getenv("GATEWAY_ADMIN_USERNAME"))
	password := os.Getenv("GATEWAY_ADMIN_PASSWORD")
	if username == "" || password == "" {
		return errorsForBootstrap()
	}
	if len(password) < 12 {
		return fmt.Errorf("GATEWAY_ADMIN_PASSWORD must contain at least 12 characters")
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	return s.db.Create(&AdminUser{
		Username:     username,
		PasswordHash: string(passwordHash),
		Enabled:      true,
	}).Error
}

func errorsForBootstrap() error {
	return fmt.Errorf("GATEWAY_ADMIN_USERNAME and GATEWAY_ADMIN_PASSWORD are required when no administrator exists")
}

func (s *Store) cleanupExpired() {
	now := time.Now()
	_ = s.db.Where("expires_at < ?", now).Delete(&AdminSession{}).Error
	_ = s.db.Where("expires_at < ?", now).Delete(&ResponseAffinity{}).Error
	_ = s.db.Where("expires_at < ?", now).Delete(&SessionAffinity{}).Error
	detailCutoff := now.Add(-DetailedLogRetentionDays * 24 * time.Hour)
	requestCutoff := now.UTC().Add(-DetailedLogRetentionDays * 24 * time.Hour)
	_ = s.db.Where("created_at < ?", detailCutoff).Delete(&RelayAttemptLog{}).Error
	_ = s.db.Where("created_at < ?", detailCutoff).Delete(&RelayStepLog{}).Error
	_ = s.db.Where("created_at < ?", requestCutoff).Delete(&RelayRequestLog{}).Error
	_ = s.db.Where("updated_at < ?", detailCutoff).Delete(&RelaySessionState{}).Error
	_ = s.db.Where("updated_at < ?", detailCutoff).Delete(&RelayChatSessionClaim{}).Error
	s.checkpointSQLiteWAL()
}

func (s *Store) runCleanup() {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		s.cleanupExpired()
	}
}
