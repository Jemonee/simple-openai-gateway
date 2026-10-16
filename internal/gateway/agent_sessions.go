package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	agentClientKindCodex   = "codex"
	agentClientKindDSH     = "dsh"
	agentClientKindWebChat = "web_chat"
	sessionUnavailable     = "unavailable"

	codexHeaderSessionSource  = "codex.header.session_id"
	codexHeaderAffinitySource = "codex.header.x-session-affinity"
	dshHeaderSessionSource    = "dsh.header.session_id"
	dshHeaderAffinitySource   = "dsh.header.x-session-affinity"
	dshHarnessSessionHeader   = "X-Deepseek-Harness-Session-Id"
)

type agentSessionRequest struct {
	TokenID  uint64
	Endpoint string
	Headers  http.Header
	Payload  *RelayPayload
	Body     []byte
	Now      time.Time
}

type agentSessionIdentity struct {
	ID                string
	Source            string
	ClientKind        string
	ClientFingerprint string
	ThreadSource      string
}

type agentSessionResolution struct {
	Identity      agentSessionIdentity
	Authoritative bool
}

type agentSessionResolver interface {
	Match(agentSessionRequest) bool
	Resolve(context.Context, *Store, agentSessionRequest) (agentSessionResolution, error)
}

var agentSessionResolvers = []agentSessionResolver{
	copilotAgentSessionResolver{},
	dshAgentSessionResolver{},
	codexAgentSessionResolver{},
}

func (s *Store) resolveAgentSession(ctx context.Context, request agentSessionRequest) (agentSessionResolution, error) {
	for _, resolver := range agentSessionResolvers {
		if resolver.Match(request) {
			return resolver.Resolve(ctx, s, request)
		}
	}
	return agentSessionResolution{}, nil
}

func applyAgentSessionResolution(payload *RelayPayload, resolution agentSessionResolution) {
	if payload == nil || !resolution.Authoritative {
		return
	}
	identity := resolution.Identity
	payload.SessionKey = truncateRunes(strings.TrimSpace(identity.ID), 512)
	payload.SessionSource = strings.TrimSpace(identity.Source)
	if payload.SessionSource == "" {
		payload.SessionSource = sessionUnavailable
	}
	payload.LogSessionKey = payload.SessionKey
	payload.LogSessionSource = payload.SessionSource
	payload.ClientKind = identity.ClientKind
	payload.ClientFingerprint = identity.ClientFingerprint
	if identity.ThreadSource != "" {
		payload.ThreadSource = identity.ThreadSource
	}
}

type codexAgentSessionResolver struct{}

func (codexAgentSessionResolver) Match(agentSessionRequest) bool {
	return true
}

func (codexAgentSessionResolver) Resolve(_ context.Context, _ *Store, request agentSessionRequest) (agentSessionResolution, error) {
	identity := agentSessionIdentity{}
	if request.Payload != nil {
		identity.ID = request.Payload.SessionKey
		identity.Source = request.Payload.SessionSource
		identity.ClientKind = request.Payload.ClientKind
	}
	// The newest Codex clients can send a shared prompt_cache_key for related
	// work while putting each active (including subagent) thread in a request
	// header. A stable header must therefore win over payload cache metadata.
	if sessionID, source := codexHeaderSessionID(request.Headers); sessionID != "" {
		identity.ID, identity.Source = sessionID, source
	} else if identity.ID == "" && request.Payload != nil {
		identity.ID, identity.Source = codexPayloadSessionID(request.Payload.values)
	}
	if identity.ClientKind == "" && (codexClientRequest(request.Headers) || identity.Source == codexHeaderSessionSource || identity.Source == codexHeaderAffinitySource) {
		identity.ClientKind = agentClientKindCodex
	}
	if identity.ClientKind != "" {
		identity.ClientFingerprint = agentClientFingerprint(identity.ClientKind, request.Headers)
	}
	return agentSessionResolution{Identity: identity, Authoritative: true}, nil
}

