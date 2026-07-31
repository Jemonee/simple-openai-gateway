package gateway

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

const (
	copilotClientKind          = "copilot"
	copilotIntegrationHeader   = "X-Github-Copilot-Integration-Id"
	copilotIntegrationID       = "copilot-cli"
	copilotHeaderSessionSource = "copilot.header_session_id"
	copilotWorkspaceStart      = "<copilot_tauri_workspace>"
	copilotWorkspaceEnd        = "</copilot_tauri_workspace>"
)

type copilotAgentSessionResolver struct{}

func (copilotAgentSessionResolver) Match(request agentSessionRequest) bool {
	return strings.EqualFold(strings.TrimSpace(request.Headers.Get(copilotIntegrationHeader)), copilotIntegrationID)
}

func (copilotAgentSessionResolver) Resolve(ctx context.Context, store *Store, request agentSessionRequest) (agentSessionResolution, error) {
	identity := agentSessionIdentity{
		Source:            sessionUnavailable,
		ClientKind:        copilotClientKind,
		ClientFingerprint: copilotClientFingerprint(request.Headers),
	}
	resolution := agentSessionResolution{Identity: identity, Authoritative: true}
	if sessionID := copilotHeaderSessionID(request.Headers); sessionID != "" {
		resolution.Identity.ID = sessionID
		resolution.Identity.Source = copilotHeaderSessionSource
		return resolution, nil
	}

	var err error
	switch request.Endpoint {
	case "chat":
		resolution.Identity, err = store.resolveCopilotChatSession(ctx, request, identity)
	case "responses":
		resolution.Identity = resolveCopilotResponsesSession(request, identity)
	}
	return resolution, err
}

func copilotClientFingerprint(_ http.Header) string {
	return hashJSON(map[string]string{
		"client-kind":    copilotClientKind,
		"integration-id": copilotIntegrationID,
	})
}

func copilotHeaderSessionID(headers http.Header) string {
	for _, key := range []string{
		"X-Copilot-Session-Id",
		"X-Github-Copilot-Session-Id",
		"X-Copilot-Thread-Id",
		"X-Github-Copilot-Thread-Id",
	} {
		if value := truncateRunes(strings.TrimSpace(headers.Get(key)), 512); value != "" {
			return value
		}
	}
	return ""
}

func copilotProjectSessionID(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	texts := make([]string, 0, 4)
	if instructions := stringValue(payload["instructions"]); instructions != "" {
		texts = append(texts, instructions)
	}
	if messages, ok := payload["messages"].([]any); ok {
		for _, rawMessage := range messages {
			message, _ := rawMessage.(map[string]any)
			if message["role"] == "system" {
				texts = append(texts, contentText(message["content"]))
			}
		}
	}
	for _, text := range texts {
		if sessionID := copilotWorkspaceUUID(text, "project_session_id"); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func copilotWorkspaceUUID(value string, key string) string {
	start := strings.Index(value, copilotWorkspaceStart)
	if start < 0 {
		return ""
	}
	workspace := value[start+len(copilotWorkspaceStart):]
	if end := strings.Index(workspace, copilotWorkspaceEnd); end >= 0 {
		workspace = workspace[:end]
	}
	prefix := key + ":"
	for _, line := range strings.Split(workspace, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		parsed, err := uuid.Parse(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
		if err == nil {
			return parsed.String()
		}
	}
	return ""
}

func copilotSessionTitle(endpoint string, body []byte) (string, bool) {
	switch endpoint {
	case "chat":
		return copilotChatSessionTitle(body)
	case "responses":
		return copilotResponsesSessionTitle(body)
	default:
		return "", false
	}
}

func cleanCopilotPrompt(value string) string {
	for _, block := range [][2]string{
		{"<current_datetime>", "</current_datetime>"},
		{"<system_notification>", "</system_notification>"},
		{"<system_reminder>", "</system_reminder>"},
	} {
		value = removeTaggedBlock(value, block[0], block[1])
	}
	return normalizeSessionName(value)
}

func copilotRenamedSessionTitle(value string) string {
	const prefix = `Renamed session to "`
	value = strings.TrimSpace(value)
	start := strings.Index(value, prefix)
	if start < 0 {
		return ""
	}
	remainder := value[start+len(prefix):]
	end := strings.Index(remainder, `"`)
	if end < 0 {
		return ""
	}
	return truncateRunes(normalizeSessionName(remainder[:end]), 80)
}

func removeTaggedBlock(value string, opening string, closing string) string {
	for {
		start := strings.Index(value, opening)
		if start < 0 {
			return value
		}
		end := strings.Index(value[start+len(opening):], closing)
		if end < 0 {
			return strings.TrimSpace(value[:start])
		}
		end += start + len(opening) + len(closing)
		value = value[:start] + " " + value[end:]
	}
}
