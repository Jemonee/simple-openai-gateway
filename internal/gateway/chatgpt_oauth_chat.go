package gateway

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

type chatGPTOAuthToolCall struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

type chatGPTOAuthResponseAccumulator struct {
	ID                     string
	Model                  string
	Created                int64
	Status                 string
	IncompleteReason       string
	Text                   strings.Builder
	Refusal                strings.Builder
	Reasoning              strings.Builder
	Tools                  []chatGPTOAuthToolCall
	OutputIndexToToolIndex map[int]int
	Usage                  map[string]any
	Completed              bool
}

func newChatGPTOAuthResponseAccumulator(model string) *chatGPTOAuthResponseAccumulator {
	return &chatGPTOAuthResponseAccumulator{
		ID:                     newChatCompletionID(),
		Model:                  model,
		Created:                time.Now().Unix(),
		OutputIndexToToolIndex: make(map[int]int),
	}
}

func chatGPTResponsesSSEToChatCompletion(body []byte, model string) ([]byte, error) {
	state := newChatGPTOAuthResponseAccumulator(model)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), maxBufferedResponseBytes)
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
		state.observe(event)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if !state.Completed {
		return nil, errors.New("ChatGPT OAuth upstream did not return a completed response")
	}
	return json.Marshal(state.chatCompletion())
}

func (s *chatGPTOAuthResponseAccumulator) observe(event map[string]any) {
	typeName := strings.ToLower(stringValue(event["type"]))
	if response, ok := event["response"].(map[string]any); ok {
		s.observeResponse(response)
	}
	if usage, ok := event["usage"].(map[string]any); ok {
		s.Usage = usage
	}
	switch typeName {
	case "response.output_text.delta":
		s.Text.WriteString(rawStringValue(event["delta"]))
	case "response.refusal.delta":
		s.Refusal.WriteString(rawStringValue(event["delta"]))
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		s.Reasoning.WriteString(rawStringValue(event["delta"]))
	case "response.output_item.added":
		item, _ := event["item"].(map[string]any)
		s.observeToolItem(jsonInt(event["output_index"]), item)
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if index, ok := s.OutputIndexToToolIndex[jsonInt(event["output_index"])]; ok {
			s.Tools[index].Arguments.WriteString(rawStringValue(event["delta"]))
		}
	case "response.completed", "response.done", "response.incomplete":
		s.Completed = true
		if typeName == "response.incomplete" && s.Status == "" {
			s.Status = "incomplete"
		}
	}
}

func (s *chatGPTOAuthResponseAccumulator) observeResponse(response map[string]any) {
	if id := stringValue(response["id"]); id != "" {
		s.ID = id
	}
	if model := stringValue(response["model"]); model != "" && s.Model == "" {
		s.Model = model
	}
	if created := jsonInt64(response["created_at"]); created > 0 {
		s.Created = created
	}
	if status := stringValue(response["status"]); status != "" {
		s.Status = status
	}
	if details, ok := response["incomplete_details"].(map[string]any); ok {
		s.IncompleteReason = stringValue(details["reason"])
	}
	if usage, ok := response["usage"].(map[string]any); ok {
		s.Usage = usage
	}
	output, _ := response["output"].([]any)
	if len(output) == 0 {
		return
	}
	var text strings.Builder
	var refusal strings.Builder
	var reasoning strings.Builder
	tools := make([]chatGPTOAuthToolCall, 0)
	for _, raw := range output {
		item, _ := raw.(map[string]any)
		switch strings.ToLower(stringValue(item["type"])) {
		case "message":
			parts, _ := item["content"].([]any)
			for _, rawPart := range parts {
				part, _ := rawPart.(map[string]any)
				switch strings.ToLower(stringValue(part["type"])) {
				case "output_text":
					text.WriteString(rawStringValue(part["text"]))
				case "refusal":
					refusal.WriteString(rawStringValue(part["refusal"]))
				}
			}
		case "function_call", "custom_tool_call":
			call := chatGPTOAuthToolCall{ID: stringValue(item["call_id"]), Name: stringValue(item["name"])}
			call.Arguments.WriteString(rawStringValue(item["arguments"]))
			if call.Arguments.Len() == 0 {
				call.Arguments.WriteString(rawStringValue(item["input"]))
			}
			tools = append(tools, call)
		case "reasoning":
			summaries, _ := item["summary"].([]any)
			for _, rawSummary := range summaries {
				summary, _ := rawSummary.(map[string]any)
				reasoning.WriteString(rawStringValue(summary["text"]))
			}
		}
	}
	if text.Len() > 0 {
		s.Text = text
	}
	if refusal.Len() > 0 {
		s.Refusal = refusal
	}
	if reasoning.Len() > 0 {
		s.Reasoning = reasoning
	}
	if len(tools) > 0 {
		s.Tools = tools
	}
}

