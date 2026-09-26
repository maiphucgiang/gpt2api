package bps

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)

type tool struct {
	Name, Namespace, Kind string
	Spec                  Object
	Schema                *jsonschema.Schema
}
type Catalog struct {
	all, allowed       map[string]tool
	Required, Parallel bool
}
type localSchemas struct{}

func (localSchemas) Load(string) (any, error) {
	return nil, errors.New("external schema resources are disabled")
}

func NewCatalog(source Object) (*Catalog, error) {
	c := &Catalog{all: map[string]tool{}, allowed: map[string]tool{}, Parallel: source["parallel_tool_calls"] != false}
	var add func([]any, string, int) error
	add = func(values []any, namespace string, depth int) error {
		if depth > 8 {
			return fail(400, "invalid_tools", "tool namespace nesting exceeds limit")
		}
		for _, raw := range values {
			spec := object(raw)
			name, kind := String(spec["name"]), String(spec["type"])
			if !toolName.MatchString(name) {
				return fail(400, "invalid_tools", "tool name is missing or invalid")
			}
			full := name
			if namespace != "" {
				full = namespace + "." + name
			}
			if kind == "namespace" {
				if err := add(list(spec["tools"]), full, depth+1); err != nil {
					return err
				}
				continue
			}
			if kind != "function" && kind != "custom" {
				return fail(400, "unsupported_tool", "BPS supports client function/custom tools, not built-in tools")
			}
			if full == "run_officejs" || full == "functions.run_officejs" {
				return fail(400, "reserved_tool_name", "run_officejs is reserved for the upstream transport")
			}
			if len(c.all) >= 128 {
				return fail(400, "too_many_tools", "at most 128 client tools are supported")
			}
			if _, exists := c.all[full]; exists {
				return fail(400, "duplicate_tool", "duplicate client tool name")
			}
			t := tool{Name: name, Namespace: namespace, Kind: kind, Spec: clone(spec)}
			if kind == "function" {
				schema := object(spec["parameters"])
				if spec["parameters"] != nil && schema == nil {
					return fail(400, "invalid_tool_schema", "function parameters must be a schema object")
				}
				if schema == nil {
					schema = Object{"type": "object"}
				}
				compiler := jsonschema.NewCompiler()
				compiler.UseLoader(localSchemas{})
				location := "https://gpt2api.invalid/tool.json"
				if err := compiler.AddResource(location, schema); err != nil {
					return fail(400, "invalid_tool_schema", "invalid client tool JSON Schema")
				}
				compiled, err := compiler.Compile(location)
				if err != nil {
					return fail(400, "invalid_tool_schema", "tool schema is invalid or references an external resource")
				}
				t.Schema = compiled
			} else if format := object(spec["format"]); len(format) > 0 {
				switch String(format["type"]) {
				case "text":
				case "grammar":
					grammar := object(format["grammar"])
					if String(grammar["definition"]) == "" || (String(grammar["syntax"]) != "lark" && String(grammar["syntax"]) != "regex") {
						return fail(400, "invalid_tool_format", "custom grammar needs a syntax and definition")
					}
				default:
					return fail(400, "invalid_tool_format", "unsupported custom tool format")
				}
			}
			c.all[full] = t
		}
		return nil
	}
	if raw, exists := source["tools"]; exists && raw != nil {
		if _, ok := raw.([]any); !ok {
			return nil, fail(400, "invalid_tools", "tools must be an array")
		}
	}
	if err := add(list(source["tools"]), "", 0); err != nil {
		return nil, err
	}
	for _, raw := range list(source["input"]) {
		item := object(raw)
		if String(item["type"]) == "additional_tools" {
			if err := add(list(item["tools"]), "", 0); err != nil {
				return nil, err
			}
		}
	}
	for key, t := range c.all {
		c.allowed[key] = t
	}
	switch choice := source["tool_choice"].(type) {
	case nil:
	case string:
		switch choice {
		case "none":
			c.allowed = map[string]tool{}
		case "auto":
		case "required":
			c.Required = true
		default:
			return nil, fail(400, "invalid_tool_choice", "unsupported tool_choice")
		}
	case map[string]any:
		c.allowed = map[string]tool{}
		selectTool := func(value Object) error {
			name := String(value["name"])
			if ns := String(value["namespace"]); ns != "" {
				name = ns + "." + name
			}
			t, ok := c.all[name]
			if !ok || t.Kind != String(value["type"]) {
				return fail(400, "invalid_tool_choice", "tool_choice must select a declared tool")
			}
			c.allowed[name] = t
			return nil
		}
		if String(choice["type"]) == "allowed_tools" {
			mode := String(choice["mode"])
			if mode != "auto" && mode != "required" {
				return nil, fail(400, "invalid_tool_choice", "invalid allowed_tools mode")
			}
			c.Required = mode == "required"
			for _, raw := range list(choice["tools"]) {
				if err := selectTool(object(raw)); err != nil {
					return nil, err
				}
			}
		} else {
			c.Required = true
			if err := selectTool(choice); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fail(400, "invalid_tool_choice", "invalid tool_choice")
	}
	if c.Required && len(c.allowed) == 0 {
		return nil, fail(400, "invalid_tool_choice", "required tool_choice has no callable tools")
	}
	return c, nil
}

func (c *Catalog) instructions() string {
	if len(c.allowed) == 0 {
		return "This is an external API conversation. Answer as assistant text. Do not invoke Office, workbook, connector, or other server tools."
	}
	names := make([]string, 0, len(c.allowed))
	for name := range c.allowed {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]any, 0, len(names))
	for _, name := range names {
		spec := clone(c.allowed[name].Spec)
		spec["name"] = name
		specs = append(specs, spec)
	}
	text := "The caller is an external API client, not an Office workbook. Only the tools in CLIENT_TOOLS are available. Return client tool requests using the native run_officejs transport; this gateway never executes its code. For each call set references to exactly [the full client tool name]. Set code to the function's JSON argument object serialized once, or to the custom tool's exact input text. Do not wrap code in another tool envelope or JavaScript, and do not call other native tools. Treat subsequent results as results of that client tool; never repeat a call already answered in the history. CLIENT_TOOLS=" + string(encode(specs))
	if c.Required {
		text += " This turn must invoke a selected client tool."
	}
	if !c.Parallel {
		text += " Invoke at most one tool in this response."
	}
	return text
}

