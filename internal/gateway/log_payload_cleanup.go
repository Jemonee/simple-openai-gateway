package gateway

import (
	"context"
	"time"

	"github.com/Jemonee/simple-openai-gateway/internal/config"

	"gorm.io/gorm"
)

const historicalPayloadCleanupAge = 30 * time.Minute

type LogPayloadCleanupResult struct {
	CutoffAt           time.Time `json:"cutoffAt"`
	RequestLogsCleared int64     `json:"requestLogsCleared"`
	AttemptLogsCleared int64     `json:"attemptLogsCleared"`
}

func (s *ManagementService) ClearHistoricalLogPayloads(ctx context.Context) (*LogPayloadCleanupResult, error) {
	return s.clearHistoricalLogPayloads(ctx, time.Now().UTC())
}

func (s *ManagementService) clearHistoricalLogPayloads(ctx context.Context, now time.Time) (*LogPayloadCleanupResult, error) {
	cutoff := now.UTC().Add(-historicalPayloadCleanupAge)
	result := &LogPayloadCleanupResult{CutoffAt: cutoff}
	err := s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		requestIDs := db.Model(&RelayRequestLog{}).Select("id").Where("created_at < ?", cutoff)
		attemptUpdate := db.Model(&RelayAttemptLog{}).
			Where("request_id IN (?)", requestIDs).
			Where("payload_log_detail <> ? OR request_body <> '' OR response_body <> '' OR request_body_truncated = ? OR response_body_truncated = ?", config.PayloadLogDetailNone, true, true).
			Updates(map[string]any{
				"payload_log_detail":      config.PayloadLogDetailNone,
				"request_body":            "",
				"request_body_truncated":  false,
				"response_body":           "",
				"response_body_truncated": false,
			})
		if attemptUpdate.Error != nil {
			return attemptUpdate.Error
		}
		result.AttemptLogsCleared = attemptUpdate.RowsAffected

		requestUpdate := db.Model(&RelayRequestLog{}).
			Where("created_at < ?", cutoff).
			Where("payload_log_detail <> ? OR request_parameters_json <> '' OR request_body <> '' OR response_body <> '' OR request_body_truncated = ? OR response_body_truncated = ?", config.PayloadLogDetailNone, true, true).
			Updates(map[string]any{
				"payload_log_detail":      config.PayloadLogDetailNone,
				"request_parameters_json": "",
				"request_body":            "",
				"request_body_truncated":  false,
				"response_body":           "",
				"response_body_truncated": false,
			})
		if requestUpdate.Error != nil {
			return requestUpdate.Error
		}
		result.RequestLogsCleared = requestUpdate.RowsAffected
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.store.checkpointSQLiteWAL()
	return result, nil
}
