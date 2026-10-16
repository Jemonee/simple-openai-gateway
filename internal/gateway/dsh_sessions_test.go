package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func dshSessionTestBody(t *testing.T, prompt string, child bool, extra ...map[string]any) []byte {
	t.Helper()
	runtime := dshRuntimeContextPrefix + "\n\nCurrent working directory: /workspace"
	if child {
		runtime += "\n\n" + dshDelegationText
	}
	messages := []map[string]any{
		{"role": "system", "content": dshIdentityText},
		{"role": "user", "content": prompt},
		{"role": "user", "content": runtime},
	}
	messages = append(messages, extra...)
	body, err := json.Marshal(map[string]any{"model": "test-model", "messages": messages})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func resolveDSHTestPayload(t *testing.T, body []byte, headers http.Header) *RelayPayload {
	t.Helper()
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	// 无需数据库：识别不再查最近会话历史，避免 fork 前缀误合并。
	resolution, err := (&Store{}).resolveAgentSession(context.Background(), agentSessionRequest{
		Endpoint: "chat", Headers: headers, Payload: payload, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	applyAgentSessionResolution(payload, resolution)
	return payload
}

func TestDSHStableHeadersSeparateParentAndChildren(t *testing.T) {
	for _, header := range []string{dshHarnessSessionHeader, "X-Session-Affinity", "X-Session-Id", "session_id", "Session-Id"} {
		t.Run(header, func(t *testing.T) {
			for _, session := range []struct {
				id    string
				child bool
			}{{"parent", false}, {"child-one", true}, {"child-two", true}} {
				for _, requestID := range []string{"request-one", "request-two"} {
					body := dshSessionTestBody(t, "identical inherited prompt", session.child)
					payload := resolveDSHTestPayload(t, body, http.Header{header: {session.id}, "X-Client-Request-Id": {requestID}})
					wantSource := dshHeaderSessionSource
					if header == "X-Session-Affinity" {
						wantSource = dshHeaderAffinitySource
					}
					wantThread := codexThreadSourceUser
					if session.child {
						wantThread = codexThreadSourceSubagent
					}
					if payload.SessionKey != session.id || payload.ClientKind != agentClientKindDSH || payload.SessionSource != wantSource || payload.ThreadSource != wantThread {
						t.Fatalf("identity = %+v", payload)
					}
					if payload.LogSessionKey != session.id || payload.LogSessionSource != wantSource {
						t.Fatalf("log identity = %+v", payload)
					}
				}
			}
		})
	}
}

func TestDSHStableHeaderOverridesSharedCacheKey(t *testing.T) {
	body := []byte(`{"model":"test-model","prompt_cache_key":"shared-cache-key","input":"hello"}`)
	payload := resolveDSHTestPayload(t, body, http.Header{
		"User-Agent":         {"deepseek-harness/1.0 (+https://github.com/deepseek-ai/deepseek-harness)"},
		"X-Session-Affinity": {"stable-child"},
	})
	if payload.SessionKey != "stable-child" || payload.ClientKind != agentClientKindDSH {
		t.Fatalf("identity = %+v", payload)
	}
}

func TestDSHRequiresSpecificClientEvidence(t *testing.T) {
	for _, test := range []struct {
		name, body string
		headers    http.Header
		kind       string
	}{
		{"generic chat", `{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`, nil, ""},
		{"codex is not DSH", `{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`, http.Header{"User-Agent": {"codex/1.0"}}, agentClientKindCodex},
		{"pi is not DSH", `{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`, http.Header{"User-Agent": {"pi/1.0"}}, ""},
		{"user quote is not identity", `{"model":"test-model","messages":[{"role":"user","content":"You are an AI agent powered by DeepSeek Harness."}]}`, nil, ""},
		{"specific user agent", `{"model":"test-model","messages":[]}`, http.Header{"user-agent": {"deepseek-harness/1.0 (+url)"}}, agentClientKindDSH},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := resolveDSHTestPayload(t, []byte(test.body), test.headers)
			if payload.ClientKind != test.kind || payload.SessionKey != "" {
				t.Fatalf("identity = %+v", payload)
			}
		})
	}
}

func TestRequestIDIsNeverSessionIdentity(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`),
		dshSessionTestBody(t, "same inherited prompt", true),
	} {
		for _, requestID := range []string{"request-1", "request-2"} {
			payload := resolveDSHTestPayload(t, body, http.Header{"X-Client-Request-Id": {requestID}})
			if payload.SessionKey != "" || payload.LogSessionKey != "" {
				t.Fatalf("request id used as session: %+v", payload)
			}
		}
	}
}

func TestDSHNoHeaderChildDoesNotMergeIntoParent(t *testing.T) {
	parent := resolveDSHTestPayload(t, dshSessionTestBody(t, "same inherited prompt", false), nil)
	child := resolveDSHTestPayload(t, dshSessionTestBody(t, "same inherited prompt", true), nil)
	if parent.SessionKey == "" || parent.SessionSource != dshChatFirstUserSource {
		t.Fatalf("main inferred identity = %+v", parent)
	}
	if child.SessionKey != "" || child.ThreadSource != codexThreadSourceSubagent || child.LogSessionSource != dshSubagentUnavailableSource {
		t.Fatalf("child identity = %+v", child)
	}
	if parent.SessionKey != codexChatHistorySessionID(dshSessionTestBody(t, "same inherited prompt", false)) {
		t.Fatal("main inferred ID changed unnecessarily")
	}
}

func TestDSHLatestRuntimeSnapshotDeterminesRole(t *testing.T) {
	mainSnapshot := map[string]any{"role": "user", "content": dshRuntimeContextPrefix + "\n\nCurrent workspace: /workspace"}
	childSnapshot := map[string]any{"role": "user", "content": dshRuntimeContextPrefix + "\n\n" + dshDelegationText}
	for _, test := range []struct {
		name string
		body []byte
		want string
	}{
		{"child supersedes main", dshSessionTestBody(t, "task", false, childSnapshot), codexThreadSourceSubagent},
		{"main supersedes inherited child", dshSessionTestBody(t, "task", true, mainSnapshot), codexThreadSourceUser},
		{"tool text ignored", dshSessionTestBody(t, "task", false, map[string]any{"role": "tool", "content": childSnapshot["content"]}), codexThreadSourceUser},
		{"ordinary quote ignored", dshSessionTestBody(t, "task", false, map[string]any{"role": "user", "content": "Example:\n" + childSnapshot["content"].(string)}), codexThreadSourceUser},
	} {
		t.Run(test.name, func(t *testing.T) {
			if payload := resolveDSHTestPayload(t, test.body, nil); payload.ThreadSource != test.want {
				t.Fatalf("thread source = %q", payload.ThreadSource)
			}
		})
	}
}

func TestDSHResponsesBlockContentIdentityAndSubagent(t *testing.T) {
	body, err := json.Marshal(map[string]any{"model": "test-model", "input": []any{
		map[string]any{"role": "developer", "content": []any{map[string]any{"type": "input_text", "text": dshIdentityText}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": dshRuntimeContextPrefix + "\n\n" + dshDelegationText}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	payload := resolveDSHTestPayload(t, body, http.Header{"session_id": {"child-session"}})
	if payload.ClientKind != agentClientKindDSH || payload.ThreadSource != codexThreadSourceSubagent || payload.SessionKey != "child-session" {
		t.Fatalf("identity = %+v", payload)
	}
}

func TestDSHUnavailableChildRoleSurvivesLogSummary(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	logs := []RelayRequestLog{
		{ID: "child-request-1", TokenID: 7, ClientKind: agentClientKindDSH, CodexSessionSource: dshSubagentUnavailableSource, Endpoint: "chat", Outcome: RelayOutcomeSuccess, CreatedAt: now},
		{ID: "child-request-2", TokenID: 7, ClientKind: agentClientKindDSH, CodexSessionSource: dshSubagentUnavailableSource, Endpoint: "chat", Outcome: RelayOutcomeSuccess, CreatedAt: now.Add(time.Second)},
	}
	if err := store.db.Create(&logs).Error; err != nil {
		t.Fatal(err)
	}
	page, err := NewManagementService(store).SessionLogs(context.Background(), SessionLogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("unidentified requests merged: %+v", page)
	}
	for _, item := range page.Items {
		if item.Identified || item.SessionID != "" || item.ParentSessionID != "" || item.ClientKind != agentClientKindDSH || item.ThreadSource != codexThreadSourceSubagent || item.SessionSource != dshSubagentUnavailableSource {
			t.Fatalf("unidentified child summary = %+v", item)
		}
	}
}