func (c *Catalog) convert(native Object) (Object, error) {
	name := String(native["name"])
	kind := String(native["type"])
	call := String(native["call_id"])
	if call == "" || len(call) > 512 || len(encode(native)) > 1<<20 {
		return nil, fail(502, "invalid_tool_call", "upstream tool identity or payload is invalid")
	}
	var payload any
	if name == "run_officejs" || name == "functions.run_officejs" {
		if kind != "function_call" {
			return nil, fail(502, "invalid_tool_call", "invalid tool transport type")
		}
		var outer Object
		if Decode([]byte(String(native["arguments"])), &outer) != nil || outer == nil {
			return nil, fail(502, "invalid_tool_call", "invalid tool transport JSON")
		}
		code, ok := outer["code"].(string)
		if !ok {
			return nil, fail(502, "invalid_tool_call", "tool transport code must be text")
		}
		refs := list(outer["references"])
		if len(refs) == 1 {
			name = String(refs[0])
			payload = code
		} else if len(refs) == 0 {
			// Accept the alternate documented envelope, without repairing its bytes.
			var inner Object
			if Decode([]byte(code), &inner) != nil {
				return nil, fail(502, "invalid_tool_call", "invalid client tool envelope")
			}
			name = String(inner["name"])
			if t, ok := c.allowed[name]; ok && t.Kind == "custom" {
				payload = inner["input"]
			} else {
				payload = inner["arguments"]
			}
		} else {
			return nil, fail(502, "invalid_tool_call", "a transport call must select exactly one client tool")
		}
	} else {
		if ns := String(native["namespace"]); ns != "" {
			name = ns + "." + name
		}
		if kind == "custom_tool_call" {
			payload = native["input"]
		} else if kind == "function_call" {
			payload = native["arguments"]
		} else {
			return nil, fail(502, "invalid_tool_call", "unsupported upstream tool type")
		}
	}
	t, ok := c.allowed[name]
	if !ok {
		return nil, fail(502, "undeclared_tool", "upstream requested a tool not allowed by this request")
	}
	out := Object{"type": t.Kind + "_call", "id": native["id"], "call_id": call, "name": t.Name, "status": "completed"}
	if t.Kind == "custom" {
		out["type"] = "custom_tool_call"
	}
	if t.Namespace != "" {
		out["namespace"] = t.Namespace
	}
	if String(out["id"]) == "" {
		out["id"] = "fc_" + hash(call)[:24]
	}
	if t.Kind == "function" {
		if text, ok := payload.(string); ok {
			if Decode([]byte(text), &payload) != nil {
				return nil, fail(502, "invalid_tool_arguments", "tool arguments are not valid JSON")
			}
		}
		if object(payload) == nil || t.Schema.Validate(payload) != nil {
			return nil, fail(502, "invalid_tool_arguments", "tool arguments do not match the declared schema")
		}
		out["arguments"] = string(encode(payload))
	} else {
		text, ok := payload.(string)
		if !ok {
			return nil, fail(502, "invalid_tool_arguments", "custom tool input must be text")
		}
		out["input"] = text
	}
	return out, nil
}

func (c *Catalog) convertOutput(response Object) (Object, []Object, []Object, error) {
	out := clone(response)
	items := []any{}
	natives, clients := []Object{}, []Object{}
	seen := map[string]bool{}
	for _, raw := range list(response["output"]) {
		item := object(raw)
		kind := String(item["type"])
		switch kind {
		case "function_call", "custom_tool_call":
			converted, err := c.convert(item)
			if err != nil {
				return nil, nil, nil, err
			}
			call := String(converted["call_id"])
			if seen[call] {
				return nil, nil, nil, fail(502, "duplicate_tool_call", "duplicate upstream tool call ID")
			}
			seen[call] = true
			natives = append(natives, item)
			clients = append(clients, converted)
			items = append(items, converted)
		case "message", "reasoning", "compaction":
			items = append(items, item)
		default:
			return nil, nil, nil, fail(502, "unsupported_output", fmt.Sprintf("unsupported BPS output type %q", kind))
		}
	}
	if c.Required && len(clients) == 0 && String(response["status"]) == "completed" {
		return nil, nil, nil, fail(502, "required_tool_missing", "upstream did not produce the required tool call")
	}
	if !c.Parallel && len(clients) > 1 {
		return nil, nil, nil, fail(502, "parallel_tools_forbidden", "upstream returned multiple tools when parallel_tool_calls is false")
	}
	out["output"] = items
	return out, natives, clients, nil
}

func fullToolName(item Object) string {
	name := String(item["name"])
	if ns := String(item["namespace"]); ns != "" {
		name = ns + "." + name
	}
	return strings.TrimSpace(name)
}
