// SPDX-License-Identifier: Apache-2.0
// Kiro wire behavior adapted from opencodex; see LICENSE.upstream.
package kiro

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
)

func inputMessages(body object, wire inference.Wire) ([]object, string, error) {
	var messages []object
	system := ""
	switch wire {
	case inference.Chat:
		for _, raw := range arr(body["messages"]) {
			if obj(raw) == nil {
				return nil, "", failure(400, "kiro_invalid_message")
			}
			messages = append(messages, obj(raw))
		}
	case inference.Responses:
		system = str(body["instructions"])
		if text, ok := body["input"].(string); ok {
			messages = append(messages, object{"role": "user", "content": text})
		} else {
			for _, raw := range arr(body["input"]) {
				m := obj(raw)
				switch str(m["type"]) {
				case "", "message":
					messages = append(messages, m)
				case "function_call":
					messages = append(messages, object{"role": "assistant", "tool_calls": []any{object{"id": m["call_id"], "function": object{"name": m["name"], "arguments": m["arguments"]}}}})
				case "function_call_output":
					messages = append(messages, object{"role": "tool", "tool_call_id": m["call_id"], "content": m["output"]})
				default:
					return nil, "", failure(400, "kiro_input_item_unsupported")
				}
			}
		}
	case inference.Anthropic:
		var err error
		system, _, err = content(body["system"])
		if err != nil {
			return nil, "", err
		}
		for _, raw := range arr(body["messages"]) {
			m := obj(raw)
			role := str(m["role"])
			if _, ok := m["content"].(string); ok {
				messages = append(messages, m)
				continue
			}
			current := object{"role": role}
			parts := []any{}
			calls := []any{}
			flush := func() {
				if len(parts) > 0 || len(calls) > 0 {
					current["content"], current["tool_calls"] = parts, calls
					messages = append(messages, current)
					current = object{"role": role}
					parts, calls = nil, nil
				}
			}
			for _, rawPart := range arr(m["content"]) {
				p := obj(rawPart)
				switch str(p["type"]) {
				case "tool_use":
					if role != "assistant" {
						return nil, "", failure(400, "kiro_invalid_tool_role")
					}
					encoded, err := json.Marshal(p["input"])
					if err != nil {
						return nil, "", err
					}
					calls = append(calls, object{"id": p["id"], "function": object{"name": p["name"], "arguments": string(encoded)}})
				case "tool_result":
					if role != "user" {
						return nil, "", failure(400, "kiro_invalid_tool_role")
					}
					flush()
					messages = append(messages, object{"role": "tool", "tool_call_id": p["tool_use_id"], "content": p["content"], "is_error": p["is_error"]})
				case "thinking", "redacted_thinking":
					return nil, "", failure(400, "kiro_foreign_reasoning_replay_unsupported")
				default:
					parts = append(parts, p)
				}
			}
			flush()
		}
	default:
		return nil, "", inference.ErrUnsupported
	}
	if len(messages) == 0 {
		return nil, "", failure(400, "kiro_messages_required")
	}
	return messages, system, nil
}

func content(v any) (string, []any, error) {
	if v == nil {
		return "", nil, nil
	}
	if s, ok := v.(string); ok {
		return s, nil, nil
	}
	parts, ok := v.([]any)
	if !ok {
		return "", nil, failure(400, "kiro_invalid_content")
	}
	var text string
	var images []any
	for _, raw := range parts {
		p := obj(raw)
		switch str(p["type"]) {
		case "text", "input_text", "output_text":
			s, ok := p["text"].(string)
			if !ok {
				return "", nil, failure(400, "kiro_invalid_text")
			}
			text = joinText(text, s)
		case "image_url", "input_image", "image":
			url := str(p["image_url"])
			if url == "" {
				url = str(obj(p["image_url"])["url"])
			}
			mime, data := "", ""
			if source := obj(p["source"]); source != nil {
				if str(source["type"]) != "base64" {
					return "", nil, failure(400, "kiro_remote_image_requires_executor")
				}
				mime, data = str(source["media_type"]), str(source["data"])
			} else {
				header, encoded, ok := strings.Cut(url, ",")
				if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
					return "", nil, failure(400, "kiro_remote_image_requires_executor")
				}
				mime, data = strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"), encoded
			}
			format := strings.TrimPrefix(mime, "image/")
			if format == "jpg" {
				format = "jpeg"
			}
			if format != "png" && format != "jpeg" && format != "gif" && format != "webp" {
				return "", nil, failure(400, "kiro_image_format_unsupported")
			}
			if data == "" {
				return "", nil, failure(400, "kiro_invalid_image")
			}
			if len(data) > 6<<20 {
				return "", nil, failure(413, "kiro_image_too_large")
			}
			if _, err := base64.StdEncoding.Strict().DecodeString(data); err != nil {
				return "", nil, failure(400, "kiro_invalid_image")
			}
			images = append(images, object{"format": format, "source": object{"bytes": data}})
			if len(images) > 20 {
				return "", nil, failure(400, "kiro_image_count_limit")
			}
		default:
			return "", nil, failure(400, "kiro_content_unsupported")
		}
	}
	return text, images, nil
}

var rejectedSchemaKeys = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields("additionalProperties pattern format minLength maxLength minimum maximum exclusiveMinimum exclusiveMaximum multipleOf minItems maxItems uniqueItems minProperties maxProperties contentEncoding contentMediaType $schema patternProperties propertyNames dependentSchemas dependentRequired if then else contains unevaluatedProperties unevaluatedItems encrypted") {
		m[k] = true
	}
	return m
}()

func sanitizeSchema(in object, depth int) object {
	if depth > 64 {
		return nil
	}
	out := object{}
	for key, value := range in {
		if rejectedSchemaKeys[key] {
			continue
		}
		if key == "required" && len(arr(value)) == 0 {
			continue
		}
		if key == "properties" || key == "$defs" || key == "definitions" {
			children := object{}
			for name, child := range obj(value) {
				c := sanitizeSchema(obj(child), depth+1)
				if c == nil {
					return nil
				}
				children[name] = c
			}
			out[key] = children
			continue
		}
		if m, ok := value.(map[string]any); ok {
			value = sanitizeSchema(m, depth+1)
			if value == nil {
				return nil
			}
		}
		if a, ok := value.([]any); ok {
			copy := make([]any, len(a))
			for i, v := range a {
				if m, ok := v.(map[string]any); ok {
					copy[i] = sanitizeSchema(m, depth+1)
					if copy[i] == nil {
						return nil
					}
				} else {
					copy[i] = v
				}
			}
			value = copy
		}
		out[key] = value
	}
	return out
}

func flattenSchema(schema object) {
	schema["type"] = "object"
	properties := obj(schema["properties"])
	if properties == nil {
		properties = object{}
	}
	required := arr(schema["required"])
	seen := map[string]bool{}
	for _, r := range required {
		seen[str(r)] = true
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		for _, v := range arr(schema[key]) {
			m := obj(v)
			for k, p := range obj(m["properties"]) {
				properties[k] = p
			}
			if key == "allOf" {
				for _, r := range arr(m["required"]) {
					if s := str(r); s != "" && !seen[s] {
						required = append(required, s)
						seen[s] = true
					}
				}
			}
		}
		delete(schema, key)
	}
	if len(properties) > 0 {
		schema["properties"] = properties
	}
	if len(required) > 0 {
		schema["required"] = required
	}
}
