package gateway

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

type LogDeleteResult struct {
	CutoffAt            time.Time `json:"cutoffAt"`
	RequestLogsDeleted  int64     `json:"requestLogsDeleted"`
	AttemptLogsDeleted  int64     `json:"attemptLogsDeleted"`
	StepLogsDeleted     int64     `json:"stepLogsDeleted"`
	ResourceRefsDeleted int64     `json:"resourceRefsDeleted"`
}

func (s *ManagementService) DeleteHistoricalLogsBefore(ctx context.Context, days int) (*LogDeleteResult, error) {
	if days <= 0 {
		return nil, errors.New("删除天数必须为正整数")
	}
	now := time.Now().UTC()
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	result := &LogDeleteResult{CutoffAt: cutoff}
	err := s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		requestIDs := db.Model(&RelayRequestLog{}).Select("id").Where("created_at < ?", cutoff)
		attemptIDs := db.Model(&RelayAttemptLog{}).Select("id").Where("created_at < ? OR request_id IN (?)", cutoff, requestIDs)
		var count int64
		if err := db.Model(&RelayAttemptLog{}).Where("request_id IN (?)", requestIDs).Count(&count).Error; err != nil {
			return err
		}
		result.AttemptLogsDeleted = count
		if err := db.Where("attempt_id IN (?)", attemptIDs).Delete(&RelayAttemptPayload{}).Error; err != nil {
			return err
		}
		if err := db.Where("request_id IN (?)", requestIDs).Delete(&RelayAttemptLog{}).Error; err != nil {
			return err
		}
		if err := db.Model(&RelayStepLog{}).Where("request_id IN (?)", requestIDs).Count(&count).Error; err != nil {
			return err
		}
		result.StepLogsDeleted = count
		if err := db.Where("request_id IN (?)", requestIDs).Delete(&RelayStepLog{}).Error; err != nil {
			return err
		}
		if err := db.Model(&RequestResource{}).Where("request_id IN (?)", requestIDs).Count(&count).Error; err != nil {
			return err
		}
		result.ResourceRefsDeleted = count
		if err := db.Where("request_id IN (?)", requestIDs).Delete(&RequestResource{}).Error; err != nil {
			return err
		}
		if err := db.Model(&RelayRequestLog{}).Where("created_at < ?", cutoff).Count(&count).Error; err != nil {
			return err
		}
		result.RequestLogsDeleted = count
		if err := db.Where("request_id IN (?)", requestIDs).Delete(&RelayRequestPayload{}).Error; err != nil {
			return err
		}
		if err := db.Where("created_at < ?", cutoff).Delete(&RelayRequestLog{}).Error; err != nil {
			return err
		}
		// Remove only auxiliary session records that no remaining detailed request references.
		if err := db.Where("NOT EXISTS (SELECT 1 FROM relay_request_logs r WHERE r.token_id = relay_session_states.token_id AND r.codex_session_id = relay_session_states.session_id)").Delete(&RelaySessionState{}).Error; err != nil {
			return err
		}
		if err := db.Where("NOT EXISTS (SELECT 1 FROM relay_request_logs r WHERE r.token_id = relay_chat_session_claims.token_id AND r.codex_session_id = relay_chat_session_claims.session_id)").Delete(&RelayChatSessionClaim{}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.store.checkpointSQLiteWAL()
	return result, nil
}
