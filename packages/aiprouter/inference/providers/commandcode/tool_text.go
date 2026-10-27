// SPDX-License-Identifier: Apache-2.0
// MiMo markup restoration adapted from opencodex; see LICENSE.upstream.
package commandcode

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"

	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
)

const heldLimit = 64 << 10

var envelope = regexp.MustCompile(`(?s)^<tool_call>\s*<function=([^>\s]+)>(.*)</tool_call>$`)
var parameter = regexp.MustCompile(`(?s)<parameter=([^>]+)>(.*?)</parameter>`)

type toolFilter struct {
	freeform  map[string]bool
	enabled   bool
	tools     map[string]object
	pending   []object
	bytes     int
	probing   bool
	streaming bool
}

func newToolFilter(in inference.AdapterInput) *toolFilter {
	f := &toolFilter{enabled: strings.HasPrefix(strings.ToLower(CanonicalModel(in.Target.Model)), "xiaomi/mimo-"), tools: map[string]object{}, freeform: map[string]bool{}}
	if !f.enabled {
		return f
	}
	m, err := chatInput(in)
	if err != nil {
		return f
	}
	for _, raw := range arr(m["tools"]) {
		fn := obj(obj(raw)["function"])
		if obj(obj(fn["extra_content"])["aiprouter"])["custom_tool"] == true {
			f.freeform[str(fn["name"])] = true
		}
	}
	tools, _ := wireTools(m)
	for _, v := range tools {
		t := obj(v)
		f.tools[str(t["name"])] = obj(t["input_schema"])
	}
	return f
}
func (f *toolFilter) drain() []object {
	v := f.pending
	f.pending = nil
	f.bytes = 0
	f.probing = false
	return v
}
func (f *toolFilter) event(e object) []object {
	kind := str(e["type"])
	if !f.enabled {
		if kind == "text-delta" || kind == "reasoning-delta" || kind == "tool-call" {
			return []object{e}
		}
		return nil
	}
	if kind == "text-start" {
		f.streaming = false
		return nil
	}
	if kind == "text-end" {
		return nil
	}
	if kind == "tool-input-start" {
		return nil
	}
	if kind == "text-delta" {
		text := str(e["text"])
		if len(f.pending) == 0 && f.streaming {
			return []object{e}
		}
		if len(f.pending) == 0 {
			trimmed := strings.TrimLeft(text, " \t\r\n")
			if !strings.HasPrefix("<tool_call>", trimmed) && !strings.Contains(text, "<tool_call>") {
				f.streaming = true
				return []object{e}
			}
			f.probing = true
		}
	}
	if kind == "tool-call" && len(f.pending) > 0 {
		// A native call is canonical. Drop only a complete envelope for that same
		// declared function; unrelated text and reasoning retain their original order.
		f.dropEcho(str(e["toolName"]), e["input"])
	}
	if len(f.pending) == 0 && !f.probing {
		return []object{e}
	}
	size, _ := json.Marshal(e)
	f.bytes += len(size)
	f.pending = append(f.pending, e)
	if f.bytes > heldLimit {
		f.streaming = true
		return f.drain()
	}
	return nil
}
func (f *toolFilter) text() string {
	var b strings.Builder
	for _, e := range f.pending {
		if e["type"] == "text-delta" {
			b.WriteString(str(e["text"]))
		}
	}
	return b.String()
}
func (f *toolFilter) replaceText(text string) {
	out := []object{}
	inserted := false
	for _, e := range f.pending {
		if e["type"] == "text-delta" {
			if !inserted && text != "" {
				out = append(out, object{"type": "text-delta", "text": text})
				inserted = true
			}
		} else {
			out = append(out, e)
		}
	}
	f.pending = out
}
func (f *toolFilter) dropEcho(name string, input any) {
	if f.tools[name] == nil {
		return
	}
	text := f.text()
	start := strings.Index(text, "<tool_call>")
	if start < 0 {
		return
	}
	tail := strings.TrimSpace(text[start:])
	match := envelope.FindStringSubmatch(tail)
	if match == nil || match[1] != name || strings.Contains(match[2], "<tool_call>") {
		return
	}
	// The upstream permits removal of malformed complete echoes for a declared
	// native function. The native call supplies the authoritative argument bytes.
	f.replaceText(text[:start])
	f.probing = len(f.pending) > 0
}
func (f *toolFilter) finish(clean bool) ([]object, bool) {
	if !f.enabled {
		return f.drain(), false
	}
	text := strings.TrimSpace(f.text())
	match := envelope.FindStringSubmatch(text)
	if clean && match != nil && !strings.Contains(match[2], "<tool_call>") {
		schema, known := f.tools[match[1]]
		if known {
			if args, ok := restoreMarkup(match[2], schema, f.freeform[match[1]]); ok {
				out := []object{}
				inserted := false
				for _, e := range f.pending {
					if e["type"] == "text-delta" {
						if !inserted {
							out = append(out, object{"type": "tool-call", "toolCallId": uuid(), "toolName": match[1], "input": args})
							inserted = true
						}
					} else {
						out = append(out, e)
					}
				}
				f.drain()
				return out, true
			}
			// Complete malformed declared envelopes are suppressed on a clean finish.
			f.replaceText("")
		}
	}
	return f.drain(), false
}
func trimNewlines(s string) string {
	s = strings.TrimPrefix(s, "\r\n")
	s = strings.TrimPrefix(s, "\n")
	s = strings.TrimSuffix(s, "\r\n")
	return strings.TrimSuffix(s, "\n")
}
func restoreMarkup(body string, schema object, freeform bool) (object, bool) {
	closed := strings.HasSuffix(strings.TrimSpace(body), "</function>")
	if closed {
		body = strings.TrimSuffix(strings.TrimSpace(body), "</function>")
	}
	props := obj(schema["properties"])
	out := object{}
	if !strings.Contains(body, "<parameter=") {
		// Custom tools are lowered by the shared translator to a single input string.
		// Restrict raw restoration to that explicit shape, never an arbitrary function.
		p := obj(props["input"])
		if !freeform || len(props) != 1 || p["type"] != "string" {
			return nil, false
		}
		out["input"] = trimNewlines(strings.TrimSuffix(body, "</parameter>"))
	} else {
		if freeform || !closed || strings.TrimSpace(parameter.ReplaceAllString(body, "")) != "" {
			return nil, false
		}
		matches := parameter.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			return nil, false
		}
		for _, m := range matches {
			key := strings.TrimSpace(m[1])
			if _, exists := out[key]; exists {
				return nil, false
			}
			rule, ok := props[key]
			if !ok {
				return nil, false
			}
			raw := trimNewlines(m[2])
			var value any
			if json.Unmarshal([]byte(raw), &value) != nil || !validSchema(value, rule, 0) {
				value = raw
			}
			if !validSchema(value, rule, 0) {
				return nil, false
			}
			out[key] = value
		}
	}
	return out, validSchema(out, schema, 0)
}

