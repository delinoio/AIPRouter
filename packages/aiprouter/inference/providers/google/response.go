// SPDX-License-Identifier: Apache-2.0
package google

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
)

// Decode preserves HTTP failures for the central executor's retry classifier.
// Success bodies are bounded, and malformed streams fail the body read instead
// of emitting a successful completion marker. Close always releases upstream.
func (a *Adapter) Decode(ctx context.Context, response *http.Response) (*http.Response, error) {
	if response == nil || response.Body == nil {
		return nil, protocol()
	}
	if err := ctx.Err(); err != nil {
		_ = response.Body.Close()
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, nil
	}
	out := new(http.Response)
	*out = *response
	out.Header = response.Header.Clone()
	out.Header.Del("Content-Length")
	out.Header.Del("Content-Encoding")
	out.Header.Del("ETag")
	out.ContentLength = -1
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		r := &streamReader{ctx: ctx, body: response.Body, adapter: a, state: newResponseState(), frameLimit: a.options.MaxFrameBytes}
		r.scanner = bufio.NewScanner(response.Body)
		r.scanner.Buffer(make([]byte, 4096), a.options.MaxFrameBytes+1)
		r.stop = context.AfterFunc(ctx, func() { _ = r.closeBody() })
		out.Body = r
		out.Header.Set("Content-Type", "text/event-stream")
		return out, nil
	}
	stop := context.AfterFunc(ctx, func() { _ = response.Body.Close() })
	defer stop()
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(a.options.MaxBodyBytes)+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, protocol()
	}
	if len(data) > a.options.MaxBodyBytes {
		return nil, failure(502, "google_response_too_large")
	}
	root, err := decodeJSON(data)
	if err != nil {
		return nil, err
	}
	state := newResponseState()
	message, err := a.convert(root, state)
	if err != nil {
		return nil, err
	}
	if !state.terminal {
		return nil, failure(502, "google_incomplete_response")
	}
	message["role"] = "assistant"
	if message["content"] == nil {
		message["content"] = ""
	}
	result := object{"id": state.id, "object": "chat.completion", "model": state.model, "choices": []any{object{"index": 0, "message": message, "finish_reason": state.finish()}}, "usage": state.usage}
	data, err = json.Marshal(result)
	if err != nil {
		return nil, protocol()
	}
	out.Body = io.NopCloser(bytes.NewReader(data))
	out.Header.Set("Content-Type", "application/json")
	return out, nil
}

type responseState struct {
	id, model, reason, thoughtSignature string
	terminal                            bool
	toolIndex                           int
	usage                               object
	callIDs                             map[string]bool
}

func newResponseState() *responseState {
	return &responseState{usage: object{}, callIDs: map[string]bool{}}
}
func (s *responseState) finish() string {
	switch s.reason {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
		return "content_filter"
	}
	if s.toolIndex > 0 {
		return "tool_calls"
	}
	return "stop"
}

