package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
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
	codexThreadSourceSubagent  = "subagent"
	codexThreadSourceAmbient   = "ambient_suggestions"
	codexThreadSourceUnknown   = "unavailable"
	codexTitlePromptMarker     = "User prompt:"
	codexAttachedPromptMarker  = "## My request for Codex:"
	codexAttachedRequestMarker = "## My request:"
	codexRequestMarker         = "My request:"
	codexFilesMentionedMarker  = "# Files mentioned by the user:"
	codexInAppBrowserStart     = "<in-app-browser-context"
	codexInAppBrowserEnd       = "</in-app-browser-context>"
	codexTitleMatchWindow      = 5 * time.Minute
	codexTitleStartTolerance   = 30 * time.Second
)

type codexDelegation struct {
	SourceThreadID string `xml:"source_thread_id"`
}

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

func codexParentSessionIDFromBody(body []byte, currentSessionID string, threadSource string) string {
	if normalizeCodexThreadSource(threadSource) != codexThreadSourceSubagent {
		return ""
	}
	payload, ok := decodeJSONObject(body)
	if !ok {
		return ""
	}
	input, _ := payload["input"].([]any)
	for _, item := range input {
		message, _ := item.(map[string]any)
		if message == nil {
			continue
		}
		content, _ := message["content"].([]any)
		for _, part := range content {
			contentPart, _ := part.(map[string]any)
			if parentID := codexParentSessionIDFromText(stringValue(contentPart["text"]), currentSessionID); parentID != "" {
				return parentID
			}
		}
	}
	return ""
}

func codexParentSessionIDFromText(text string, currentSessionID string) string {
	const startTag = "<codex_delegation>"
	const endTag = "</codex_delegation>"
	for searchFrom := 0; searchFrom < len(text); {
		start := strings.Index(text[searchFrom:], startTag)
		if start < 0 {
			return ""
		}
		start += searchFrom
		end := strings.Index(text[start+len(startTag):], endTag)
		if end < 0 {
			return ""
		}
		end += start + len(startTag) + len(endTag)
		var delegation codexDelegation
		if xml.Unmarshal([]byte(text[start:end]), &delegation) == nil {
			parentID := strings.TrimSpace(delegation.SourceThreadID)
			if parentID != "" && len([]rune(parentID)) <= 512 && parentID != strings.TrimSpace(currentSessionID) {
				return parentID
			}
		}
		searchFrom = end
	}
	return ""
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
	for _, marker := range []string{codexTitlePromptMarker, "Original prompt:", "User request:", codexRequestMarker, "Prompt:", "用户请求：", "用户问题："} {
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
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = removeCodexInAppBrowserContext(value)

	// User input can contain the transport envelope and the visible request in
	// the same text part. Consume those wrappers repeatedly because Codex may
	// prepend more than one skill/link before the actual request.
	for {
		value = strings.TrimSpace(value)
		if value == "" {
			return ""
		}
		if strings.HasPrefix(strings.ToLower(value), "<skill>") {
			if end := strings.Index(strings.ToLower(value), "</skill>"); end >= 0 {
				value = value[end+len("</skill>"):]
				continue
			}
			return ""
		}
		if marker, ok := consumeCodexRequestMarker(value); ok {
			value = value[marker:]
			continue
		}
		if consumed, ok := consumeCodexSkillPrefix(value); ok {
			value = value[consumed:]
			continue
		}
		break
	}

	if imageIndex := strings.Index(value, "<image "); imageIndex >= 0 {
		value = value[:imageIndex]
	}
	return strings.Join(strings.Fields(value), " ")
}

func removeCodexInAppBrowserContext(value string) string {
	for {
		lower := strings.ToLower(value)
		start := strings.Index(lower, codexInAppBrowserStart)
		if start < 0 {
			return value
		}
		endOffset := strings.Index(lower[start:], codexInAppBrowserEnd)
		if endOffset < 0 {
			return strings.TrimSpace(value[:start])
		}
		end := start + endOffset + len(codexInAppBrowserEnd)
		value = value[:start] + " " + value[end:]
	}
}

func consumeCodexRequestMarker(value string) (int, bool) {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	for _, marker := range []string{codexAttachedPromptMarker, codexAttachedRequestMarker, codexRequestMarker} {
		if strings.HasPrefix(trimmed, marker) {
			return len(value) - len(trimmed) + len(marker), true
		}
	}
	if strings.HasPrefix(trimmed, codexFilesMentionedMarker) {
		for _, marker := range []string{codexAttachedPromptMarker, codexAttachedRequestMarker} {
			if index := strings.Index(trimmed, marker); index >= 0 {
				return len(value) - len(trimmed) + index + len(marker), true
			}
		}
	}
	return 0, false
}

func consumeCodexSkillPrefix(value string) (int, bool) {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	leading := len(value) - len(trimmed)
	if strings.HasPrefix(trimmed, "[$") {
		if closeLabel := strings.Index(trimmed, "]("); closeLabel > 2 {
			if end := strings.Index(trimmed[closeLabel+2:], ")"); end >= 0 && isCodexSkillInvocation(trimmed[1:closeLabel]) {
				return leading + closeLabel + 2 + end + 1, true
			}
		}
		if end := strings.Index(trimmed, "]"); end > 2 && isCodexSkillInvocation(trimmed[1:end]) {
			return leading + end + 1, true
		}
	}
	if len(trimmed) > 1 && trimmed[0] == '$' {
		end := strings.IndexAny(trimmed, " \t\r\n")
		if end < 0 {
			end = len(trimmed)
		}
		candidate := strings.Trim(trimmed[:end], "[](){}:;,.")
		if isCodexSkillInvocation(candidate) {
			return leading + end, true
		}
	}
	return 0, false
}

// Codex serializes a skill invocation either as `$skill-name` or as a
// Markdown link such as `[$skill-name](/path/SKILL.md)`. Both are transport
// metadata and must not become the session title prefix.
func isCodexSkillInvocationToken(value string) bool {
	if isCodexSkillInvocation(value) {
		return true
	}
	if !strings.HasPrefix(value, "[$") || !strings.HasSuffix(value, ")") {
		return false
	}
	closeLabel := strings.Index(value, "](")
	if closeLabel <= 2 {
		return false
	}
	return isCodexSkillInvocation(value[1:closeLabel])
}

func isCodexInjectedSkillText(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "<skill>") {
		return false
	}
	end := strings.Index(lower, "</skill>")
	return end < 0 || strings.TrimSpace(trimmed[end+len("</skill>"):]) == ""
}

