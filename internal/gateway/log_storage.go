package gateway

import (
	"context"
	"time"
)

type LogStorageUsage struct {
	CutoffAt            time.Time `json:"cutoffAt"`
	PayloadBytes        int64     `json:"payloadBytes"`
	RequestPayloadBytes int64     `json:"requestPayloadBytes"`
	AttemptPayloadBytes int64     `json:"attemptPayloadBytes"`
}

func (s *ManagementService) LogStorageUsage(ctx context.Context) (*LogStorageUsage, error) {
	result := &LogStorageUsage{CutoffAt: time.Now().UTC().Add(-historicalPayloadCleanupAge)}
	if err := s.store.db.WithContext(ctx).Model(&RelayRequestLog{}).
		Select("COALESCE(SUM(length(request_parameters_json) + length(request_body) + length(response_body)), 0)").
		Scan(&result.RequestPayloadBytes).Error; err != nil {
		return nil, err
	}
	if err := s.store.db.WithContext(ctx).Model(&RelayAttemptLog{}).
		Select("COALESCE(SUM(length(request_body) + length(response_body)), 0)").
		Scan(&result.AttemptPayloadBytes).Error; err != nil {
		return nil, err
	}
	result.PayloadBytes = result.RequestPayloadBytes + result.AttemptPayloadBytes
	return result, nil
}
