package gateway

import (
	"context"
	"testing"
)

func TestLogStorageUsageCountsRetainedPayloadBytes(t *testing.T) {
	store := newTestStore(t)
	if err := store.db.Create(&RelayRequestLog{ID: "request-1", RequestParametersJSON: "1234", RequestBody: "567", ResponseBody: "89"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelayAttemptLog{RequestID: "request-1", RequestBody: "abcd", ResponseBody: "ef"}).Error; err != nil {
		t.Fatal(err)
	}

	usage, err := NewManagementService(store).LogStorageUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usage.RequestPayloadBytes != 9 || usage.AttemptPayloadBytes != 6 || usage.PayloadBytes != 15 {
		t.Fatalf("storage usage = %+v", usage)
	}
}
