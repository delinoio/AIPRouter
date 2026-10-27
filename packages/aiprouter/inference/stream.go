// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// readSSE bounds each event independently; total stream length is not a retained buffer.
func readSSE(r io.Reader, emit func(string, []byte) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var data []byte
	event := ""
	flush := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		payload := data[:len(data)-1]
		e := emit(event, payload)
		data = nil
		event = ""
		return e
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if e := flush(); e != nil {
				return e
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		key, val, _ := strings.Cut(line, ":")
		val = strings.TrimPrefix(val, " ")
		switch key {
		case "event":
			event = val
		case "data":
			if len(data)+len(val)+1 > 2<<20 {
				return &Error{502, "stream_event_too_large", 0}
			}
			data = append(data, val...)
			data = append(data, '\n')
		}
	}
	if e := scanner.Err(); e != nil {
		return e
	}
	return flush()
}
func emitSSE(w http.ResponseWriter, event string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if event != "" {
		if _, e = fmt.Fprintf(w, "event: %s\n", event); e != nil {
			return e
		}
	}
	if _, e = fmt.Fprintf(w, "data: %s\n\n", b); e != nil {
		return e
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
func mergeUsage(a *Usage, b Usage) {
	if b.InputTokens != nil {
		a.InputTokens = b.InputTokens
	}
	if b.OutputTokens != nil {
		a.OutputTokens = b.OutputTokens
	}
	if b.CacheReadTokens != nil {
		a.CacheReadTokens = b.CacheReadTokens
	}
	if b.CacheWriteTokens != nil {
		a.CacheWriteTokens = b.CacheWriteTokens
	}
}

type idleReader struct {
	io.Reader
	timer *time.Timer
	idle  time.Duration
}

func (r idleReader) Read(p []byte) (int, error) {
	n, e := r.Reader.Read(p)
	if n > 0 && r.timer != nil {
		r.timer.Reset(r.idle)
	}
	return n, e
}
func (h *Handler) deliverStream(ctx context.Context, w http.ResponseWriter, p Principal, in Wire, op Operation, b object, t Target, resp *http.Response, rec *UsageRecord, cancel context.CancelFunc, stall time.Duration) error {
	streaming := b["stream"] == true
	native := in == t.Wire
	var timer *time.Timer
	if stall > 0 {
		timer = time.AfterFunc(stall, func() { cancel(); resp.Body.Close() })
		defer timer.Stop()
	}
	if !streaming {
		deadline := time.AfterFunc(15*time.Minute, func() { cancel(); resp.Body.Close() })
		defer deadline.Stop()
	}
	var terminal bool
	var final object
	var r = result{ID: newID(), Model: rec.Model, Stop: "stop"}
	tools := map[int]*toolCall{}
	toolOrder := []int{}
	size := 0
	emitter := &streamEmitter{w: w, wire: in, id: r.ID, model: r.Model}
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	err := readSSE(idleReader{resp.Body, timer, stall}, func(event string, data []byte) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if string(data) == "[DONE]" {
			terminal = true
			if native && streaming {
				_, e := io.WriteString(w, "data: [DONE]\n\n")
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				return e
			}
			return nil
		}
		m, e := decode(data)
		if e != nil {
			return &Error{502, "invalid_upstream_event", 0}
		}
		if str(m["type"]) == "error" || m["error"] != nil {
			return &Error{502, "upstream_stream_error", 0}
		}
		mergeUsage(&rec.Usage, readUsage(m, t.Wire))
		if t.Wire == Responses && (str(m["type"]) == "response.completed" || str(m["type"]) == "response.incomplete") {
			final = obj(m["response"])
			terminal = true
			mergeUsage(&rec.Usage, readUsage(final, Responses))
		}
		if t.Wire == Responses && str(m["type"]) == "response.failed" {
			return &Error{502, "upstream_stream_error", 0}
		}
		if t.Wire == Anthropic {
			switch str(m["type"]) {
			case "message_start":
				mergeUsage(&rec.Usage, readUsage(obj(m["message"]), Anthropic))
			case "message_stop":
				terminal = true
			}
		}
		if native && streaming {
			if _, e := io.WriteString(w, "event: "+safeEventName(event)+"\ndata: "+string(data)+"\n\n"); e != nil {
				return e
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return nil
		}
		// Accumulation is bounded for translated output and buffered native responses.
		size += len(data)
		if size > 32<<20 {
			return &Error{502, "translation_buffer_limit", 0}
		}
		text := ""
		switch t.Wire {
		case Chat:
			for _, v := range arr(m["choices"]) {
				c := obj(v)
				if str(c["finish_reason"]) != "" {
					r.Stop = str(c["finish_reason"])
				}
				d := obj(c["delta"])
				if d["reasoning_content"] != nil {
					return unsupportedField()
				}
				text += str(d["content"])
				for _, v := range arr(d["tool_calls"]) {
					c := obj(v)
					i := index(c["index"])
					tc := tools[i]
					if tc == nil {
						tc = &toolCall{}
						tools[i] = tc
						toolOrder = append(toolOrder, i)
					}
					if s := str(c["id"]); s != "" {
						tc.ID = s
					}
					f := obj(c["function"])
					tc.Name += str(f["name"])
					tc.Arguments += str(f["arguments"])
				}
			}
		case Anthropic:
			switch str(m["type"]) {
			case "content_block_start":
				q := obj(m["content_block"])
				i := index(m["index"])
				if str(q["type"]) == "tool_use" {
					tools[i] = &toolCall{ID: str(q["id"]), Name: str(q["name"])}
					toolOrder = append(toolOrder, i)
				} else if str(q["type"]) == "text" {
					text = str(q["text"])
				} else {
					return unsupportedField()
				}
			case "content_block_delta":
				d := obj(m["delta"])
				switch str(d["type"]) {
				case "text_delta":
					text = str(d["text"])
				case "input_json_delta":
					tc := tools[index(m["index"])]
					if tc == nil {
						return &Error{502, "invalid_tool_stream", 0}
					}
					tc.Arguments += str(d["partial_json"])
				default:
					return unsupportedField()
				}
			case "message_delta":
				r.Stop = str(obj(m["delta"])["stop_reason"])
			}
		case Responses:
			switch str(m["type"]) {
			case "response.output_text.delta":
				text = str(m["delta"])
			case "response.output_item.added":
				q := obj(m["item"])
				if str(q["type"]) == "function_call" {
					i := index(m["output_index"])
					tools[i] = &toolCall{ID: str(q["call_id"]), Name: str(q["name"])}
					toolOrder = append(toolOrder, i)
				} else if str(q["type"]) != "message" {
					return unsupportedField()
				}
			case "response.function_call_arguments.delta":
				tc := tools[index(m["output_index"])]
				if tc == nil {
					return &Error{502, "invalid_tool_stream", 0}
				}
				tc.Arguments += str(m["delta"])
			}
		}
		r.Text += text
		if streaming && text != "" {
			return emitter.text(text)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !terminal {
		return &Error{502, "upstream_stream_incomplete", 0}
	}
	if native && streaming {
		if in == Responses && final != nil {
			if e := h.save(ctx, p, b, final); e != nil {
				h.event(ctx, "continuation_write_failed")
			}
		}
		return nil
	}
	if final != nil {
		r, err = parseResult(final, t.Wire)
		if err != nil {
			return err
		}
	} else {
		for _, i := range toolOrder {
			r.Tools = append(r.Tools, *tools[i])
		}
	}
	r.Usage = rec.Usage
	if err = validateTools(r, b, in); err != nil {
		return err
	}
	out := r.response(in)
	if in == Responses {
		if e := h.save(ctx, p, b, out); e != nil {
			h.event(ctx, "continuation_write_failed")
		}
	}
	if streaming {
		return emitter.finish(r)
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(out)
}
func safeEventName(s string) string {
	for _, c := range s {
		if !(c == '.' || c == '_' || c == '-' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return "message"
		}
	}
	if s == "" {
		return "message"
	}
	return s
}
func index(v any) int { n, _ := strconv.Atoi(fmt.Sprint(v)); return n }

type streamEmitter struct {
	w                    http.ResponseWriter
	wire                 Wire
	id, model            string
	started, textStarted bool
	seq                  int
}

func (s *streamEmitter) event(name string, m object) error {
	if s.wire == Responses {
		m["type"] = name
		m["sequence_number"] = s.seq
		s.seq++
	}
	return emitSSE(s.w, name, m)
}
func (s *streamEmitter) start() error {
	if s.started {
		return nil
	}
	s.started = true
	switch s.wire {
	case Responses:
		return s.event("response.created", object{"response": object{"id": s.id, "object": "response", "model": s.model, "status": "in_progress", "output": []any{}}})
	case Anthropic:
		return s.event("message_start", object{"type": "message_start", "message": object{"id": s.id, "type": "message", "role": "assistant", "model": s.model, "content": []any{}, "stop_reason": nil, "usage": object{}}})
	default:
		return s.chat(object{"role": "assistant"}, nil)
	}
}
func (s *streamEmitter) chat(delta object, finish any) error {
	return emitSSE(s.w, "", object{"id": s.id, "object": "chat.completion.chunk", "model": s.model, "choices": []any{object{"index": 0, "delta": delta, "finish_reason": finish}}})
}
func (s *streamEmitter) text(text string) error {
	if e := s.start(); e != nil {
		return e
	}
	if !s.textStarted {
		s.textStarted = true
		switch s.wire {
		case Responses:
			if e := s.event("response.output_item.added", object{"output_index": 0, "item": object{"id": "msg_" + s.id, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}}); e != nil {
				return e
			}
			if e := s.event("response.content_part.added", object{"output_index": 0, "item_id": "msg_" + s.id, "content_index": 0, "part": object{"type": "output_text", "text": "", "annotations": []any{}}}); e != nil {
				return e
			}
		case Anthropic:
			if e := s.event("content_block_start", object{"type": "content_block_start", "index": 0, "content_block": object{"type": "text", "text": ""}}); e != nil {
				return e
			}
		}
	}
	switch s.wire {
	case Responses:
		return s.event("response.output_text.delta", object{"output_index": 0, "item_id": "msg_" + s.id, "content_index": 0, "delta": text})
	case Anthropic:
		return s.event("content_block_delta", object{"type": "content_block_delta", "index": 0, "delta": object{"type": "text_delta", "text": text}})
	default:
		return s.chat(object{"content": text}, nil)
	}
}
func (s *streamEmitter) finish(r result) error {
	if e := s.start(); e != nil {
		return e
	}
	r.ID = s.id
	out := r.response(s.wire)
	offset := 0
	if s.textStarted {
		offset = 1
		switch s.wire {
		case Anthropic:
			if e := s.event("content_block_stop", object{"type": "content_block_stop", "index": 0}); e != nil {
				return e
			}
		case Responses:
			if e := s.event("response.output_text.done", object{"output_index": 0, "item_id": "msg_" + s.id, "content_index": 0, "text": r.Text}); e != nil {
				return e
			}
			item := obj(arr(out["output"])[0])
			if e := s.event("response.content_part.done", object{"output_index": 0, "item_id": "msg_" + s.id, "content_index": 0, "part": arr(item["content"])[0]}); e != nil {
				return e
			}
			if e := s.event("response.output_item.done", object{"output_index": 0, "item": item}); e != nil {
				return e
			}
		}
	}
	for i, t := range r.Tools {
		idx := offset + i
		switch s.wire {
		case Chat:
			if e := s.chat(object{"tool_calls": []any{object{"index": i, "id": t.ID, "type": "function", "function": object{"name": t.Name, "arguments": t.Arguments}}}}, nil); e != nil {
				return e
			}
		case Anthropic:
			if e := s.event("content_block_start", object{"type": "content_block_start", "index": idx, "content_block": object{"type": "tool_use", "id": t.ID, "name": t.Name, "input": object{}}}); e != nil {
				return e
			}
			if e := s.event("content_block_delta", object{"type": "content_block_delta", "index": idx, "delta": object{"type": "input_json_delta", "partial_json": t.Arguments}}); e != nil {
				return e
			}
			if e := s.event("content_block_stop", object{"type": "content_block_stop", "index": idx}); e != nil {
				return e
			}
		case Responses:
			item := obj(arr(out["output"])[idx])
			start := clone(item)
			start["arguments"] = ""
			start["status"] = "in_progress"
			if e := s.event("response.output_item.added", object{"output_index": idx, "item": start}); e != nil {
				return e
			}
			if e := s.event("response.function_call_arguments.delta", object{"output_index": idx, "item_id": item["id"], "delta": t.Arguments}); e != nil {
				return e
			}
			if e := s.event("response.function_call_arguments.done", object{"output_index": idx, "item_id": item["id"], "arguments": t.Arguments}); e != nil {
				return e
			}
			if e := s.event("response.output_item.done", object{"output_index": idx, "item": item}); e != nil {
				return e
			}
		}
	}
	switch s.wire {
	case Responses:
		name := "response.completed"
		if out["status"] == "incomplete" {
			name = "response.incomplete"
		}
		return s.event(name, object{"response": out})
	case Anthropic:
		if e := s.event("message_delta", object{"type": "message_delta", "delta": object{"stop_reason": out["stop_reason"], "stop_sequence": nil}, "usage": out["usage"]}); e != nil {
			return e
		}
		return s.event("message_stop", object{"type": "message_stop"})
	default:
		finish := "stop"
		if len(r.Tools) > 0 {
			finish = "tool_calls"
		}
		if r.Stop == "length" || r.Stop == "max_tokens" {
			finish = "length"
		}
		if e := s.chat(object{}, finish); e != nil {
			return e
		}
		if e := emitSSE(s.w, "", object{"id": s.id, "object": "chat.completion.chunk", "model": s.model, "choices": []any{}, "usage": out["usage"]}); e != nil {
			return e
		}
		_, e := io.WriteString(s.w, "data: [DONE]\n\n")
		if f, ok := s.w.(http.Flusher); ok {
			f.Flush()
		}
		return e
	}
}

var _ = errors.Is
