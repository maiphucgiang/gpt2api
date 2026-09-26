package protocol

import (
	"fmt"
	"strings"
)

func ResponsesInputMessages(input any, instructions string) []Message {
	messages, _ := ParseResponsesInput(input, instructions)
	return messages
}

// ParseResponsesInput normalizes supported content without discarding unknown input items.
func ParseResponsesInput(input any, instructions string) ([]Message, error) {
	messages := []Message{}
	switch value := input.(type) {
	case string:
		if strings.TrimSpace(value) != "" {
			messages = append(messages, Message{Role: "user", Content: value})
		}
	case []any:
		for index, raw := range value {
			object, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("input item %d must be a message", index)
			}
			kind := stringValue(object["type"])
			if kind != "" && kind != "message" {
				return nil, fmt.Errorf("input item type %q is not supported", kind)
			}
			role := stringValue(object["role"])
			if role == "" {
				role = "user"
			}
			switch role {
			case "user", "assistant", "system", "developer":
			default:
				return nil, fmt.Errorf("input role %q is not supported", role)
			}
			content, err := responsesContent(object["content"])
			if err != nil {
				return nil, fmt.Errorf("input item %d: %w", index, err)
			}
			messages = append(messages, Message{Role: role, Content: content})
		}
	default:
		return nil, fmt.Errorf("input must be text or a message array")
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("input cannot be empty")
	}
	if strings.TrimSpace(instructions) != "" {
		messages = append([]Message{{Role: "system", Content: instructions}}, messages...)
	}
	return messages, nil
}

func responsesContent(value any) (any, error) {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return text, nil
	}
	parts, ok := value.([]any)
	if !ok || len(parts) == 0 {
		return nil, fmt.Errorf("content cannot be empty")
	}
	normalized := make([]any, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("content part must be an object")
		}
		switch stringValue(part["type"]) {
		case "input_text", "output_text", "text":
			text, ok := part["text"].(string)
			if !ok {
				return nil, fmt.Errorf("text content must be a string")
			}
			normalized = append(normalized, map[string]any{"type": "text", "text": text})
		case "input_image", "image_url":
			image := part["image_url"]
			switch image.(type) {
			case string:
				image = map[string]any{"url": image}
			case map[string]any:
			default:
				return nil, fmt.Errorf("input_image requires image_url; file_id is unsupported")
			}
			normalized = append(normalized, map[string]any{"type": "image_url", "image_url": image})
		default:
			return nil, fmt.Errorf("content type %q is not supported", stringValue(part["type"]))
		}
	}
	return normalized, nil
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
