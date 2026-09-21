package protocol

import (
	"fmt"
	"strings"
)

func ResponsesInputMessages(input any, instructions string) []Message {
	messages := make([]Message, 0, 4)
	if strings.TrimSpace(instructions) != "" {
		messages = append(messages, Message{Role: "system", Content: instructions})
	}
	switch typed := input.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			messages = append(messages, Message{Role: "user", Content: typed})
		}
	case []any:
		for _, raw := range typed {
			if object, ok := raw.(map[string]any); ok {
				role := stringValue(object["role"])
				if role == "" {
					role = "user"
				}
				messages = append(messages, Message{Role: role, Content: object["content"]})
				continue
			}
			if text := stringValue(raw); text != "" {
				messages = append(messages, Message{Role: "user", Content: text})
			}
		}
	}
	return messages
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
