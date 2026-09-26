package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/maiphucgiang/gpt2api/internal/protocol"
)

type responsesOutput struct {
	kind, id, callID, name string
	index                  int
	text                   strings.Builder
}

type responsesStreamWriter struct {
	writer             http.ResponseWriter
	header             http.Header
	ctx                context.Context
	cancel             context.CancelFunc
	request            protocol.ChatRequest
	id                 string
	created            int64
	sequence, status   int
	buffer             bytes.Buffer
	outputs            []*responsesOutput
	message, reasoning *responsesOutput
	tools              map[int]*responsesOutput
	usage              map[string]any
	finishReason       string
	terminal           bool
	err                error
}

func newResponsesStreamWriter(w http.ResponseWriter, ctx context.Context, cancel context.CancelFunc, request protocol.ChatRequest) *responsesStreamWriter {
	id := "resp_" + strings.TrimPrefix(newChatID(), "chatcmpl-")
	stream := &responsesStreamWriter{writer: w, header: make(http.Header), ctx: ctx, cancel: cancel, request: request, id: id, created: time.Now().Unix(), tools: map[int]*responsesOutput{}}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	stream.emit("response.created", map[string]any{"response": stream.response("in_progress")})
	stream.emit("response.in_progress", map[string]any{"response": stream.response("in_progress")})
	return stream
}

func (s *responsesStreamWriter) Header() http.Header    { return s.header }
func (s *responsesStreamWriter) WriteHeader(status int) { s.status = status }
func (s *responsesStreamWriter) Flush() {
	if flusher, ok := s.writer.(http.Flusher); ok && s.err == nil {
		flusher.Flush()
	}
}

func (s *responsesStreamWriter) emit(event string, value map[string]any) {
	if s.err != nil || s.ctx.Err() != nil {
		return
	}
	value["type"], value["sequence_number"] = event, s.sequence
	s.sequence++
	raw, err := json.Marshal(value)
	if err == nil {
		_, err = fmt.Fprintf(s.writer, "event: %s\ndata: %s\n\n", event, raw)
	}
	if err != nil {
		s.err = err
		s.cancel()
		return
	}
	s.Flush()
}

