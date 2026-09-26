package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/maiphucgiang/gpt2api/internal/bps"
)

type bpsOutput struct {
	w                               http.ResponseWriter
	source                          bps.Object
	format, id                      string
	created                         any
	started, terminal               bool
	sequence, toolIndex, blockIndex int
	textBlock                       int
	cancel                          context.CancelFunc
}

func newBPSOutput(w http.ResponseWriter, source bps.Object, format string, cancel context.CancelFunc) *bpsOutput {
	return &bpsOutput{w: w, source: source, format: format, cancel: cancel, textBlock: -1}
}
func (s *bpsOutput) write(event string, value any) error {
	if !s.started {
		s.w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		s.w.Header().Set("Cache-Control", "no-cache")
		s.w.Header().Set("X-Accel-Buffering", "no")
		s.started = true
	}
	raw, err := json.Marshal(value)
	if err == nil {
		if event == "" {
			_, err = fmt.Fprintf(s.w, "data: %s\n\n", raw)
		} else {
			_, err = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, raw)
		}
	}
	if err != nil {
		s.cancel()
		return err
	}
	if flusher, ok := s.w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
func (s *bpsOutput) chunk(delta bps.Object, reason any, usage any) error {
	value := bps.Object{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.source["model"], "choices": []any{bps.Object{"index": 0, "delta": delta, "finish_reason": reason}}}
	if usage != nil {
		value["usage"] = usage
	}
	return s.write("", value)
}
func (s *bpsOutput) stopText() error {
	if s.textBlock < 0 {
		return nil
	}
	index := s.textBlock
	s.textBlock = -1
	return s.write("content_block_stop", bps.Object{"type": "content_block_stop", "index": index})
}
func (s *bpsOutput) event(event bps.Object) error {
	kind := bps.String(event["type"])
	s.sequence = intValue(event["sequence_number"]) + 1
	if kind == "response.created" {
		response := bpsObject(event["response"])
		s.id = bps.String(response["id"])
		s.created = response["created_at"]
	}
	if s.format == "responses" {
		err := s.write(kind, event)
		if err == nil && (kind == "response.completed" || kind == "response.incomplete") {
			s.terminal = true
		}
		return err
	}
	if kind == "response.created" {
		if s.format == "chat" {
			return s.chunk(bps.Object{"role": "assistant", "content": ""}, nil, nil)
		}
		return s.write("message_start", bps.Object{"type": "message_start", "message": bps.Object{"id": s.id, "type": "message", "role": "assistant", "model": s.source["model"], "content": []any{}, "stop_reason": nil, "usage": bps.Object{"input_tokens": 0, "output_tokens": 0}}})
	}
	if kind == "response.in_progress" {
		// A comment keeps connections alive without inventing model output.
		_, err := fmt.Fprint(s.w, ": keepalive\n\n")
		if err != nil {
			s.cancel()
			return err
		}
		if f, ok := s.w.(http.Flusher); ok {
			f.Flush()
		}
		return nil
	}
	if kind == "response.output_text.delta" {
		if s.format == "chat" {
			return s.chunk(bps.Object{"content": event["delta"]}, nil, nil)
		}
		if s.textBlock < 0 {
			s.textBlock = s.blockIndex
			s.blockIndex++
			if err := s.write("content_block_start", bps.Object{"type": "content_block_start", "index": s.textBlock, "content_block": bps.Object{"type": "text", "text": ""}}); err != nil {
				return err
			}
		}
		return s.write("content_block_delta", bps.Object{"type": "content_block_delta", "index": s.textBlock, "delta": bps.Object{"type": "text_delta", "text": event["delta"]}})
	}
	if kind == "response.reasoning_summary_text.delta" || kind == "response.reasoning_text.delta" {
		if s.format == "chat" {
			return s.chunk(bps.Object{"reasoning_content": event["delta"]}, nil, nil)
		}
		return nil
	}
	if kind == "response.output_item.done" {
		item := bpsObject(event["item"])
		typ := bps.String(item["type"])
		if typ != "function_call" && typ != "custom_tool_call" {
			return nil
		}
		name := bps.String(item["name"])
		if ns := bps.String(item["namespace"]); ns != "" {
			name = ns + "." + name
		}
		if s.format == "chat" {
			kind, field := "function", "arguments"
			if typ == "custom_tool_call" {
				kind, field = "custom", "input"
			}
			call := bps.Object{"index": s.toolIndex, "id": item["call_id"], "type": kind, kind: bps.Object{"name": name, field: item[field]}}
			s.toolIndex++
			return s.chunk(bps.Object{"tool_calls": []any{call}}, nil, nil)
		}
		if err := s.stopText(); err != nil {
			return err
		}
		index := s.blockIndex
		s.blockIndex++
		s.toolIndex++
		if err := s.write("content_block_start", bps.Object{"type": "content_block_start", "index": index, "content_block": bps.Object{"type": "tool_use", "id": item["call_id"], "name": name, "input": bps.Object{}}}); err != nil {
			return err
		}
		if err := s.write("content_block_delta", bps.Object{"type": "content_block_delta", "index": index, "delta": bps.Object{"type": "input_json_delta", "partial_json": item["arguments"]}}); err != nil {
			return err
		}
		return s.write("content_block_stop", bps.Object{"type": "content_block_stop", "index": index})
	}
	if kind == "response.completed" || kind == "response.incomplete" {
		response := bpsObject(event["response"])
		if s.format == "chat" {
			reason := "stop"
			if s.toolIndex > 0 {
				reason = "tool_calls"
			}
			if kind == "response.incomplete" {
				reason = "length"
			}
			if err := s.chunk(bps.Object{}, reason, bpsUsage(response)); err != nil {
				return err
			}
		} else {
			if err := s.stopText(); err != nil {
				return err
			}
			stop := "end_turn"
			if s.toolIndex > 0 {
				stop = "tool_use"
			}
			if kind == "response.incomplete" {
				stop = "max_tokens"
			}
			usage := bpsObject(response["usage"])
			if err := s.write("message_delta", bps.Object{"type": "message_delta", "delta": bps.Object{"stop_reason": stop, "stop_sequence": nil}, "usage": bps.Object{"input_tokens": usage["input_tokens"], "output_tokens": usage["output_tokens"]}}); err != nil {
				return err
			}
			if err := s.write("message_stop", bps.Object{"type": "message_stop"}); err != nil {
				return err
			}
		}
		s.terminal = true
	}
	return nil
}
func (s *bpsOutput) failed(err error) {
	if s.terminal {
		return
	}
	code, message := "bps_upstream_error", "BPS response failed"
	var upstream *bps.Error
	if errors.As(err, &upstream) {
		code, message = upstream.Code, upstream.Message
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code, message = "bps_timeout", "BPS request timed out"
	}
	errorBody := bps.Object{"code": code, "type": "upstream_error", "message": message}
	switch s.format {
	case "responses":
		_ = s.write("response.failed", bps.Object{"type": "response.failed", "sequence_number": s.sequence, "response": bps.Object{"id": s.id, "object": "response", "model": s.source["model"], "status": "failed", "output": []any{}, "error": errorBody}})
	case "messages":
		_ = s.write("error", bps.Object{"type": "error", "error": errorBody})
	default:
		_ = s.write("", bps.Object{"error": errorBody})
	}
	s.terminal = true
}
func (s *bpsOutput) finish() {
	if s.terminal && s.format != "messages" {
		_, err := fmt.Fprint(s.w, "data: [DONE]\n\n")
		if err != nil {
			s.cancel()
		}
		if f, ok := s.w.(http.Flusher); ok {
			f.Flush()
		}
	}
}