func codexPayloadSessionID(values map[string]any) (string, string) {
	for _, candidate := range []struct {
		value  any
		source string
	}{
		{values["session_id"], "codex.payload.session_id"},
		{values["sessionId"], "codex.payload.session_id"},
		{values["thread_id"], "codex.payload.thread_id"},
		{values["threadId"], "codex.payload.thread_id"},
		{values["conversation_id"], "codex.payload.conversation_id"},
		{values["conversationId"], "codex.payload.conversation_id"},
	} {
		if value := validAgentSessionID(stringValue(candidate.value)); value != "" {
			return value, candidate.source
		}
	}
	for _, containerKey := range []string{"client_metadata", "metadata"} {
		metadata, _ := values[containerKey].(map[string]any)
		for _, candidate := range []struct {
			field  string
			source string
		}{
			{"session_id", "session_id"}, {"sessionId", "session_id"},
			{"thread_id", "thread_id"}, {"threadId", "thread_id"},
			{"conversation_id", "conversation_id"}, {"conversationId", "conversation_id"},
		} {
			if value := validAgentSessionID(stringValue(metadata[candidate.field])); value != "" {
				return value, "codex." + containerKey + "." + candidate.source
			}
		}
	}
	return "", ""
}

func codexHeaderSessionID(headers http.Header) (string, string) {
	for _, candidate := range []struct {
		key    string
		source string
	}{
		{key: "X-Session-Affinity", source: codexHeaderAffinitySource},
		{key: "X-Session-Id", source: codexHeaderSessionSource},
		{key: "session_id", source: codexHeaderSessionSource},
		{key: "Session-Id", source: codexHeaderSessionSource},
	} {
		if value := validAgentSessionID(headerValue(headers, candidate.key)); value != "" {
			return value, candidate.source
		}
	}
	return "", ""
}

func validAgentSessionID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 512 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func headerValue(headers http.Header, name string) string {
	if value := headers.Get(name); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) || len(values) == 0 {
			continue
		}
		return values[0]
	}
	return ""
}

func codexChatHistorySessionID(body []byte) string {
	payload, ok := decodeJSONObject(body)
	if !ok {
		return ""
	}
	messages, _ := payload["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "user" {
			continue
		}
		text := strings.TrimSpace(contentText(message["content"]))
		if text == "" || strings.HasPrefix(text, "<system-reminder>") || strings.HasPrefix(text, "Current runtime context.") {
			continue
		}
		return "codex-chat-" + hashJSON(message)
	}
	return ""
}

func codexClientRequest(headers http.Header) bool {
	for _, key := range []string{"User-Agent", "X-Client-Name"} {
		if strings.Contains(strings.ToLower(headers.Get(key)), "codex") {
			return true
		}
	}
	return false
}

func agentClientFingerprint(kind string, headers http.Header) string {
	values := map[string]string{"client-kind": kind}
	for _, key := range []string{"User-Agent", "X-Client-Name"} {
		if value := strings.TrimSpace(headers.Get(key)); value != "" {
			values[strings.ToLower(key)] = truncateRunes(value, 512)
		}
	}
	return hashJSON(values)
}

func persistAgentSessionIdentity(db *gorm.DB, tokenID uint64, sessionID string, source string, clientKind string, fingerprint string) error {
	if tokenID == 0 || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	updates := make(map[string]any, 3)
	if source = strings.TrimSpace(source); source != "" {
		updates["session_source"] = source
	}
	if clientKind = strings.TrimSpace(clientKind); clientKind != "" {
		updates["client_kind"] = clientKind
	}
	if fingerprint = strings.TrimSpace(fingerprint); fingerprint != "" {
		updates["client_fingerprint"] = fingerprint
	}
	if len(updates) == 0 {
		return nil
	}
	return db.Model(&RelaySessionState{}).
		Where("token_id = ? AND session_id = ?", tokenID, sessionID).
		Updates(updates).Error
}
