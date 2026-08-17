package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
)

type relayMultipartPart struct {
	header   textproto.MIMEHeader
	formName string
	data     []byte
}

type relayMultipartPayload struct {
	contentType string
	boundary    string
	parts       []relayMultipartPart
}

type RelayPayload struct {
	values                map[string]any
	multipart             *relayMultipartPayload
	Model                 string
	Stream                bool
	PinnedChannelID       uint64
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

func (p *RelayPayload) ConfigureSession(sessionID string, sessionSource string, clientKind string, threadSource string) {
	if p == nil {
		return
	}
	p.SessionKey = truncateRunes(strings.TrimSpace(sessionID), 512)
	p.SessionSource = truncateRunes(strings.TrimSpace(sessionSource), 48)
	if p.SessionSource == "" {
		p.SessionSource = sessionUnavailable
	}
	p.LogSessionKey = p.SessionKey
	p.LogSessionSource = p.SessionSource
	p.ClientKind = truncateRunes(strings.TrimSpace(clientKind), 32)
	p.ThreadSource = normalizeCodexThreadSource(threadSource)
}

func ParseRelayPayload(data []byte) (*RelayPayload, error) {
	return ParseRelayPayloadWithContentType(data, "application/json")
}

func ParseRelayPayloadWithContentType(data []byte, contentType string) (*RelayPayload, error) {
	mediaType, parameters, mediaTypeErr := mime.ParseMediaType(contentType)
	if mediaTypeErr == nil && strings.EqualFold(mediaType, "multipart/form-data") {
		return parseMultipartRelayPayload(data, contentType, parameters["boundary"])
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/form-data") {
		return nil, errors.New("request body must be valid multipart/form-data")
	}

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
	return newRelayPayload(values, model), nil
}

func parseMultipartRelayPayload(data []byte, contentType string, boundary string) (*RelayPayload, error) {
	if strings.TrimSpace(boundary) == "" {
		return nil, errors.New("multipart boundary is required")
	}
	reader := multipart.NewReader(bytes.NewReader(data), boundary)
	parts := make([]relayMultipartPart, 0)
	values := make(map[string]any)
	model := ""
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("request body must be valid multipart/form-data")
		}
		partData, err := io.ReadAll(part)
		_ = part.Close()
		if err != nil {
			return nil, errors.New("request body must be valid multipart/form-data")
		}
		formName := part.FormName()
		parts = append(parts, relayMultipartPart{
			header:   cloneMIMEHeader(part.Header),
			formName: formName,
			data:     partData,
		})
		if formName == "" || part.FileName() != "" {
			continue
		}
		value := string(partData)
		appendRelayFormValue(values, formName, value)
		if formName == "model" && model == "" {
			model = strings.TrimSpace(value)
		}
	}
	if model == "" {
		return nil, errors.New("model is required")
	}
	payload := newRelayPayload(values, model)
	payload.multipart = &relayMultipartPayload{contentType: contentType, boundary: boundary, parts: parts}
	return payload, nil
}

func appendRelayFormValue(values map[string]any, key string, value string) {
	existing, exists := values[key]
	if !exists {
		values[key] = value
		return
	}
	if items, ok := existing.([]any); ok {
		values[key] = append(items, value)
		return
	}
	values[key] = []any{existing, value}
}

func cloneMIMEHeader(source textproto.MIMEHeader) textproto.MIMEHeader {
	cloned := make(textproto.MIMEHeader, len(source))
	for key, values := range source {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func newRelayPayload(values map[string]any, model string) *RelayPayload {
	payload := &RelayPayload{values: values, Model: model}
	payload.Stream, _ = values["stream"].(bool)
	if stream, ok := values["stream"].(string); ok {
		payload.Stream = strings.EqualFold(strings.TrimSpace(stream), "true")
	}
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
	return payload
}

func loggableRequestParameters(values map[string]any) string {
	parameters := make(map[string]any)
	for _, key := range []string{
		"model", "stream", "max_output_tokens", "max_completion_tokens", "max_tokens", "temperature", "top_p",
		"frequency_penalty", "presence_penalty", "seed", "n", "parallel_tool_calls", "store", "service_tier",
		"truncation", "reasoning_effort", "verbosity", "max_tool_calls", "size", "quality", "background",
		"output_format", "output_compression", "moderation", "response_format", "partial_images",
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
	body, _, err := p.UpstreamRequestBody(upstreamModel, endpoint, includeStreamUsage)
	return body, err
}

func (p *RelayPayload) UpstreamRequestBody(upstreamModel string, endpoint string, includeStreamUsage bool) ([]byte, string, error) {
	if p.multipart != nil {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := writer.SetBoundary(p.multipart.boundary); err != nil {
			return nil, "", err
		}
		for _, item := range p.multipart.parts {
			part, err := writer.CreatePart(cloneMIMEHeader(item.header))
			if err != nil {
				return nil, "", err
			}
			partData := item.data
			if item.formName == "model" {
				partData = []byte(upstreamModel)
			}
			if _, err := part.Write(partData); err != nil {
				return nil, "", err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, "", err
		}
		return body.Bytes(), p.multipart.contentType, nil
	}

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
	body, err := json.Marshal(values)
	return body, "application/json", err
}

func (p *RelayPayload) IsMultipart() bool {
	return p != nil && p.multipart != nil
}
