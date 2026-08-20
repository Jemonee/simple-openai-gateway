package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type oauthRoundTripFunc func(*http.Request) (*http.Response, error)

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
	if string(body) != `{"id":"resp_1","status":"completed"}` {
		t.Fatalf("unexpected converted body: %s", body)
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
