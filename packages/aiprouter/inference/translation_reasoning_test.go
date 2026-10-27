// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func translationObject(t *testing.T, s string) object {
	t.Helper()
	m, e := decode([]byte(s))
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func translationJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func TestTranslationReasoningRoundTrips(t *testing.T) {
	fixtures := []struct {
		name    string
		msg     object
		needles []string
	}{
		{"devin", object{"role": "assistant", "reasoning_content": "private thought", "reasoning_signature": "devin-sig1:anthropic:opaque"}, []string{"private thought", "devin-sig1:anthropic:opaque"}},
		{"kiro", object{"role": "assistant", "reasoning_content": "summary", "kiro_reasoning": "signature:.KTR~~opaque"}, []string{"summary", "signature:.KTR~~opaque"}},
		{"redacted", translationObject(t, `{"role":"assistant","reasoning_details":[{"type":"reasoning.encrypted","data":"redacted-one","format":"anthropic-claude-v1"},{"type":"reasoning.text","text":"exact signed text","signature":"abcdefghijklmnop==","format":"anthropic-claude-v1"}]}`), []string{"redacted-one", "exact signed text", "abcdefghijklmnop=="}},
		{"google", translationObject(t, `{"role":"assistant","extra_content":{"google":{"parts":[{"thought":true,"text":"hidden","thoughtSignature":"opaque-google"}]}}}`), []string{"hidden", "opaque-google"}},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			body := object{"model": "m", "messages": []any{f.msg}}
			responses, e := translateRequest(body, Chat, Responses)
			if e != nil {
				t.Fatal(e)
			}
			anthropic, e := translateRequest(responses, Responses, Anthropic)
			if e != nil {
				t.Fatal(e)
			}
			back, e := translateRequest(anthropic, Anthropic, Chat)
			if e != nil {
				t.Fatal(e)
			}
			// Decode the final canonical envelope before checking opaque values.
			items, e := messagesToResponses(arr(back["messages"]))
			if e != nil {
				t.Fatal(e)
			}
			decoded := translationJSON(back)
			for _, v := range items {
				if enc := str(obj(v)["encrypted_content"]); strings.HasPrefix(enc, reasoningPrefix) {
					env, e := decodeReasoning(enc)
					if e != nil {
						t.Fatal(e)
					}
					decoded += translationJSON(env)
				}
			}
			for _, want := range f.needles {
				if !strings.Contains(decoded, want) {
					t.Fatalf("lost %q: %s", want, decoded)
				}
			}
			if f.name == "google" && strings.Contains(translationJSON(back), `"content":"hidden"`) {
				t.Fatal("opaque thought became visible content")
			}
		})
	}
}
func TestTranslationNativeCiphertextAndHiddenText(t *testing.T) {
	text := "hidden exact text"
	for _, item := range []object{
		{"type": "reasoning", "id": "rs_native", "encrypted_content": "native-ciphertext", "summary": []any{}},
		reasoningItem("", reasoningEnvelope{Signature: "abcdefghijklmnop==", Text: &text}),
	} {
		msgs, e := toMessages(object{"input": []any{item}}, Responses)
		if e != nil {
			t.Fatal(e)
		}
		back, e := messagesToResponses(msgs)
		if e != nil {
			t.Fatal(e)
		}
		if translationJSON(back[0]) != translationJSON(item) {
			t.Fatalf("changed reasoning: %s", translationJSON(back))
		}
		blocks := reasoningToAnthropic(item)
		if len(blocks) != 1 {
			t.Fatal(blocks)
		}
		restored := anthropicReasoning(obj(blocks[0]))
		if str(item["encrypted_content"]) == "native-ciphertext" && restored["encrypted_content"] != "native-ciphertext" {
			t.Fatal(restored)
		}
	}
}
func TestTranslationCustomNamespaceAndSignatures(t *testing.T) {
	request := translationObject(t, `{"model":"m","input":[],"tools":[{"type":"namespace","name":"work","tools":[{"type":"custom","name":"exec","description":"raw","format":{"type":"text"}}]}]}`)
	for _, wire := range []Wire{Chat, Anthropic} {
		translated, e := translateRequest(request, Responses, wire)
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(translationJSON(translated), "work__exec") {
			t.Fatal(translated)
		}
	}
	msg := translationObject(t, `{"role":"assistant","tool_calls":[{"id":"call1","type":"function","function":{"name":"work__exec","arguments":"{\"input\":\"echo 🦀\\nraw\"}"},"provider_metadata":{"tool_kind":"custom","tool_name":"exec","tool_namespace":"work"},"extra_content":{"google":{"thought_signature":"call-bound"}}}]}`)
	r, e := parseResult(object{"choices": []any{object{"message": msg}}}, Chat)
	if e != nil {
		t.Fatal(e)
	}
	if e = validateTools(r, request, Responses); e != nil {
		t.Fatal(e)
	}
	output := r.response(Responses)
	item := obj(arr(output["output"])[0])
	if item["type"] != "custom_tool_call" || item["namespace"] != "work" || item["input"] != "echo 🦀\nraw" {
		t.Fatal(item)
	}
	if str(obj(obj(item["extra_content"])["google"])["thought_signature"]) != "call-bound" {
		t.Fatal(item)
	}
	back, e := translateRequest(object{"input": arr(output["output"])}, Responses, Anthropic)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(translationJSON(back), "call-bound") {
		t.Fatal(back)
	}
}
func TestTranslationCustomCatalogRestoration(t *testing.T) {
	r, e := parseResult(translationObject(t, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"exec","arguments":"{\"input\":\"hello\"}"}}]}}]}`), Chat)
	if e != nil {
		t.Fatal(e)
	}
	if e = validateTools(r, translationObject(t, `{"tools":[{"type":"custom","name":"exec"}]}`), Responses); e != nil {
		t.Fatal(e)
	}
	if obj(r.Items[0])["type"] != "custom_tool_call" {
		t.Fatal(r.Items)
	}
	bad := translationObject(t, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c","function":{"name":"exec","arguments":"{\"input\":7}"},"provider_metadata":{"tool_kind":"custom"}}]}}]}`)
	if _, e := parseResult(bad, Chat); e == nil {
		t.Fatal("nonstring custom input accepted")
	}
}
func TestTranslationCacheUsageMatrix(t *testing.T) {
	u := Usage{}
	a, b, c, d := int64(20), int64(5), int64(7), int64(3)
	u.InputTokens = &a
	u.OutputTokens = &b
	u.CacheReadTokens = &c
	u.CacheWriteTokens = &d
	for _, w := range []Wire{Chat, Responses, Anthropic} {
		m := translationObject(t, translationJSON(object{"usage": usageObject(u, w)}))
		got := readUsage(m, w)
		if translationJSON(got) != translationJSON(u) {
			t.Fatalf("%s: %s", w, translationJSON(got))
		}
	}
	for _, w := range []Wire{Chat, Responses, Anthropic} {
		if got := readUsage(object{}, w); got.InputTokens != nil || got.OutputTokens != nil || got.CacheWriteTokens != nil {
			t.Fatal(got)
		}
	}
}
func TestTranslationLateSignatureOrdering(t *testing.T) {
	h, _, _, _, usage := harness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w,
			"data: {\"id\":\"upstream-private\",\"choices\":[{\"delta\":{\"reasoning_content\":\"thought\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{\"reasoning_signature\":\"devin-sig1:anthropic:late\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"cache_creation_input_tokens\":3}}\n\ndata: [DONE]\n\n")
	}, Chat)
	out := call(h, "/v1/messages", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	body := out.Body.String()
	if out.Code != 200 {
		t.Fatal(body)
	}
	sig := strings.Index(body, `"type":"signature_delta"`)
	stop := strings.Index(body, `event: content_block_stop`)
	text := strings.Index(body, `"text":"answer"`)
	if sig < 0 || stop < sig || text < stop {
		t.Fatalf("invalid signature order: %s", body)
	}
	if len(usage.records) != 1 || usage.records[0].ResponseID == "" || usage.records[0].ResponseID == "upstream-private" {
		t.Fatal(usage.records)
	}
	if usage.records[0].Usage.CacheWriteTokens == nil || *usage.records[0].Usage.CacheWriteTokens != 3 {
		t.Fatal(usage.records)
	}
}
func TestTranslationErrorUsageSurvives(t *testing.T) {
	for _, wire := range []Wire{Chat, Responses, Anthropic} {
		t.Run(string(wire), func(t *testing.T) {
			rec := &UsageRecord{}
			w := httptest.NewRecorder()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			event := object{"type": "error", "usage": object{"cache_creation_input_tokens": json.Number("9")}}
			if wire == Responses {
				event = object{"type": "response.failed", "response": object{"usage": object{"cache_creation_input_tokens": json.Number("9")}}}
			}
			resp := &http.Response{Body: http.NoBody}
			resp.Body = ioNopCloser("data: " + translationJSON(event) + "\n\n")
			h := &Handler{}
			e := h.deliverStream(ctx, w, Principal{}, Chat, Generate, object{"stream": true}, Target{Wire: wire}, resp, rec, cancel, time.Second)
			if e == nil || rec.Usage.CacheWriteTokens == nil || *rec.Usage.CacheWriteTokens != 9 {
				t.Fatalf("error=%v usage=%+v", e, rec.Usage)
			}
		})
	}
}

