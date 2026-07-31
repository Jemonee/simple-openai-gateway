package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

const payloadDeltaVersion = 1

type payloadManifest struct {
	Fields map[string]string   `json:"fields"`
	Arrays map[string][]string `json:"arrays,omitempty"`
}

type payloadDeltaMetadata struct {
	Version       int            `json:"version"`
	Mode          string         `json:"mode"`
	BaseRequestID string         `json:"baseRequestId"`
	OmittedFields []string       `json:"omittedFields,omitempty"`
	OmittedItems  map[string]int `json:"omittedItems,omitempty"`
	RemovedFields []string       `json:"removedFields,omitempty"`
}

type payloadDeltaEnvelope struct {
	Metadata payloadDeltaMetadata `json:"_gatewayLog"`
	Payload  map[string]any       `json:"payload"`
}

func compactSessionPayload(db *gorm.DB, tokenID uint64, sessionID string, requestID string, title string, threadSource string, body []byte, responseBody []byte, now time.Time) []byte {
	sessionID = truncateRunes(strings.TrimSpace(sessionID), 512)
	if tokenID == 0 || sessionID == "" {
		return body
	}
	manifest, ok := buildPayloadManifest(body)
	if !ok {
		upsertSessionTitleWithoutManifest(db, tokenID, sessionID, requestID, title, threadSource, now)
		return body
	}
	requestManifestJSON, _ := json.Marshal(manifest)
	appendResponseManifest(&manifest, body, responseBody)

	var state RelaySessionState
	err := db.Where("token_id = ? AND session_id = ?", tokenID, sessionID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		encodedManifest, _ := json.Marshal(manifest)
		_ = db.Create(&RelaySessionState{
			TokenID:             tokenID,
			SessionID:           sessionID,
			Title:               title,
			ThreadSource:        threadSource,
			LatestRequestID:     requestID,
			LastActivityAt:      &now,
			RequestManifestJSON: string(requestManifestJSON),
			PayloadManifestJSON: string(encodedManifest),
			CreatedAt:           now,
			UpdatedAt:           now,
		}).Error
		return body
	}
	if err != nil {
		return body
	}

	compacted := body
	if state.LatestRequestID != "" {
		compacted = compactPayloadAgainstManifest(body, state.PayloadManifestJSON, state.LatestRequestID, "session")
	}
	encodedManifest, _ := json.Marshal(manifest)
	updates := map[string]any{
		"latest_request_id":     requestID,
		"last_activity_at":      now,
		"request_manifest_json": string(requestManifestJSON),
		"payload_manifest_json": string(encodedManifest),
		"updated_at":            now,
	}
	if !state.TitleCustomized && strings.TrimSpace(state.Title) == "" && title != "" {
		updates["title"] = title
	}
	if preferred := preferredCodexThreadSource(state.ThreadSource, threadSource); preferred != state.ThreadSource {
		updates["thread_source"] = preferred
	}
	_ = db.Model(&RelaySessionState{}).
		Where("token_id = ? AND session_id = ?", tokenID, sessionID).
		Updates(updates).Error
	return compacted
}

func upsertSessionTitleWithoutManifest(db *gorm.DB, tokenID uint64, sessionID string, requestID string, title string, threadSource string, now time.Time) {
	var state RelaySessionState
	err := db.Where("token_id = ? AND session_id = ?", tokenID, sessionID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		_ = db.Create(&RelaySessionState{
			TokenID: tokenID, SessionID: sessionID, Title: title, ThreadSource: threadSource, LatestRequestID: requestID,
			LastActivityAt: &now, CreatedAt: now, UpdatedAt: now,
		}).Error
		return
	}
	if err != nil {
		return
	}
	updates := map[string]any{"latest_request_id": requestID, "last_activity_at": now, "updated_at": now}
	if !state.TitleCustomized && strings.TrimSpace(state.Title) == "" && title != "" {
		updates["title"] = title
	}
	if preferred := preferredCodexThreadSource(state.ThreadSource, threadSource); preferred != state.ThreadSource {
		updates["thread_source"] = preferred
	}
	_ = db.Model(&RelaySessionState{}).
		Where("token_id = ? AND session_id = ?", tokenID, sessionID).
		Updates(updates).Error
}

func compactAttemptPayload(body []byte, original []byte, requestID string) []byte {
	manifest, ok := buildPayloadManifest(original)
	if !ok {
		return body
	}
	encodedManifest, _ := json.Marshal(manifest)
	return compactPayloadAgainstManifest(body, string(encodedManifest), requestID, "attempt")
}

func compactPayloadAgainstManifest(body []byte, manifestJSON string, baseRequestID string, mode string) []byte {
	current, ok := decodeJSONObject(body)
	if !ok {
		return body
	}
	var previous payloadManifest
	if json.Unmarshal([]byte(manifestJSON), &previous) != nil || len(previous.Fields) == 0 {
		return body
	}

	metadata := payloadDeltaMetadata{
		Version:       payloadDeltaVersion,
		Mode:          mode,
		BaseRequestID: baseRequestID,
		OmittedItems:  make(map[string]int),
	}
	delta := make(map[string]any)
	keys := make([]string, 0, len(current))
	for key := range current {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := current[key]
		if items, arrayOK := value.([]any); arrayOK && (key == "messages" || key == "input") {
			if omitted, prefixOK := matchingArrayPrefix(items, previous.Arrays[key]); prefixOK && omitted > 0 {
				metadata.OmittedItems[key] = omitted
				if omitted < len(items) {
					delta[key] = items[omitted:]
				}
				continue
			}
		}
		if previous.Fields[key] != "" && previous.Fields[key] == hashJSON(value) {
			metadata.OmittedFields = append(metadata.OmittedFields, key)
			continue
		}
		delta[key] = value
	}
	for key := range previous.Fields {
		if _, exists := current[key]; !exists {
			metadata.RemovedFields = append(metadata.RemovedFields, key)
		}
	}
	sort.Strings(metadata.OmittedFields)
	sort.Strings(metadata.RemovedFields)
	if len(metadata.OmittedFields) == 0 && len(metadata.OmittedItems) == 0 {
		return body
	}
	encoded, err := json.Marshal(payloadDeltaEnvelope{Metadata: metadata, Payload: delta})
	if err != nil || len(encoded) >= len(bytes.TrimSpace(body)) {
		return body
	}
	return encoded
}

