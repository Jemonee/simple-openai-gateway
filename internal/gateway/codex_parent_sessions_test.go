package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func codexDelegationBody(t *testing.T, sessionID string, parentSessionID string) []byte {
	t.Helper()
	delegation := "<codex_delegation><source_thread_id>" + parentSessionID + "</source_thread_id><task>inspect</task></codex_delegation>"
	body, err := json.Marshal(map[string]any{
		"prompt_cache_key": sessionID,
		"metadata":         map[string]any{"thread_source": codexThreadSourceSubagent},
		"input": []any{
			map[string]any{"role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "instructions"}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": delegation}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestCodexParentSessionIDFromBody(t *testing.T) {
	body := codexDelegationBody(t, "child-session", "parent-session")
	if got := codexParentSessionIDFromBody(body, "child-session", codexThreadSourceSubagent); got != "parent-session" {
		t.Fatalf("parent session = %q", got)
	}
	if got := codexParentSessionIDFromBody(body, "child-session", codexThreadSourceUser); got != "" {
		t.Fatalf("user request was misclassified with parent %q", got)
	}
	if got := codexParentSessionIDFromBody(codexDelegationBody(t, "same-session", "same-session"), "same-session", codexThreadSourceSubagent); got != "" {
		t.Fatalf("self parent = %q", got)
	}
	if got := codexParentSessionIDFromBody(codexDelegationBody(t, "child-session", strings.Repeat("x", 513)), "child-session", codexThreadSourceSubagent); got != "" {
		t.Fatalf("oversized parent = %q", got)
	}
}

func TestCompactSessionPayloadPersistsParentSession(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	body := codexDelegationBody(t, "child-session", "parent-session")
	compactSessionPayload(store.db, 7, "child-session", "request-1", "delegated task", codexThreadSourceSubagent, body, nil, now)

	var state RelaySessionState
	if err := store.db.Where("token_id = ? AND session_id = ?", 7, "child-session").First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.ParentSessionID != "parent-session" {
		t.Fatalf("parent session = %q", state.ParentSessionID)
	}

	withoutDelegation, err := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": "continue"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	compactSessionPayload(store.db, 7, "child-session", "request-2", "delegated task", codexThreadSourceSubagent, withoutDelegation, nil, now.Add(time.Minute))
	if err := store.db.Where("token_id = ? AND session_id = ?", 7, "child-session").First(&state).Error; err != nil {
		t.Fatal(err)
	}
	if state.ParentSessionID != "parent-session" {
		t.Fatalf("parent session was overwritten with %q", state.ParentSessionID)
	}
}

func TestBackfillCodexSessionParents(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	states := []RelaySessionState{
		{TokenID: 7, SessionID: "child-session", ThreadSource: codexThreadSourceSubagent, CreatedAt: now, UpdatedAt: now},
		{TokenID: 7, SessionID: "quoted-by-user", ThreadSource: codexThreadSourceUser, CreatedAt: now, UpdatedAt: now},
	}
	if err := store.db.Create(&states).Error; err != nil {
		t.Fatal(err)
	}
	logs := []RelayRequestLog{
		{ID: "child-request", TokenID: 7, CodexSessionID: "child-session", RequestBody: compressStoredPayload(codexDelegationBody(t, "child-session", "parent-session")), CreatedAt: now},
		{ID: "user-request", TokenID: 7, CodexSessionID: "quoted-by-user", RequestBody: compressStoredPayload(codexDelegationBody(t, "quoted-by-user", "not-a-parent")), CreatedAt: now},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.backfillCodexSessionParents(); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillCodexSessionParents(); err != nil {
		t.Fatalf("idempotent rerun: %v", err)
	}

	var child RelaySessionState
	if err := store.db.Where("token_id = ? AND session_id = ?", 7, "child-session").First(&child).Error; err != nil {
		t.Fatal(err)
	}
	if child.ParentSessionID != "parent-session" {
		t.Fatalf("backfilled parent = %q", child.ParentSessionID)
	}
	var user RelaySessionState
	if err := store.db.Where("token_id = ? AND session_id = ?", 7, "quoted-by-user").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.ParentSessionID != "" {
		t.Fatalf("user parent = %q", user.ParentSessionID)
	}
	var migrations int64
	if err := store.db.Model(&GatewayMigration{}).Where("name = ?", "codex_session_parent_v1").Count(&migrations).Error; err != nil {
		t.Fatal(err)
	}
	if migrations != 1 {
		t.Fatalf("migration count = %d", migrations)
	}
}
