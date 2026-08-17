package gateway

import (
	"fmt"
	"reflect"
	"testing"
)

func TestGatewayQueryIndexesAreCreated(t *testing.T) {
	store := newTestStore(t)
	if err := store.db.AutoMigrate(&VerificationJob{}, &VerificationAttempt{}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		columns []string
	}{
		{name: "idx_admin_session_expires", columns: []string{"expires_at"}},
		{name: "idx_admin_session_login_client", columns: []string{"login_type", "client_token_id"}},
		{name: "idx_channel_model_routable", columns: []string{"model_id", "enabled"}},
		{name: "idx_relay_request_token_created", columns: []string{"token_id", "created_at"}},
		{name: "idx_relay_request_session_created", columns: []string{"token_id", "codex_session_id", "created_at"}},
		{name: "idx_relay_attempt_request_created", columns: []string{"request_id", "created_at"}},
		{name: "idx_relay_attempt_channel_created", columns: []string{"channel_id", "created_at"}},
		{name: "idx_relay_attempt_model_created", columns: []string{"channel_model_id", "created_at"}},
		{name: "idx_verification_job_session_sequence", columns: []string{"session_id", "sequence"}},
		{name: "idx_verification_attempt_session_order", columns: []string{"session_id", "sequence", "attempt_no"}},
		{name: "idx_gateway_resource_last_seen", columns: []string{"last_seen_at"}},
		{name: "idx_gateway_resource_kind_last_seen", columns: []string{"kind", "last_seen_at"}},
		{name: "idx_request_resource_resource_request", columns: []string{"resource_id", "request_id"}},
		{name: "uidx_request_resource_identity", columns: []string{"request_id", "resource_id", "location"}},
		{name: "idx_channel_model_daily_channel_date", columns: []string{"channel_id", "date"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			columns := sqliteIndexColumns(t, store, test.name)
			if len(columns) == 0 {
				t.Fatalf("missing index %s", test.name)
			}
			if !reflect.DeepEqual(columns, test.columns) {
				t.Fatalf("index %s columns = %v, want %v", test.name, columns, test.columns)
			}
		})
	}
}

func TestRequestResourceIdentityIsUnique(t *testing.T) {
	store := newTestStore(t)
	association := RequestResource{RequestID: "request-1", ResourceID: 7, Location: "messages[0]"}
	if err := store.db.Create(&association).Error; err != nil {
		t.Fatal(err)
	}
	duplicate := RequestResource{RequestID: association.RequestID, ResourceID: association.ResourceID, Location: association.Location}
	if err := store.db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate request resource association was accepted")
	}
}

func sqliteIndexColumns(t *testing.T, store *Store, indexName string) []string {
	t.Helper()
	type indexColumn struct {
		Sequence int    `gorm:"column:seqno"`
		Name     string `gorm:"column:name"`
	}
	var rows []indexColumn
	if err := store.db.Raw(fmt.Sprintf("PRAGMA index_info(%q)", indexName)).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	columns := make([]string, 0, len(rows))
	for _, row := range rows {
		columns = append(columns, row.Name)
	}
	return columns
}
