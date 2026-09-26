package bps

import (
	"fmt"
	"strconv"
	"strings"
)

type streamState struct {
	p            *Prepared
	replay       *Replay
	emit         func(Object) error
	sequence     int
	base         Object
	text         map[string]string
	done         map[string]bool
	added, parts map[string]bool
	indices      map[int]int
	itemIDs      map[int]string
}

func (s *streamState) send(event Object) error {
	event = clone(event)
	if raw, ok := event["output_index"]; ok {
		native, err := strconv.Atoi(fmt.Sprint(raw))
		if err != nil || native < 0 {
			return fail(502, "invalid_bps_event", "invalid output index")
		}
		if s.indices == nil {
			s.indices = map[int]int{}
			s.itemIDs = map[int]string{}
		}
		index, exists := s.indices[native]
		if String(event["type"]) == "response.output_item.added" {
			if exists {
				return fail(502, "invalid_bps_event", "duplicate output item")
			}
			index = len(s.indices)
			s.indices[native] = index
			s.itemIDs[native] = String(object(event["item"])["id"])
		} else if !exists {
			return fail(502, "invalid_bps_event", "BPS emitted a delta before its output item")
		}
		if id := String(event["item_id"]); id != "" && id != s.itemIDs[native] {
			return fail(502, "invalid_bps_event", "output item identity changed")
		}
		event["output_index"] = index
	}
	event["sequence_number"] = s.sequence
	s.sequence++
	if s.emit != nil {
		return s.emit(event)
	}
	return nil
}

func (s *streamState) progress() Object {
	out := clone(s.base)
	out["status"] = "in_progress"
	out["output"] = []any{}
	return out
}
func toolItem(item Object) bool {
	kind := String(item["type"])
	return kind == "function_call" || kind == "custom_tool_call"
}
func textKey(id string, index any) string { return id + ":" + fmt.Sprint(index) }

func (s *streamState) event(event Object) (Object, bool, error) {
	if s.added == nil {
		s.added = map[string]bool{}
		s.parts = map[string]bool{}
	}
	kind := String(event["type"])
	if kind == "response.created" || kind == "response.in_progress" {
		response := object(event["response"])
		if String(response["id"]) == "" {
			return nil, false, fail(502, "invalid_bps_event", "response identity is missing")
		}
		if s.base != nil && String(s.base["id"]) != String(response["id"]) {
			return nil, false, fail(502, "invalid_bps_event", "response identity changed")
		}
		if actual := String(response["model"]); actual != "" && actual != s.p.UpstreamModel {
			return nil, false, fail(502, "bps_model_mismatch", "BPS returned a different model than requested")
		}
		s.base = clone(response)
		s.base["model"] = s.p.Model
		return nil, false, s.send(Object{"type": kind, "response": s.progress()})
	}
	if s.base == nil {
		return nil, false, fail(502, "invalid_bps_event", "BPS emitted output before response.created")
	}
	switch kind {
	case "response.failed", "error":
		return nil, false, fail(502, "bps_response_failed", "BPS reported a failed response")
	case "response.completed", "response.incomplete":
		response := object(event["response"])
		if String(response["id"]) != String(s.base["id"]) || String(response["status"]) != strings.TrimPrefix(kind, "response.") {
			return nil, false, fail(502, "invalid_bps_event", "terminal response identity or status mismatch")
		}
		if actual := String(response["model"]); actual != "" && actual != s.p.UpstreamModel {
			return nil, false, fail(502, "bps_model_mismatch", "BPS returned a different model than requested")
		}
		out, natives, clients, err := s.p.Tools.convertOutput(response)
		if err != nil {
			return nil, false, err
		}
		out["model"] = s.p.Model
		if kind == "response.incomplete" && len(clients) > 0 {
			return nil, false, fail(502, "incomplete_tool_response", "BPS stopped before completing the tool response")
		}
		// Validate the whole batch before releasing any executable call.
		for _, raw := range list(out["output"]) {
			if item := object(raw); String(item["type"]) == "message" {
				if err = s.checkMessage(item); err != nil {
					return nil, false, err
				}
			}
		}
		items := list(out["output"])
		for index, id := range s.itemIDs {
			if index >= len(items) || String(object(items[index])["id"]) != id {
				return nil, false, fail(502, "invalid_bps_event", "terminal response omitted or changed a streamed item")
			}
		}
		if err = s.replay.save(s.p.Owner, s.p.Model, s.p.Account, natives, clients); err != nil {
			return nil, false, err
		}
		for index, raw := range list(out["output"]) {
			item := object(raw)
			if toolItem(item) {
				err = s.emitTool(index, item)
			} else if String(item["type"]) == "message" {
				err = s.finishMessage(index, item)
			}
			if _, exists := s.indices[index]; !exists && err == nil {
				err = s.send(Object{"type": "response.output_item.added", "output_index": index, "item": item})
				if err == nil {
					err = s.send(Object{"type": "response.output_item.done", "output_index": index, "item": item})
				}
			}
			if err != nil {
				return nil, false, err
			}
		}
		// Text may precede a validated tool batch in the public stream; keep the final list consistent.
		ordered := make([]any, len(items))
		for index, item := range items {
			ordered[s.indices[index]] = item
		}
		out["output"] = ordered
		if err = s.send(Object{"type": kind, "response": out}); err != nil {
			return nil, false, err
		}
		return out, true, nil
	case "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		return nil, false, nil
	case "response.output_item.added", "response.output_item.done":
		item := object(event["item"])
		if toolItem(item) {
			return nil, false, nil
		}
		switch String(item["type"]) {
		case "message":
			if kind == "response.output_item.done" {
				index, _ := strconv.Atoi(fmt.Sprint(event["output_index"]))
				return nil, false, s.finishMessage(index, item)
			}
			s.added[String(item["id"])] = true
		case "reasoning", "compaction":
		default:
			return nil, false, fail(502, "unsupported_output", "BPS returned an unsupported server tool")
		}
	case "response.output_text.delta":
		delta, ok := event["delta"].(string)
		if !ok {
			return nil, false, fail(502, "invalid_bps_event", "text delta must be a string")
		}
		s.text[textKey(String(event["item_id"]), event["content_index"])] += delta
	case "response.output_text.done":
		// Item completion supplies a complete, ordered content list below.
		return nil, false, nil
	case "response.content_part.added":
		s.parts[textKey(String(event["item_id"]), event["content_index"])] = true
	case "response.content_part.done":
		return nil, false, nil
	}
	return nil, false, s.send(event)
}

