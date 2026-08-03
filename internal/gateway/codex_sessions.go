package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	codexTitleSessionSource    = "codex_title_generation"
	codexGuardianSessionSource = "codex_guardian"
	codexGuardianSessionPrefix = "guardian:"
	codexThreadSourceUser      = "user"
	codexThreadSourceAmbient   = "ambient_suggestions"
	codexThreadSourceUnknown   = "unavailable"
	codexTitlePromptMarker     = "User prompt:"
	codexAttachedPromptMarker  = "## My request for Codex:"
	codexTitleMatchWindow      = 5 * time.Minute
	codexTitleStartTolerance   = 30 * time.Second
)

var codexAuxiliarySessionSources = []string{codexTitleSessionSource, codexGuardianSessionSource}

func applyCodexPayloadSession(payload *RelayPayload, values map[string]any) {
	payload.SessionKey = stringValue(values["prompt_cache_key"])
	if payload.SessionKey != "" {
		payload.SessionSource = "prompt_cache_key"
	}
	for _, metadataKey := range []string{"client_metadata", "metadata"} {
		if payload.SessionKey != "" {
			break
		}
		metadata, _ := values[metadataKey].(map[string]any)
		if metadata == nil {
			continue
		}
		for _, field := range []string{"session_id", "thread_id"} {
			if payload.SessionKey = stringValue(metadata[field]); payload.SessionKey != "" {
				payload.SessionSource = metadataKey + "." + field
				break
			}
		}
	}
	payload.SessionKey = truncateRunes(payload.SessionKey, 512)
	if payload.SessionKey == "" {
		payload.SessionSource = sessionUnavailable
	}
	payload.LogSessionKey = payload.SessionKey
	payload.LogSessionSource = payload.SessionSource
	payload.ThreadSource = codexThreadSourceFromPayload(values)
}

func codexThreadSourceFromPayload(payload map[string]any) string {
	for _, metadataKey := range []string{"client_metadata", "metadata"} {
		metadata, _ := payload[metadataKey].(map[string]any)
		if metadata == nil {
			continue
		}
		if source := normalizeCodexThreadSource(stringValue(metadata["thread_source"])); source != "" {
			return source
		}
		turnMetadata := codexTurnMetadata(metadata["x-codex-turn-metadata"])
		if source := normalizeCodexThreadSource(stringValue(turnMetadata["thread_source"])); source != "" {
			return source
		}
	}
	return ""
}

func codexCompactionRequestFromPayload(payload map[string]any) bool {
	isCompaction, _ := codexCompactionMetadataFromPayload(payload)
	return isCompaction
}

func codexCompactionMetadataFromPayload(payload map[string]any) (bool, string) {
	for _, metadataKey := range []string{"client_metadata", "metadata"} {
		metadata, _ := payload[metadataKey].(map[string]any)
		if metadata == nil {
			continue
		}
		turnMetadata := codexTurnMetadata(metadata["x-codex-turn-metadata"])
		if strings.EqualFold(stringValue(turnMetadata["request_kind"]), "compaction") {
			compaction, _ := turnMetadata["compaction"].(map[string]any)
			trigger := strings.ToLower(strings.TrimSpace(stringValue(compaction["trigger"])))
			if trigger == "" && strings.EqualFold(stringValue(compaction["reason"]), "context_limit") {
				trigger = contextWindowAutoCompactionTrigger
			}
			return true, truncateRunes(trigger, 16)
		}
	}
	return false, ""
}

func codexTurnMetadata(value any) map[string]any {
	switch typed := value.(type) {
	case string:
		metadata := make(map[string]any)
		if json.Unmarshal([]byte(typed), &metadata) == nil {
			return metadata
		}
	case map[string]any:
		return typed
	}
	return nil
}

func codexCompactionRequestFromBody(body []byte) bool {
	isCompaction, _ := codexCompactionMetadataFromBody(body)
	return isCompaction
}