func (s *responsesStreamWriter) Write(raw []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if s.terminal {
		return len(raw), nil
	}
	if s.buffer.Len()+len(raw) > 8<<20 {
		s.err = errors.New("chat stream frame exceeds 8MB")
		s.cancel()
		return 0, s.err
	}
	_, _ = s.buffer.Write(raw)
	if s.status >= 400 {
		if json.Valid(s.buffer.Bytes()) {
			s.consume(s.buffer.Bytes())
			s.buffer.Reset()
		}
		return len(raw), s.err
	}
	for {
		data := s.buffer.Bytes()
		index, width := bytes.Index(data, []byte("\n\n")), 2
		if crlf := bytes.Index(data, []byte("\r\n\r\n")); crlf >= 0 && (index < 0 || crlf < index) {
			index, width = crlf, 4
		}
		if index < 0 {
			break
		}
		frame := append([]byte(nil), data[:index]...)
		s.buffer.Next(index + width)
		values := []string{}
		for _, line := range strings.Split(strings.ReplaceAll(string(frame), "\r\n", "\n"), "\n") {
			if strings.HasPrefix(line, "data:") {
				values = append(values, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if len(values) > 0 {
			s.consume([]byte(strings.Join(values, "\n")))
		}
	}
	if s.err != nil {
		return 0, s.err
	}
	return len(raw), nil
}

func (s *responsesStreamWriter) consume(raw []byte) {
	if s.terminal {
		return
	}
	if string(raw) == "[DONE]" {
		s.finish()
		return
	}
	var chunk struct {
		Error   map[string]any `json:"error"`
		Usage   map[string]any `json:"usage"`
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
				Tools     []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &chunk) != nil {
		s.fail("invalid upstream stream frame")
		return
	}
	if chunk.Error != nil {
		s.fail(firstNonEmpty(stringValue(chunk.Error["message"]), "upstream request failed"))
		return
	}
	if s.status >= 400 {
		s.fail("upstream request failed")
		return
	}
	if chunk.Usage != nil {
		s.usage = responsesUsage(chunk.Usage)
	}
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			continue
		}
		if choice.Delta.Reasoning != "" {
			s.textDelta("reasoning", choice.Delta.Reasoning)
		}
		if choice.Delta.Content != "" {
			s.textDelta("message", choice.Delta.Content)
		}
		for _, delta := range choice.Delta.Tools {
			item := s.tools[delta.Index]
			if item == nil {
				item = &responsesOutput{kind: "function_call", id: fmt.Sprintf("fc_%s_%d", s.id, delta.Index), callID: delta.ID, name: delta.Function.Name, index: len(s.outputs)}
				s.tools[delta.Index] = item
				s.outputs = append(s.outputs, item)
				s.emit("response.output_item.added", map[string]any{"output_index": item.index, "item": item.value(false)})
			} else {
				if delta.ID != "" {
					item.callID = delta.ID
				}
				item.name += delta.Function.Name
			}
			if delta.Function.Arguments != "" {
				item.text.WriteString(delta.Function.Arguments)
				s.emit("response.function_call_arguments.delta", map[string]any{"item_id": item.id, "output_index": item.index, "delta": delta.Function.Arguments})
			}
		}
		if choice.FinishReason != "" {
			s.finishReason = choice.FinishReason
		}
	}
}

func (s *responsesStreamWriter) textDelta(kind, delta string) {
	item := s.message
	if kind == "reasoning" {
		item = s.reasoning
	}
	if item == nil {
		item = &responsesOutput{kind: kind, id: fmt.Sprintf("msg_%s_%d", s.id, len(s.outputs)), index: len(s.outputs)}
		if kind == "reasoning" {
			item.id = "rs_" + s.id
			s.reasoning = item
		} else {
			s.message = item
		}
		s.outputs = append(s.outputs, item)
		s.emit("response.output_item.added", map[string]any{"output_index": item.index, "item": item.value(false)})
		if kind == "reasoning" {
			s.emit("response.reasoning_summary_part.added", map[string]any{"item_id": item.id, "output_index": item.index, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
		} else {
			s.emit("response.content_part.added", map[string]any{"item_id": item.id, "output_index": item.index, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
		}
	}
	item.text.WriteString(delta)
	event, indexName := "response.output_text.delta", "content_index"
	if kind == "reasoning" {
		event, indexName = "response.reasoning_summary_text.delta", "summary_index"
	}
	value := map[string]any{"item_id": item.id, "output_index": item.index, indexName: 0, "delta": delta}
	if kind == "message" {
		value["logprobs"] = []any{}
	}
	s.emit(event, value)
}

func (o *responsesOutput) value(done bool) map[string]any {
	status := "in_progress"
	if done {
		status = "completed"
	}
	item := map[string]any{"id": o.id, "type": o.kind, "status": status}
	switch o.kind {
	case "function_call":
		item["call_id"], item["name"], item["arguments"] = o.callID, o.name, o.text.String()
	case "reasoning":
		summary := []any{}
		if done {
			summary = append(summary, map[string]any{"type": "summary_text", "text": o.text.String()})
		}
		item["summary"] = summary
	default:
		content := []any{}
		if done {
			content = append(content, map[string]any{"type": "output_text", "text": o.text.String(), "annotations": []any{}})
		}
		item["role"], item["content"] = "assistant", content
	}
	return item
}

func (s *responsesStreamWriter) response(status string) map[string]any {
	output := make([]any, 0, len(s.outputs))
	for _, item := range s.outputs {
		value := item.value(status != "in_progress")
		if status == "failed" || status == "incomplete" {
			value["status"] = "incomplete"
		}
		output = append(output, value)
	}
	return map[string]any{"id": s.id, "object": "response", "created_at": s.created, "status": status, "model": s.request.Model, "output": output, "usage": s.usage, "error": nil, "incomplete_details": nil, "parallel_tool_calls": false, "tool_choice": "none", "tools": []any{}, "store": false}
}

func (s *responsesStreamWriter) fail(message string) {
	if s.terminal {
		return
	}
	response := s.response("failed")
	response["error"] = map[string]any{"code": "server_error", "message": message}
	s.emit("response.failed", map[string]any{"response": response})
	s.terminal = true
}

func (s *responsesStreamWriter) finish() {
	if s.terminal || s.err != nil || s.ctx.Err() != nil {
		return
	}
	if len(s.outputs) == 0 {
		s.textDelta("message", "")
	}
	for _, item := range s.outputs {
		switch item.kind {
		case "function_call":
			s.emit("response.function_call_arguments.done", map[string]any{"item_id": item.id, "output_index": item.index, "name": item.name, "arguments": item.text.String()})
		case "reasoning":
			part := map[string]any{"type": "summary_text", "text": item.text.String()}
			s.emit("response.reasoning_summary_text.done", map[string]any{"item_id": item.id, "output_index": item.index, "summary_index": 0, "text": item.text.String()})
			s.emit("response.reasoning_summary_part.done", map[string]any{"item_id": item.id, "output_index": item.index, "summary_index": 0, "part": part})
		default:
			s.emit("response.output_text.done", map[string]any{"item_id": item.id, "output_index": item.index, "content_index": 0, "text": item.text.String(), "logprobs": []any{}})
			s.emit("response.content_part.done", map[string]any{"item_id": item.id, "output_index": item.index, "content_index": 0, "part": map[string]any{"type": "output_text", "text": item.text.String(), "annotations": []any{}}})
		}
		value := item.value(true)
		if s.finishReason == "length" || s.finishReason == "content_filter" {
			value["status"] = "incomplete"
		}
		s.emit("response.output_item.done", map[string]any{"output_index": item.index, "item": value})
	}
	if s.usage == nil {
		text, reasoning := "", ""
		for _, item := range s.outputs {
			if item.kind == "reasoning" {
				reasoning += item.text.String()
			} else {
				text += item.text.String()
			}
		}
		s.usage = responsesUsage(usageFor(protocol.ExtractMessage(s.request.Messages), text, reasoning))
	}
	status := "completed"
	if s.finishReason == "length" || s.finishReason == "content_filter" {
		status = "incomplete"
	}
	response := s.response(status)
	if status == "incomplete" {
		reason := "max_output_tokens"
		if s.finishReason == "content_filter" {
			reason = "content_filter"
		}
		response["incomplete_details"] = map[string]any{"reason": reason}
	}
	s.emit("response."+status, map[string]any{"response": response})
	s.terminal = true
	if s.err == nil && s.ctx.Err() == nil {
		_, s.err = fmt.Fprint(s.writer, "data: [DONE]\n\n")
		if s.err != nil {
			s.cancel()
		} else {
			s.Flush()
		}
	}
}

func responsesUsage(usage map[string]any) map[string]any {
	if usage == nil {
		return nil
	}
	if _, ok := usage["input_tokens"]; ok {
		return usage
	}
	return map[string]any{"input_tokens": usage["prompt_tokens"], "output_tokens": usage["completion_tokens"], "total_tokens": usage["total_tokens"], "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}
}
