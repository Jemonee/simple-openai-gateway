package api

import "testing"

func TestChatCompletionContentSupportsStringAndParts(t *testing.T) {
	stringContent, err := chatCompletionContent([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	if err != nil || stringContent != "hello" {
		t.Fatalf("string content = %q, %v", stringContent, err)
	}
	partsContent, err := chatCompletionContent([]byte(`{"choices":[{"message":{"content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}}]}`))
	if err != nil || partsContent != "first\nsecond" {
		t.Fatalf("parts content = %q, %v", partsContent, err)
	}
}

func TestValidateWebChatRequest(t *testing.T) {
	input := webChatRequest{
		SessionID: " web-chat-1 ",
		Model:     " model-a ",
		Messages:  []webChatMessage{{Role: "USER", Content: " hello "}},
	}
	if err := validateWebChatRequest(&input); err != nil {
		t.Fatal(err)
	}
	if input.SessionID != "web-chat-1" || input.Model != "model-a" || input.Messages[0].Role != "user" || input.Messages[0].Content != "hello" {
		t.Fatalf("normalized input = %+v", input)
	}
}