func codexCompactionMetadataFromBody(body []byte) (bool, string) {
	payload, ok := decodeJSONObject(body)
	if !ok {
		return false, ""
	}
	if _, isDelta := payload["_gatewayLog"]; isDelta {
		if nested, nestedOK := payload["payload"].(map[string]any); nestedOK {
			payload = nested
		}
	}
	return codexCompactionMetadataFromPayload(payload)
}

func codexThreadSourceFromBody(body []byte) string {
	payload, ok := decodeJSONObject(body)
	if !ok {
		return ""
	}
	return codexThreadSourceFromPayload(payload)
}

func normalizeCodexThreadSource(source string) string {
	return truncateRunes(strings.ToLower(strings.TrimSpace(source)), 48)
}

func preferredCodexThreadSource(current string, candidate string) string {
	current = normalizeCodexThreadSource(current)
	candidate = normalizeCodexThreadSource(candidate)
	if candidate == "" {
		return current
	}
	if current == "" || candidate == codexThreadSourceUser {
		return candidate
	}
	return current
}

func loggedCodexSessionIdentity(sessionID string, source string) (string, string) {
	sessionID = strings.TrimSpace(sessionID)
	if strings.HasPrefix(sessionID, codexGuardianSessionPrefix) {
		canonical := strings.TrimSpace(strings.TrimPrefix(sessionID, codexGuardianSessionPrefix))
		if canonical != "" {
			return canonical, codexGuardianSessionSource
		}
	}
	return sessionID, source
}

func codexRequestPrompt(body []byte) string {
	payload, ok := decodeJSONObject(body)
	if !ok {
		return ""
	}
	if input, exists := payload["input"]; exists {
		return normalizeCodexPrompt(latestResponsesInputText(input))
	}
	if messages, ok := payload["messages"].([]any); ok {
		return normalizeCodexPrompt(latestUserMessageText(messages))
	}
	return ""
}

func codexTitleRequestPrompt(body []byte) (string, bool) {
	payload, ok := decodeJSONObject(body)
	if !ok {
		return "", false
	}
	if !payloadRequestsTitleSchema(payload) {
		return "", false
	}
	instruction := ""
	if input, exists := payload["input"]; exists {
		instruction = latestResponsesInputText(input)
	} else if messages, ok := payload["messages"].([]any); ok {
		instruction = latestUserMessageText(messages)
	}
	instruction = normalizeCodexPrompt(instruction)
	if instruction == "" {
		return "", false
	}
	for _, marker := range []string{codexTitlePromptMarker, "Original prompt:", "User request:", "Prompt:", "用户请求：", "用户问题："} {
		if markerIndex := strings.LastIndex(strings.ToLower(instruction), strings.ToLower(marker)); markerIndex >= 0 {
			prompt := normalizeCodexPrompt(instruction[markerIndex+len(marker):])
			if prompt != "" {
				return prompt, true
			}
		}
	}
	return instruction, true
}

func payloadRequestsTitleSchema(payload map[string]any) bool {
	if text, ok := payload["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok && titleSchema(format) {
			return true
		}
	}
	responseFormat, _ := payload["response_format"].(map[string]any)
	if responseFormat == nil || responseFormat["type"] != "json_schema" {
		return false
	}
	if nested, ok := responseFormat["json_schema"].(map[string]any); ok {
		return titleSchema(nested)
	}
	return titleSchema(responseFormat)
}

func titleSchema(format map[string]any) bool {
	if format["type"] != nil && format["type"] != "json_schema" {
		return false
	}
	schema, _ := format["schema"].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	_, hasTitle := properties["title"]
	return hasTitle
}

func normalizeCodexPrompt(value string) string {
	if markerIndex := strings.LastIndex(value, codexAttachedPromptMarker); markerIndex >= 0 {
		value = value[markerIndex+len(codexAttachedPromptMarker):]
	}
	if imageIndex := strings.Index(value, "<image "); imageIndex >= 0 {
		value = value[:imageIndex]
	}
	return strings.Join(strings.Fields(value), " ")
}

