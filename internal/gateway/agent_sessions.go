package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	agentClientKindCodex = "codex"
	sessionUnavailable   = "unavailable"
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
}

type codexAgentSessionResolver struct{}

func (codexAgentSessionResolver) Match(agentSessionRequest) bool {
	return true
}

func (codexAgentSessionResolver) Resolve(_ context.Context, _ *Store, request agentSessionRequest) (agentSessionResolution, error) {
	identity := agentSessionIdentity{
		ID:     request.Payload.SessionKey,
		Source: request.Payload.SessionSource,
	}
	if codexClientRequest(request.Headers) {
		identity.ClientKind = agentClientKindCodex
		identity.ClientFingerprint = agentClientFingerprint(agentClientKindCodex, request.Headers)
	}
	return agentSessionResolution{Identity: identity, Authoritative: true}, nil
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