type translationReader struct{ *strings.Reader }

func (translationReader) Close() error       { return nil }
func ioNopCloser(s string) translationReader { return translationReader{strings.NewReader(s)} }
func TestTranslationStreamBoundsAndFraming(t *testing.T) {
	for _, w := range []Wire{Chat, Responses, Anthropic} {
		s := newTranslationStream(w)
		if e := s.consume(object{"index": json.Number("999999999")}); e == nil {
			t.Fatal("unbounded index accepted")
		}
	}
	s := newTranslationStream(Chat)
	for _, raw := range []string{
		`{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"run","arguments":"{"},"extra_content":{"google":{"thought_signature":"opaque"}}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]}}]}`,
		`{"choices":[{"delta":{"kiro_reasoning":"signature:late"}}]}`,
	} {
		if e := s.consume(translationObject(t, raw)); e != nil {
			t.Fatal(e)
		}
	}
	r, e := s.result()
	if e != nil {
		t.Fatal(e)
	}
	out := r.response(Responses)
	items := arr(out["output"])
	if len(items) != 2 || obj(items[0])["type"] != "reasoning" || obj(items[1])["type"] != "function_call" {
		t.Fatal(out)
	}
	if !strings.Contains(translationJSON(out), "opaque") {
		t.Fatal(out)
	}
	env, e := decodeReasoning(str(obj(items[0])["encrypted_content"]))
	if e != nil || env.Kiro != "signature:late" {
		t.Fatal(env, e)
	}
}