func (s *chatGPTOAuthResponseAccumulator) observeToolItem(outputIndex int, item map[string]any) {
	itemType := strings.ToLower(stringValue(item["type"]))
	if itemType != "function_call" && itemType != "custom_tool_call" {
		return
	}
	if _, exists := s.OutputIndexToToolIndex[outputIndex]; exists {
		return
	}
	index := len(s.Tools)
	s.OutputIndexToToolIndex[outputIndex] = index
	s.Tools = append(s.Tools, chatGPTOAuthToolCall{ID: stringValue(item["call_id"]), Name: stringValue(item["name"])})
}

func (s *chatGPTOAuthResponseAccumulator) chatCompletion() map[string]any {
	message := map[string]any{"role": "assistant", "content": nil}
	if s.Text.Len() > 0 {
		message["content"] = s.Text.String()
	}
	if s.Refusal.Len() > 0 {
		message["refusal"] = s.Refusal.String()
	}
	if s.Reasoning.Len() > 0 {
		message["reasoning_content"] = s.Reasoning.String()
	}
	if len(s.Tools) > 0 {
		toolCalls := make([]any, 0, len(s.Tools))
		for _, call := range s.Tools {
			arguments := call.Arguments.String()
			if arguments == "" {
				arguments = "{}"
			}
			toolCalls = append(toolCalls, map[string]any{
				"id": call.ID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": arguments},
			})
		}
		message["tool_calls"] = toolCalls
	}
	return map[string]any{
		"id": s.ID, "object": "chat.completion", "created": s.Created, "model": s.Model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": s.finishReason()}},
		"usage":   chatUsageFromResponses(s.Usage),
	}
}

func (s *chatGPTOAuthResponseAccumulator) finishReason() string {
	if s.Status == "incomplete" {
		switch s.IncompleteReason {
		case "max_output_tokens":
			return "length"
		case "content_filter":
			return "content_filter"
		}
	}
	if len(s.Tools) > 0 {
		return "tool_calls"
	}
	return "stop"
}

type chatGPTOAuthChatStreamConverter struct {
	ID                     string
	Model                  string
	Created                int64
	SentRole               bool
	SawToolCall            bool
	Finalized              bool
	IncludeUsage           bool
	NextToolIndex          int
	OutputIndexToToolIndex map[int]int
}

func newChatGPTOAuthChatStreamConverter(model string, includeUsage bool) *chatGPTOAuthChatStreamConverter {
	return &chatGPTOAuthChatStreamConverter{
		ID: newChatCompletionID(), Model: model, Created: time.Now().Unix(), IncludeUsage: includeUsage,
		OutputIndexToToolIndex: make(map[int]int),
	}
}