func codexLogPayloadMetadata(requestBody []byte, responseBody []byte) (string, bool, string) {
	if titlePrompt, isTitleRequest := codexTitleRequestPrompt(requestBody); isTitleRequest {
		return hashCodexPrompt(titlePrompt), true, codexGeneratedTitle(responseBody)
	}
	return hashCodexPrompt(codexRequestPrompt(requestBody)), false, ""
}

func hashCodexPrompt(value string) string {
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func codexGeneratedTitle(responseBody []byte) string {
	output := strings.TrimSpace(responsesOutputText(responseBody))
	if output == "" {
		return ""
	}
	if strings.HasPrefix(output, "```") {
		lines := strings.Split(output, "\n")
		if len(lines) >= 3 {
			output = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	for range 2 {
		var unquoted string
		if json.Unmarshal([]byte(output), &unquoted) != nil {
			break
		}
		output = strings.TrimSpace(unquoted)
	}
	if start, end := strings.Index(output, "{"), strings.LastIndex(output, "}"); start >= 0 && end > start {
		output = output[start : end+1]
	}
	var result struct {
		Title string `json:"title"`
	}
	if json.Unmarshal([]byte(output), &result) != nil {
		return ""
	}
	return truncateRunes(normalizeSessionName(result.Title), 80)
}

func responsesOutputText(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}
	if !bytes.HasPrefix(trimmed, []byte("data:")) && !bytes.HasPrefix(trimmed, []byte("event:")) {
		payload, ok := decodeJSONObject(trimmed)
		if !ok {
			return ""
		}
		return responseObjectText(payload)
	}
	var doneText string
	var deltaText strings.Builder
	var completed map[string]any
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
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
		switch typeName {
		case "response.output_text.done":
			doneText, _ = event["text"].(string)
		case "response.output_text.delta":
			if delta, ok := event["delta"].(string); ok {
				deltaText.WriteString(delta)
			}
		case "response.completed":
			completed, _ = event["response"].(map[string]any)
		}
	}
	if strings.TrimSpace(doneText) != "" {
		return doneText
	}
	if completedText := responseObjectText(completed); completedText != "" {
		return completedText
	}
	return deltaText.String()
}

func responseObjectText(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if outputText, ok := payload["output_text"].(string); ok {
		return outputText
	}
	output, _ := payload["output"].([]any)
	var result strings.Builder
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		result.WriteString(contentText(item["content"]))
	}
	if result.Len() == 0 {
		if choices, ok := payload["choices"].([]any); ok {
			for _, rawChoice := range choices {
				choice, _ := rawChoice.(map[string]any)
				message, _ := choice["message"].(map[string]any)
				result.WriteString(contentText(message["content"]))
			}
		}
	}
	return result.String()
}

func codexTitleMatchesMain(titleBody []byte, mainBody []byte, titleHash string, mainHash string) bool {
	if titleHash != "" && mainHash != "" && titleHash == mainHash {
		return true
	}
	mainPrompt := normalizeCodexPrompt(codexRequestPrompt(mainBody))
	if mainPrompt == "" {
		return false
	}
	titlePayload, ok := decodeJSONObject(titleBody)
	if !ok {
		return false
	}
	instruction := ""
	if input, exists := titlePayload["input"]; exists {
		instruction = latestResponsesInputText(input)
	} else if messages, ok := titlePayload["messages"].([]any); ok {
		instruction = latestUserMessageText(messages)
	}
	return strings.Contains(strings.ToLower(normalizeCodexPrompt(instruction)), strings.ToLower(mainPrompt))
}