// Unknown schema assertions fail closed: text is never promoted to an executable
// call merely because the adapter does not understand a constraint.
func validSchema(v, raw any, depth int) bool {
	if depth > 16 {
		return false
	}
	if b, ok := raw.(bool); ok {
		return b
	}
	s := obj(raw)
	if s == nil {
		return false
	}
	for k := range s {
		switch k {
		case "$schema", "$id", "title", "description", "default", "examples", "deprecated", "readOnly", "writeOnly", "type", "enum", "const", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "minLength", "maxLength", "minItems", "maxItems", "uniqueItems", "items", "minProperties", "maxProperties", "properties", "required", "additionalProperties", "anyOf", "oneOf", "allOf":
		default:
			return false
		}
	}
	if t, ok := s["type"]; ok {
		types := arr(t)
		if text, ok := t.(string); ok {
			types = []any{text}
		}
		matched := false
		for _, typ := range types {
			if matchesType(v, str(typ)) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	if e, ok := s["enum"]; ok {
		matched := false
		for _, item := range arr(e) {
			if reflect.DeepEqual(v, item) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	if c, ok := s["const"]; ok && !reflect.DeepEqual(v, c) {
		return false
	}
	for _, key := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties"} {
		limit, exists := s[key]
		if !exists {
			continue
		}
		n, ok := limit.(float64)
		if !ok {
			return false
		}
		actual, numeric := v.(float64)
		switch key {
		case "minLength", "maxLength":
			text, ok := v.(string)
			if !ok {
				return false
			}
			actual = float64(len([]rune(text)))
			numeric = true
		case "minItems", "maxItems":
			a, ok := v.([]any)
			if !ok {
				return false
			}
			actual = float64(len(a))
			numeric = true
		case "minProperties", "maxProperties":
			o := obj(v)
			if o == nil {
				return false
			}
			actual = float64(len(o))
			numeric = true
		}
		if !numeric {
			return false
		}
		switch key {
		case "minimum", "minLength", "minItems", "minProperties":
			if actual < n {
				return false
			}
		case "maximum", "maxLength", "maxItems", "maxProperties":
			if actual > n {
				return false
			}
		case "exclusiveMinimum":
			if actual <= n {
				return false
			}
		case "exclusiveMaximum":
			if actual >= n {
				return false
			}
		case "multipleOf":
			if n <= 0 || actual/n != float64(int64(actual/n)) {
				return false
			}
		}
	}
	if rule, ok := s["items"]; ok {
		a, ok := v.([]any)
		if !ok {
			return false
		}
		for _, item := range a {
			if !validSchema(item, rule, depth+1) {
				return false
			}
		}
	}
	if unique, ok := s["uniqueItems"]; ok {
		b, ok := unique.(bool)
		a, isArray := v.([]any)
		if !ok || !isArray {
			return false
		}
		if b {
			for i, x := range a {
				for _, y := range a[i+1:] {
					if reflect.DeepEqual(x, y) {
						return false
					}
				}
			}
		}
	}
	o := obj(v)
	if required, ok := s["required"]; ok {
		a, valid := required.([]any)
		if !valid || o == nil {
			return false
		}
		for _, key := range a {
			if _, ok := o[str(key)]; !ok {
				return false
			}
		}
	}
	properties := obj(s["properties"])
	if _, ok := s["properties"]; ok && (o == nil || properties == nil) {
		return false
	}
	for key, item := range o {
		if rule, ok := properties[key]; ok {
			if !validSchema(item, rule, depth+1) {
				return false
			}
		} else if rule, ok := s["additionalProperties"]; ok && !validSchema(item, rule, depth+1) {
			return false
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if raw, ok := s[key]; ok {
			branches := arr(raw)
			if len(branches) == 0 {
				return false
			}
			count := 0
			for _, b := range branches {
				if validSchema(v, b, depth+1) {
					count++
				}
			}
			if key == "anyOf" && count < 1 || key == "oneOf" && count != 1 || key == "allOf" && count != len(branches) {
				return false
			}
		}
	}
	return true
}
func matchesType(v any, t string) bool {
	switch t {
	case "null":
		return v == nil
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		return obj(v) != nil
	case "array":
		_, ok := v.([]any)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		n, ok := v.(float64)
		return ok && n >= -9007199254740991 && n <= 9007199254740991 && n == float64(int64(n))
	}
	return false
}
