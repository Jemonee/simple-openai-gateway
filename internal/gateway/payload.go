package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

type RelayPayload struct {
	values                map[string]any
	Model                 string
	Stream                bool
	PreviousResponseID    string
	SessionKey            string
	SessionSource         string
	LogSessionKey         string
	LogSessionSource      string
	ClientKind            string
	ClientFingerprint     string
	ThreadSource          string
	IsCompactionRequest   bool
	CompactionTrigger     string
	RequestParametersJSON string
	DeclaredMaxOutput     int64
}

func ParseRelayPayload(data []byte) (*RelayPayload, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	values := make(map[string]any)
	if err := decoder.Decode(&values); err != nil {
		return nil, errors.New("request body must be a JSON object")
	}
	model, _ := values["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, errors.New("model is required")
	}
	payload := &RelayPayload{values: values, Model: model}
	payload.Stream, _ = values["stream"].(bool)
	payload.PreviousResponseID, _ = values["previous_response_id"].(string)
	applyCodexPayloadSession(payload, values)
	payload.IsCompactionRequest, payload.CompactionTrigger = codexCompactionMetadataFromPayload(values)
	for _, key := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens"} {
		if value, ok := values[key].(json.Number); ok {
			parsed, _ := value.Int64()
			if parsed > 0 {
				payload.DeclaredMaxOutput = parsed
				break
			}
		}
	}
	payload.RequestParametersJSON = loggableRequestParameters(values)
	return payload, nil
}

func loggableRequestParameters(values map[string]any) string {
	parameters := make(map[string]any)
	for _, key := range []string{
		"model", "stream", "max_output_tokens", "max_completion_tokens", "max_tokens", "temperature", "top_p",
		"frequency_penalty", "presence_penalty", "seed", "n", "parallel_tool_calls", "store", "service_tier",
		"truncation", "reasoning_effort", "verbosity", "max_tool_calls",
	} {
		if value, ok := loggableScalar(values[key]); ok {
			parameters[key] = value
		}
	}
	if values["previous_response_id"] != nil {
		parameters["has_previous_response_id"] = stringValue(values["previous_response_id"]) != ""
	}
	if messages, ok := values["messages"].([]any); ok {
		parameters["message_count"] = len(messages)
	}
	if input, ok := values["input"].([]any); ok {
		parameters["input_item_count"] = len(input)
	}
	copyLoggableObject(parameters, values, "reasoning", []string{"effort", "summary"})
	copyLoggableObject(parameters, values, "stream_options", []string{"include_usage"})
	copyLoggableType(parameters, values, "response_format")
	if text, ok := values["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok {
			if value, ok := loggableScalar(format["type"]); ok {
				parameters["text_format"] = value
			}
		}
	}
	for _, key := range []string{"include", "modalities"} {
		if values, ok := loggableStringList(values[key]); ok {
			parameters[key] = values
		}
	}
	if choice, ok := loggableToolChoice(values["tool_choice"]); ok {
		parameters["tool_choice"] = choice
	}
	if tools, ok := values["tools"].([]any); ok {
		parameters["tool_count"] = len(tools)
		if names := loggableToolNames(tools); len(names) > 0 {
			parameters["tool_names"] = names
		}
	}
	encoded, err := json.Marshal(parameters)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func loggableScalar(value any) (any, bool) {
	switch typed := value.(type) {
	case bool, json.Number:
		return typed, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, false
		}
		return truncateRunes(trimmed, 160), true
	default:
		return nil, false
	}
}

func copyLoggableObject(destination map[string]any, source map[string]any, key string, allowed []string) {
	object, ok := source[key].(map[string]any)
	if !ok {
		return
	}
	filtered := make(map[string]any)
	for _, field := range allowed {
		if value, ok := loggableScalar(object[field]); ok {
			filtered[field] = value
		}
	}
	if len(filtered) > 0 {
		destination[key] = filtered
	}
}

func copyLoggableType(destination map[string]any, source map[string]any, key string) {
	object, ok := source[key].(map[string]any)
	if !ok {
		return
	}
	if value, ok := loggableScalar(object["type"]); ok {
		destination[key] = map[string]any{"type": value}
	}
}

func loggableStringList(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, min(len(items), 32))
	for _, item := range items {
		text := stringValue(item)
		if text != "" {
			result = append(result, truncateRunes(text, 160))
		}
		if len(result) == 32 {
			break
		}
	}
	return result, true
}

func loggableToolChoice(value any) (any, bool) {
	if scalar, ok := loggableScalar(value); ok {
		return scalar, true
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	filtered := make(map[string]any)
	if value, ok := loggableScalar(object["type"]); ok {
		filtered["type"] = value
	}
	if function, ok := object["function"].(map[string]any); ok {
		if name, ok := loggableScalar(function["name"]); ok {
			filtered["function"] = map[string]any{"name": name}
		}
	}
	return filtered, len(filtered) > 0
}

func loggableToolNames(tools []any) []string {
	names := make([]string, 0, min(len(tools), 32))
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := ""
		if function, ok := tool["function"].(map[string]any); ok {
			name = stringValue(function["name"])
		}
		if name == "" {
			name = stringValue(tool["name"])
		}
		if name != "" {
			names = append(names, truncateRunes(name, 160))
		}
		if len(names) == 32 {
			break
		}
	}
	return names
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func (p *RelayPayload) UpstreamBody(upstreamModel string, endpoint string, includeStreamUsage bool) ([]byte, error) {
	values := make(map[string]any, len(p.values)+1)
	for key, value := range p.values {
		values[key] = value
	}
	values["model"] = upstreamModel
	if endpoint == "chat" && p.Stream && includeStreamUsage {
		streamOptions, _ := values["stream_options"].(map[string]any)
		if streamOptions == nil {
			streamOptions = make(map[string]any)
		}
		streamOptions["include_usage"] = true
		values["stream_options"] = streamOptions
	}
	return json.Marshal(values)
}