func (s *Store) mergePrecedingCodexTitleRequest(db *gorm.DB, log *RelayRequestLog, rawBody []byte, mainStartedAt time.Time) (string, error) {
	if log.CodexSessionID == "" || log.CodexSessionSource != "prompt_cache_key" {
		return "", nil
	}
	var existingMainRequests int64
	if err := db.Model(&RelayRequestLog{}).
		Where("token_id = ? AND codex_session_id = ? AND codex_session_source NOT IN ?", log.TokenID, log.CodexSessionID, codexAuxiliarySessionSources).
		Count(&existingMainRequests).Error; err != nil {
		return "", err
	}
	if existingMainRequests > 0 {
		return "", nil
	}
	mainPromptHash := log.CodexPromptHash
	if mainPromptHash == "" {
		mainPromptHash = hashCodexPrompt(codexRequestPrompt(rawBody))
	}
	if mainPromptHash == "" {
		return "", nil
	}
	var candidates []RelayRequestLog
	if err := db.Select("id, token_id, codex_session_id, codex_session_source, session_name, codex_prompt_hash, codex_title_request, codex_generated_title, request_body, response_body, latency_ms, duration_ms, created_at").
		Where("token_id = ? AND codex_session_id <> ? AND codex_session_source = ? AND outcome = ?", log.TokenID, log.CodexSessionID, "prompt_cache_key", RelayOutcomeSuccess).
		Where("created_at >= ? AND created_at <= ?", mainStartedAt.Add(-codexTitleStartTolerance), mainStartedAt.Add(codexTitleMatchWindow)).
		Where("codex_title_request = ? OR request_parameters_json LIKE ?", true, `%"text_format":"json_schema"%`).
		Order("created_at DESC, id DESC").Limit(8).Find(&candidates).Error; err != nil {
		return "", err
	}
	for index := range candidates {
		candidate := &candidates[index]
		candidateStartedAt := candidate.CreatedAt.Add(-relayRequestElapsedDuration(*candidate))
		if candidateStartedAt.Before(mainStartedAt.Add(-codexTitleStartTolerance)) || candidateStartedAt.After(mainStartedAt.Add(codexTitleStartTolerance)) {
			continue
		}
		candidatePromptHash := candidate.CodexPromptHash
		if candidatePromptHash == "" {
			titlePrompt, isTitleRequest := codexTitleRequestPrompt([]byte(decompressStoredPayload(candidate.RequestBody)))
			if !isTitleRequest {
				continue
			}
			candidatePromptHash = hashCodexPrompt(titlePrompt)
		}
		candidateBody := []byte(decompressStoredPayload(candidate.RequestBody))
		if !codexTitleMatchesMain(candidateBody, rawBody, candidatePromptHash, mainPromptHash) {
			continue
		}
		var candidateRequestCount int64
		if err := db.Model(&RelayRequestLog{}).
			Where("token_id = ? AND codex_session_id = ?", candidate.TokenID, candidate.CodexSessionID).
			Count(&candidateRequestCount).Error; err != nil {
			return "", err
		}
		if candidateRequestCount != 1 {
			continue
		}
		title := candidate.CodexGeneratedTitle
		if title == "" {
			title = codexGeneratedTitle([]byte(decompressStoredPayload(candidate.ResponseBody)))
		}
		if title == "" {
			continue
		}
		return s.mergeCodexTitleLog(db, *candidate, log.CodexSessionID, title)
	}
	return "", nil
}

