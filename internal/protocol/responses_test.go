package protocol

import "testing"

func TestResponsesInputNormalizesTextAndImages(t *testing.T) {
	messages, err := ParseResponsesInput([]any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hello"}, map[string]any{"type": "input_image", "image_url": "https://example.test/image.png"}}}}, "instructions")
	if err != nil || len(messages) != 2 {
		t.Fatalf("parse: %v", err)
	}
	if ExtractMessage(messages) != "[system]: instructions\n\n[user]: hello" {
		t.Fatal("input text was discarded")
	}
	parts := messages[1].Content.([]any)
	if parts[1].(map[string]any)["type"] != "image_url" {
		t.Fatal("image was discarded")
	}
}

func TestResponsesInputRejectsUnsupportedItems(t *testing.T) {
	for _, input := range []any{nil, "", []any{}, []any{map[string]any{"type": "function_call_output", "call_id": "call-1", "output": "result"}}, []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_file", "file_id": "file-1"}}}}} {
		if _, err := ParseResponsesInput(input, "instructions"); err == nil {
			t.Fatalf("unsupported input was accepted: %#v", input)
		}
	}
}
