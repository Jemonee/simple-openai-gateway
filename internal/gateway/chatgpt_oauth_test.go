package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type oauthRoundTripFunc func(*http.Request) (*http.Response, error)

type delayedOAuthSSEReader struct {
	chunks [][]byte
	delay  time.Duration
	index  int
}

func (r *delayedOAuthSSEReader) Read(destination []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	if r.index > 0 {
		time.Sleep(r.delay)
	}
	chunk := r.chunks[r.index]
	r.index++
	return copy(destination, chunk), nil
}

func (r *delayedOAuthSSEReader) Close() error { return nil }

func (f oauthRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestChatGPTOAuthRequestBodyTransformsResponsesPayload(t *testing.T) {
	payload, err := ParseRelayPayload([]byte(`{"model":"public","input":"hello","stream":false,"store":true,"temperature":0.2,"reasoning":{"effort":"high"}}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := payload.ChatGPTOAuthRequestBody("gpt-5.4", "responses")
	if err != nil {
		t.Fatal(err)
	}
	var transformed map[string]any
	if err := json.Unmarshal(body, &transformed); err != nil {
		t.Fatal(err)
	}
	if transformed["model"] != "gpt-5.4" || transformed["store"] != false || transformed["stream"] != true {
		t.Fatalf("unexpected control fields: %#v", transformed)
	}
	if _, ok := transformed["temperature"]; ok {
		t.Fatal("unsupported temperature was not removed")
	}
	input, ok := transformed["input"].([]any)
	if !ok || len(input) != 1 || input[0].(map[string]any)["type"] != "message" {
		t.Fatalf("unexpected input: %#v", transformed["input"])
	}
	include := transformed["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("unexpected reasoning include: %#v", include)
	}
}

func TestChatGPTOAuthPromotesSystemInput(t *testing.T) {
	payload, err := ParseRelayPayload([]byte(`{"model":"public","input":[{"type":"message","role":"system","content":"follow policy"},{"type":"message","role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := payload.ChatGPTOAuthRequestBody("gpt-5.4", "responses")
	if err != nil {
		t.Fatal(err)
	}
	var transformed map[string]any
	if err := json.Unmarshal(body, &transformed); err != nil {
		t.Fatal(err)
	}
	if transformed["instructions"] != "follow policy" {
		t.Fatalf("system input was not promoted: %#v", transformed)
	}
	if input := transformed["input"].([]any); len(input) != 1 {
		t.Fatalf("system input was not removed: %#v", input)
	}
}

func TestChatGPTResponsesSSEToJSON(t *testing.T) {
	body, err := chatGPTResponsesSSEToJSON([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\ndata: [DONE]\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"id":"resp_1","output":[],"status":"completed"}` {
		t.Fatalf("unexpected converted body: %s", body)
	}
}

func TestChatGPTResponsesSSEToJSONAggregatesOutputBeforeEmptyCompletedResponse(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_aggregate","object":"response","status":"in_progress","output":[]}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		`data: {"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hel"}`,
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"lo"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"reason_1","type":"reasoning","summary":[]}}`,
		`data: {"type":"response.reasoning_summary_text.delta","output_index":1,"summary_index":0,"delta":"because"}`,
		`data: {"type":"response.output_item.added","output_index":2,"item":{"id":"call_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"city\":"}`,
		`data: {"type":"response.function_call_arguments.done","output_index":2,"arguments":"{\"city\":\"Paris\"}"}`,
		`data: {"type":"response.completed","response":{"id":"resp_aggregate","object":"response","status":"completed","output":[],"usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}}`,
	}, "\n\n") + "\n\n"

	converted, err := chatGPTResponsesSSEToJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(converted, &response); err != nil {
		t.Fatal(err)
	}
	output, _ := response["output"].([]any)
	if len(output) != 3 {
		t.Fatalf("aggregated output = %#v", output)
	}
	message := output[0].(map[string]any)
	content := message["content"].([]any)[0].(map[string]any)
	if content["text"] != "hello" || message["status"] != "completed" {
		t.Fatalf("aggregated message = %#v", message)
	}
	reasoning := output[1].(map[string]any)
	if reasoning["summary"].([]any)[0].(map[string]any)["text"] != "because" {
		t.Fatalf("aggregated reasoning = %#v", reasoning)
	}
	tool := output[2].(map[string]any)
	if tool["arguments"] != `{"city":"Paris"}` || tool["name"] != "lookup" {
		t.Fatalf("aggregated tool call = %#v", tool)
	}
	usage := response["usage"].(map[string]any)
	if usage["total_tokens"] != float64(16) {
		t.Fatalf("aggregated usage = %#v", usage)
	}
}

func TestChatGPTOAuthResponseIsSSEDetectsBodyWhenContentTypeIsIncorrect(t *testing.T) {
	body := []byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	if !chatGPTOAuthResponseIsSSE(http.Header{"Content-Type": []string{"application/json"}}, body) {
		t.Fatal("SSE body was not detected when the upstream content type was application/json")
	}
	if chatGPTOAuthResponseIsSSE(http.Header{"Content-Type": []string{"application/json"}}, []byte(`{"status":"completed"}`)) {
		t.Fatal("ordinary JSON response was incorrectly classified as SSE")
	}
}

func TestAdaptChatGPTOAuthBufferedResponseToStandardChatCompletion(t *testing.T) {
	body := []byte(`{"id":"resp_json","model":"gpt-5.6-luna","status":"completed","output_text":"hello","usage":{"input_tokens":2,"output_tokens":1}}`)
	converted, adapted, err := adaptChatGPTOAuthBufferedResponse(http.Header{"Content-Type": []string{"application/json"}}, body, "chat", "gpt-5.6-luna")
	if err != nil || !adapted {
		t.Fatalf("adapted = %v, error = %v", adapted, err)
	}
	var completion map[string]any
	if err := json.Unmarshal(converted, &completion); err != nil {
		t.Fatal(err)
	}
	if completion["object"] != "chat.completion" || completion["model"] != "gpt-5.6-luna" {
		t.Fatalf("standard completion metadata = %#v", completion)
	}
	choice := completion["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if message["content"] != "hello" || choice["finish_reason"] != "stop" {
		t.Fatalf("standard completion content = %#v", completion)
	}
}

func TestRelayConvertsOAuthChatSSEWhenContentTypeIsIncorrect(t *testing.T) {
	store := newTestStore(t)
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://unused.invalid")
	if err := store.db.Model(&Channel{}).Where("id = ?", channels[0].ID).Updates(map[string]any{
		"auth_type": ChannelTypeChatGPTOAuth,
		"base_url":  ChatGPTCodexBaseURL,
	}).Error; err != nil {
		t.Fatal(err)
	}

	relay := newTestRelay(store)
	relay.client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body := strings.Join([]string{
			`event: response.output_text.delta`,
			`data: {"type":"response.output_text.delta","delta":"hello"}`,
			`data: {"type":"response.completed","response":{"id":"resp_chat","model":"upstream-1","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`,
		}, "\n\n") + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})

	raw := []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	payload, err := ParseRelayPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := relay.Relay(context.Background(), recorder, nil, "", "chat", token, payload, raw); publicErr != nil {
		t.Fatal(publicErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var completion map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &completion); err != nil {
		t.Fatal(err)
	}
	choices, _ := completion["choices"].([]any)
	if len(choices) != 1 || choices[0].(map[string]any)["finish_reason"] != "stop" {
		t.Fatalf("converted chat completion = %#v", completion)
	}
}

func TestRelayStreamsOAuthResponsesWhenContentTypeIsIncorrect(t *testing.T) {
	store := newTestStore(t)
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://unused.invalid")
	if err := store.db.Model(&Channel{}).Where("id = ?", channels[0].ID).Updates(map[string]any{
		"auth_type": ChannelTypeChatGPTOAuth,
		"base_url":  ChatGPTCodexBaseURL,
	}).Error; err != nil {
		t.Fatal(err)
	}

	relay := newTestRelay(store)
	relay.client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body := strings.Join([]string{
			`event: response.created\ndata: {"type":"response.created","response":{"id":"resp_stream","object":"response","status":"in_progress","output":[]}}`,
			`event: response.output_text.delta\ndata: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}`,
			`event: response.completed\ndata: {"type":"response.completed","response":{"id":"resp_stream","object":"response","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
		}, "\n\n") + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})

	raw := []byte(`{"model":"public-model","input":"hello","stream":true}`)
	payload, err := ParseRelayPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := relay.Relay(context.Background(), recorder, nil, "", "responses", token, payload, raw); publicErr != nil {
		t.Fatal(publicErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("content type = %q", contentType)
	}
	streamBody := recorder.Body.String()
	if !strings.Contains(streamBody, "response.output_text.delta") || !strings.Contains(streamBody, "response.completed") {
		t.Fatalf("stream body = %q", streamBody)
	}
}

func TestRelayAggregatesOAuthResponsesWhenCompletedOutputIsEmpty(t *testing.T) {
	store := newTestStore(t)
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://unused.invalid")
	if err := store.db.Model(&Channel{}).Where("id = ?", channels[0].ID).Updates(map[string]any{
		"auth_type": ChannelTypeChatGPTOAuth,
		"base_url":  ChatGPTCodexBaseURL,
	}).Error; err != nil {
		t.Fatal(err)
	}

	relay := newTestRelay(store)
	relay.client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body := strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_buffered","object":"response","status":"in_progress","output":[]}}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}}`,
			`data: {"type":"response.completed","response":{"id":"resp_buffered","object":"response","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
		}, "\n\n") + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})

	raw := []byte(`{"model":"public-model","input":"hello","stream":false}`)
	payload, err := ParseRelayPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if publicErr := relay.Relay(context.Background(), recorder, nil, "", "responses", token, payload, raw); publicErr != nil {
		t.Fatal(publicErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	output, _ := response["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("aggregated response = %#v", response)
	}
	content := output[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if content["text"] != "hello" {
		t.Fatalf("aggregated response = %#v", response)
	}
}

func TestRelayRecordsOAuthBufferedSSETimingMetrics(t *testing.T) {
	store := newTestStore(t)
	token, _, channels, _ := createRouteFixture(t, store, RoutingPriorityWeighted, "http://unused.invalid")
	if err := store.db.Model(&Channel{}).Where("id = ?", channels[0].ID).Updates(map[string]any{
		"auth_type": ChannelTypeChatGPTOAuth,
		"base_url":  ChatGPTCodexBaseURL,
	}).Error; err != nil {
		t.Fatal(err)
	}
	relay := newTestRelay(store)
	relay.client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: &delayedOAuthSSEReader{delay: 30 * time.Millisecond, chunks: [][]byte{
				[]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_metrics\",\"model\":\"upstream-1\",\"status\":\"in_progress\"}}\n\n"),
				[]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"),
				[]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_metrics\",\"model\":\"upstream-1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"),
			}},
			Request: request,
		}, nil
	})
	raw := []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	payload, err := ParseRelayPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if publicErr := relay.Relay(context.Background(), httptest.NewRecorder(), nil, "", "chat", token, payload, raw); publicErr != nil {
		t.Fatal(publicErr)
	}
	var log RelayRequestLog
	if err := store.db.First(&log).Error; err != nil {
		t.Fatal(err)
	}
	if log.FirstResponseMS <= 0 || log.FirstTokenMS <= 0 || log.TokensPerSecond <= 0 {
		t.Fatalf("OAuth buffered timing metrics = %+v", log)
	}
}

func TestChatGPTOAuthRequestBodyTransformsChatMessages(t *testing.T) {
	payload, err := ParseRelayPayload([]byte(`{"model":"public","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hello"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Paris\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"sunny"}],"functions":[{"name":"lookup","parameters":{"type":"object"}}],"function_call":{"name":"lookup"},"stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := payload.ChatGPTOAuthRequestBody("gpt-5.4", "chat")
	if err != nil {
		t.Fatal(err)
	}
	var transformed map[string]any
	if err := json.Unmarshal(body, &transformed); err != nil {
		t.Fatal(err)
	}
	if transformed["model"] != "gpt-5.4" || transformed["stream"] != true || transformed["instructions"] != "be concise" {
		t.Fatalf("unexpected controls: %#v", transformed)
	}
	if _, exists := transformed["messages"]; exists {
		t.Fatal("chat messages were not removed")
	}
	input, _ := transformed["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("unexpected input: %#v", input)
	}
	functionCall := input[1].(map[string]any)
	if functionCall["type"] != "function_call" || functionCall["call_id"] != "call_1" || functionCall["name"] != "lookup" {
		t.Fatalf("unexpected function call: %#v", functionCall)
	}
	toolOutput := input[2].(map[string]any)
	if toolOutput["type"] != "function_call_output" || toolOutput["call_id"] != "call_1" || toolOutput["output"] != "sunny" {
		t.Fatalf("unexpected tool output: %#v", toolOutput)
	}
	tools, _ := transformed["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "lookup" || tools[0].(map[string]any)["function"] != nil {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	choice, _ := transformed["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["name"] != "lookup" {
		t.Fatalf("unexpected tool choice: %#v", choice)
	}
}

func TestChatGPTResponsesSSEToChatCompletion(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_chat","model":"gpt-5.4","created_at":123}}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.output_text.delta","delta":" world"}`,
		`data: {"type":"response.completed","response":{"id":"resp_chat","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":2,"input_tokens_details":{"cached_tokens":3}}}}`,
		`data: [DONE]`,
	}, "\n\n")
	converted, err := chatGPTResponsesSSEToChatCompletion([]byte(body), "public-model")
	if err != nil {
		t.Fatal(err)
	}
	var completion map[string]any
	if err := json.Unmarshal(converted, &completion); err != nil {
		t.Fatal(err)
	}
	if completion["object"] != "chat.completion" || completion["model"] != "public-model" {
		t.Fatalf("unexpected completion metadata: %#v", completion)
	}
	choices := completion["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	if message["content"] != "hello world" || choices[0].(map[string]any)["finish_reason"] != "stop" {
		t.Fatalf("unexpected completion choice: %#v", choices[0])
	}
	usage := completion["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(7) || usage["completion_tokens"] != float64(2) || usage["total_tokens"] != float64(9) {
		t.Fatalf("unexpected usage: %#v", usage)
	}
}

func TestChatGPTOAuthChatStreamConversion(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_stream","model":"gpt-5.4","created_at":123}}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.completed","response":{"id":"resp_stream","status":"completed","usage":{"input_tokens":4,"output_tokens":1}}}`,
	}, "\n\n") + "\n\n"
	reader := newChatGPTOAuthChatSSEReadCloser(io.NopCloser(strings.NewReader(upstream)), "public-model", true)
	converted, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(converted)
	for _, expected := range []string{`"object":"chat.completion.chunk"`, `"role":"assistant"`, `"content":"hello"`, `"finish_reason":"stop"`, `"prompt_tokens":4`, `data: [DONE]`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("converted stream missing %q: %s", expected, text)
		}
	}
	tracker := newSSETerminalTracker("chat", 1)
	if !tracker.Observe(converted) {
		t.Fatalf("converted stream is not terminal: %s", text)
	}
}

func TestChatGPTOAuthChatStreamConversionDoesNotWaitForCompletion(t *testing.T) {
	reader := newChatGPTOAuthChatSSEReadCloser(&delayedOAuthSSEReader{delay: 80 * time.Millisecond, chunks: [][]byte{
		[]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_stream\",\"model\":\"gpt-5.4\"}}\n\n"),
		[]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"),
		[]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream\",\"status\":\"completed\"}}\n\n"),
	}}, "public-model", false)
	started := time.Now()
	buffer := make([]byte, 4096)
	n, err := reader.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || time.Since(started) >= 60*time.Millisecond {
		t.Fatalf("stream adapter waited for completion: bytes=%d elapsed=%s", n, time.Since(started))
	}
	if !strings.Contains(string(buffer[:n]), "chat.completion.chunk") {
		t.Fatalf("first adapted chunk = %q", buffer[:n])
	}
}