func (s *Store) mergeFollowingCodexTitleRequest(db *gorm.DB, log *RelayRequestLog, titleStartedAt time.Time) (string, string, error) {
	if log.CodexSessionID == "" || log.CodexSessionSource != "prompt_cache_key" || !log.CodexTitleRequest || log.CodexGeneratedTitle == "" {
		return "", "", nil
	}
	var candidates []RelayRequestLog
	if err := db.Select("id, token_id, codex_session_id, codex_session_source, codex_prompt_hash, request_body, latency_ms, duration_ms, created_at").
		Where("token_id = ? AND codex_session_id <> ? AND codex_session_id <> ''", log.TokenID, log.CodexSessionID).
		Where("codex_session_source = ? AND codex_title_request = ? AND outcome = ?", "prompt_cache_key", false, RelayOutcomeSuccess).
		Where("created_at >= ? AND created_at <= ?", titleStartedAt.Add(-codexTitleStartTolerance), titleStartedAt.Add(codexTitleMatchWindow)).
		Order("created_at DESC, id DESC").Limit(8).Find(&candidates).Error; err != nil {
		return "", "", err
	}
	for index := range candidates {
		candidate := &candidates[index]
		candidateStartedAt := candidate.CreatedAt.Add(-relayRequestElapsedDuration(*candidate))
		if candidateStartedAt.Before(titleStartedAt.Add(-codexTitleStartTolerance)) || candidateStartedAt.After(titleStartedAt.Add(codexTitleStartTolerance)) {
			continue
		}
		candidatePromptHash := candidate.CodexPromptHash
		if candidatePromptHash == "" {
			candidatePromptHash = hashCodexPrompt(codexRequestPrompt([]byte(decompressStoredPayload(candidate.RequestBody))))
		}
		titleBody := []byte(decompressStoredPayload(log.RequestBody))
		candidateBody := []byte(decompressStoredPayload(candidate.RequestBody))
		if !codexTitleMatchesMain(titleBody, candidateBody, log.CodexPromptHash, candidatePromptHash) {
			continue
		}
		title, err := s.mergeCodexTitleLog(db, *log, candidate.CodexSessionID, log.CodexGeneratedTitle)
		if err != nil || title == "" {
			return "", "", err
		}
		if err := s.applyMergedCodexTitle(db, candidate.TokenID, candidate.CodexSessionID, candidate.ID, title, log.CreatedAt); err != nil {
			return "", "", err
		}
		return candidate.CodexSessionID, title, nil
	}
	return "", "", nil
}

func (s *Store) mergeCodexTitleLog(db *gorm.DB, candidate RelayRequestLog, canonicalSessionID string, generatedTitle string) (string, error) {
	title := generatedTitle
	var oldState RelaySessionState
	stateErr := db.Where("token_id = ? AND session_id = ?", candidate.TokenID, candidate.CodexSessionID).First(&oldState).Error
	if stateErr == nil && oldState.TitleCustomized && strings.TrimSpace(oldState.Title) != "" {
		title = oldState.Title
	} else if stateErr != nil && !errors.Is(stateErr, gorm.ErrRecordNotFound) {
		return "", stateErr
	}
	result := db.Model(&RelayRequestLog{}).
		Where("id = ? AND token_id = ? AND codex_session_id = ? AND codex_session_source = ?", candidate.ID, candidate.TokenID, candidate.CodexSessionID, "prompt_cache_key").
		Updates(map[string]any{
			"codex_session_id":     canonicalSessionID,
			"codex_session_source": codexTitleSessionSource,
			"session_name":         title,
		})
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected != 1 {
		return "", nil
	}
	if err := db.Where("token_id = ? AND session_id = ?", candidate.TokenID, candidate.CodexSessionID).Delete(&RelaySessionState{}).Error; err != nil {
		return "", err
	}
	return title, nil
}

func (s *Store) applyMergedCodexTitle(db *gorm.DB, tokenID uint64, sessionID string, requestID string, title string, now time.Time) error {
	var state RelaySessionState
	err := db.Where("token_id = ? AND session_id = ?", tokenID, sessionID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.Create(&RelaySessionState{
			TokenID: tokenID, SessionID: sessionID, Title: title, LatestRequestID: requestID,
			LastActivityAt: &now, CreatedAt: now, UpdatedAt: now,
		}).Error
	}
	if err != nil || state.TitleCustomized {
		return err
	}
	return db.Model(&RelaySessionState{}).
		Where("token_id = ? AND session_id = ?", tokenID, sessionID).
		Updates(map[string]any{"title": title, "last_activity_at": now, "updated_at": now}).Error
}

