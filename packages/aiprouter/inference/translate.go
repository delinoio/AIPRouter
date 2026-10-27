// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type object = map[string]any

func obj(v any) object { m, _ := v.(map[string]any); return m }
func arr(v any) []any  { a, _ := v.([]any); return a }
func str(v any) string { s, _ := v.(string); return s }
func clone(m object) object {
	n := object{}
	for k, v := range m {
		n[k] = v
	}
	return n
}
func decode(b []byte) (object, error) {
	var m object
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	if e := d.Decode(&m); e != nil || m == nil {
		return nil, &Error{400, "invalid_json", 0}
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, &Error{400, "invalid_json", 0}
	}
	return m, nil
}
func unsupportedField() error { return &Error{400, "unsupported_translation_field", 0} }

// translateRequest uses Chat's message structure as a private intermediary. Native paths
// retain unknown fields; translated paths reject fields whose semantics cannot be preserved.
func translateRequest(body object, from, to Wire) (object, error) {
	if from == to {
		return clone(body), nil
	}
	allowed := map[string]bool{"model": true, "stream": true, "messages": true, "input": true, "instructions": true, "system": true, "tools": true, "tool_choice": true, "temperature": true, "top_p": true, "max_tokens": true, "max_completion_tokens": true, "max_output_tokens": true, "parallel_tool_calls": true, "store": true, "previous_response_id": true, "stream_options": true, "metadata": true, "user": true}
	for k := range body {
		if !allowed[k] {
			return nil, unsupportedField()
		}
	}
	messages, err := toMessages(body, from)
	if err != nil {
		return nil, err
	}
	out := object{"model": body["model"], "stream": body["stream"]}
	for _, k := range []string{"temperature", "top_p", "metadata"} {
		if v, ok := body[k]; ok {
			out[k] = v
		}
	}
	max := body["max_output_tokens"]
	if max == nil {
		max = body["max_completion_tokens"]
	}
	if max == nil {
		max = body["max_tokens"]
	}
	switch to {
	case Chat:
		out["messages"] = messages
		if max != nil {
			out["max_completion_tokens"] = max
		}
		if body["stream"] == true {
			out["stream_options"] = object{"include_usage": true}
		}
	case Responses:
		input, err := messagesToResponses(messages)
		if err != nil {
			return nil, err
		}
		out["input"] = input
		out["store"] = false
		if max != nil {
			out["max_output_tokens"] = max
		}
	case Anthropic:
		msgs, system, err := messagesToAnthropic(messages)
		if err != nil {
			return nil, err
		}
		out["messages"] = msgs
		if len(system) > 0 {
			out["system"] = system
		}
		if max == nil {
			max = 8192
		}
		out["max_tokens"] = max
	default:
		return nil, ErrUnsupported
	}
	if tools := arr(body["tools"]); len(tools) > 0 {
		converted := []any{}
		for _, v := range tools {
			t := obj(v)
			f := t
			switch from {
			case Chat:
				if str(t["type"]) != "function" {
					return nil, unsupportedField()
				}
				f = obj(t["function"])
			case Responses:
				if str(t["type"]) != "function" {
					return nil, unsupportedField()
				}
			case Anthropic:
				if t["type"] != nil {
					return nil, unsupportedField()
				}
			}
			schema := f["parameters"]
			if from == Anthropic {
				schema = f["input_schema"]
			}
			fn := object{"name": f["name"], "description": f["description"], "parameters": schema}
			if f["strict"] != nil {
				fn["strict"] = f["strict"]
			}
			switch to {
			case Chat:
				converted = append(converted, object{"type": "function", "function": fn})
			case Responses:
				fn["type"] = "function"
				converted = append(converted, fn)
			case Anthropic:
				if f["strict"] == true {
					return nil, unsupportedField()
				}
				converted = append(converted, object{"name": f["name"], "description": f["description"], "input_schema": schema})
			}
		}
		out["tools"] = converted
	}
	if choice, ok := body["tool_choice"]; ok {
		c, e := translateChoice(choice, from, to)
		if e != nil {
			return nil, e
		}
		out["tool_choice"] = c
	}
	if v, ok := body["parallel_tool_calls"]; ok {
		if to == Anthropic {
			return nil, unsupportedField()
		}
		out["parallel_tool_calls"] = v
	}
	return out, nil
}
func translateChoice(v any, from, to Wire) (any, error) {
	s := str(v)
	name := ""
	if m := obj(v); m != nil {
		switch from {
		case Chat:
			name = str(obj(m["function"])["name"])
		case Responses:
			name = str(m["name"])
		case Anthropic:
			s = str(m["type"])
			if s == "tool" {
				name = str(m["name"])
			}
			if s == "any" {
				s = "required"
			}
		}
	}
	if name != "" {
		switch to {
		case Chat:
			return object{"type": "function", "function": object{"name": name}}, nil
		case Responses:
			return object{"type": "function", "name": name}, nil
		case Anthropic:
			return object{"type": "tool", "name": name}, nil
		}
	}
	if s != "auto" && s != "none" && s != "required" {
		return nil, unsupportedField()
	}
	if to == Anthropic {
		if s == "required" {
			s = "any"
		}
		return object{"type": s}, nil
	}
	return s, nil
}
func toMessages(b object, w Wire) ([]any, error) {
	var out []any
	switch w {
	case Chat:
		return arr(b["messages"]), nil
	case Responses:
		if s := str(b["instructions"]); s != "" {
			out = append(out, object{"role": "system", "content": s})
		}
		if s, ok := b["input"].(string); ok {
			return append(out, object{"role": "user", "content": s}), nil
		}
		for _, v := range arr(b["input"]) {
			m := obj(v)
			switch str(m["type"]) {
			case "function_call":
				out = append(out, object{"role": "assistant", "tool_calls": []any{object{"id": m["call_id"], "type": "function", "function": object{"name": m["name"], "arguments": m["arguments"]}}}})
			case "function_call_output":
				out = append(out, object{"role": "tool", "tool_call_id": m["call_id"], "content": m["output"]})
			case "", "message":
				c, e := contentToChat(m["content"], w)
				if e != nil {
					return nil, e
				}
				out = append(out, object{"role": m["role"], "content": c})
			default:
				return nil, unsupportedField()
			}
		}
	case Anthropic:
		if b["system"] != nil {
			c, e := contentToChat(b["system"], w)
			if e != nil {
				return nil, e
			}
			out = append(out, object{"role": "system", "content": c})
		}
		for _, v := range arr(b["messages"]) {
			m := obj(v)
			if s, ok := m["content"].(string); ok {
				out = append(out, object{"role": m["role"], "content": s})
				continue
			}
			var parts, calls []any
			flush := func() {
				if len(parts) > 0 || len(calls) > 0 {
					mm := object{"role": m["role"], "content": parts}
					if len(calls) > 0 {
						mm["tool_calls"] = calls
					}
					out = append(out, mm)
					parts = nil
					calls = nil
				}
			}
			for _, p := range arr(m["content"]) {
				q := obj(p)
				switch str(q["type"]) {
				case "tool_use":
					args, _ := json.Marshal(q["input"])
					calls = append(calls, object{"id": q["id"], "type": "function", "function": object{"name": q["name"], "arguments": string(args)}})
				case "tool_result":
					flush()
					if q["is_error"] == true {
						return nil, unsupportedField()
					}
					c, e := contentToChat(q["content"], w)
					if e != nil {
						return nil, e
					}
					out = append(out, object{"role": "tool", "tool_call_id": q["tool_use_id"], "content": c})
				default:
					c, e := contentToChat([]any{p}, w)
					if e != nil {
						return nil, e
					}
					parts = append(parts, arr(c)...)
				}
			}
			flush()
		}
	default:
		return nil, ErrUnsupported
	}
	return out, nil
}
func contentToChat(v any, w Wire) (any, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	parts := []any{}
	for _, p := range arr(v) {
		m := obj(p)
		switch str(m["type"]) {
		case "text", "input_text", "output_text":
			parts = append(parts, object{"type": "text", "text": m["text"]})
		case "input_image":
			parts = append(parts, object{"type": "image_url", "image_url": object{"url": m["image_url"], "detail": m["detail"]}})
		case "image":
			s := obj(m["source"])
			u := str(s["url"])
			if str(s["type"]) == "base64" {
				u = "data:" + str(s["media_type"]) + ";base64," + str(s["data"])
			}
			parts = append(parts, object{"type": "image_url", "image_url": object{"url": u}})
		default:
			return nil, unsupportedField()
		}
	}
	return parts, nil
}
func messagesToResponses(msgs []any) ([]any, error) {
	out := []any{}
	for _, v := range msgs {
		m := obj(v)
		if str(m["role"]) == "tool" {
			out = append(out, object{"type": "function_call_output", "call_id": m["tool_call_id"], "output": m["content"]})
			continue
		}
		if m["content"] != nil {
			parts := []any{}
			textType := "input_text"
			if str(m["role"]) == "assistant" {
				textType = "output_text"
			}
			if s, ok := m["content"].(string); ok {
				parts = append(parts, object{"type": textType, "text": s})
			} else {
				for _, p := range arr(m["content"]) {
					q := obj(p)
					switch str(q["type"]) {
					case "text":
						parts = append(parts, object{"type": textType, "text": q["text"]})
					case "image_url":
						im := obj(q["image_url"])
						parts = append(parts, object{"type": "input_image", "image_url": im["url"], "detail": im["detail"]})
					default:
						return nil, unsupportedField()
					}
				}
			}
			out = append(out, object{"type": "message", "role": m["role"], "content": parts})
		}
		for _, t := range arr(m["tool_calls"]) {
			c := obj(t)
			f := obj(c["function"])
			out = append(out, object{"type": "function_call", "call_id": c["id"], "name": f["name"], "arguments": f["arguments"]})
		}
	}
	return out, nil
}
func messagesToAnthropic(msgs []any) ([]any, []any, error) {
	out, system := []any{}, []any{}
	for _, v := range msgs {
		m := obj(v)
		role := str(m["role"])
		parts := []any{}
		if s, ok := m["content"].(string); ok {
			parts = append(parts, object{"type": "text", "text": s})
		} else {
			for _, p := range arr(m["content"]) {
				q := obj(p)
				switch str(q["type"]) {
				case "text":
					parts = append(parts, q)
				case "image_url":
					u := str(obj(q["image_url"])["url"])
					source := object{"type": "url", "url": u}
					if strings.HasPrefix(u, "data:") {
						head, data, ok := strings.Cut(u, ",")
						if !ok || !strings.HasSuffix(head, ";base64") {
							return nil, nil, unsupportedField()
						}
						source = object{"type": "base64", "media_type": strings.TrimSuffix(strings.TrimPrefix(head, "data:"), ";base64"), "data": data}
					}
					parts = append(parts, object{"type": "image", "source": source})
				default:
					return nil, nil, unsupportedField()
				}
			}
		}
		if role == "system" || role == "developer" {
			system = append(system, parts...)
			continue
		}
		if role == "tool" {
			role = "user"
			parts = []any{object{"type": "tool_result", "tool_use_id": m["tool_call_id"], "content": parts}}
		}
		for _, v := range arr(m["tool_calls"]) {
			c := obj(v)
			f := obj(c["function"])
			var args object
			if json.Unmarshal([]byte(str(f["arguments"])), &args) != nil {
				return nil, nil, &Error{400, "invalid_tool_arguments", 0}
			}
			parts = append(parts, object{"type": "tool_use", "id": c["id"], "name": f["name"], "input": args})
		}
		out = append(out, object{"role": role, "content": parts})
	}
	return out, system, nil
}

