package gateway

import (
	"context"
	"net/http"
	"strings"
)

const (
	dshIdentityText              = "You are an AI agent powered by DeepSeek Harness."
	dshRuntimeContextPrefix      = "Current runtime context. This snapshot supersedes earlier runtime-context snapshots."
	dshDelegationText            = "You are a delegated subagent: your permission scope was fixed when you were started and cannot be widened from inside this session — operations that require approval are rejected automatically. When the task needs access beyond that scope, do not retry the denied operation; state the limitation in your reply so the delegating agent can handle it."
	dshChatFirstUserSource       = "dsh_chat.first_user"
	dshSubagentUnavailableSource = "dsh.subagent.unavailable"
)

type dshAgentSessionResolver struct{}

func (dshAgentSessionResolver) Match(request agentSessionRequest) bool {
	if request.Payload != nil && request.Payload.ClientKind != "" && request.Payload.ClientKind != agentClientKindDSH {
		return false
	}
	return dshClientRequest(request.Headers) || dshPayloadIdentity(request.Payload)
}

func (dshAgentSessionResolver) Resolve(_ context.Context, _ *Store, request agentSessionRequest) (agentSessionResolution, error) {
	identity := agentSessionIdentity{ClientKind: agentClientKindDSH, Source: sessionUnavailable}
	if request.Payload != nil {
		identity.ID = request.Payload.SessionKey
		identity.Source = request.Payload.SessionSource
		identity.ThreadSource = dshThreadSource(request.Payload.values)
	}
	identity.ClientFingerprint = agentClientFingerprint(agentClientKindDSH, request.Headers)
	// 专属会话头优先于缓存键；通用稳定头只有确认 DSH 后才标为 DSH 来源。
	if sessionID := validAgentSessionID(headerValue(request.Headers, dshHarnessSessionHeader)); sessionID != "" {
		identity.ID, identity.Source = sessionID, dshHeaderSessionSource
	} else if sessionID, source := codexHeaderSessionID(request.Headers); sessionID != "" {
		identity.ID, identity.Source = sessionID, dshHeaderSessionSource
		if source == codexHeaderAffinitySource {
			identity.Source = dshHeaderAffinitySource
		}
	}
	// 自定义 chat 路由可能不发 session 头。只对确认的主会话保留首条用户消息推断，
	// 不查共享历史前缀：fork 会继承父历史，不能据此把子会话并入主会话。
	// 相同首问的独立主会话仍可能碰撞；该来源是推断，不是客户端稳定会话标识。
	if identity.ID == "" && request.Endpoint == "chat" && identity.ThreadSource == codexThreadSourceUser {
		identity.ID = codexChatHistorySessionID(request.Body)
		if identity.ID != "" {
			identity.Source = dshChatFirstUserSource
		}
	}
	if identity.ID == "" && identity.ThreadSource == codexThreadSourceSubagent {
		identity.Source = dshSubagentUnavailableSource
	}
	return agentSessionResolution{Identity: identity, Authoritative: true}, nil
}

func dshClientRequest(headers http.Header) bool {
	if strings.TrimSpace(headerValue(headers, dshHarnessSessionHeader)) != "" {
		return true
	}
	for _, product := range strings.Fields(strings.ToLower(headerValue(headers, "User-Agent"))) {
		if strings.HasPrefix(product, "deepseek-harness/") {
			return true
		}
	}
	return false
}

func dshPayloadIdentity(payload *RelayPayload) bool {
	return payload != nil && dshPayloadHasIdentity(payload.values)
}

func dshPayloadHasIdentity(payload map[string]any) bool {
	if hasDSHIdentityLine(stringValue(payload["instructions"])) {
		return true
	}
	for _, key := range []string{"messages", "input"} {
		messages, _ := payload[key].([]any)
		for _, raw := range messages {
			message, _ := raw.(map[string]any)
			if message["role"] == "system" || message["role"] == "developer" {
				if hasDSHIdentityLine(contentText(message["content"])) {
					return true
				}
			}
		}
	}
	return false
}

func hasDSHIdentityLine(text string) bool {
	return hasExactLine(text, dshIdentityText)
}

func hasExactLine(text, expected string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == expected {
			return true
		}
	}
	return false
}

func dshThreadSource(payload map[string]any) string {
	// 只看最后一份 runtime snapshot。fork 的历史可能保留祖先的 snapshot；
	// 工具输出和普通用户引用不能用作当前代理身份依据。
	for _, key := range []string{"messages", "input"} {
		messages, _ := payload[key].([]any)
		for index := len(messages) - 1; index >= 0; index-- {
			message, _ := messages[index].(map[string]any)
			if message["role"] != "user" {
				continue
			}
			text := strings.TrimSpace(contentText(message["content"]))
			if !strings.HasPrefix(text, dshRuntimeContextPrefix+"\n") {
				continue
			}
			if hasExactLine(text, dshDelegationText) {
				return codexThreadSourceSubagent
			}
			return codexThreadSourceUser
		}
	}
	// 没有当前 runtime 证据，不猜主/子角色，也不按共享首问聚合。
	return ""
}