func TestChatGPTResponsesSSEToChatCompletionToolCall(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_7","name":"lookup"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"city\":"}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"Paris\"}"}`,
		`data: {"type":"response.completed","response":{"id":"resp_tool","status":"completed","output":[],"usage":{"input_tokens":8,"output_tokens":3}}}`,
	}, "\n\n")
	converted, err := chatGPTResponsesSSEToChatCompletion([]byte(body), "public-model")
	if err != nil {
		t.Fatal(err)
	}
	var completion map[string]any
	if err := json.Unmarshal(converted, &completion); err != nil {
		t.Fatal(err)
	}
	choice := completion["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("unexpected finish reason: %#v", choice)
	}
	message := choice["message"].(map[string]any)
	call := message["tool_calls"].([]any)[0].(map[string]any)
	function := call["function"].(map[string]any)
	if call["id"] != "call_7" || function["name"] != "lookup" || function["arguments"] != `{"city":"Paris"}` {
		t.Fatalf("unexpected tool call: %#v", call)
	}
}

func TestValidateOAuthRedirectURI(t *testing.T) {
	if _, err := validateOAuthRedirectURI("http://example.com/callback"); err == nil {
		t.Fatal("non-local HTTP redirect URI should be rejected")
	}
	if _, err := validateOAuthRedirectURI("https://example.com/callback?code=1"); err == nil {
		t.Fatal("redirect query should be rejected")
	}
	if _, err := validateOAuthRedirectURI("http://user@localhost/callback"); err == nil {
		t.Fatal("redirect user info should be rejected")
	}
	if normalized, err := validateOAuthRedirectURI("http://localhost:1455/auth/callback"); err != nil || normalized != "http://localhost:1455/auth/callback" {
		t.Fatalf("valid redirect rejected: %q, %v", normalized, err)
	}
}

func TestChatGPTOAuthExchangeUsesPKCEAndDeletesSession(t *testing.T) {
	service := NewChatGPTOAuthService()
	service.tokenURL = "https://auth.test/oauth/token"
	var requestForm url.Values
	service.client = &http.Client{Transport: oauthRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(request.Body)
		requestForm, _ = url.ParseQuery(string(data))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"access","refresh_token":"refresh","expires_in":3600}`))}, nil
	})}
	started, err := service.Start(ChatGPTOAuthStartInput{RedirectURI: "http://localhost:1455/auth/callback"})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := service.Exchange(context.Background(), ChatGPTOAuthExchangeInput{SessionID: started.SessionID, Code: "code", State: started.State})
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != "access" || credential.RefreshToken != "refresh" || credential.ExpiresAt == nil || credential.ExpiresAt.Before(time.Now()) {
		t.Fatalf("unexpected credential: %#v", credential)
	}
	if requestForm.Get("code_verifier") == "" || requestForm.Get("code") != "code" {
		t.Fatalf("PKCE form missing: %v", requestForm)
	}
	if _, err := service.Exchange(context.Background(), ChatGPTOAuthExchangeInput{SessionID: started.SessionID, Code: "code", State: started.State}); err == nil {
		t.Fatal("OAuth session should be single-use")
	}
}
