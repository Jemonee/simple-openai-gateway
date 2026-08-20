package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMigrateRelayPayloadTablesIsResumableAndKeepsDetailsReadable(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	requestBody := strings.Repeat(`{"input":"historical request"}`, 120)
	responseBody := strings.Repeat(`{"output":"historical response"}`, 120)
	request := RelayRequestLog{
		ID: "payload-migration-request", TokenID: 7, Endpoint: "responses", RequestedModel: "model-a",
		RequestParametersJSON: `{"reasoning":{"effort":"high"}}`, RequestBody: requestBody,
		ResponseBody: responseBody, StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, CreatedAt: now,
	}
	if err := store.db.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	attempt := RelayAttemptLog{
		RequestID: request.ID, ChannelID: 1, ChannelModelID: 1, UpstreamModel: "model-a",
		RequestBody: requestBody, ResponseBody: responseBody, StatusCode: http.StatusOK,
		Success: true, Outcome: RelayOutcomeSuccess, CreatedAt: now,
	}
	if err := store.db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}

	if err := store.migrateRelayPayloadTables(); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateRelayPayloadTables(); err != nil {
		t.Fatal(err)
	}
	var legacyRequest struct {
		RequestParametersJSON string
		RequestBody           string
		ResponseBody          string
	}
	if err := store.db.Table("relay_request_logs").Select("request_parameters_json, request_body, response_body").Where("id = ?", request.ID).Scan(&legacyRequest).Error; err != nil {
		t.Fatal(err)
	}
	if legacyRequest.RequestParametersJSON != "" || legacyRequest.RequestBody != "" || legacyRequest.ResponseBody != "" {
		t.Fatalf("legacy request payload was not cleared: %+v", legacyRequest)
	}
	var storedRequest RelayRequestPayload
	if err := store.db.First(&storedRequest, "request_id = ?", request.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedRequest.RequestBody != requestBody || storedRequest.ResponseBody != responseBody || storedRequest.RequestParametersJSON != request.RequestParametersJSON {
		t.Fatalf("migrated request payload mismatch: %+v", storedRequest)
	}
	var storedAttempt RelayAttemptPayload
	if err := store.db.First(&storedAttempt, "attempt_id = ?", attempt.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedAttempt.RequestBody != requestBody || storedAttempt.ResponseBody != responseBody {
		t.Fatalf("migrated attempt payload mismatch: %+v", storedAttempt)
	}

	detail, err := NewManagementService(store).LogDetail(context.Background(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.RequestBody != requestBody || detail.ResponseBody != responseBody || len(detail.Attempts) != 1 || detail.Attempts[0].RequestBody != requestBody || detail.Attempts[0].ResponseBody != responseBody {
		t.Fatalf("migrated detail mismatch: %+v", detail)
	}
}

func TestRelayRequestWritesPayloadOutsideMetadataTables(t *testing.T) {
	store := newTestStore(t)
	request := RelayRequestLog{
		ID: "payload-write-request", TokenID: 1, Endpoint: "responses", RequestedModel: "model-a",
		RequestParametersJSON: `{"temperature":0.2}`, RequestBody: `{"input":"request"}`,
		ResponseBody: `{"output":"response"}`, StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess,
	}
	if err := saveRelayRequestLog(store.db, &request, false); err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		RequestParametersJSON string
		RequestBody           string
		ResponseBody          string
	}
	if err := store.db.Table("relay_request_logs").Select("request_parameters_json, request_body, response_body").Where("id = ?", request.ID).Scan(&metadata).Error; err != nil {
		t.Fatal(err)
	}
	if metadata.RequestParametersJSON != "" || metadata.RequestBody != "" || metadata.ResponseBody != "" {
		t.Fatalf("payload leaked into metadata row: %+v", metadata)
	}
	var payload RelayRequestPayload
	if err := store.db.First(&payload, "request_id = ?", request.ID).Error; err != nil {
		t.Fatal(err)
	}
	if payload.RequestParametersJSON != request.RequestParametersJSON || payload.RequestBody != request.RequestBody || payload.ResponseBody != request.ResponseBody {
		t.Fatalf("stored payload mismatch: %+v", payload)
	}
}

func TestStoreMigrateCreatesPayloadTablesAndMarker(t *testing.T) {
	store := newTestStore(t)
	if err := store.migrate(); err != nil {
		t.Fatal(err)
	}
	var requestPayloadCount int64
	if err := store.db.Model(&RelayRequestPayload{}).Count(&requestPayloadCount).Error; err != nil {
		t.Fatal(err)
	}
	var migrationCount int64
	if err := store.db.Model(&GatewayMigration{}).Where("name = ?", "relay_payload_tables_v1").Count(&migrationCount).Error; err != nil {
		t.Fatal(err)
	}
	if migrationCount != 1 {
		t.Fatalf("relay payload migration marker count = %d", migrationCount)
	}
}