func (c *chatGPTOAuthChatStreamConverter) Convert(event []byte) ([]byte, error) {
	payload, ok := firstSSEPayload(event)
	if !ok {
		return nil, nil
	}
	typeName := strings.ToLower(stringValue(payload["type"]))
	if failure, failed := upstreamApplicationError(mustJSON(payload)); failed {
		_ = failure
		return event, nil
	}
	if response, ok := payload["response"].(map[string]any); ok {
		c.updateResponseIdentity(response)
	}
	var chunks []map[string]any
	switch typeName {
	case "response.created":
		chunks = append(chunks, c.ensureRole()...)
	case "response.output_text.delta":
		chunks = append(chunks, c.ensureRole()...)
		if delta := rawStringValue(payload["delta"]); delta != "" {
			chunks = append(chunks, c.deltaChunk(map[string]any{"content": delta}))
		}
	case "response.refusal.delta":
		chunks = append(chunks, c.ensureRole()...)
		if delta := rawStringValue(payload["delta"]); delta != "" {
			chunks = append(chunks, c.deltaChunk(map[string]any{"refusal": delta}))
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		chunks = append(chunks, c.ensureRole()...)
		if delta := rawStringValue(payload["delta"]); delta != "" {
			chunks = append(chunks, c.deltaChunk(map[string]any{"reasoning_content": delta}))
		}
	case "response.output_item.added":
		item, _ := payload["item"].(map[string]any)
		itemType := strings.ToLower(stringValue(item["type"]))
		if itemType == "function_call" || itemType == "custom_tool_call" {
			chunks = append(chunks, c.ensureRole()...)
			outputIndex := jsonInt(payload["output_index"])
			toolIndex := c.NextToolIndex
			c.NextToolIndex++
			c.OutputIndexToToolIndex[outputIndex] = toolIndex
			c.SawToolCall = true
			chunks = append(chunks, c.deltaChunk(map[string]any{"tool_calls": []any{map[string]any{
				"index": toolIndex, "id": stringValue(item["call_id"]), "type": "function",
				"function": map[string]any{"name": stringValue(item["name"]), "arguments": ""},
			}}}))
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if toolIndex, exists := c.OutputIndexToToolIndex[jsonInt(payload["output_index"])]; exists {
			if delta := rawStringValue(payload["delta"]); delta != "" {
				chunks = append(chunks, c.deltaChunk(map[string]any{"tool_calls": []any{map[string]any{
					"index": toolIndex, "function": map[string]any{"arguments": delta},
				}}}))
			}
		}
	case "response.completed", "response.done", "response.incomplete":
		chunks = append(chunks, c.ensureRole()...)
		finishReason := "stop"
		response, _ := payload["response"].(map[string]any)
		if strings.ToLower(stringValue(response["status"])) == "incomplete" || typeName == "response.incomplete" {
			if details, ok := response["incomplete_details"].(map[string]any); ok {
				switch stringValue(details["reason"]) {
				case "max_output_tokens":
					finishReason = "length"
				case "content_filter":
					finishReason = "content_filter"
				}
			}
		} else if c.SawToolCall {
			finishReason = "tool_calls"
		}
		usage, _ := response["usage"].(map[string]any)
		var chatUsage map[string]any
		if c.IncludeUsage && usage != nil {
			chatUsage = chatUsageFromResponses(usage)
		}
		chunks = append(chunks, c.finishChunk(finishReason, chatUsage))
		c.Finalized = true
	default:
		return nil, nil
	}
	return encodeChatSSEChunks(chunks, c.Finalized), nil
}

func (c *chatGPTOAuthChatStreamConverter) updateResponseIdentity(response map[string]any) {
	if id := stringValue(response["id"]); id != "" {
		c.ID = id
	}
	if model := stringValue(response["model"]); model != "" && c.Model == "" {
		c.Model = model
	}
	if created := jsonInt64(response["created_at"]); created > 0 {
		c.Created = created
	}
}

func (c *chatGPTOAuthChatStreamConverter) ensureRole() []map[string]any {
	if c.SentRole {
		return nil
	}
	return []map[string]any{c.roleChunk()}
}

func (c *chatGPTOAuthChatStreamConverter) roleChunk() map[string]any {
	c.SentRole = true
	return c.deltaChunk(map[string]any{"role": "assistant", "content": ""})
}

func (c *chatGPTOAuthChatStreamConverter) deltaChunk(delta map[string]any) map[string]any {
	return map[string]any{
		"id": c.ID, "object": "chat.completion.chunk", "created": c.Created, "model": c.Model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
	}
}

func (c *chatGPTOAuthChatStreamConverter) finishChunk(reason string, usage map[string]any) map[string]any {
	chunk := map[string]any{
		"id": c.ID, "object": "chat.completion.chunk", "created": c.Created, "model": c.Model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": reason}},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	return chunk
}

type chatGPTOAuthChatSSEReadCloser struct {
	source    io.ReadCloser
	reader    *bufio.Reader
	converter *chatGPTOAuthChatStreamConverter
	pending   bytes.Reader
}

func newChatGPTOAuthChatSSEReadCloser(source io.ReadCloser, model string, includeUsage bool) io.ReadCloser {
	return &chatGPTOAuthChatSSEReadCloser{
		source: source, reader: bufio.NewReader(source), converter: newChatGPTOAuthChatStreamConverter(model, includeUsage),
	}
}

func (r *chatGPTOAuthChatSSEReadCloser) Read(destination []byte) (int, error) {
	for {
		if r.pending.Len() > 0 {
			return r.pending.Read(destination)
		}
		event, err := readSSEEvent(r.reader, "responses")
		if len(event) > 0 {
			converted, convertErr := r.converter.Convert(event)
			if convertErr != nil {
				return 0, convertErr
			}
			if len(converted) > 0 {
				r.pending.Reset(converted)
				continue
			}
		}
		if err != nil {
			return 0, err
		}
	}
}

func (r *chatGPTOAuthChatSSEReadCloser) Close() error {
	return r.source.Close()
}

func chatStreamIncludesUsage(payload *RelayPayload) bool {
	if payload == nil {
		return false
	}
	options, _ := payload.values["stream_options"].(map[string]any)
	include, _ := options["include_usage"].(bool)
	return include
}

func chatUsageFromResponses(usage map[string]any) map[string]any {
	if usage == nil {
		return nil
	}
	inputTokens := jsonInt64(usage["input_tokens"])
	outputTokens := jsonInt64(usage["output_tokens"])
	result := map[string]any{
		"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens,
	}
	if details, ok := usage["input_tokens_details"].(map[string]any); ok {
		result["prompt_tokens_details"] = map[string]any{
			"cached_tokens": jsonInt64(details["cached_tokens"]),
		}
	}
	if details, ok := usage["output_tokens_details"].(map[string]any); ok {
		result["completion_tokens_details"] = map[string]any{
			"reasoning_tokens": jsonInt64(details["reasoning_tokens"]),
		}
	}
	return result
}

func firstSSEPayload(event []byte) (map[string]any, bool) {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(data, &payload) == nil {
			return payload, true
		}
	}
	return nil, false
}

func encodeChatSSEChunks(chunks []map[string]any, done bool) []byte {
	var output bytes.Buffer
	for _, chunk := range chunks {
		data, err := json.Marshal(chunk)
		if err != nil {
			continue
		}
		output.WriteString("data: ")
		output.Write(data)
		output.WriteString("\n\n")
	}
	if done {
		output.WriteString("data: [DONE]\n\n")
	}
	return output.Bytes()
}

func newChatCompletionID() string {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "chatcmpl-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "chatcmpl-" + hex.EncodeToString(data)
}

func jsonInt(value any) int {
	return int(jsonInt64(value))
}

func jsonInt64(value any) int64 {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case float64:
		return int64(typed)
	case int:
		return int64(typed)
	case int64:
		return typed
	default:
		return 0
	}
}

func rawStringValue(value any) string {
	text, _ := value.(string)
	return text
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