type toolCall struct{ ID, Name, Arguments string }
type result struct {
	ID, Model, Text, Stop string
	Tools                 []toolCall
	Usage                 Usage
}

func token(v any) *int64 {
	n, ok := v.(json.Number)
	if !ok {
		return nil
	}
	i, e := n.Int64()
	if e != nil || i < 0 {
		return nil
	}
	return &i
}
func readUsage(m object, w Wire) Usage {
	u := obj(m["usage"])
	if u == nil {
		return Usage{}
	}
	if w == Chat {
		return Usage{InputTokens: token(u["prompt_tokens"]), OutputTokens: token(u["completion_tokens"]), CacheReadTokens: token(obj(u["prompt_tokens_details"])["cached_tokens"])}
	}
	v := Usage{InputTokens: token(u["input_tokens"]), OutputTokens: token(u["output_tokens"]), CacheReadTokens: token(obj(u["input_tokens_details"])["cached_tokens"])}
	if w == Anthropic {
		v.CacheReadTokens = token(u["cache_read_input_tokens"])
		v.CacheWriteTokens = token(u["cache_creation_input_tokens"])
		if v.InputTokens != nil {
			x := *v.InputTokens
			if v.CacheReadTokens != nil {
				x += *v.CacheReadTokens
			}
			if v.CacheWriteTokens != nil {
				x += *v.CacheWriteTokens
			}
			v.InputTokens = &x
		}
	}
	return v
}
func parseResult(m object, w Wire) (result, error) {
	r := result{ID: str(m["id"]), Model: str(m["model"]), Stop: "stop", Usage: readUsage(m, w)}
	switch w {
	case Chat:
		c := arr(m["choices"])
		if len(c) != 1 {
			return r, unsupportedField()
		}
		ch := obj(c[0])
		r.Stop = str(ch["finish_reason"])
		msg := obj(ch["message"])
		if msg["reasoning_content"] != nil {
			return r, unsupportedField()
		}
		r.Text = str(msg["content"])
		for _, t := range arr(msg["tool_calls"]) {
			m := obj(t)
			f := obj(m["function"])
			r.Tools = append(r.Tools, toolCall{str(m["id"]), str(f["name"]), str(f["arguments"])})
		}
	case Responses:
		for _, v := range arr(m["output"]) {
			q := obj(v)
			switch str(q["type"]) {
			case "message":
				for _, p := range arr(q["content"]) {
					part := obj(p)
					if str(part["type"]) != "output_text" {
						return r, unsupportedField()
					}
					r.Text += str(part["text"])
				}
			case "function_call":
				r.Tools = append(r.Tools, toolCall{str(q["call_id"]), str(q["name"]), str(q["arguments"])})
			default:
				return r, unsupportedField()
			}
		}
		if str(m["status"]) == "incomplete" {
			r.Stop = "length"
		}
	case Anthropic:
		r.Stop = str(m["stop_reason"])
		for _, v := range arr(m["content"]) {
			q := obj(v)
			switch str(q["type"]) {
			case "text":
				r.Text += str(q["text"])
			case "tool_use":
				b, _ := json.Marshal(q["input"])
				r.Tools = append(r.Tools, toolCall{str(q["id"]), str(q["name"]), string(b)})
			default:
				return r, unsupportedField()
			}
		}
	default:
		return r, ErrUnsupported
	}
	return r, nil
}
func (r result) response(w Wire) object {
	if r.ID == "" {
		r.ID = newID()
	}
	usage := object{}
	if r.Usage.InputTokens != nil {
		usage["input_tokens"] = *r.Usage.InputTokens
	}
	if r.Usage.OutputTokens != nil {
		usage["output_tokens"] = *r.Usage.OutputTokens
	}
	switch w {
	case Responses:
		output := []any{}
		if r.Text != "" {
			output = append(output, object{"type": "message", "id": "msg_" + r.ID, "role": "assistant", "status": "completed", "content": []any{object{"type": "output_text", "text": r.Text, "annotations": []any{}}}})
		}
		for _, t := range r.Tools {
			output = append(output, object{"type": "function_call", "id": "fc_" + t.ID, "call_id": t.ID, "name": t.Name, "arguments": t.Arguments, "status": "completed"})
		}
		status := "completed"
		if r.Stop == "length" || r.Stop == "max_tokens" {
			status = "incomplete"
		}
		return object{"id": r.ID, "object": "response", "model": r.Model, "status": status, "output": output, "usage": usage}
	case Anthropic:
		content := []any{}
		if r.Text != "" {
			content = append(content, object{"type": "text", "text": r.Text})
		}
		for _, t := range r.Tools {
			var input any
			_ = json.Unmarshal([]byte(t.Arguments), &input)
			content = append(content, object{"type": "tool_use", "id": t.ID, "name": t.Name, "input": input})
		}
		stop := "end_turn"
		if len(r.Tools) > 0 {
			stop = "tool_use"
		}
		if r.Stop == "length" || r.Stop == "max_tokens" {
			stop = "max_tokens"
		}
		return object{"id": r.ID, "type": "message", "role": "assistant", "model": r.Model, "content": content, "stop_reason": stop, "stop_sequence": nil, "usage": usage}
	default:
		msg := object{"role": "assistant", "content": r.Text}
		calls := []any{}
		for _, t := range r.Tools {
			calls = append(calls, object{"id": t.ID, "type": "function", "function": object{"name": t.Name, "arguments": t.Arguments}})
		}
		stop := "stop"
		if len(calls) > 0 {
			msg["tool_calls"] = calls
			stop = "tool_calls"
		}
		if r.Stop == "length" || r.Stop == "max_tokens" {
			stop = "length"
		}
		u := object{}
		if r.Usage.InputTokens != nil {
			u["prompt_tokens"] = *r.Usage.InputTokens
		}
		if r.Usage.OutputTokens != nil {
			u["completion_tokens"] = *r.Usage.OutputTokens
		}
		if r.Usage.InputTokens != nil && r.Usage.OutputTokens != nil {
			u["total_tokens"] = *r.Usage.InputTokens + *r.Usage.OutputTokens
		}
		return object{"id": r.ID, "object": "chat.completion", "model": r.Model, "choices": []any{object{"index": 0, "message": msg, "finish_reason": stop}}, "usage": u}
	}
}
func validateTools(r result, body object, w Wire) error {
	names := map[string]bool{}
	for _, v := range arr(body["tools"]) {
		t := obj(v)
		if w == Chat {
			t = obj(t["function"])
		}
		names[str(t["name"])] = true
	}
	seen := map[string]bool{}
	for _, t := range r.Tools {
		if t.ID == "" || t.Name == "" || !names[t.Name] || seen[t.ID] {
			return &Error{502, "invalid_upstream_tool_call", 0}
		}
		seen[t.ID] = true
		var args object
		if json.Unmarshal([]byte(t.Arguments), &args) != nil || args == nil {
			return &Error{502, "invalid_upstream_tool_arguments", 0}
		}
	}
	return nil
}
func wirePath(w Wire) string {
	switch w {
	case Chat:
		return "/chat/completions"
	case Anthropic:
		return "/messages"
	case Responses:
		return "/responses"
	}
	return fmt.Sprint("")
}