func isCodexSkillInvocation(value string) bool {
	if len(value) < 2 || value[0] != '$' {
		return false
	}
	hasLetter := false
	for _, character := range value[1:] {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z':
			hasLetter = true
		case character >= '0' && character <= '9', character == '-', character == '_':
		default:
			return false
		}
	}
	return hasLetter
}

func isReplaceableCodexSessionTitle(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if isCodexAmbientSessionTitle(normalized) {
		return true
	}
	return strings.HasPrefix(normalized, "# files") ||
		strings.HasPrefix(normalized, "files ment") ||
		strings.HasPrefix(normalized, "<skill>") ||
		strings.HasPrefix(normalized, "[$") ||
		strings.HasPrefix(normalized, "<codex_del")
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
	title := truncateRunes(normalizeSessionName(result.Title), 80)
	if isCodexAmbientSessionTitle(title) {
		return ""
	}
	return title
}

func isCodexAmbientSessionTitle(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.Trim(normalized, " .:：-_")
	switch normalized {
	case "in app", "in-app", "in app browser", "in-app browser":
		return true
	default:
		return false
	}
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
	cutoff, hasCutoff := s.detailedLogCutoff(time.Now())
	return s.db.Transaction(func(db *gorm.DB) error {
		var guardians []RelayRequestLog
		guardianDB := db.Select("id, codex_session_id").Where("codex_session_id LIKE ?", codexGuardianSessionPrefix+"%")
		if hasCutoff {
			guardianDB = guardianDB.Where("created_at >= ?", cutoff)
		}
		if err := guardianDB.
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
		titleDB := db.Select("id, token_id, codex_session_id, codex_session_source, session_name, request_body, response_body, latency_ms, duration_ms, created_at").
			Where("codex_session_source = ? AND outcome = ?", "prompt_cache_key", RelayOutcomeSuccess)
		if hasCutoff {
			titleDB = titleDB.Where("created_at >= ?", cutoff)
		}
		if err := titleDB.
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

func (s *Store) backfillCodexSessionParents() error {
	const migrationName = "codex_session_parent_v1"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var states []RelaySessionState
		if err := db.Select("token_id, session_id, thread_source").
			Where("thread_source = ? AND parent_session_id = ''", codexThreadSourceSubagent).
			Find(&states).Error; err != nil {
			return err
		}
		for _, state := range states {
			var log RelayRequestLog
			err := db.Select("request_body").
				Where("token_id = ? AND codex_session_id = ? AND request_body <> ''", state.TokenID, state.SessionID).
				Order("created_at ASC, id ASC").First(&log).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			parentSessionID := codexParentSessionIDFromBody(
				[]byte(decompressStoredPayload(log.RequestBody)), state.SessionID, state.ThreadSource,
			)
			if parentSessionID == "" {
				continue
			}
			if err := db.Model(&RelaySessionState{}).
				Where("token_id = ? AND session_id = ? AND parent_session_id = ''", state.TokenID, state.SessionID).
				Update("parent_session_id", parentSessionID).Error; err != nil {
				return err
			}
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now().UTC()}).Error
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

func (s *Store) correctSuccessfulCodexCompactionCounts() error {
	const migrationName = "successful_codex_compaction_counts_v1"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
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
			Where("is_compaction = ? AND codex_session_id <> '' AND NOT (outcome = ? OR (outcome = '' AND status_code >= 200 AND status_code < 300))",
				true, RelayOutcomeSuccess).
			Group("token_id, codex_session_id").Scan(&counts).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, count := range counts {
			if err := db.Model(&RelaySessionState{}).
				Where("token_id = ? AND session_id = ?", count.TokenID, count.SessionID).
				UpdateColumn("compaction_count", gorm.Expr(
					"CASE WHEN compaction_count > ? THEN compaction_count - ? ELSE 0 END",
					count.CompactionCount, count.CompactionCount,
				)).Error; err != nil {
				return err
			}
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: now}).Error
	})
}

func relayRequestElapsedDuration(log RelayRequestLog) time.Duration {
	return time.Duration(max(log.LatencyMS, 0)+max(log.DurationMS, 0)) * time.Millisecond
}
