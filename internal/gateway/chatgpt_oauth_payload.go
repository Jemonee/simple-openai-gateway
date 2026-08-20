package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

func chatGPTResponsesSSEToJSON(body []byte) ([]byte, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), maxBufferedResponseBytes)
	var completed map[string]any
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		if response, ok := event["response"].(map[string]any); ok {
			completed = response
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if completed == nil {
		return nil, errors.New("ChatGPT OAuth upstream did not return a completed response")
	}
	return json.Marshal(completed)
}

var chatGPTOAuthUnsupportedFields = []string{
	"max_output_tokens",
	"max_completion_tokens",
	"max_tokens",
	"temperature",
	"top_p",
	"frequency_penalty",
	"presence_penalty",
	"user",
	"metadata",
	"prompt_cache_retention",
	"safety_identifier",
	"stream_options",
}

var chatGPTOAuthChatOnlyFields = []string{
	"messages",
	"functions",
	"function_call",
	"n",
	"stop",
	"logprobs",
	"top_logprobs",
	"modalities",
	"audio",
}

// ChatGPTOAuthRequestBody converts the public OpenAI request shape into the
// Responses shape accepted by ChatGPT's internal Codex endpoint. The input
// payload is never mutated, which keeps request logging and retry behavior
// stable across channels.
func (p *RelayPayload) ChatGPTOAuthRequestBody(upstreamModel string, endpoint string) ([]byte, error) {
	if p == nil || p.multipart != nil {
		return nil, errors.New("ChatGPT OAuth channels accept JSON requests only")
	}
	values, err := cloneRelayJSONValues(p.values)
	if err != nil {
		return nil, err
	}
	values["model"] = upstreamModel
	values["store"] = false
	values["stream"] = true
	for _, key := range chatGPTOAuthUnsupportedFields {
		delete(values, key)
	}
	if endpoint == "chat" {
		if err := convertChatMessagesToResponses(values); err != nil {
			return nil, err
		}
		normalizeChatGPTChatTools(values)
		normalizeChatGPTChatOptions(values)
		for _, key := range chatGPTOAuthChatOnlyFields {
			delete(values, key)
		}
	} else if endpoint != "responses" {
		return nil, errors.New("ChatGPT OAuth channels support the /v1/responses endpoint only")
	} else {
		normalizeChatGPTLegacyTools(values)
		if err := normalizeChatGPTInput(values); err != nil {
			return nil, err
		}
	}
	ensureChatGPTReasoningInclude(values)
	return json.Marshal(values)
}

func cloneRelayJSONValues(values map[string]any) (map[string]any, error) {
	data, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	cloned := make(map[string]any)
	if err := decoder.Decode(&cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func ensureChatGPTReasoningInclude(values map[string]any) {
	if _, ok := values["reasoning"].(map[string]any); !ok {
		return
	}
	include, _ := values["include"].([]any)
	for _, item := range include {
		if stringValue(item) == "reasoning.encrypted_content" {
			return
		}
	}
	// The internal endpoint expects encrypted reasoning to be explicitly
	// requested whenever a reasoning object is present.
	values["include"] = append(include, "reasoning.encrypted_content")
}

func normalizeChatGPTLegacyTools(values map[string]any) {
	if raw, ok := values["functions"].([]any); ok {
		tools := make([]any, 0, len(raw))
		for _, function := range raw {
			tools = append(tools, map[string]any{"type": "function", "function": function})
		}
		values["tools"] = tools
		delete(values, "functions")
	}
	if raw, ok := values["function_call"]; ok {
		switch choice := raw.(type) {
		case string:
			values["tool_choice"] = choice
		case map[string]any:
			if name := stringValue(choice["name"]); name != "" {
				values["tool_choice"] = map[string]any{"type": "function", "name": name}
			}
		}
		delete(values, "function_call")
	}
	if tools, ok := values["tools"].([]any); ok {
		normalized := make([]any, 0, len(tools))
		for _, item := range tools {
			tool, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if tool["type"] == nil {
				tool["type"] = "function"
			}
			if function, ok := tool["function"].(map[string]any); ok {
				if function["name"] == nil || strings.TrimSpace(stringValue(function["name"])) == "" {
					continue
				}
			}
			normalized = append(normalized, tool)
		}
		values["tools"] = normalized
	}
}

func convertChatMessagesToResponses(values map[string]any) error {
	if _, hasInput := values["input"]; hasInput {
		return normalizeChatGPTInput(values)
	}
	messages, ok := values["messages"].([]any)
	if !ok {
		values["input"] = []any{}
		return nil
	}
	input := make([]any, 0, len(messages))
	var instructions []string
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringValue(message["role"])))
		if role == "system" || role == "developer" {
			if text := messageText(message["content"]); text != "" {
				instructions = append(instructions, text)
			}
			continue
		}
		switch role {
		case "tool":
			callID := stringValue(message["tool_call_id"])
			if callID == "" {
				continue
			}
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": callID, "output": chatMessageOutput(message["content"]),
			})
			continue
		case "function":
			callID := stringValue(message["name"])
			if callID == "" {
				continue
			}
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": callID, "output": chatMessageOutput(message["content"]),
			})
			continue
		case "assistant":
			if content := chatAssistantContent(message["content"]); content != nil {
				input = append(input, map[string]any{"type": "message", "role": "assistant", "content": content})
			}
			if toolCalls, ok := message["tool_calls"].([]any); ok {
				for _, rawCall := range toolCalls {
					call, _ := rawCall.(map[string]any)
					function, _ := call["function"].(map[string]any)
					name := stringValue(function["name"])
					if name == "" {
						continue
					}
					arguments := stringValue(function["arguments"])
					if arguments == "" {
						arguments = "{}"
					}
					input = append(input, map[string]any{
						"type": "function_call", "call_id": stringValue(call["id"]), "name": name, "arguments": arguments,
					})
				}
			}
			continue
		case "user":
		default:
			role = "user"
		}
		content := chatInputContent(message["content"])
		if content != nil {
			input = append(input, map[string]any{"type": "message", "role": role, "content": content})
		}
	}
	if len(instructions) > 0 {
		values["instructions"] = strings.Join(instructions, "\n\n")
	}
	delete(values, "messages")
	values["input"] = input
	return nil
}