func (a *Adapter) convert(root object, s *responseState) (object, error) {
	if root["error"] != nil {
		return nil, failure(502, "google_upstream_error")
	}
	if a.cca {
		root = obj(root["response"])
		if root == nil {
			return nil, failure(502, "google_missing_response_wrapper")
		}
		if root["error"] != nil {
			return nil, failure(502, "google_upstream_error")
		}
	}
	if s.id == "" {
		s.id = text(root["responseId"])
		if s.id == "" {
			id, e := randomID()
			if e != nil {
				return nil, e
			}
			s.id = "chatcmpl-" + id
		}
	}
	if model := text(root["modelVersion"]); model != "" {
		s.model = model
	}
	if usage := root["usageMetadata"]; usage != nil {
		u := obj(usage)
		if u == nil {
			return nil, protocol()
		}
		if err := mergeUsage(s.usage, u); err != nil {
			return nil, err
		}
		s.terminal = true
	}
	if block := text(obj(root["promptFeedback"])["blockReason"]); block != "" && block != "BLOCK_REASON_UNSPECIFIED" {
		s.reason = "SAFETY"
		s.terminal = true
	}
	delta := object{}
	if root["candidates"] == nil {
		return delta, nil
	}
	candidates, ok := root["candidates"].([]any)
	if !ok {
		return nil, protocol()
	}
	if len(candidates) == 0 {
		return delta, nil
	}
	// The public adapter projects one candidate. Reject additional candidates so
	// no independently generated answer or tool invocation disappears silently.
	if len(candidates) != 1 {
		return nil, failure(502, "google_multiple_candidates")
	}
	candidate := obj(candidates[0])
	if candidate == nil {
		return nil, protocol()
	}
	if reason := text(candidate["finishReason"]); reason != "" {
		switch reason {
		case "STOP", "MAX_TOKENS", "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
			s.reason = reason
			s.terminal = true
		default:
			return nil, failure(502, "google_terminal_failure")
		}
	}
	if candidate["content"] == nil {
		return delta, nil
	}
	content := obj(candidate["content"])
	if content == nil {
		return nil, protocol()
	}
	if content["parts"] == nil {
		return delta, nil
	}
	parts, ok := content["parts"].([]any)
	if !ok {
		return nil, protocol()
	}
	// Validate the complete frame before emitting any content or tool start.
	for _, raw := range parts {
		p := obj(raw)
		if p == nil {
			return nil, protocol()
		}
		if p["functionCall"] != nil {
			call := obj(p["functionCall"])
			if call == nil || strings.TrimSpace(text(call["name"])) == "" {
				return nil, failure(502, "google_invalid_tool_call")
			}
			if call["args"] != nil && obj(call["args"]) == nil {
				return nil, failure(502, "google_invalid_tool_arguments")
			}
		}
		for k := range p {
			switch k {
			case "text", "thought", "thoughtSignature", "thought_signature", "extra_content", "functionCall":
			default:
				return nil, failure(502, "google_unsupported_response_part")
			}
		}
		if len(signature(p)) > maxSignatureBytes {
			return nil, failure(502, "google_signature_too_large")
		}
	}
	var visible, reasoning strings.Builder
	var calls, opaque []any
	for _, raw := range parts {
		p := obj(raw)
		sig := signature(p)
		if p["thought"] == true {
			if realSignature(sig) {
				s.thoughtSignature = sig
			}
			// Keep the thought visibility bit and signature together. Never fold
			// raw thought text into normal assistant content.
			opaque = append(opaque, p)
			if a.options.ShowThinkingSummaries {
				reasoning.WriteString(text(p["text"]))
			}
		} else {
			visible.WriteString(text(p["text"]))
		}
		if call := obj(p["functionCall"]); call != nil {
			if s.toolIndex >= 4096 {
				return nil, failure(502, "google_tool_call_limit")
			}
			id := text(call["id"])
			if id == "" {
				var err error
				id, err = randomID()
				if err != nil {
					return nil, err
				}
				id = "call_" + id
			}
			if s.callIDs[id] {
				return nil, failure(502, "google_duplicate_tool_call")
			}
			s.callIDs[id] = true
			args := call["args"]
			if args == nil {
				args = object{}
			}
			encoded, err := json.Marshal(args)
			if err != nil {
				return nil, protocol()
			}
			tool := object{"index": s.toolIndex, "id": id, "type": "function", "function": object{"name": call["name"], "arguments": string(encoded)}}
			if sig == "" {
				sig = s.thoughtSignature
			}
			if realSignature(sig) {
				tool["extra_content"] = opaqueSignature(sig)
			}
			calls = append(calls, tool)
			s.toolIndex++
		}
	}
	if visible.Len() > 0 {
		delta["content"] = visible.String()
	}
	if reasoning.Len() > 0 {
		delta["reasoning_content"] = reasoning.String()
	}
	if len(calls) > 0 {
		delta["tool_calls"] = calls
	}
	if len(opaque) > 0 {
		delta["extra_content"] = object{"google": object{"parts": opaque}}
	}
	return delta, nil
}

