package provider

import (
	"strings"
	"testing"

	"github.com/maiphucgiang/gpt2api/internal/protocol"
)

func TestOpenAIChatPayloadUsesRawMessageText(t *testing.T) {
	payload, err := openAIChatPayload(protocol.ChatRequest{
		Model: "gpt-5-6",
		Messages: []protocol.Message{
			{Role: "user", Content: "请回复Go测试成功"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("unexpected messages: %#v", payload["messages"])
	}
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	if len(parts) != 1 || parts[0] != "请回复Go测试成功" {
		t.Fatalf("message text was decorated: %#v", parts)
	}
	if strings.Contains(parts[0].(string), "[user]") {
		t.Fatalf("message contains protocol decoration: %q", parts[0])
	}
}

func TestOpenAIChatPayloadOmitsThinkingEffort(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "extended"} {
		payload, err := openAIChatPayload(protocol.ChatRequest{
			Model:           "auto",
			ReasoningEffort: effort,
			Messages:        []protocol.Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := payload["thinking_effort"]; ok {
			t.Fatalf("web chat must not send thinking_effort=%q: %#v", effort, payload["thinking_effort"])
		}
	}
}

func TestOpenAIChatPayloadRejectsImageParts(t *testing.T) {
	_, err := openAIChatPayload(protocol.ChatRequest{
		Model: "gpt-5-5",
		Messages: []protocol.Message{{
			Role: "user",
			Content: []any{
				map[string]any{"type": "text", "text": "look at this"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/a.png"}},
			},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "image content is not supported for web chat") {
		t.Fatalf("expected image rejection, got %v", err)
	}
	_, err = openAIChatPayload(protocol.ChatRequest{
		Model: "gpt-5-5",
		Messages: []protocol.Message{{
			Role:    "user",
			Content: []any{map[string]any{"type": "input_image", "image_url": "data:image/png;base64,aaaa"}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "image content is not supported for web chat") {
		t.Fatalf("expected input_image rejection, got %v", err)
	}
}

func TestOpenAIChatStateReturnsSSEDetailAsError(t *testing.T) {
	state := &openAIChatState{}
	_, err := state.event(map[string]any{"detail": "Invalid conversation body"})
	if err == nil || !strings.Contains(err.Error(), "Invalid conversation body") {
		t.Fatalf("expected upstream detail error, got %v", err)
	}
}