func (s *streamState) emitTool(index int, item Object) error {
	field, eventName := "arguments", "response.function_call_arguments"
	if String(item["type"]) == "custom_tool_call" {
		field, eventName = "input", "response.custom_tool_call_input"
	}
	added := clone(item)
	added[field] = ""
	added["status"] = "in_progress"
	if err := s.send(Object{"type": "response.output_item.added", "output_index": index, "item": added}); err != nil {
		return err
	}
	if err := s.send(Object{"type": eventName + ".delta", "output_index": index, "item_id": item["id"], "delta": item[field]}); err != nil {
		return err
	}
	if err := s.send(Object{"type": eventName + ".done", "output_index": index, "item_id": item["id"], field: item[field]}); err != nil {
		return err
	}
	return s.send(Object{"type": "response.output_item.done", "output_index": index, "item": item})
}

func (s *streamState) checkMessage(item Object) error {
	id := String(item["id"])
	for index, raw := range list(item["content"]) {
		part := object(raw)
		if String(part["type"]) != "output_text" {
			continue
		}
		text := String(part["text"])
		seen := s.text[textKey(id, index)]
		if !strings.HasPrefix(text, seen) || (s.done[id] && text != seen) {
			return fail(502, "invalid_bps_event", "terminal text contradicts streamed content")
		}
	}
	return nil
}

func (s *streamState) finishMessage(index int, item Object) error {
	if err := s.checkMessage(item); err != nil {
		return err
	}
	id := String(item["id"])
	if s.done[id] {
		return nil
	}
	if !s.added[id] {
		added := clone(item)
		added["content"] = []any{}
		added["status"] = "in_progress"
		if err := s.send(Object{"type": "response.output_item.added", "output_index": index, "item": added}); err != nil {
			return err
		}
		s.added[id] = true
	}
	for ci, raw := range list(item["content"]) {
		part := object(raw)
		key := textKey(id, ci)
		if !s.parts[key] {
			added := clone(part)
			if String(part["type"]) == "output_text" {
				added["text"] = ""
			}
			if err := s.send(Object{"type": "response.content_part.added", "output_index": index, "item_id": id, "content_index": ci, "part": added}); err != nil {
				return err
			}
			s.parts[key] = true
		}
		if String(part["type"]) == "output_text" {
			text := String(part["text"])
			if rest := strings.TrimPrefix(text, s.text[key]); rest != "" {
				if err := s.send(Object{"type": "response.output_text.delta", "output_index": index, "item_id": id, "content_index": ci, "delta": rest}); err != nil {
					return err
				}
			}
			s.text[key] = text
			if err := s.send(Object{"type": "response.output_text.done", "output_index": index, "item_id": id, "content_index": ci, "text": text}); err != nil {
				return err
			}
		}
		if err := s.send(Object{"type": "response.content_part.done", "output_index": index, "item_id": id, "content_index": ci, "part": part}); err != nil {
			return err
		}
	}
	s.done[id] = true
	return s.send(Object{"type": "response.output_item.done", "output_index": index, "item": item})
}