func (s *Store) backfillCodexAuxiliarySessions() error {
	const migrationName = "codex_auxiliary_sessions_v3"
	var migration GatewayMigration
	err := s.db.First(&migration, "name = ?", migrationName).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	cutoff := time.Now().UTC().Add(-DetailedLogRetentionDays * 24 * time.Hour)
	return s.db.Transaction(func(db *gorm.DB) error {
		var guardians []RelayRequestLog
		if err := db.Select("id, codex_session_id").
			Where("created_at >= ? AND codex_session_id LIKE ?", cutoff, codexGuardianSessionPrefix+"%").
			Find(&guardians).Error; err != nil {
			return err
		}
		for _, guardian := range guardians {
			canonical, source := loggedCodexSessionIdentity(guardian.CodexSessionID, "prompt_cache_key")
			if canonical == guardian.CodexSessionID {
				continue
			}
			if err := db.Model(&RelayRequestLog{}).Where("id = ?", guardian.ID).
				Updates(map[string]any{"codex_session_id": canonical, "codex_session_source": source}).Error; err != nil {
				return err
			}
		}

		var titleRequests []RelayRequestLog
		if err := db.Select("id, token_id, codex_session_id, codex_session_source, session_name, request_body, response_body, latency_ms, duration_ms, created_at").
			Where("created_at >= ? AND codex_session_source = ? AND outcome = ?", cutoff, "prompt_cache_key", RelayOutcomeSuccess).
			Where("request_parameters_json LIKE ? OR request_parameters_json LIKE ?", `%"text_format":"json_schema"%`, `%"response_format":{"type":"json_schema"%`).
			Order("created_at ASC, id ASC").Find(&titleRequests).Error; err != nil {
			return err
		}
		for _, titleRequest := range titleRequests {
			titlePrompt, isTitleRequest := codexTitleRequestPrompt([]byte(decompressStoredPayload(titleRequest.RequestBody)))
			if !isTitleRequest {
				continue
			}
			title := codexGeneratedTitle([]byte(decompressStoredPayload(titleRequest.ResponseBody)))
			if title == "" {
				continue
			}
			var possibleMainRequests []RelayRequestLog
			if err := db.Select("id, token_id, codex_session_id, codex_session_source, request_body, latency_ms, duration_ms, created_at").
				Where("token_id = ? AND codex_session_id <> ? AND codex_session_id <> '' AND codex_session_source = ?", titleRequest.TokenID, titleRequest.CodexSessionID, "prompt_cache_key").
				Where("created_at >= ? AND created_at <= ?", titleRequest.CreatedAt.Add(-codexTitleMatchWindow), titleRequest.CreatedAt.Add(codexTitleMatchWindow)).
				Order("created_at ASC, id ASC").Limit(20).Find(&possibleMainRequests).Error; err != nil {
				return err
			}
			titleStartedAt := titleRequest.CreatedAt.Add(-relayRequestElapsedDuration(titleRequest))
			for _, mainRequest := range possibleMainRequests {
				mainStartedAt := mainRequest.CreatedAt.Add(-relayRequestElapsedDuration(mainRequest))
				if mainStartedAt.Before(titleStartedAt.Add(-codexTitleStartTolerance)) || mainStartedAt.After(titleStartedAt.Add(codexTitleStartTolerance)) {
					continue
				}
				titleBody := []byte(decompressStoredPayload(titleRequest.RequestBody))
				mainBody := []byte(decompressStoredPayload(mainRequest.RequestBody))
				if !codexTitleMatchesMain(titleBody, mainBody, hashCodexPrompt(titlePrompt), hashCodexPrompt(codexRequestPrompt(mainBody))) {
					continue
				}
				mergedTitle, err := s.mergeCodexTitleLog(db, titleRequest, mainRequest.CodexSessionID, title)
				if err != nil {
					return err
				}
				if mergedTitle != "" {
					if err := s.applyMergedCodexTitle(db, mainRequest.TokenID, mainRequest.CodexSessionID, mainRequest.ID, mergedTitle, mainRequest.CreatedAt); err != nil {
						return err
					}
				}
				break
			}
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now()}).Error
	})
}

