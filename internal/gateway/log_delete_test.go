package gateway

import (
	"context"
	"testing"
	"time"
)

func TestDeleteHistoricalLogsRemovesAssociatesKeepsDailyStats(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	old := RelayRequestLog{ID: "delete-old", TokenID: 1, CreatedAt: now.Add(-10 * 24 * time.Hour)}
	keep := RelayRequestLog{ID: "delete-keep", TokenID: 1, CreatedAt: now.Add(-2 * 24 * time.Hour)}
	if err := store.db.Create(&[]RelayRequestLog{old, keep}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelayAttemptLog{RequestID: old.ID, CreatedAt: old.CreatedAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelayStepLog{RequestID: old.ID, CreatedAt: old.CreatedAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&TokenDailyStat{Date: "2026-07-01", TokenID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := NewManagementService(store).DeleteHistoricalLogsBefore(context.Background(), 7)
	if err != nil || result.RequestLogsDeleted != 1 || result.AttemptLogsDeleted != 1 || result.StepLogsDeleted != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var count int64
	if err := store.db.Model(&TokenDailyStat{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("daily stats count=%d err=%v", count, err)
	}
}
