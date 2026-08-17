package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	nativeVerificationTimeout          = 300 * time.Second
	nativeVerificationMaxResponseBytes = 64 << 20
	nativeProxyMaxHeaderBytes          = 64 << 10
)

type nativeRawHeader struct {
	Name  string
	Value string
}

type nativeRawProfile struct {
	StartLine string
	Headers   []nativeRawHeader
	Body      []byte
}

type nativeReasoning struct {
	Effort  string `json:"effort"`
	Context string `json:"context,omitempty"`
}

type nativeProfilePayload struct {
	Model             string            `json:"model"`
	Input             []json.RawMessage `json:"input"`
	ToolChoice        json.RawMessage   `json:"tool_choice"`
	ParallelToolCalls bool              `json:"parallel_tool_calls"`
	Reasoning         nativeReasoning   `json:"reasoning"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	Include           []string          `json:"include"`
	PromptCacheKey    string            `json:"prompt_cache_key"`
	Text              json.RawMessage   `json:"text"`
	ClientMetadata    nativeClientMeta  `json:"client_metadata"`
}

type nativeClientMeta struct {
	TurnID         string `json:"turn_id"`
	InstallationID string `json:"x-codex-installation-id"`
	TurnMetadata   string `json:"x-codex-turn-metadata"`
	SessionID      string `json:"session_id"`
	ThreadID       string `json:"thread_id"`
	WindowID       string `json:"x-codex-window-id"`
}

type nativeTurnHeaderMetadata struct {
	InstallationID string `json:"installation_id"`
	SessionID      string `json:"session_id"`
	ThreadID       string `json:"thread_id"`
	TurnID         string `json:"turn_id"`
	WindowID       string `json:"window_id"`
	RequestKind    string `json:"request_kind"`
	ThreadSource   string `json:"thread_source"`
	Sandbox        string `json:"sandbox"`
	StartedAtMS    int64  `json:"turn_started_at_unix_ms"`
}

type nativeTurnBodyMetadata struct {
	InstallationID    string                  `json:"installation_id"`
	SessionID         string                  `json:"session_id"`
	ThreadID          string                  `json:"thread_id"`
	TurnID            string                  `json:"turn_id"`
	WindowID          string                  `json:"window_id"`
	RequestKind       string                  `json:"request_kind"`
	ThreadSource      string                  `json:"thread_source"`
	Sandbox           string                  `json:"sandbox"`
	CodeModeToolNames nativeCodeModeToolNames `json:"code_mode_tool_names"`
	StartedAtMS       int64                   `json:"turn_started_at_unix_ms"`
}

type nativeCodeModeToolNames struct {
	ApplyPatch   nativeCodeModeTool `json:"apply_patch"`
	ShellCommand nativeCodeModeTool `json:"shell_command"`
	UpdatePlan   nativeCodeModeTool `json:"update_plan"`
	ViewImage    nativeCodeModeTool `json:"view_image"`
}

type nativeCodeModeTool struct {
	Name      string  `json:"name"`
	Namespace *string `json:"namespace"`
}

type nativeMessageItem struct {
	Type    string                 `json:"type"`
	Role    string                 `json:"role"`
	ID      string                 `json:"id"`
	Content []nativeMessageContent `json:"content"`
}

type nativeMessageContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type nativeHistoryMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type nativeRequestIdentity struct {
	InstallationID string
	SessionID      string
	TurnID         string
	StartedAt      time.Time
	CurrentDate    string
	NextMessageID  func() (string, error)
}

type nativeBuiltRequest struct {
	Raw []byte
}

type nativeHTTPResult struct {
	Status int
	Header http.Header
	Body   []byte
}

var nativePrivateFieldPatterns = []*regexp.Regexp{
	regexp.MustCompile(`[A-Za-z]:\\Users\\`),
	regexp.MustCompile(`\\\\[^\\\s]+\\Users\\`),
	regexp.MustCompile(`(?i)\bsub2api\b`),
	regexp.MustCompile(`(?i)\bAppData\b`),
}

func (s *VerificationService) performNativeProbeGo(ctx context.Context, baseURL, apiKey, model, _ string, manifest verificationJobManifest) (verificationTransportResult, []byte, []byte, verificationTransportMeta, error) {
	messages, _ := json.Marshal(manifest.Messages)
	meta := verificationTransportMeta{Path: "/responses", Stream: true, RequestFormat: "native_codex", TransportBackend: verificationTransportGoFallback}
	target, err := url.Parse(baseURL + "/responses")
	if err != nil {
		return verificationTransportResult{}, messages, nil, meta, err
	}
	profile, err := verificationAssets.ReadFile("verification_assets/native-0.147.0.raw")
	if err != nil {
		return verificationTransportResult{}, messages, nil, meta, err
	}
	history := []nativeHistoryMessage{}
	if manifest.ContextMode == "fixed_32k_history" {
		history, err = loadNativeVerificationHistory()
		if err != nil {
			return verificationTransportResult{}, messages, nil, meta, err
		}
	}
	identity, err := newNativeRequestIdentity()
	if err != nil {
		return verificationTransportResult{}, messages, nil, meta, err
	}
	built, err := buildNativeCodexRequest(profile, target, apiKey, model, manifest.Effort, manifest.Messages, history, identity)
	if err != nil {
		return verificationTransportResult{}, messages, nil, meta, err
	}
	started := time.Now()
	report, err := sendNativeRawHTTP(ctx, target, built.Raw, nativeVerificationTimeout)
	meta.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		return verificationTransportResult{}, messages, nil, meta, fmt.Errorf("native Codex transport failed: %w", err)
	}
	meta.Status = report.Status
	meta.ContentType = report.Header.Get("Content-Type")
	if report.Status < http.StatusOK || report.Status >= http.StatusMultipleChoices {
		return verificationTransportResult{}, messages, report.Body, meta, fmt.Errorf("upstream returned HTTP %d", report.Status)
	}
	parsed, err := parseResponsesSSE(report.Body)
	if err != nil {
		return verificationTransportResult{}, messages, report.Body, meta, err
	}
	parsed.UpstreamCost, parsed.UpstreamCostOK = ParseUpstreamCostMicros(report.Body)
	return parsed, messages, report.Body, meta, nil
}

func parseNativeRawProfile(raw []byte) (nativeRawProfile, error) {
	separator := bytes.Index(raw, []byte("\r\n\r\n"))
	separatorSize := 4
	if separator < 0 {
		separator = bytes.Index(raw, []byte("\n\n"))
		separatorSize = 2
	}
	if separator < 0 {
		return nativeRawProfile{}, errors.New("native request profile has incomplete HTTP headers")
	}
	lines := strings.Split(strings.ReplaceAll(string(raw[:separator]), "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nativeRawProfile{}, errors.New("native request profile is missing a start line")
	}
	profile := nativeRawProfile{StartLine: lines[0], Body: append([]byte(nil), raw[separator+separatorSize:]...)}
	for _, line := range lines[1:] {
		colon := strings.IndexByte(line, ':')
		if colon < 1 {
			return nativeRawProfile{}, fmt.Errorf("invalid native request header %q", line)
		}
		profile.Headers = append(profile.Headers, nativeRawHeader{Name: line[:colon], Value: strings.TrimLeft(line[colon+1:], " \t")})
	}
	for _, header := range profile.Headers {
		if strings.EqualFold(header.Name, "content-length") {
			length, err := strconv.Atoi(header.Value)
			if err != nil || length != len(profile.Body) {
				return nativeRawProfile{}, errors.New("native request profile has an invalid Content-Length")
			}
			return profile, nil
		}
	}
	return nativeRawProfile{}, errors.New("native request profile is missing Content-Length")
}

func newNativeRequestIdentity() (nativeRequestIdentity, error) {
	installationID := uuid.NewString()
	sessionID, err := newNativeUUID7(time.Now())
	if err != nil {
		return nativeRequestIdentity{}, err
	}
	turnID, err := newNativeUUID7(time.Now())
	if err != nil {
		return nativeRequestIdentity{}, err
	}
	startedAt := time.Now().UTC()
	return nativeRequestIdentity{
		InstallationID: installationID,
		SessionID:      sessionID,
		TurnID:         turnID,
		StartedAt:      startedAt,
		CurrentDate:    startedAt.Format(time.DateOnly),
		NextMessageID: func() (string, error) {
			value, err := newNativeUUID7(time.Now())
			if err != nil {
				return "", err
			}
			return "msg_" + value, nil
		},
	}, nil
}

func newNativeUUID7(now time.Time) (string, error) {
	var value uuid.UUID
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", err
	}
	milliseconds := uint64(now.UnixMilli())
	for index := 5; index >= 0; index-- {
		value[index] = byte(milliseconds)
		milliseconds >>= 8
	}
	value[6] = (value[6] & 0x0f) | 0x70
	value[8] = (value[8] & 0x3f) | 0x80
	return value.String(), nil
}

func buildNativeCodexRequest(profileData []byte, target *url.URL, apiKey, model, effort string, probes []verificationMessage, history []nativeHistoryMessage, identity nativeRequestIdentity) (nativeBuiltRequest, error) {
	profile, err := parseNativeRawProfile(profileData)
	if err != nil {
		return nativeBuiltRequest{}, err
	}
	if target == nil || (target.Scheme != "https" && target.Scheme != "http") || target.Hostname() == "" {
		return nativeBuiltRequest{}, errors.New("native request target must use HTTP or HTTPS and include a host")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nativeBuiltRequest{}, errors.New("native request API key is empty")
	}
	if identity.NextMessageID == nil || identity.SessionID == "" || identity.TurnID == "" || identity.InstallationID == "" {
		return nativeBuiltRequest{}, errors.New("native request identity is incomplete")
	}
	if identity.CurrentDate == "" {
		identity.CurrentDate = identity.StartedAt.UTC().Format(time.DateOnly)
	}

	var payload nativeProfilePayload
	if err := json.Unmarshal(profile.Body, &payload); err != nil {
		return nativeBuiltRequest{}, fmt.Errorf("decode native request profile: %w", err)
	}
	input, err := buildNativeInput(payload.Input, probes, history, identity.NextMessageID, identity.CurrentDate)
	if err != nil {
		return nativeBuiltRequest{}, err
	}
	headerMetadata, bodyMetadata, err := nativeTurnMetadata(identity)
	if err != nil {
		return nativeBuiltRequest{}, err
	}
	payload.Model = model
	payload.Input = input
	payload.Reasoning.Effort = effort
	payload.Store = false
	payload.Stream = true
	payload.Include = appendMissingString(payload.Include, "reasoning.encrypted_content")
	payload.PromptCacheKey = identity.SessionID
	payload.ClientMetadata = nativeClientMeta{
		TurnID:         identity.TurnID,
		InstallationID: identity.InstallationID,
		TurnMetadata:   bodyMetadata,
		SessionID:      identity.SessionID,
		ThreadID:       identity.SessionID,
		WindowID:       identity.SessionID + ":0",
	}
	body, err := marshalNativeJSON(payload)
	if err != nil {
		return nativeBuiltRequest{}, err
	}
	if err := validateNativeRequestPrivacy(body); err != nil {
		return nativeBuiltRequest{}, err
	}
	authorization := strings.TrimSpace(apiKey)
	if !strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		authorization = "Bearer " + authorization
	}
	overrides := map[string]string{
		"authorization":         authorization,
		"host":                  nativeHostHeader(target),
		"x-codex-window-id":     identity.SessionID + ":0",
		"x-codex-turn-metadata": headerMetadata,
		"x-client-request-id":   identity.SessionID,
		"session-id":            identity.SessionID,
		"thread-id":             identity.SessionID,
	}
	raw, err := renderNativeRawRequest(profile, nativeRequestPath(target), body, overrides)
	if err != nil {
		return nativeBuiltRequest{}, err
	}
	return nativeBuiltRequest{Raw: raw}, nil
}

func buildNativeInput(profileInput []json.RawMessage, probes []verificationMessage, history []nativeHistoryMessage, nextID func() (string, error), currentDate string) ([]json.RawMessage, error) {
	if len(probes) == 0 {
		return nil, errors.New("native probe messages must not be empty")
	}
	for index, probe := range probes {
		if probe.Role != "developer" && probe.Role != "user" {
			return nil, errors.New("native probe messages must use developer or user roles")
		}
		if index < len(probes)-1 && probe.Role != "developer" {
			return nil, errors.New("only developer messages may precede the final native probe message")
		}
	}
	if probes[len(probes)-1].Role != "user" {
		return nil, errors.New("the final native probe message must use the user role")
	}
	type itemShape struct {
		Type string `json:"type"`
		Role string `json:"role"`
		ID   string `json:"id"`
	}
	shapes := make([]itemShape, len(profileInput))
	userIndexes := []int{}
	for index, raw := range profileInput {
		if err := json.Unmarshal(raw, &shapes[index]); err != nil {
			return nil, fmt.Errorf("decode native profile input item %d: %w", index, err)
		}
		if shapes[index].Type == "message" && shapes[index].Role == "user" {
			userIndexes = append(userIndexes, index)
		}
	}
	if len(userIndexes) < 2 {
		return nil, errors.New("native request profile is missing environment and final user messages")
	}
	environmentIndex := userIndexes[len(userIndexes)-2]
	finalIndex := userIndexes[len(userIndexes)-1]

	environmentID, err := nextID()
	if err != nil {
		return nil, err
	}
	finalID, err := nextID()
	if err != nil {
		return nil, err
	}
	environment, err := nativeMessageJSON("user", environmentID, nativeEnvironmentContext(currentDate))
	if err != nil {
		return nil, err
	}
	final, err := nativeMessageJSON("user", finalID, probes[len(probes)-1].Content)
	if err != nil {
		return nil, err
	}
	items := append([]json.RawMessage(nil), profileInput...)
	items[environmentIndex] = environment
	items[finalIndex] = final
	for index, shape := range shapes {
		if index == environmentIndex || index == finalIndex || shape.Type != "message" || shape.ID == "" {
			continue
		}
		messageID, err := nextID()
		if err != nil {
			return nil, err
		}
		items[index], err = replaceNativeMessageID(items[index], shape.ID, messageID)
		if err != nil {
			return nil, err
		}
	}

	historyItems := make([]json.RawMessage, 0, len(history))
	for _, message := range history {
		if message.Role != "user" && message.Role != "assistant" {
			return nil, errors.New("native history messages must use user or assistant roles")
		}
		messageID, err := nextID()
		if err != nil {
			return nil, err
		}
		item, err := nativeMessageJSON(message.Role, messageID, message.Text)
		if err != nil {
			return nil, err
		}
		historyItems = append(historyItems, item)
	}
	prefixItems := make([]json.RawMessage, 0, len(probes)-1)
	for _, message := range probes[:len(probes)-1] {
		messageID, err := nextID()
		if err != nil {
			return nil, err
		}
		item, err := nativeMessageJSON(message.Role, messageID, message.Content)
		if err != nil {
			return nil, err
		}
		prefixItems = append(prefixItems, item)
	}

	result := make([]json.RawMessage, 0, len(items)+len(prefixItems)+len(historyItems))
	result = append(result, items[:environmentIndex]...)
	result = append(result, prefixItems...)
	result = append(result, items[environmentIndex:finalIndex]...)
	result = append(result, historyItems...)
	result = append(result, items[finalIndex:]...)
	return result, nil
}

func nativeMessageJSON(role, id, text string) (json.RawMessage, error) {
	contentType := "input_text"
	if role == "assistant" {
		contentType = "output_text"
	}
	return marshalNativeJSON(nativeMessageItem{
		Type: "message",
		Role: role,
		ID:   id,
		Content: []nativeMessageContent{{
			Type: contentType,
			Text: text,
		}},
	})
}

func replaceNativeMessageID(raw json.RawMessage, oldID, newID string) (json.RawMessage, error) {
	oldValue, _ := json.Marshal(oldID)
	newValue, _ := json.Marshal(newID)
	needle := append([]byte(`"id":`), oldValue...)
	replacement := append([]byte(`"id":`), newValue...)
	updated := bytes.Replace(raw, needle, replacement, 1)
	if bytes.Equal(updated, raw) {
		return nil, fmt.Errorf("native profile message %s does not contain its declared id", oldID)
	}
	return updated, nil
}

func nativeTurnMetadata(identity nativeRequestIdentity) (string, string, error) {
	base := nativeTurnHeaderMetadata{
		InstallationID: identity.InstallationID,
		SessionID:      identity.SessionID,
		ThreadID:       identity.SessionID,
		TurnID:         identity.TurnID,
		WindowID:       identity.SessionID + ":0",
		RequestKind:    "turn",
		ThreadSource:   "user",
		Sandbox:        "none",
		StartedAtMS:    identity.StartedAt.UnixMilli(),
	}
	header, err := marshalNativeJSON(base)
	if err != nil {
		return "", "", err
	}
	tool := func(name string) nativeCodeModeTool { return nativeCodeModeTool{Name: name} }
	body, err := marshalNativeJSON(nativeTurnBodyMetadata{
		InstallationID: base.InstallationID,
		SessionID:      base.SessionID,
		ThreadID:       base.ThreadID,
		TurnID:         base.TurnID,
		WindowID:       base.WindowID,
		RequestKind:    base.RequestKind,
		ThreadSource:   base.ThreadSource,
		Sandbox:        base.Sandbox,
		CodeModeToolNames: nativeCodeModeToolNames{
			ApplyPatch:   tool("apply_patch"),
			ShellCommand: tool("shell_command"),
			UpdatePlan:   tool("update_plan"),
			ViewImage:    tool("view_image"),
		},
		StartedAtMS: base.StartedAtMS,
	})
	return string(header), string(body), err
}

func nativeEnvironmentContext(currentDate string) string {
	return strings.Join([]string{
		"<environment_context>",
		"  <cwd>C:\\workspace</cwd>",
		"  <shell>powershell</shell>",
		"  <current_date>" + currentDate + "</current_date>",
		"  <timezone>UTC</timezone>",
		"  <filesystem><workspace_roots><root>C:\\workspace</root></workspace_roots><permission_profile type=\"managed\"><file_system type=\"restricted\"><entry access=\"read\"><special>:root</special></entry></file_system></permission_profile></filesystem>",
		"</environment_context>",
	}, "\n")
}

func marshalNativeJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n")), nil
}

func appendMissingString(values []string, wanted string) []string {
	for _, value := range values {
		if value == wanted {
			return values
		}
	}
	return append(values, wanted)
}

func validateNativeRequestPrivacy(body []byte) error {
	for _, pattern := range nativePrivateFieldPatterns {
		if pattern.Match(body) {
			return fmt.Errorf("privacy scan rejected native body: %s", pattern.String())
		}
	}
	return nil
}

func renderNativeRawRequest(profile nativeRawProfile, path string, body []byte, overrides map[string]string) ([]byte, error) {
	parts := strings.Fields(profile.StartLine)
	if len(parts) != 3 {
		return nil, errors.New("native request profile has an invalid start line")
	}
	for name, value := range overrides {
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("native request header %s contains a line break", name)
		}
	}
	parts[1] = path
	var output bytes.Buffer
	output.WriteString(strings.Join(parts, " "))
	output.WriteString("\r\n")
	contentLengthSeen := false
	for _, header := range profile.Headers {
		name := strings.ToLower(header.Name)
		value := header.Value
		if name == "content-length" {
			value = strconv.Itoa(len(body))
			contentLengthSeen = true
		}
		if override, ok := overrides[name]; ok {
			value = override
		}
		output.WriteString(header.Name)
		output.WriteString(": ")
		output.WriteString(value)
		output.WriteString("\r\n")
	}
	if !contentLengthSeen {
		return nil, errors.New("native request profile is missing Content-Length")
	}
	output.WriteString("\r\n")
	output.Write(body)
	return output.Bytes(), nil
}

func nativeRequestPath(target *url.URL) string {
	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}
	if target.RawQuery != "" {
		path += "?" + target.RawQuery
	}
	return path
}

func nativeHostHeader(target *url.URL) string {
	host := target.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if target.Port() != "" && target.Port() != "443" {
		return host + ":" + target.Port()
	}
	return host
}

func loadNativeVerificationHistory() ([]nativeHistoryMessage, error) {
	data, err := verificationAssets.ReadFile("verification_assets/fixed_32k_history.json")
	if err != nil {
		return nil, err
	}
	var history []nativeHistoryMessage
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, err
	}
	return history, nil
}

func sendNativeRawHTTP(ctx context.Context, target *url.URL, raw []byte, timeout time.Duration) (nativeHTTPResult, error) {
	conn, err := openNativeConnection(ctx, target, timeout)
	if err != nil {
		return nativeHTTPResult{}, err
	}
	defer conn.Close()
	stopCancellation := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancellation()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	if err := writeAll(conn, raw); err != nil {
		return nativeHTTPResult{}, err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		return nativeHTTPResult{}, err
	}
	defer response.Body.Close()
	body, err := readNativeResponseBody(response)
	if err != nil {
		return nativeHTTPResult{}, err
	}
	return nativeHTTPResult{Status: response.StatusCode, Header: response.Header.Clone(), Body: body}, nil
}

func openNativeConnection(ctx context.Context, target *url.URL, timeout time.Duration) (net.Conn, error) {
	if target == nil {
		return nil, errors.New("native request target is nil")
	}
	switch target.Scheme {
	case "http":
		hostname := strings.ToLower(target.Hostname())
		if hostname != "127.0.0.1" && hostname != "localhost" && hostname != "::1" {
			return nil, errors.New("plain HTTP native send is restricted to loopback hosts")
		}
		return (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", nativeTargetAddress(target, "80"))
	case "https":
		return openNativeTLSConnection(ctx, target, timeout)
	default:
		return nil, fmt.Errorf("unsupported native URL protocol: %s", target.Scheme)
	}
}

func openNativeTLSConnection(ctx context.Context, target *url.URL, timeout time.Duration) (net.Conn, error) {
	proxy, err := nativeProxyFor(target, os.LookupEnv)
	if err != nil {
		return nil, err
	}
	var conn net.Conn
	if proxy != nil {
		conn, err = openNativeProxyTunnel(ctx, proxy, target, timeout)
	} else {
		conn, err = (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", nativeTargetAddress(target, "443"))
	}
	if err != nil {
		return nil, err
	}
	tlsConn := tls.Client(conn, &tls.Config{ServerName: target.Hostname(), NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12})
	if timeout > 0 {
		_ = tlsConn.SetDeadline(time.Now().Add(timeout))
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	negotiated := tlsConn.ConnectionState().NegotiatedProtocol
	if negotiated != "" && negotiated != "http/1.1" {
		tlsConn.Close()
		return nil, fmt.Errorf("unexpected ALPN protocol: %s", negotiated)
	}
	return tlsConn, nil
}

func nativeProxyFor(target *url.URL, lookup func(string) (string, bool)) (*url.URL, error) {
	noProxy := firstEnvironmentValue(lookup, "NO_PROXY", "no_proxy")
	hostname := strings.ToLower(target.Hostname())
	for _, item := range strings.Split(noProxy, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if item == "*" || hostname == item || strings.HasSuffix(hostname, nativeNoProxySuffix(item)) {
			return nil, nil
		}
	}
	value := firstEnvironmentValue(lookup, "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "HTTP_PROXY", "http_proxy")
	if value == "" {
		return nil, nil
	}
	proxy, err := url.Parse(value)
	if err != nil || proxy.Hostname() == "" {
		return nil, fmt.Errorf("invalid native proxy URL %q", value)
	}
	return proxy, nil
}

func nativeNoProxySuffix(item string) string {
	if strings.HasPrefix(item, ".") {
		return item
	}
	return "." + item
}

func firstEnvironmentValue(lookup func(string) (string, bool), keys ...string) string {
	for _, key := range keys {
		if value, ok := lookup(key); ok && value != "" {
			return value
		}
	}
	return ""
}

func openNativeProxyTunnel(ctx context.Context, proxy, target *url.URL, timeout time.Duration) (net.Conn, error) {
	proxyPort := proxy.Port()
	if proxyPort == "" {
		proxyPort = "80"
	}
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", net.JoinHostPort(proxy.Hostname(), proxyPort))
	if err != nil {
		return nil, err
	}
	stopCancellation := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancellation()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	authority := nativeTargetAddress(target, "443")
	var request strings.Builder
	fmt.Fprintf(&request, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n", authority, authority)
	if proxy.User != nil {
		password, _ := proxy.User.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(proxy.User.Username() + ":" + password))
		fmt.Fprintf(&request, "Proxy-Authorization: Basic %s\r\n", credentials)
	}
	request.WriteString("\r\n")
	if err := writeAll(conn, []byte(request.String())); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	statusLine, headerBytes, err := readNativeProxyHead(reader)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if headerBytes > nativeProxyMaxHeaderBytes {
		conn.Close()
		return nil, errors.New("proxy CONNECT response exceeds size limit")
	}
	fields := strings.Fields(statusLine)
	if len(fields) < 2 || (fields[0] != "HTTP/1.1" && fields[0] != "HTTP/1.0") || fields[1] != "200" {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed: %s", statusLine)
	}
	return &nativeBufferedConn{Conn: conn, reader: reader}, nil
}

func readNativeProxyHead(reader *bufio.Reader) (string, int, error) {
	total := 0
	statusLine := ""
	for {
		line, err := reader.ReadString('\n')
		total += len(line)
		if total > nativeProxyMaxHeaderBytes {
			return "", total, errors.New("proxy CONNECT response exceeds size limit")
		}
		if err != nil {
			return "", total, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if statusLine == "" {
			statusLine = line
			continue
		}
		if line == "" {
			return statusLine, total, nil
		}
	}
}

type nativeBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *nativeBufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func nativeTargetAddress(target *url.URL, defaultPort string) string {
	port := target.Port()
	if port == "" {
		port = defaultPort
	}
	return net.JoinHostPort(target.Hostname(), port)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func readNativeResponseBody(response *http.Response) ([]byte, error) {
	encoding := strings.ToLower(response.Header.Get("Content-Encoding"))
	encoded, err := readNativeEncodedBody(response.Body, encoding == "")
	if err != nil {
		return nil, err
	}
	var reader io.ReadCloser
	switch encoding {
	case "":
		return encoded, nil
	case "gzip":
		reader, err = gzip.NewReader(bytes.NewReader(encoded))
	case "deflate":
		reader, err = zlib.NewReader(bytes.NewReader(encoded))
	case "br":
		return nil, errors.New("native response used unsupported Brotli encoding without advertising it")
	default:
		return nil, fmt.Errorf("unsupported native response content encoding %q", encoding)
	}
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return readNativeEncodedBody(reader, false)
}

func readNativeEncodedBody(reader io.Reader, stopAtTerminalSSE bool) ([]byte, error) {
	var output bytes.Buffer
	buffer := make([]byte, 32<<10)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			if output.Len()+count > nativeVerificationMaxResponseBytes {
				return nil, errors.New("native response exceeds size limit")
			}
			output.Write(buffer[:count])
			if stopAtTerminalSSE && hasNativeTerminalSSEEvent(output.Bytes()) {
				return output.Bytes(), nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return output.Bytes(), nil
			}
			return nil, err
		}
	}
}

func hasNativeTerminalSSEEvent(body []byte) bool {
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	blocks := strings.Split(normalized, "\n\n")
	for _, block := range blocks[:max(0, len(blocks)-1)] {
		dataLines := []string{}
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimLeft(strings.TrimPrefix(line, "data:"), " \t"))
			}
		}
		payload := strings.Join(dataLines, "\n")
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(payload), &event) == nil && (event.Type == "response.completed" || event.Type == "response.incomplete" || event.Type == "response.failed") {
			return true
		}
	}
	return false
}
