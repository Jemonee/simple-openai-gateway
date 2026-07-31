package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	copilotChatHistorySource = "copilot_chat.history"
	copilotChatProjectSource = "copilot_chat.project_session_id"
	copilotChatActiveWindow  = 12 * time.Hour
)

func (s *Store) resolveCopilotChatSession(ctx context.Context, request agentSessionRequest, base agentSessionIdentity) (agentSessionIdentity, error) {
	if request.TokenID == 0 {
		return base, nil
	}
	if sessionID := copilotProjectSessionID(request.Payload.values); sessionID != "" {
		base.ID = sessionID
		base.Source = copilotChatProjectSource
		return base, nil
	}

	manifest, ok := buildPayloadManifest(request.Body)
	if !ok || len(manifest.Arrays["messages"]) == 0 {
		return base, nil
	}
	manifestJSONBytes, err := json.Marshal(manifest)
	if err != nil {
		return base, err
	}
	manifestJSON := string(manifestJSONBytes)
	historyHash := hashJSON(manifest.Arrays["messages"])
	cutoff := request.Now.Add(-copilotChatActiveWindow)
	resolvedID := ""

	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var existing RelayChatSessionClaim
		err := db.Where(
			"token_id = ? AND client_fingerprint = ? AND request_history_hash = ? AND updated_at >= ?",
			request.TokenID, base.ClientFingerprint, historyHash, cutoff,
		).First(&existing).Error
		if err == nil {
			resolvedID = existing.SessionID
			return db.Model(&existing).Update("updated_at", request.Now).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		resolvedID, err = resolveChatHistoryCandidate(db, request.TokenID, base.ClientFingerprint, manifest.Arrays["messages"], cutoff)
		if err != nil {
			return err
		}
		if resolvedID == "" {
			resolvedID = "chat_" + uuid.NewString()
		}

		claim := RelayChatSessionClaim{
			TokenID: request.TokenID, ClientFingerprint: base.ClientFingerprint, RequestHistoryHash: historyHash,
			SessionID: resolvedID, RequestManifestJSON: manifestJSON, CreatedAt: request.Now, UpdatedAt: request.Now,
		}
		result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&claim)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			if err := db.Where(
				"token_id = ? AND client_fingerprint = ? AND request_history_hash = ?",
				request.TokenID, base.ClientFingerprint, historyHash,
			).First(&claim).Error; err != nil {
				return err
			}
			resolvedID = claim.SessionID
		}

		state := RelaySessionState{
			TokenID: request.TokenID, SessionID: resolvedID, SessionSource: copilotChatHistorySource,
			ClientKind: copilotClientKind, ClientFingerprint: base.ClientFingerprint,
			RequestManifestJSON: manifestJSON, PayloadManifestJSON: manifestJSON,
			CreatedAt: request.Now, UpdatedAt: request.Now,
		}
		return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error
	})
	if err != nil {
		return base, err
	}
	base.ID = resolvedID
	base.Source = copilotChatHistorySource
	return base, nil
}

func copilotChatMessages(body []byte) ([]any, bool) {
	payload, ok := decodeJSONObject(body)
	if !ok {
		return nil, false
	}
	messages, _ := payload["messages"].([]any)
	return messages, len(messages) > 0
}

func copilotChatSessionTitle(body []byte) (string, bool) {
	messages, ok := copilotChatMessages(body)
	if !ok {
		return "", false
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message, _ := messages[index].(map[string]any)
		if message["role"] != "tool" {
			continue
		}
		if title := copilotRenamedSessionTitle(contentText(message["content"])); title != "" {
			return title, true
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message, _ := messages[index].(map[string]any)
		if message["role"] != "user" {
			continue
		}
		if prompt := cleanCopilotPrompt(contentText(message["content"])); prompt != "" {
			return truncateRunes(prompt, 80), false
		}
	}
	return "", false
}

func resolveChatHistoryCandidate(db *gorm.DB, tokenID uint64, fingerprint string, current []string, cutoff time.Time) (string, error) {
	scores := make(map[string]int)
	var states []RelaySessionState
	if err := db.Select("session_id, request_manifest_json, payload_manifest_json").
		Where("token_id = ? AND client_fingerprint = ? AND updated_at >= ?", tokenID, fingerprint, cutoff).
		Order("updated_at DESC").Limit(128).Find(&states).Error; err != nil {
		return "", err
	}
	for _, state := range states {
		requestHistory := manifestConversationHashes(state.RequestManifestJSON, "messages")
		expectedHistory := manifestConversationHashes(state.PayloadManifestJSON, "messages")
		if len(expectedHistory) > len(requestHistory) && isHashPrefix(expectedHistory, current) {
			scores[state.SessionID] = max(scores[state.SessionID], 4)
		}
		if len(requestHistory) < len(current) && isHashPrefix(requestHistory, current) {
			scores[state.SessionID] = max(scores[state.SessionID], 3)
		}
	}

	var claims []RelayChatSessionClaim
	if err := db.Select("session_id, request_manifest_json").
		Where("token_id = ? AND client_fingerprint = ? AND updated_at >= ?", tokenID, fingerprint, cutoff).
		Order("updated_at DESC").Limit(128).Find(&claims).Error; err != nil {
		return "", err
	}
	for _, claim := range claims {
		history := manifestConversationHashes(claim.RequestManifestJSON, "messages")
		if len(history) < len(current) && isHashPrefix(history, current) {
			scores[claim.SessionID] = max(scores[claim.SessionID], 2)
		}
	}

	bestID := ""
	bestScore := 0
	ambiguous := false
	for sessionID, score := range scores {
		if score > bestScore {
			bestID, bestScore, ambiguous = sessionID, score, false
		} else if score == bestScore && score > 0 && sessionID != bestID {
			ambiguous = true
		}
	}
	if ambiguous {
		return "", nil
	}
	return bestID, nil
}

func manifestConversationHashes(manifestJSON string, key string) []string {
	var manifest payloadManifest
	if manifestJSON == "" || json.Unmarshal([]byte(manifestJSON), &manifest) != nil {
		return nil
	}
	return manifest.Arrays[key]
}

func isHashPrefix(prefix []string, values []string) bool {
	if len(prefix) == 0 || len(prefix) > len(values) {
		return false
	}
	for index := range prefix {
		if prefix[index] != values[index] {
			return false
		}
	}
	return true
}