func chatInputContent(value any) any {
	if text, ok := value.(string); ok {
		return text
	}
	parts, ok := value.([]any)
	if !ok {
		return nil
	}
	normalized := make([]any, 0, len(parts))
	for _, raw := range parts {
		part, _ := raw.(map[string]any)
		switch strings.ToLower(stringValue(part["type"])) {
		case "text", "input_text":
			if text := stringValue(part["text"]); text != "" {
				normalized = append(normalized, map[string]any{"type": "input_text", "text": text})
			}
		case "image_url", "input_image":
			imageURL := stringValue(part["image_url"])
			if object, ok := part["image_url"].(map[string]any); ok {
				imageURL = stringValue(object["url"])
			}
			if imageURL != "" {
				normalized = append(normalized, map[string]any{"type": "input_image", "image_url": imageURL})
			}
		}
	}
	return normalized
}

func chatAssistantContent(value any) any {
	if text, ok := value.(string); ok {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []any{map[string]any{"type": "output_text", "text": text}}
	}
	parts, ok := value.([]any)
	if !ok {
		return nil
	}
	normalized := make([]any, 0, len(parts))
	for _, raw := range parts {
		part, _ := raw.(map[string]any)
		if text := stringValue(part["text"]); text != "" {
			normalized = append(normalized, map[string]any{"type": "output_text", "text": text})
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func chatMessageOutput(value any) string {
	if text := messageText(value); text != "" {
		return text
	}
	if value == nil {
		return "(empty)"
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 {
		return "(empty)"
	}
	return string(encoded)
}

func normalizeChatGPTChatTools(values map[string]any) {
	normalized := make([]any, 0)
	if tools, ok := values["tools"].([]any); ok {
		for _, raw := range tools {
			tool, _ := raw.(map[string]any)
			if function, ok := tool["function"].(map[string]any); ok {
				if name := stringValue(function["name"]); name != "" {
					flattened := cloneJSONMap(function)
					flattened["type"] = "function"
					normalized = append(normalized, flattened)
				}
				continue
			}
			if stringValue(tool["type"]) != "" {
				normalized = append(normalized, tool)
			}
		}
	}
	if functions, ok := values["functions"].([]any); ok {
		for _, raw := range functions {
			function, _ := raw.(map[string]any)
			if name := stringValue(function["name"]); name != "" {
				flattened := cloneJSONMap(function)
				flattened["type"] = "function"
				normalized = append(normalized, flattened)
			}
		}
	}
	if len(normalized) > 0 {
		values["tools"] = normalized
	} else {
		delete(values, "tools")
	}

	if raw, ok := values["function_call"]; ok && values["tool_choice"] == nil {
		values["tool_choice"] = raw
	}
	if choice, ok := values["tool_choice"].(map[string]any); ok {
		if function, ok := choice["function"].(map[string]any); ok {
			if name := stringValue(function["name"]); name != "" {
				values["tool_choice"] = map[string]any{"type": "function", "name": name}
			}
		} else if name := stringValue(choice["name"]); name != "" {
			values["tool_choice"] = map[string]any{"type": "function", "name": name}
		}
	}
}

func normalizeChatGPTChatOptions(values map[string]any) {
	if effort := stringValue(values["reasoning_effort"]); effort != "" {
		reasoning, _ := values["reasoning"].(map[string]any)
		if reasoning == nil {
			reasoning = make(map[string]any)
		}
		reasoning["effort"] = effort
		if reasoning["summary"] == nil {
			reasoning["summary"] = "auto"
		}
		values["reasoning"] = reasoning
	}
	delete(values, "reasoning_effort")
	if responseFormat, ok := values["response_format"].(map[string]any); ok {
		text, _ := values["text"].(map[string]any)
		if text == nil {
			text = make(map[string]any)
		}
		text["format"] = responseFormat
		values["text"] = text
	}
	delete(values, "response_format")
}

func cloneJSONMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func normalizeChatGPTInput(values map[string]any) error {
	switch input := values["input"].(type) {
	case nil:
		values["input"] = []any{}
	case string:
		if strings.TrimSpace(input) == "" {
			values["input"] = []any{}
		} else {
			values["input"] = []any{map[string]any{"type": "message", "role": "user", "content": input}}
		}
	case []any:
		filtered := make([]any, 0, len(input))
		instructions := make([]string, 0)
		for _, item := range input {
			if itemMap, ok := item.(map[string]any); ok {
				if strings.TrimSpace(stringValue(itemMap["type"])) == "" && strings.TrimSpace(stringValue(itemMap["role"])) != "" {
					itemMap["type"] = "message"
				}
				if strings.EqualFold(stringValue(itemMap["role"]), "system") {
					if text := messageText(itemMap["content"]); text != "" {
						instructions = append(instructions, text)
					}
					continue
				}
				if strings.TrimSpace(stringValue(itemMap["type"])) != "" {
					filtered = append(filtered, itemMap)
				}
			}
		}
		values["input"] = filtered
		if len(instructions) > 0 {
			values["instructions"] = strings.Join(instructions, "\n\n")
		}
	default:
		return errors.New("ChatGPT OAuth input must be a string or array")
	}
	return nil
}

func messageText(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			if text := stringValue(object["text"]); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
