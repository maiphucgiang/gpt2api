package httpapi

import (
	"errors"

	"github.com/maiphucgiang/gpt2api/internal/model"
	"github.com/maiphucgiang/gpt2api/internal/protocol"
)

func validateResponsesRequest(request protocol.ResponsesRequest, route model.ChatRoute, messages []protocol.Message) error {
	if request.PreviousResponseID != "" || request.Conversation != nil || request.Background || (request.Store != nil && *request.Store) {
		return errors.New("stored/background Responses and previous_response_id are unsupported; send the complete input history with store:false")
	}
	if len(request.Tools) > 0 {
		return errors.New("function and built-in tools are unsupported by the ChatGPT Web provider")
	}
	if request.ToolChoice != nil && request.ToolChoice != "none" && request.ToolChoice != "auto" {
		return errors.New("tool_choice is unsupported by the ChatGPT Web provider")
	}
	if format, ok := request.Text["format"].(map[string]any); ok && format["type"] != nil && format["type"] != "text" {
		return errors.New("structured output formats are unsupported by the ChatGPT Web provider")
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 2) {
		return errors.New("temperature must be between 0 and 2")
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return errors.New("top_p must be between 0 and 1")
	}
	if request.MaxOutputTokens != nil && *request.MaxOutputTokens < 1 {
		return errors.New("max_output_tokens must be positive")
	}
	if protocol.ExtractMessage(messages) == "" {
		return errors.New("input must contain text")
	}
	if !route.Image {
		for _, message := range messages {
			parts, _ := message.Content.([]any)
			for _, raw := range parts {
				part, _ := raw.(map[string]any)
				if part["type"] == "image_url" {
					return errors.New("image input is supported only by image models in this provider")
				}
			}
		}
	}
	return nil
}