func (s *Store) backfillCodexThreadSources() error {
	const migrationName = "codex_thread_sources_v1"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var logs []RelayRequestLog
		if err := db.Select("token_id, codex_session_id, request_body").
			Where("codex_session_id <> '' AND request_body <> ''").Find(&logs).Error; err != nil {
			return err
		}
		type sessionIdentity struct {
			tokenID   uint64
			sessionID string
		}
		sources := make(map[sessionIdentity]string)
		for _, log := range logs {
			source := codexThreadSourceFromBody([]byte(decompressStoredPayload(log.RequestBody)))
			if source == "" {
				continue
			}
			identity := sessionIdentity{tokenID: log.TokenID, sessionID: log.CodexSessionID}
			sources[identity] = preferredCodexThreadSource(sources[identity], source)
		}
		now := time.Now().UTC()
		for identity, source := range sources {
			var state RelaySessionState
			err := db.Where("token_id = ? AND session_id = ?", identity.tokenID, identity.sessionID).First(&state).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if err := db.Create(&RelaySessionState{
					TokenID: identity.tokenID, SessionID: identity.sessionID, ThreadSource: source,
					CreatedAt: now, UpdatedAt: now,
				}).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			preferred := preferredCodexThreadSource(state.ThreadSource, source)
			if preferred != state.ThreadSource {
				if err := db.Model(&RelaySessionState{}).
					Where("token_id = ? AND session_id = ?", identity.tokenID, identity.sessionID).
					Update("thread_source", preferred).Error; err != nil {
					return err
				}
			}
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: now}).Error
	})
}

func incrementSessionCompactionCount(db *gorm.DB, tokenID uint64, sessionID string, now time.Time) error {
	sessionID = truncateRunes(strings.TrimSpace(sessionID), 512)
	if tokenID == 0 || sessionID == "" {
		return nil
	}
	result := db.Model(&RelaySessionState{}).
		Where("token_id = ? AND session_id = ?", tokenID, sessionID).
		UpdateColumn("compaction_count", gorm.Expr("compaction_count + 1"))
	if result.Error != nil || result.RowsAffected > 0 {
		return result.Error
	}
	return db.Create(&RelaySessionState{
		TokenID: tokenID, SessionID: sessionID, CompactionCount: 1,
		CreatedAt: now, UpdatedAt: now,
	}).Error
}

func (s *Store) backfillCodexCompactionTracking() error {
	const migrationName = "codex_compaction_tracking_v1"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var logs []RelayRequestLog
		if err := db.Select("id, request_body").Where("request_body <> ''").Find(&logs).Error; err != nil {
			return err
		}
		for _, log := range logs {
			body := []byte(decompressStoredPayload(log.RequestBody))
			if !codexCompactionRequestFromBody(body) {
				continue
			}
			if err := db.Model(&RelayRequestLog{}).Where("id = ?", log.ID).
				UpdateColumn("is_compaction", true).Error; err != nil {
				return err
			}
		}

		if err := db.Model(&RelaySessionState{}).Where("compaction_count <> 0").
			UpdateColumn("compaction_count", 0).Error; err != nil {
			return err
		}
		type sessionCompactionCount struct {
			TokenID         uint64
			SessionID       string
			CompactionCount int64
		}
		var counts []sessionCompactionCount
		if err := db.Model(&RelayRequestLog{}).
			Select("token_id, codex_session_id AS session_id, COUNT(*) AS compaction_count").
			Where("is_compaction = ? AND codex_session_id <> ''", true).
			Group("token_id, codex_session_id").Scan(&counts).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, count := range counts {
			result := db.Model(&RelaySessionState{}).
				Where("token_id = ? AND session_id = ?", count.TokenID, count.SessionID).
				UpdateColumn("compaction_count", count.CompactionCount)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				if err := db.Create(&RelaySessionState{
					TokenID: count.TokenID, SessionID: count.SessionID, CompactionCount: count.CompactionCount,
					CreatedAt: now, UpdatedAt: now,
				}).Error; err != nil {
					return err
				}
			}
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: now}).Error
	})
}

func relayRequestElapsedDuration(log RelayRequestLog) time.Duration {
	return time.Duration(max(log.LatencyMS, 0)+max(log.DurationMS, 0)) * time.Millisecond
}
