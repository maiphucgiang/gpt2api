package bps

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/maiphucgiang/gpt2api/internal/model"
)

type Prepared struct {
	Source                Object
	Tools                 *Catalog
	Owner, Model, Account string
	UpstreamModel         string
	Body                  Object
}

func Validate(source Object) (*Catalog, error) {
	for field, upper := range map[string]float64{"temperature": 2, "top_p": 1, "max_output_tokens": 1000000} {
		if value, ok := source[field]; ok && value != nil {
			n, err := strconv.ParseFloat(fmt.Sprint(value), 64)
			if _, text := value.(string); text || err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > upper || (field == "max_output_tokens" && (n < 1 || math.Trunc(n) != n)) {
				return nil, fail(400, "invalid_request", "invalid "+field)
			}
		}
	}
	if source["store"] == true || source["background"] == true || source["previous_response_id"] != nil || source["conversation"] != nil {
		return nil, fail(400, "unsupported_continuation", "BPS requires full input history and store:false; stored/background responses are unsupported")
	}
	if tier := String(source["service_tier"]); tier != "" && tier != "auto" && tier != "default" {
		return nil, fail(400, "unsupported_service_tier", "BPS supports only the default service tier")
	}
	if format := object(object(source["text"])["format"]); len(format) > 0 && String(format["type"]) != "text" {
		return nil, fail(400, "unsupported_text_format", "BPS structured output formats are unsupported")
	}
	for _, field := range []string{"stream", "parallel_tool_calls", "store", "background"} {
		if value, ok := source[field]; ok && value != nil {
			if _, ok := value.(bool); !ok {
				return nil, fail(400, "invalid_request", field+" must be boolean")
			}
		}
	}
	effort := String(object(source["reasoning"])["effort"])
	if effort == "" {
		effort = String(source["reasoning_effort"])
	}
	switch effort {
	case "", "low", "medium", "high", "xhigh":
	default:
		return nil, fail(400, "unsupported_reasoning", "BPS reasoning effort must be low, medium, high or xhigh")
	}
	switch input := source["input"].(type) {
	case string:
		if strings.TrimSpace(input) == "" {
			return nil, fail(400, "invalid_input", "input cannot be empty")
		}
	case []any:
		if len(input) == 0 {
			return nil, fail(400, "invalid_input", "input cannot be empty")
		}
	default:
		return nil, fail(400, "invalid_input", "input must be text or an item array")
	}
	return NewCatalog(source)
}

func message(role, text string) Object {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	return Object{"type": "message", "role": role, "content": []any{Object{"type": kind, "text": text}}}
}

