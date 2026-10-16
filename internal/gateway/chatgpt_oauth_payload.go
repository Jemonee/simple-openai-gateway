package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
)

const (
	maxChatGPTOAuthResponseIndex = 4096
	// Bound sparse slice growth across all outputs, including content and summaries.
	maxChatGPTOAuthPartSlots = 64 << 10
)

func chatGPTResponsesSSEToJSON(body []byte) ([]byte, error) {
	state := newChatGPTOAuthResponsesAccumulator()
	reader := bufio.NewReader(bytes.NewReader(body))
	for {
		event, err := readSSEEventWithLimit(reader, "responses", maxSSEEventBytes)
		if payload, ok := firstSSEPayload(event); ok {
			if err := state.observe(payload); err != nil {
				return nil, err
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			break
		}
	}
	if !state.Terminal || state.Response == nil {
		return nil, errors.New("ChatGPT OAuth upstream did not return a completed response")
	}
	return json.Marshal(state.finalResponse())
}

type chatGPTOAuthResponsesAccumulator struct {
	Response  map[string]any
	Output    map[int]map[string]any
	Terminal  bool
	partSlots int
}

func newChatGPTOAuthResponsesAccumulator() *chatGPTOAuthResponsesAccumulator {
	return &chatGPTOAuthResponsesAccumulator{Output: make(map[int]map[string]any)}
}

func (s *chatGPTOAuthResponsesAccumulator) observe(event map[string]any) error {
	typeName := strings.ToLower(stringValue(event["type"]))
	outputIndex, partIndex, err := chatGPTOAuthResponseEventIndices(typeName, event)
	if err != nil {
		return err
	}
	if response, ok := event["response"].(map[string]any); ok {
		s.observeResponse(response)
	}
	switch typeName {
	case "response.output_item.added", "response.output_item.done":
		if item, ok := event["item"].(map[string]any); ok {
			s.mergeOutputItem(outputIndex, item)
		}
	case "response.content_part.added", "response.content_part.done":
		if part, ok := event["part"].(map[string]any); ok {
			return s.mergeIndexedPart(outputIndex, "content", partIndex, part, "message")
		}
	case "response.output_text.delta":
		part, err := s.ensureIndexedPart(outputIndex, "content", partIndex, "message", "output_text")
		if err != nil {
			return err
		}
		part["text"] = rawStringValue(part["text"]) + rawStringValue(event["delta"])
	case "response.output_text.done":
		part, err := s.ensureIndexedPart(outputIndex, "content", partIndex, "message", "output_text")
		if err != nil {
			return err
		}
		if text := rawStringValue(event["text"]); text != "" {
			part["text"] = text
		}
	case "response.refusal.delta":
		part, err := s.ensureIndexedPart(outputIndex, "content", partIndex, "message", "refusal")
		if err != nil {
			return err
		}
		part["refusal"] = rawStringValue(part["refusal"]) + rawStringValue(event["delta"])
	case "response.refusal.done":
		part, err := s.ensureIndexedPart(outputIndex, "content", partIndex, "message", "refusal")
		if err != nil {
			return err
		}
		if refusal := rawStringValue(event["refusal"]); refusal != "" {
			part["refusal"] = refusal
		}
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		if part, ok := event["part"].(map[string]any); ok {
			return s.mergeIndexedPart(outputIndex, "summary", partIndex, part, "reasoning")
		}
	case "response.reasoning_summary_text.delta":
		part, err := s.ensureIndexedPart(outputIndex, "summary", partIndex, "reasoning", "summary_text")
		if err != nil {
			return err
		}
		part["text"] = rawStringValue(part["text"]) + rawStringValue(event["delta"])
	case "response.reasoning_summary_text.done":
		part, err := s.ensureIndexedPart(outputIndex, "summary", partIndex, "reasoning", "summary_text")
		if err != nil {
			return err
		}
		if text := rawStringValue(event["text"]); text != "" {
			part["text"] = text
		}
	case "response.function_call_arguments.delta":
		item := s.ensureOutputItem(outputIndex, "function_call")
		item["arguments"] = rawStringValue(item["arguments"]) + rawStringValue(event["delta"])
	case "response.function_call_arguments.done":
		item := s.ensureOutputItem(outputIndex, "function_call")
		if arguments := rawStringValue(event["arguments"]); arguments != "" {
			item["arguments"] = arguments
		}
	case "response.custom_tool_call_input.delta":
		item := s.ensureOutputItem(outputIndex, "custom_tool_call")
		item["input"] = rawStringValue(item["input"]) + rawStringValue(event["delta"])
	case "response.custom_tool_call_input.done":
		item := s.ensureOutputItem(outputIndex, "custom_tool_call")
		if input := rawStringValue(event["input"]); input != "" {
			item["input"] = input
		}
	case "response.completed", "response.incomplete", "response.failed", "response.done":
		s.Terminal = true
	}
	return nil
}

func chatGPTOAuthResponseEventIndices(typeName string, event map[string]any) (int, int, error) {
	partField := ""
	switch typeName {
	case "response.content_part.added", "response.content_part.done",
		"response.output_text.delta", "response.output_text.done", "response.refusal.delta", "response.refusal.done":
		partField = "content_index"
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		partField = "summary_index"
	case "response.output_item.added", "response.output_item.done",
		"response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
	default:
		return 0, 0, nil
	}
	outputIndex, err := chatGPTOAuthResponseEventIndex(event, "output_index")
	if err != nil || partField == "" {
		return outputIndex, 0, err
	}
	partIndex, err := chatGPTOAuthResponseEventIndex(event, partField)
	return outputIndex, partIndex, err
}

func chatGPTOAuthResponseEventIndex(event map[string]any, field string) (int, error) {
	value, exists := event[field]
	if !exists {
		return 0, nil
	}
	switch typed := value.(type) {
	case float64:
		// Check before converting so non-integers and overflowing floats cannot wrap.
		if typed >= 0 && typed < maxChatGPTOAuthResponseIndex && typed == math.Trunc(typed) {
			return int(typed), nil
		}
	case json.Number:
		if index, err := typed.Int64(); err == nil && index >= 0 && index < maxChatGPTOAuthResponseIndex {
			return int(index), nil
		}
	case int:
		if typed >= 0 && typed < maxChatGPTOAuthResponseIndex {
			return typed, nil
		}
	case int64:
		if typed >= 0 && typed < maxChatGPTOAuthResponseIndex {
			return int(typed), nil
		}
	}
	return 0, fmt.Errorf("ChatGPT OAuth upstream %s must be an integer between 0 and %d", field, maxChatGPTOAuthResponseIndex-1)
}

func (s *chatGPTOAuthResponsesAccumulator) observeResponse(response map[string]any) {
	if s.Response == nil {
		s.Response = make(map[string]any, len(response))
	}
	for key, value := range response {
		if key == "output" {
			continue
		}
		s.Response[key] = value
	}
	if output, ok := response["output"].([]any); ok {
		for index, rawItem := range output {
			if item, ok := rawItem.(map[string]any); ok {
				s.mergeOutputItem(index, item)
			}
		}
	}
}

func (s *chatGPTOAuthResponsesAccumulator) ensureOutputItem(index int, itemType string) map[string]any {
	if index < 0 {
		index = 0
	}
	item := s.Output[index]
	if item == nil {
		item = map[string]any{"type": itemType}
		s.Output[index] = item
	}
	if stringValue(item["type"]) == "" {
		item["type"] = itemType
	}
	return item
}

func (s *chatGPTOAuthResponsesAccumulator) mergeOutputItem(index int, incoming map[string]any) {
	item := s.ensureOutputItem(index, stringValue(incoming["type"]))
	mergeChatGPTOAuthResponseMap(item, incoming)
}

func (s *chatGPTOAuthResponsesAccumulator) ensureIndexedPart(outputIndex int, field string, partIndex int, itemType string, partType string) (map[string]any, error) {
	if partIndex < 0 || partIndex >= maxChatGPTOAuthResponseIndex {
		return nil, fmt.Errorf("ChatGPT OAuth upstream %s index is out of range", field)
	}
	item := s.ensureOutputItem(outputIndex, itemType)
	parts, _ := item[field].([]any)
	if growth := partIndex + 1 - len(parts); growth > 0 {
		if growth > maxChatGPTOAuthPartSlots-s.partSlots {
			return nil, errors.New("ChatGPT OAuth upstream indexed content exceeds size limit")
		}
		parts = append(parts, make([]any, growth)...)
		s.partSlots += growth
	}
	part, _ := parts[partIndex].(map[string]any)
	if part == nil {
		part = map[string]any{"type": partType}
		parts[partIndex] = part
	}
	item[field] = parts
	return part, nil
}

func (s *chatGPTOAuthResponsesAccumulator) mergeIndexedPart(outputIndex int, field string, partIndex int, incoming map[string]any, itemType string) error {
	part, err := s.ensureIndexedPart(outputIndex, field, partIndex, itemType, stringValue(incoming["type"]))
	if err != nil {
		return err
	}
	mergeChatGPTOAuthResponseMap(part, incoming)
	return nil
}

func mergeChatGPTOAuthResponseMap(target map[string]any, incoming map[string]any) {
	for key, value := range incoming {
		if nested, ok := value.(map[string]any); ok {
			current, _ := target[key].(map[string]any)
			if current == nil {
				current = make(map[string]any)
				target[key] = current
			}
			mergeChatGPTOAuthResponseMap(current, nested)
			continue
		}
		if values, ok := value.([]any); ok && len(values) == 0 {
			if current, exists := target[key].([]any); exists && len(current) > 0 {
				continue
			}
		}
		target[key] = value
	}
}

func (s *chatGPTOAuthResponsesAccumulator) finalResponse() map[string]any {
	indices := make([]int, 0, len(s.Output))
	for index := range s.Output {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	output := make([]any, 0, len(indices))
	for _, index := range indices {
		output = append(output, s.Output[index])
	}
	s.Response["output"] = output
	return s.Response
}

func chatGPTOAuthResponseIsSSE(header http.Header, body []byte) bool {
	if isEventStream(header) {
		return true
	}
	for _, line := range bytes.Split(bytes.TrimSpace(body), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("data:")) || bytes.HasPrefix(line, []byte("event:")) {
			return true
		}
	}
	return false
}

func adaptChatGPTOAuthBufferedResponse(header http.Header, body []byte, endpoint string, model string) ([]byte, bool, error) {
	if chatGPTOAuthResponseIsSSE(header, body) {
		if endpoint == "chat" {
			converted, err := chatGPTResponsesSSEToChatCompletion(body, model)
			return converted, true, err
		}
		if endpoint == "responses" {
			converted, err := chatGPTResponsesSSEToJSON(body)
			return converted, true, err
		}
	}
	if endpoint == "chat" {
		var response map[string]any
		if err := json.Unmarshal(body, &response); err == nil {
			if _, alreadyChat := response["choices"]; alreadyChat {
				return body, false, nil
			}
			if status := strings.ToLower(strings.TrimSpace(stringValue(response["status"]))); status != "" {
				switch status {
				case "completed", "incomplete":
					converted, convertErr := chatGPTResponsesJSONToChatCompletion(body, model)
					return converted, true, convertErr
				}
			}
			if _, hasOutput := response["output"]; hasOutput {
				converted, convertErr := chatGPTResponsesJSONToChatCompletion(body, model)
				return converted, true, convertErr
			}
		}
	}
	return body, false, nil
}

func adaptChatGPTOAuthBufferedResponseReader(reader io.Reader, header http.Header, endpoint string, model string, observe func([]byte)) ([]byte, bool, error) {
	body, err := readChatGPTOAuthResponseBody(reader, observe)
	if err != nil {
		return body, false, err
	}
	return adaptChatGPTOAuthBufferedResponse(header, body, endpoint, model)
}

func readChatGPTOAuthResponseBody(reader io.Reader, observe func([]byte)) ([]byte, error) {
	buffered := bufio.NewReader(reader)
	body := bytes.NewBuffer(nil)
	for {
		event, err := readSSEEventWithLimit(buffered, "responses", maxSSEEventBytes)
		if len(event) > 0 {
			if body.Len()+len(event) > maxBufferedResponseBytes {
				return body.Bytes(), errBufferedResponseTooLarge
			}
			_, _ = body.Write(event)
			if observe != nil {
				observe(event)
			}
			if sseEventIsTerminalSuccess(event, "responses") {
				return body.Bytes(), nil
			}
			if _, failed := sseApplicationError(event); failed {
				return body.Bytes(), nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return body.Bytes(), nil
			}
			return body.Bytes(), err
		}
	}
}

type chatGPTOAuthPrefixedReadCloser struct {
	io.Reader
	io.Closer
}

func prepareChatGPTOAuthStreamResponse(response *http.Response, endpoint string, model string, includeUsage bool) bool {
	if response == nil {
		return false
	}
	if !isEventStream(response.Header) {
		originalBody := response.Body
		buffered := bufio.NewReader(originalBody)
		prefix, detected := probeChatGPTOAuthSSEPrefix(buffered)
		response.Body = &chatGPTOAuthPrefixedReadCloser{
			Reader: io.MultiReader(bytes.NewReader(prefix), buffered),
			Closer: originalBody,
		}
		if !detected {
			return false
		}
	}
	if endpoint == "chat" {
		response.Body = newChatGPTOAuthChatSSEReadCloser(response.Body, model, includeUsage)
	}
	response.Header.Del("Content-Length")
	response.Header.Set("Content-Type", "text/event-stream")
	return true
}

func probeChatGPTOAuthSSEPrefix(reader *bufio.Reader) ([]byte, bool) {
	const maxProbeBytes = 64 << 10
	var prefix bytes.Buffer
	for prefix.Len() <= maxProbeBytes {
		fragment, err := reader.ReadSlice('\n')
		_, _ = prefix.Write(fragment)
		line := bytes.TrimSpace(fragment)
		if len(line) > 0 && !bytes.HasPrefix(line, []byte(":")) {
			return prefix.Bytes(), bytes.HasPrefix(line, []byte("event:")) || bytes.HasPrefix(line, []byte("data:"))
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return prefix.Bytes(), false
		}
	}
	return prefix.Bytes(), false
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
