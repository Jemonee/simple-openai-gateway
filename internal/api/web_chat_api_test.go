package api

import (
	"testing"

	"github.com/Jemonee/simple-openai-gateway/internal/gateway"
)

func TestNewAdminWebChatTokenPreservesConsoleChannelScope(t *testing.T) {
	for _, test := range []struct {
		name        string
		role        string
		wantOwnerID uint64
	}{
		{name: "administrator", role: gateway.ConsoleRoleAdmin},
		{name: "legacy administrator"},
		{name: "regular user", role: gateway.ConsoleRoleUser, wantOwnerID: 7},
		{name: "unrecognized role", role: "unknown", wantOwnerID: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			user := &gateway.AdminUser{ID: 7, Username: "console-user", Role: test.role}
			token := newAdminWebChatToken(user)
			if token.OwnerUserID != test.wantOwnerID {
				t.Fatalf("temporary console token owner = %d, want %d", token.OwnerUserID, test.wantOwnerID)
			}
			if token.ID != webChatTokenIDBase+user.ID || !token.Enabled || !token.AllowAllModels || !token.AllowAllChannels {
				t.Fatalf("temporary console token = %+v", token)
			}
		})
	}
}

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

func TestValidateImageGenerationRequest(t *testing.T) {
	input := imageGenerationRequest{Model: " gpt-image-2.5-flare ", Prompt: " sunset ", Size: "2k", Quality: "HIGH"}
	if err := validateImageGenerationRequest(&input); err != nil {
		t.Fatal(err)
	}
	if input.Model != "gpt-image-2.5-flare" || input.Prompt != "sunset" || input.Size != "2K" || input.Quality != "high" {
		t.Fatalf("normalized input = %+v", input)
	}
	if err := validateImageGenerationRequest(&imageGenerationRequest{Model: "gpt-image-2.5-flare", Prompt: "sunset", Size: "1024x1024", Quality: "high"}); err == nil {
		t.Fatal("unsupported size passed validation")
	}
}

func TestImageGenerationURLsSupportsURLAndBase64(t *testing.T) {
	images, err := imageGenerationURLs([]byte(`{"data":[{"url":"https://example.com/image.png"},{"b64_json":"aW1hZ2U="}]}`))
	if err != nil || len(images) != 2 || images[0] != "https://example.com/image.png" || images[1] != "data:image/png;base64,aW1hZ2U=" {
		t.Fatalf("images = %#v, err = %v", images, err)
	}
}