func Prepare(source Object, owner, account string, tools *Catalog, replay *Replay) (*Prepared, error) {
	publicModel := String(source["model"])
	route, ok := model.ResolveChat(publicModel)
	if !ok || !route.BPS {
		return nil, fail(400, "invalid_model", "BPS requires a supported bsg/ model ID")
	}
	if owner == "" || account == "" {
		return nil, fail(400, "missing_identity", "BPS requires caller and account identities")
	}
	rawInput := list(source["input"])
	if text, ok := source["input"].(string); ok {
		rawInput = []any{message("user", text)}
	}
	normalized := []any{}
	calls := map[string]bool{}
	results := map[string]bool{}
	for _, raw := range rawInput {
		original := object(raw)
		if original == nil {
			return nil, fail(400, "invalid_input", "input items must be objects")
		}
		item := clone(original)
		delete(item, "internal_chat_message_metadata_passthrough")
		kind := String(item["type"])
		switch kind {
		case "", "message":
			role := String(item["role"])
			if role == "" {
				role = "user"
			}
			switch role {
			case "user", "assistant", "developer", "system":
			default:
				return nil, fail(400, "invalid_input", "unsupported message role")
			}
			if text, ok := item["content"].(string); ok {
				item = message(role, text)
			} else {
				if len(list(item["content"])) == 0 {
					return nil, fail(400, "invalid_input", "message content cannot be empty")
				}
				for _, rawPart := range list(item["content"]) {
					part := object(rawPart)
					typ := String(part["type"])
					if typ != "input_text" && typ != "output_text" {
						return nil, fail(400, "unsupported_input", "BPS currently accepts text, reasoning and tool history; image/file attachments require separate upload support")
					}
					if _, ok := part["text"].(string); !ok {
						return nil, fail(400, "invalid_input", "text content must be a string")
					}
				}
				item["type"] = "message"
				item["role"] = role
			}
			normalized = append(normalized, item)
		case "function_call", "custom_tool_call":
			call := String(item["call_id"])
			entry, ok := replay.get(owner, publicModel, call)
			if !ok || entry.Account != account {
				return nil, fail(409, "tool_history_missing", "tool continuation requires the original account and retained history")
			}
			if calls[call] || fullToolName(item) != fullToolName(entry.Client) {
				return nil, fail(400, "invalid_tool_history", "duplicate or changed historical tool call")
			}
			field := "arguments"
			if kind == "custom_tool_call" {
				field = "input"
			}
			expected, actual := entry.Client[field], item[field]
			if field == "arguments" {
				var a, b any
				if Decode([]byte(String(expected)), &a) != nil || Decode([]byte(String(actual)), &b) != nil || string(encode(a)) != string(encode(b)) {
					return nil, fail(400, "invalid_tool_history", "historical tool arguments changed")
				}
			} else if String(expected) != String(actual) {
				return nil, fail(400, "invalid_tool_history", "historical custom input changed")
			}
			calls[call] = true
			normalized = append(normalized, entry.Native)
		case "function_call_output", "custom_tool_call_output":
			call := String(item["call_id"])
			entry, ok := replay.get(owner, publicModel, call)
			if !ok || entry.Account != account || !calls[call] || results[call] {
				return nil, fail(400, "invalid_tool_history", "tool result must follow exactly one matching call in the full history")
			}
			output, ok := item["output"].(string)
			if !ok {
				return nil, fail(400, "invalid_tool_output", "tool output must be text")
			}
			if String(entry.Native["name"]) == "update_plan" && output == "Plan updated" {
				output = `{"status":"ok"}`
			}
			normalized = append(normalized, Object{"type": "function_call_output", "id": entry.Native["id"], "call_id": call, "output": output})
			results[call] = true
		case "reasoning":
			if encrypted := String(item["encrypted_content"]); encrypted != "" {
				normalized = append(normalized, Object{"type": "reasoning", "summary": []any{}, "encrypted_content": encrypted})
			}
		case "additional_tools":
		case "compaction":
			if String(item["encrypted_content"]) == "" {
				return nil, fail(400, "invalid_compaction", "compaction history must contain encrypted_content")
			}
			normalized = append(normalized, item)
		default:
			return nil, fail(400, "unsupported_input", fmt.Sprintf("unsupported BPS input item type %q", kind))
		}
	}
	if len(normalized) == 0 {
		return nil, fail(400, "invalid_input", "input contains no usable history")
	}
	for call := range calls {
		if !results[call] {
			return nil, fail(400, "missing_tool_result", "full history must contain results for previous tool calls")
		}
	}
	// Stable prologue keeps the growing conversation eligible for prompt caching.
	input := []any{}
	if instructions := String(source["instructions"]); instructions != "" {
		input = append(input, message("developer", instructions))
	}
	input = append(input, message("developer", tools.instructions()))
	input = append(input, normalized...)
	effort := String(object(source["reasoning"])["effort"])
	if effort == "" {
		effort = String(source["reasoning_effort"])
	}
	if effort == "" {
		effort = "medium"
	}
	body := Object{"model": route.UpstreamModel, "model_selection": "explicit", "stream": true, "store": false, "input": input, "reasoning_effort": effort}
	if value := source["context_management"]; value != nil && len(list(value)) > 0 {
		body["context_management"] = value
	}
	scope := hash(owner, account, publicModel, conversationIdentity(source))
	lastUser := -1
	for i, raw := range normalized {
		if String(object(raw)["role"]) == "user" {
			lastUser = i
		}
	}
	prefix := normalized[:1]
	if lastUser >= 0 {
		prefix = normalized[:lastUser+1]
	}
	rounds := 1
	inResults := false
	for _, raw := range normalized[lastUser+1:] {
		isResult := String(object(raw)["type"]) == "function_call_output"
		if isResult && !inResults {
			rounds++
		}
		inResults = isResult
	}
	body["metadata"] = Object{"task_id": uuid(scope), "turn_id": uuid(scope, string(encode(prefix))), "agent_iteration": fmt.Sprint(rounds)}
	body["prompt_cache_key"] = scope
	return &Prepared{Source: source, Tools: tools, Owner: owner, Model: publicModel, UpstreamModel: route.UpstreamModel, Account: account, Body: body}, nil
}
