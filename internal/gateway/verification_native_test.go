package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBuildNativeCodexRequestPreservesProfileShape(t *testing.T) {
	profileData, err := verificationAssets.ReadFile("verification_assets/native-0.147.0.raw")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := parseNativeRawProfile(profileData)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse("https://provider.example:8443/v1/responses?trace=1")
	if err != nil {
		t.Fatal(err)
	}
	const fixedNativeID = "019ffdca-54c0-7000-8000-000000000000"
	identity := nativeRequestIdentity{
		InstallationID: "11111111-1111-4111-8111-111111111111",
		SessionID:      fixedNativeID,
		TurnID:         fixedNativeID,
		StartedAt:      time.Date(2026, time.August, 14, 1, 2, 3, 456000000, time.UTC),
		CurrentDate:    "2026-08-14",
		NextMessageID: func() (string, error) {
			return "msg_" + fixedNativeID, nil
		},
	}
	built, err := buildNativeCodexRequest(
		profileData,
		target,
		"test-local-only",
		"gpt-5.6-terra",
		"high",
		[]verificationMessage{{Role: "developer", Content: "Use the probe."}, {Role: "user", Content: "Reply with exactly: native-ok"}},
		nil,
		identity,
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseNativeRawProfile(built.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.StartLine != "POST /v1/responses?trace=1 HTTP/1.1" {
		t.Fatalf("native start line = %q", parsed.StartLine)
	}
	if len(parsed.Headers) != len(profile.Headers) {
		t.Fatalf("native header count = %d, want %d", len(parsed.Headers), len(profile.Headers))
	}
	for index := range profile.Headers {
		if parsed.Headers[index].Name != profile.Headers[index].Name {
			t.Fatalf("native header %d name = %q, want %q", index, parsed.Headers[index].Name, profile.Headers[index].Name)
		}
	}
	assertNativeHeader(t, parsed.Headers, "authorization", "Bearer test-local-only")
	assertNativeHeader(t, parsed.Headers, "host", "provider.example:8443")
	assertNativeHeader(t, parsed.Headers, "session-id", identity.SessionID)
	if !bytes.Contains(parsed.Body, []byte(`<environment_context>`)) || bytes.Contains(parsed.Body, []byte(`\u003cenvironment_context\u003e`)) {
		t.Fatal("native body escaped the environment markup")
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(parsed.Body)), "2a68275dc576ef4f517ca53a69c863b61c24c53bb39a6f6eb9bb1d4a7c4cac8c"; got != want {
		t.Fatalf("native body SHA-256 = %s, want %s", got, want)
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(built.Raw)), "4638ac85718806cabb0dcb5343c973e69cedffc0e0a1428bb2bb07dc1390474d"; got != want {
		t.Fatalf("native raw request SHA-256 = %s, want %s", got, want)
	}

	var payload struct {
		Model      string           `json:"model"`
		Input      []nativeTestItem `json:"input"`
		Reasoning  nativeReasoning  `json:"reasoning"`
		Store      bool             `json:"store"`
		Stream     bool             `json:"stream"`
		Include    []string         `json:"include"`
		CacheKey   string           `json:"prompt_cache_key"`
		ClientMeta nativeClientMeta `json:"client_metadata"`
	}
	if err := json.Unmarshal(parsed.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "gpt-5.6-terra" || payload.Reasoning.Effort != "high" || payload.Store || !payload.Stream {
		t.Fatalf("unexpected native payload settings: %#v", payload)
	}
	if payload.CacheKey != identity.SessionID || payload.ClientMeta.SessionID != identity.SessionID || payload.ClientMeta.TurnID != identity.TurnID {
		t.Fatalf("unexpected native identity: %#v", payload.ClientMeta)
	}
	if !containsString(payload.Include, "reasoning.encrypted_content") {
		t.Fatalf("native include = %#v", payload.Include)
	}
	if len(payload.Input) != 8 {
		t.Fatalf("native input count = %d, want 8", len(payload.Input))
	}
	assertNativeMessage(t, payload.Input[5], "developer", "Use the probe.")
	assertNativeMessageContains(t, payload.Input[6], "user", "<current_date>2026-08-14</current_date>")
	assertNativeMessage(t, payload.Input[7], "user", "Reply with exactly: native-ok")
}

func TestBuildNativeInputInsertsHistoryBeforeFinalPrompt(t *testing.T) {
	profileData, err := verificationAssets.ReadFile("verification_assets/native-0.147.0.raw")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := parseNativeRawProfile(profileData)
	if err != nil {
		t.Fatal(err)
	}
	var payload nativeProfilePayload
	if err := json.Unmarshal(profile.Body, &payload); err != nil {
		t.Fatal(err)
	}
	items, err := buildNativeInput(
		payload.Input,
		[]verificationMessage{{Role: "user", Content: "Current question"}},
		[]nativeHistoryMessage{{Role: "user", Text: "Earlier question"}, {Role: "assistant", Text: "Earlier answer"}},
		func() (string, error) { return "msg_fixed", nil },
		"2026-08-14",
	)
	if err != nil {
		t.Fatal(err)
	}
	decoded := make([]nativeTestItem, len(items))
	for index := range items {
		if err := json.Unmarshal(items[index], &decoded[index]); err != nil {
			t.Fatal(err)
		}
	}
	if len(decoded) != 9 {
		t.Fatalf("native input count = %d, want 9", len(decoded))
	}
	assertNativeMessageContains(t, decoded[5], "user", "<environment_context>")
	assertNativeMessage(t, decoded[6], "user", "Earlier question")
	assertNativeMessage(t, decoded[7], "assistant", "Earlier answer")
	assertNativeMessage(t, decoded[8], "user", "Current question")
}

type nativeTestItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	ID      string `json:"id"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func assertNativeHeader(t *testing.T, headers []nativeRawHeader, name, want string) {
	t.Helper()
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			if header.Value != want {
				t.Fatalf("native header %s = %q, want %q", name, header.Value, want)
			}
			return
		}
	}
	t.Fatalf("native header %s is missing", name)
}

func assertNativeMessage(t *testing.T, item nativeTestItem, role, text string) {
	t.Helper()
	if item.Type != "message" || item.Role != role || len(item.Content) != 1 || item.Content[0].Text != text {
		t.Fatalf("native message = %#v, want role=%s text=%q", item, role, text)
	}
}

func assertNativeMessageContains(t *testing.T, item nativeTestItem, role, text string) {
	t.Helper()
	if item.Type != "message" || item.Role != role || len(item.Content) != 1 || !strings.Contains(item.Content[0].Text, text) {
		t.Fatalf("native message = %#v, want role=%s containing %q", item, role, text)
	}
}

func TestNativeProxyForMatchesRuntimePolicy(t *testing.T) {
	target, _ := url.Parse("https://api.example.com/v1/responses")
	environment := map[string]string{
		"HTTPS_PROXY": "http://proxy.example:8080",
		"HTTP_PROXY":  "http://fallback.example:8080",
	}
	lookup := func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	}
	proxy, err := nativeProxyFor(target, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if proxy == nil || proxy.String() != environment["HTTPS_PROXY"] {
		t.Fatalf("native proxy = %v", proxy)
	}
	environment["NO_PROXY"] = ".example.com"
	proxy, err = nativeProxyFor(target, lookup)
	if err != nil || proxy != nil {
		t.Fatalf("NO_PROXY result = %v, %v", proxy, err)
	}
}

func TestReadNativeResponseStopsAtTerminalSSE(t *testing.T) {
	event := "data: {\"type\":\"response.completed\",\"response\":{\"output_text\":\"ok\"}}\n\n"
	raw := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(event), event)
	response, err := http.ReadResponse(bufio.NewReader(strings.NewReader(raw)), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	body, err := readNativeResponseBody(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != event {
		t.Fatalf("native SSE body = %q", body)
	}
}

func TestReadNativeGzipResponse(t *testing.T) {
	want := []byte("data: {\"type\":\"response.completed\"}\n\n")
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	response := &http.Response{Header: http.Header{"Content-Encoding": []string{"gzip"}}, Body: io.NopCloser(bytes.NewReader(compressed.Bytes()))}
	got, err := readNativeResponseBody(response)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("native gzip body = %q", got)
	}
}