func mergeUsage(dst, src object) error {
	// A later usage-only frame can revise cumulative counters. Do not retain an
	// earlier derived total when either component changes.
	if src["promptTokenCount"] != nil || src["candidatesTokenCount"] != nil {
		delete(dst, "total_tokens")
	}
	for from, to := range map[string]string{"promptTokenCount": "prompt_tokens", "candidatesTokenCount": "completion_tokens", "totalTokenCount": "total_tokens", "cachedContentTokenCount": "cached_tokens", "thoughtsTokenCount": "reasoning_tokens"} {
		v, exists := src[from]
		if !exists || v == nil {
			continue
		}
		n, ok := v.(json.Number)
		if !ok {
			return protocol()
		}
		value, err := n.Int64()
		if err != nil || value < 0 {
			return protocol()
		}
		switch to {
		case "cached_tokens":
			d := obj(dst["prompt_tokens_details"])
			if d == nil {
				d = object{}
				dst["prompt_tokens_details"] = d
			}
			d[to] = value
		case "reasoning_tokens":
			d := obj(dst["completion_tokens_details"])
			if d == nil {
				d = object{}
				dst["completion_tokens_details"] = d
			}
			d[to] = value
		default:
			dst[to] = value
		}
	}
	// The contract deliberately differs from upstream's zero defaults: absence
	// remains unknown. Derive a total only when both counters are known.
	if dst["total_tokens"] == nil {
		in, iok := dst["prompt_tokens"].(int64)
		out, ook := dst["completion_tokens"].(int64)
		if iok && ook && in <= math.MaxInt64-out {
			dst["total_tokens"] = in + out
		}
	}
	return nil
}

type streamReader struct {
	ctx        context.Context
	body       io.ReadCloser
	adapter    *Adapter
	state      *responseState
	scanner    *bufio.Scanner
	frameLimit int
	pending    []byte
	end        bool
	stop       func() bool
	closeOnce  sync.Once
	closeErr   error
}

func (r *streamReader) closeBody() error {
	r.closeOnce.Do(func() { r.closeErr = r.body.Close() })
	return r.closeErr
}
func (r *streamReader) Close() error {
	if r.stop != nil {
		r.stop()
	}
	return r.closeBody()
}

func (r *streamReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 {
		if err := r.ctx.Err(); err != nil {
			_ = r.Close()
			return 0, err
		}
		if r.end {
			return 0, io.EOF
		}
		frame, event, eof, err := r.frame()
		if err != nil {
			r.end = true
			_ = r.Close()
			return 0, err
		}
		if event == "error" {
			r.end = true
			_ = r.Close()
			return 0, failure(502, "google_upstream_stream_error")
		}
		if len(frame) > 0 && string(frame) != "[DONE]" {
			// JSON null is a keepalive, not a claimed response candidate.
			if string(bytes.TrimSpace(frame)) != "null" {
				root, e := decodeJSON(frame)
				if e != nil {
					r.end = true
					_ = r.Close()
					return 0, e
				}
				delta, e := r.adapter.convert(root, r.state)
				if e != nil {
					r.end = true
					_ = r.Close()
					return 0, e
				}
				if len(delta) > 0 {
					chunk := object{"id": r.state.id, "object": "chat.completion.chunk", "model": r.state.model, "choices": []any{object{"index": 0, "delta": delta, "finish_reason": nil}}}
					r.pending = encodeEvent(chunk)
				}
			}
		}
		if eof || string(frame) == "[DONE]" {
			if !r.state.terminal {
				r.end = true
				_ = r.Close()
				return 0, failure(502, "google_incomplete_stream")
			}
			terminal := object{"id": r.state.id, "object": "chat.completion.chunk", "model": r.state.model, "choices": []any{object{"index": 0, "delta": object{}, "finish_reason": r.state.finish()}}, "usage": r.state.usage}
			r.pending = append(r.pending, encodeEvent(terminal)...)
			r.pending = append(r.pending, []byte("data: [DONE]\n\n")...)
			r.end = true
			_ = r.Close()
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}
func encodeEvent(v object) []byte {
	b, _ := json.Marshal(v)
	return append(append([]byte("data: "), b...), []byte("\n\n")...)
}

func (r *streamReader) frame() ([]byte, string, bool, error) {
	var data []byte
	event := ""
	size := 0
	for r.scanner.Scan() {
		if err := r.ctx.Err(); err != nil {
			return nil, "", false, err
		}
		line := r.scanner.Bytes()
		size += len(line) + 1
		if size > r.frameLimit {
			return nil, "", false, failure(502, "google_stream_frame_too_large")
		}
		if len(line) == 0 {
			if len(data) > 0 || event != "" {
				return bytes.TrimSuffix(data, []byte("\n")), event, false, nil
			}
			size = 0
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			value = nil
		}
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "data":
			data = append(data, value...)
			data = append(data, '\n')
		case "event":
			event = string(value)
		}
	}
	if err := r.ctx.Err(); err != nil {
		return nil, "", false, err
	}
	if r.scanner.Err() != nil {
		return nil, "", false, failure(502, "google_stream_read_failed")
	}
	return bytes.TrimSuffix(data, []byte("\n")), event, true, nil
}
