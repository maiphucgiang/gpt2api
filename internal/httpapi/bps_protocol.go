package httpapi

import (
	"errors"
	"strings"

	"github.com/maiphucgiang/gpt2api/internal/bps"
	"github.com/maiphucgiang/gpt2api/internal/protocol"
)

func bpsMessage(role string, content any) (bps.Object, error) {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	parts := []any{}
	switch value := content.(type) {
	case string:
		if value != "" {
			parts = append(parts, bps.Object{"type": kind, "text": value})
		}
	case nil:
	case []any:
		for _, raw := range value {
			part, _ := raw.(map[string]any)
			if stringValue(part["type"]) != "text" && stringValue(part["type"]) != "input_text" && stringValue(part["type"]) != "output_text" {
				return nil, errors.New("BPS chat currently supports text content only")
			}
			text, ok := part["text"].(string)
			if !ok {
				return nil, errors.New("message text must be a string")
			}
			parts = append(parts, bps.Object{"type": kind, "text": text})
		}
	default:
		return nil, errors.New("invalid message content")
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return bps.Object{"type": "message", "role": role, "content": parts}, nil
}

func bpsChatRequest(request protocol.ChatRequest) (bps.Object, error) {
	source := bps.Object{"model": request.Model, "reasoning_effort": request.ReasoningEffort, "tool_choice": request.ToolChoice}
	if request.ParallelToolCalls != nil {
		source["parallel_tool_calls"] = *request.ParallelToolCalls
	}
	if request.PromptCacheKey != "" {
		source["prompt_cache_key"] = request.PromptCacheKey
	}
	if request.MaxTokens != nil {
		source["max_output_tokens"] = *request.MaxTokens
	}
	if request.Temperature != nil {
		source["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		source["top_p"] = *request.TopP
	}
	tools := []any{}
	for _, raw := range request.Tools {
		typ := bps.String(raw["type"])
		spec, _ := raw[typ].(map[string]any)
		if spec == nil {
			return nil, errors.New("Chat tools require a function or custom definition")
		}
		copy := cloneMap(spec)
		copy["type"] = typ
		tools = append(tools, copy)
	}
	source["tools"] = tools
	if choice, ok := source["tool_choice"].(map[string]any); ok {
		typ := stringValue(choice["type"])
		definition, _ := choice[typ].(map[string]any)
		source["tool_choice"] = bps.Object{"type": typ, "name": definition["name"]}
	}
	input := []any{}
	for _, msg := range request.Messages {
		if msg.Role == "tool" {
			text, ok := msg.Content.(string)
			if !ok || msg.ToolCallID == "" {
				return nil, errors.New("tool messages require tool_call_id and text content")
			}
			input = append(input, bps.Object{"type": "function_call_output", "call_id": msg.ToolCallID, "output": text})
			continue
		}
		item, err := bpsMessage(msg.Role, msg.Content)
		if err != nil {
			return nil, err
		}
		if item != nil {
			input = append(input, item)
		}
		for _, raw := range msg.ToolCalls {
			call, _ := raw.(map[string]any)
			typ := bps.String(call["type"])
			definition, _ := call[typ].(map[string]any)
			if msg.Role != "assistant" || (typ != "function" && typ != "custom") || definition == nil {
				return nil, errors.New("invalid historical Chat tool call")
			}
			kind, field := "function_call", "arguments"
			if typ == "custom" {
				kind, field = "custom_tool_call", "input"
			}
			input = append(input, bps.Object{"type": kind, "call_id": call["id"], "name": definition["name"], field: definition[field]})
		}
	}
	source["input"] = input
	return source, nil
}

func bpsMessagesRequest(request bps.Object) (bps.Object, error) {
	source := bps.Object{"model": request["model"], "stream": request["stream"], "instructions": anthropicText(request["system"])}
	for from, to := range map[string]string{"max_tokens": "max_output_tokens", "temperature": "temperature", "top_p": "top_p"} {
		if value, ok := request[from]; ok && value != nil {
			source[to] = value
		}
	}
	tools := []any{}
	for _, raw := range bpsArray(request["tools"]) {
		spec := bpsObject(raw)
		tools = append(tools, bps.Object{"type": "function", "name": spec["name"], "description": spec["description"], "parameters": spec["input_schema"]})
	}
	source["tools"] = tools
	choice := bpsObject(request["tool_choice"])
	switch bps.String(choice["type"]) {
	case "", "auto":
		source["tool_choice"] = "auto"
	case "none":
		source["tool_choice"] = "none"
	case "any":
		source["tool_choice"] = "required"
	case "tool":
		source["tool_choice"] = bps.Object{"type": "function", "name": choice["name"]}
	default:
		return nil, errors.New("unsupported Messages tool_choice")
	}
	if choice["disable_parallel_tool_use"] == true {
		source["parallel_tool_calls"] = false
	}
	input := []any{}
	for _, raw := range bpsArray(request["messages"]) {
		msg := bpsObject(raw)
		role := bps.String(msg["role"])
		if role != "user" && role != "assistant" {
			return nil, errors.New("Messages role must be user or assistant")
		}
		if text, ok := msg["content"].(string); ok {
			item, _ := bpsMessage(role, text)
			if item != nil {
				input = append(input, item)
			}
			continue
		}
		for _, rawPart := range bpsArray(msg["content"]) {
			part := bpsObject(rawPart)
			switch bps.String(part["type"]) {
			case "text":
				item, err := bpsMessage(role, part["text"])
				if err != nil {
					return nil, err
				}
				if item != nil {
					input = append(input, item)
				}
			case "tool_use":
				if role != "assistant" {
					return nil, errors.New("tool_use must belong to assistant")
				}
				input = append(input, bps.Object{"type": "function_call", "call_id": part["id"], "name": part["name"], "arguments": bpsJSON(part["input"])})
			case "tool_result":
				if role != "user" {
					return nil, errors.New("tool_result must belong to user")
				}
				output := anthropicText(part["content"])
				if part["is_error"] == true {
					output = "Tool reported an error: " + output
				}
				input = append(input, bps.Object{"type": "function_call_output", "call_id": part["tool_use_id"], "output": output})
			default:
				return nil, errors.New("unsupported Messages content block for BPS")
			}
		}
	}
	source["input"] = input
	return source, nil
}

func bpsArray(value any) []any       { list, _ := value.([]any); return list }
func bpsObject(value any) bps.Object { object, _ := value.(map[string]any); return object }
func bpsUsage(response bps.Object) bps.Object {
	usage := bpsObject(response["usage"])
	input, output := usage["input_tokens"], usage["output_tokens"]
	total := usage["total_tokens"]
	if total == nil {
		total = intValue(input) + intValue(output)
	}
	return bps.Object{"prompt_tokens": input, "completion_tokens": output, "total_tokens": total, "prompt_tokens_details": usage["input_tokens_details"], "completion_tokens_details": usage["output_tokens_details"]}
}
func bpsCompletion(response bps.Object, format string) bps.Object {
	if format == "responses" {
		return response
	}
	text, thinking := strings.Builder{}, strings.Builder{}
	calls, content := []any{}, []any{}
	for _, raw := range bpsArray(response["output"]) {
		item := bpsObject(raw)
		name := bps.String(item["name"])
		if ns := bps.String(item["namespace"]); ns != "" {
			name = ns + "." + name
		}
		switch bps.String(item["type"]) {
		case "message":
			for _, rawPart := range bpsArray(item["content"]) {
				part := bpsObject(rawPart)
				if bps.String(part["type"]) == "output_text" {
					value := bps.String(part["text"])
					text.WriteString(value)
					content = append(content, bps.Object{"type": "text", "text": value})
				}
			}
		case "reasoning":
			for _, rawPart := range bpsArray(item["summary"]) {
				value := bps.String(bpsObject(rawPart)["text"])
				thinking.WriteString(value)
			}
		case "function_call":
			calls = append(calls, bps.Object{"id": item["call_id"], "type": "function", "function": bps.Object{"name": name, "arguments": item["arguments"]}})
			var args any
			_ = bps.Decode([]byte(bps.String(item["arguments"])), &args)
			content = append(content, bps.Object{"type": "tool_use", "id": item["call_id"], "name": name, "input": args})
		case "custom_tool_call":
			calls = append(calls, bps.Object{"id": item["call_id"], "type": "custom", "custom": bps.Object{"name": name, "input": item["input"]}})
		}
	}
	finish, stop := "stop", "end_turn"
	if len(calls) > 0 {
		finish, stop = "tool_calls", "tool_use"
	}
	if bps.String(response["status"]) == "incomplete" {
		finish, stop = "length", "max_tokens"
	}
	if format == "messages" {
		usage := bpsObject(response["usage"])
		return bps.Object{"id": response["id"], "type": "message", "role": "assistant", "model": response["model"], "content": content, "stop_reason": stop, "stop_sequence": nil, "usage": bps.Object{"input_tokens": usage["input_tokens"], "output_tokens": usage["output_tokens"]}}
	}
	msg := bps.Object{"role": "assistant", "content": text.String()}
	if thinking.Len() > 0 {
		msg["reasoning_content"] = thinking.String()
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	return bps.Object{"id": response["id"], "object": "chat.completion", "created": response["created_at"], "model": response["model"], "choices": []any{bps.Object{"index": 0, "message": msg, "finish_reason": finish}}, "usage": bpsUsage(response)}
}
