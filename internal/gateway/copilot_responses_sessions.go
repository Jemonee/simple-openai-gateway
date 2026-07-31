package gateway

const (
	copilotResponsesPromptCacheSource = "copilot_responses.prompt_cache_key"
	copilotResponsesProjectSource     = "copilot_responses.project_session_id"
)

func resolveCopilotResponsesSession(request agentSessionRequest, base agentSessionIdentity) agentSessionIdentity {
	if sessionID := truncateRunes(stringValue(request.Payload.values["prompt_cache_key"]), 512); sessionID != "" {
		base.ID = sessionID
		base.Source = copilotResponsesPromptCacheSource
		return base
	}
	if sessionID := copilotProjectSessionID(request.Payload.values); sessionID != "" {
		base.ID = sessionID
		base.Source = copilotResponsesProjectSource
	}
	return base
}

func copilotResponsesSessionTitle(body []byte) (string, bool) {
	payload, ok := decodeJSONObject(body)
	if !ok {
		return "", false
	}
	input, _ := payload["input"].([]any)
	for index := len(input) - 1; index >= 0; index-- {
		item, _ := input[index].(map[string]any)
		if title := copilotRenamedSessionTitle(contentText(item["output"])); title != "" {
			return title, true
		}
	}
	if prompt := cleanCopilotPrompt(latestResponsesInputText(payload["input"])); prompt != "" {
		return truncateRunes(prompt, 80), false
	}
	return "", false
}