func buildPayloadManifest(body []byte) (payloadManifest, bool) {
	payload, ok := decodeJSONObject(body)
	if !ok {
		return payloadManifest{}, false
	}
	manifest := payloadManifest{Fields: make(map[string]string), Arrays: make(map[string][]string)}
	for key, value := range payload {
		manifest.Fields[key] = hashJSON(value)
		if items, arrayOK := value.([]any); arrayOK && (key == "messages" || key == "input") {
			hashes := make([]string, 0, len(items))
			for _, item := range items {
				hashes = append(hashes, conversationItemHash(item))
			}
			manifest.Arrays[key] = hashes
		}
	}
	return manifest, true
}

func decodeJSONObject(body []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if decoder.Decode(&payload) != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

func matchingArrayPrefix(items []any, previousHashes []string) (int, bool) {
	if len(previousHashes) == 0 || len(previousHashes) > len(items) {
		return 0, false
	}
	for index, previousHash := range previousHashes {
		if conversationItemHash(items[index]) != previousHash {
			return 0, false
		}
	}
	return len(previousHashes), true
}

func appendResponseManifest(manifest *payloadManifest, requestBody []byte, responseBody []byte) {
	if len(responseBody) == 0 {
		return
	}
	payload, ok := decodeJSONObject(requestBody)
	if !ok {
		return
	}
	arrayKey := ""
	if _, exists := payload["messages"].([]any); exists {
		arrayKey = "messages"
	} else if _, exists := payload["input"].([]any); exists {
		arrayKey = "input"
	}
	if arrayKey == "" {
		return
	}
	manifest.Arrays[arrayKey] = append(manifest.Arrays[arrayKey], responseConversationHashes(responseBody)...)
}

func responseConversationHashes(body []byte) []string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		return sseResponseConversationHashes(trimmed)
	}
	payload, ok := decodeJSONObject(trimmed)
	if !ok {
		return nil
	}
	return responseObjectConversationHashes(payload)
}

func responseObjectConversationHashes(payload map[string]any) []string {
	result := make([]string, 0)
	if choices, ok := payload["choices"].([]any); ok {
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			if message, exists := choice["message"]; exists {
				result = append(result, conversationItemHash(message))
			}
		}
	}
	if output, ok := payload["output"].([]any); ok {
		for _, item := range output {
			result = append(result, conversationItemHash(item))
		}
	}
	return compactHashes(result)
}

func sseResponseConversationHashes(body []byte) []string {
	var assistantText strings.Builder
	var completed map[string]any
	completedItems := make([]string, 0)
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var event map[string]any
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		typeName, _ := event["type"].(string)
		if (typeName == "response.completed" || typeName == "response.failed") && event["response"] != nil {
			completed, _ = event["response"].(map[string]any)
		}
		if typeName == "response.output_item.done" && event["item"] != nil {
			completedItems = append(completedItems, conversationItemHash(event["item"]))
		}
		if (typeName == "response.output_text.delta" || typeName == "response.reasoning_summary_text.delta") && event["delta"] != nil {
			if text, ok := event["delta"].(string); ok && typeName == "response.output_text.delta" {
				assistantText.WriteString(text)
			}
		}
		if choices, ok := event["choices"].([]any); ok {
			for _, item := range choices {
				choice, _ := item.(map[string]any)
				delta, _ := choice["delta"].(map[string]any)
				assistantText.WriteString(contentText(delta["content"]))
			}
		}
	}
	if completed != nil {
		if hashes := responseObjectConversationHashes(completed); len(hashes) > 0 {
			return hashes
		}
	}
	if hashes := compactHashes(completedItems); len(hashes) > 0 {
		return hashes
	}
	if text := assistantText.String(); text != "" {
		return []string{conversationItemHash(map[string]any{"role": "assistant", "content": text})}
	}
	return nil
}

func conversationItemHash(value any) string {
	item, ok := value.(map[string]any)
	if !ok {
		return hashJSON(value)
	}
	canonical := make(map[string]any)
	for _, key := range []string{"role", "name", "call_id", "tool_call_id", "arguments", "output"} {
		if item[key] != nil {
			canonical[key] = item[key]
		}
	}
	if item["role"] == nil && item["type"] != nil {
		canonical["type"] = item["type"]
	}
	if content, exists := item["content"]; exists {
		if text := strings.TrimSpace(contentText(content)); text != "" {
			canonical["text"] = text
		}
	}
	if text := strings.TrimSpace(contentText(item["text"])); text != "" {
		canonical["text"] = text
	}
	if item["tool_calls"] != nil {
		canonical["tool_calls"] = item["tool_calls"]
	}
	if len(canonical) == 0 {
		return hashJSON(value)
	}
	return hashJSON(canonical)
}

func compactHashes(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func hashJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
