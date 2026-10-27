// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type executionFixture struct{ endpoint string }

func (a executionFixture) Build(context.Context, AdapterInput) (AdapterRequest, error) {
	panic("executing adapter must bypass Build")
}
func (a executionFixture) Decode(context.Context, *http.Response) (*http.Response, error) {
	panic("executing adapter already decoded")
}
func (a executionFixture) Execute(ctx context.Context, in AdapterInput, ex AdapterExecutor, _ AdapterStateStore) (*http.Response, Wire, error) {
	r, e := ex.Do(ctx, AdapterRequest{URL: a.endpoint, Body: []byte(`{}`)})
	return r, Chat, e
}

type refreshFixture struct{}

func (refreshFixture) RecoverHTTP(_ context.Context, _ AdapterInput, f HTTPFailure) (RecoveryDecision, error) {
	if f.Status == 401 || f.Header.Get("x-zed-expired-token") != "" {
		return RecoveryDecision{Action: RecoveryRefresh}, nil
	}
	return RecoveryDecision{Action: RecoveryDefault}, nil
}
func TestExecutingAdapterSharesRecovery(t *testing.T) {
	for _, status := range []int{401, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			h, s, _, creds, u := harness(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					w.Header().Set("x-zed-expired-token", "true")
					w.WriteHeader(status)
					io.WriteString(w, `{}`)
					return
				}
				io.WriteString(w, fixture(Chat))
			}, Chat)
			route := h.c.Router.(testRouter)
			route.route.Targets[0].Wire = "fixture-executing"
			route.route.Targets[0].Auth = OAuth
			h.c.Router = route
			h.c.Adapters = map[Wire]Adapter{"fixture-executing": executionFixture{s.URL}}
			h.c.RecoveryPolicies = map[Wire]HTTPRecoveryPolicy{"fixture-executing": refreshFixture{}}
			w := call(h, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
			if w.Code != 200 || calls != 2 || creds.refreshes.Load() != 1 || u.records[0].Sends != 2 {
				t.Fatalf("code=%d body=%s calls=%d refresh=%d usage=%+v", w.Code, w.Body, calls, creds.refreshes.Load(), u.records)
			}
		})
	}
}
func TestExecutorDeadlineIncludesBody(t *testing.T) {
	h, s, _, _, _ := harness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, Chat)
	x := &requestExecutor{h: h, target: Target{BaseURL: s.URL}, record: &UsageRecord{}, maxSends: 1}
	r, e := x.Do(context.Background(), AdapterRequest{URL: s.URL, AttemptTimeout: 30 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	if _, e = io.ReadAll(r.Body); e == nil {
		t.Fatal("body read escaped attempt deadline")
	}
}
func TestStateKeyFencesGenerationAndAccount(t *testing.T) {
	s := scopedAdapterState{p: Principal{OrganizationID: "o", UserID: "u", KeyID: "k", MembershipGeneration: 1}, t: Target{Provider: "p", Model: "m"}, c: Credential{AccountID: "a"}}
	first := s.key("history")
	s.p.MembershipGeneration++
	if first == s.key("history") {
		t.Fatal("generation not fenced")
	}
	s.p.MembershipGeneration--
	s.c.AccountID = "b"
	if first == s.key("history") {
		t.Fatal("account not fenced")
	}
}
func TestDictationFrameLifecycle(t *testing.T) {
	v := &dictationFrames{}
	start := `{"type":"session.start","config":{"input_audio_format":"pcm16","sample_rate_hz":24000,"num_channels":1,"max_buffer_size_bytes":4096,"max_utterance_duration_ms":1000,"session_ttl_ms":10000,"provider_mode":"buffered","transcript_delivery_mode":"delta","vad":{"type":"server_vad","threshold":0.5,"prefix_padding_ms":0,"silence_duration_ms":500}}}`
	if v.validate([]byte(`{"type":"audio.append","audio":"AAA="}`)) || v.validate([]byte(strings.Replace(start, `"num_channels":1`, `"num_channels":2`, 1))) || !v.validate([]byte(start)) || v.validate([]byte(start)) {
		t.Fatal("invalid start transition")
	}
	if v.validate([]byte(`{"type":"audio.append","audio":"AA=="}`)) || !v.validate([]byte(`{"type":"audio.append","audio":"AAA="}`)) || !v.validate([]byte(`{"type":"session.close"}`)) || v.validate([]byte(`{"type":"audio.append","audio":"AAA="}`)) {
		t.Fatal("invalid audio/close transition")
	}
}

func TestTokenEstimateProjectsOnlyProtocolContent(t *testing.T) {
	b, e := decode([]byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"short","signature":"SIGNATURE"},{"type":"tool_use","input":{"type":"image","source":{"type":"base64","data":"SCHEMA_IS_TEXT"}}}]}]}`))
	if e != nil {
		t.Fatal(e)
	}
	native := estimateMessageTokens(b, "claude", TokenProjection{true, true, true})
	projected := estimateMessageTokens(b, "claude", TokenProjection{})
	if native <= projected {
		t.Fatal("reasoning was not projected")
	}
	if !strings.Contains(fmt.Sprint(b), "SIGNATURE") {
		t.Fatal("estimator mutated request")
	}
	if estimatedTextTokens("한국어한국어", "m") <= estimatedTextTokens("abcdef", "m") {
		t.Fatal("CJK density lost")
	}
	h, _, _, creds, u := harness(t, func(http.ResponseWriter, *http.Request) { t.Error("routed estimation must not send") }, Chat)
	w := call(h, "/v1/messages/count_tokens", `{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	if w.Code != 200 || creds.selections.Load() != 0 || u.records[0].Sends != 0 {
		t.Fatalf("%d %s %+v", w.Code, w.Body, u.records)
	}
}

func TestSupportTransportBudgetAtWrite(t *testing.T) {
	calls := 0
	h, s, a, _, _ := harness(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.RawQuery != "catalog=1" {
			t.Error("lost query")
		}
		io.WriteString(w, `{}`)
	}, Chat)
	rec := &UsageRecord{}
	ctx := requestSupportBudget(context.Background(), rec, 1)
	req, _ := http.NewRequest("GET", s.URL+"?catalog=1", nil)
	gate := func(ctx context.Context, start func(context.Context) error) error {
		return a.Dispatch(ctx, Principal{}, start)
	}
	resp, e := h.c.Destinations.Do(ctx, req, gate, nil)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if _, e = h.c.Destinations.Do(ctx, req, gate, nil); e == nil {
		t.Fatal("support send bypassed budget")
	}
	if calls != 1 || rec.Sends != 1 {
		t.Fatalf("calls=%d sends=%d", calls, rec.Sends)
	}
	a.denied.Store(true)
	ctx = requestSupportBudget(context.Background(), &UsageRecord{}, 1)
	if _, e = h.c.Destinations.Do(ctx, req, gate, nil); e == nil {
		t.Fatal("revoked support send allowed")
	}
	if calls != 1 {
		t.Fatal("revoked request reached upstream")
	}
}

type receiptMemory map[string]ProviderState

func (s receiptMemory) LoadProviderState(_ context.Context, _ Principal, ns, key string) (ProviderState, error) {
	return s[ns+key], nil
}
func (s receiptMemory) SaveProviderState(_ context.Context, _ Principal, ns, key string, v ProviderState) error {
	s[ns+key] = v
	return nil
}
func TestDirectHistoryRequiresScopedReceipt(t *testing.T) {
	h := &Handler{c: Config{Authorizer: &testAuth{}, ProviderStates: receiptMemory{}}}
	scope := scopedAdapterState{h: h, p: Principal{OrganizationID: "o", UserID: "u", KeyID: "k", MembershipGeneration: 1}, t: Target{Provider: "google", Wire: "google", Model: "m"}, c: Credential{AccountID: "a", Secret: "token"}}
	history := object{"messages": []any{object{"role": "assistant", "content": []any{object{"type": "thinking", "thinking": "reason", "signature": "opaque"}}}}}
	ctx := context.WithValue(context.Background(), replayScopeKey{}, scope)
	if h.validateReplay(ctx, history, scope) == nil {
		t.Fatal("unissued signature accepted")
	}
	if e := h.CaptureReplayReceipts(ctx, history); e != nil {
		t.Fatal(e)
	}
	if e := h.validateReplay(ctx, history, scope); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*scopedAdapterState){func(s *scopedAdapterState) { s.p.MembershipGeneration++ }, func(s *scopedAdapterState) { s.p.UserID = "other" }, func(s *scopedAdapterState) { s.c.AccountID = "b" }, func(s *scopedAdapterState) { s.t.Model = "other" }, func(s *scopedAdapterState) { s.t.Provider = "other" }} {
		other := scope
		mutate(&other)
		if h.validateReplay(ctx, history, other) == nil {
			t.Fatal("scope mismatch accepted")
		}
	}
	envelope := encodeReasoning(reasoningEnvelope{Signature: "opaque", Text: func() *string { s := "reason"; return &s }()})
	if e := h.validateReplay(ctx, object{"input": []any{object{"type": "reasoning", "encrypted_content": envelope}}}, scope); e != nil {
		t.Fatal("translated envelope lost valid binding", e)
	}
}

type repairFixture struct {
	endpoint string
	repairs  int
	prepared bool
}

func (a *repairFixture) PrepareContext(ctx context.Context) context.Context {
	a.prepared = true
	return context.WithValue(ctx, repairContextKey{}, true)
}

type repairContextKey struct{}

func (a *repairFixture) Build(ctx context.Context, _ AdapterInput) (AdapterRequest, error) {
	return AdapterRequest{URL: a.endpoint, Body: []byte(`{"schema":"bad"}`), ResponseWire: Chat}, nil
}
func (a *repairFixture) Decode(ctx context.Context, r *http.Response) (*http.Response, error) {
	if ctx.Value(repairContextKey{}) != true {
		return nil, ErrUnavailable
	}
	return r, nil
}
func (a *repairFixture) RepairRequest(ctx context.Context, in AdapterInput, r AdapterRequest, f HTTPFailure) (AdapterRequest, bool, error) {
	a.repairs++
	if ctx.Value(repairContextKey{}) != true || in.RequestID == "" {
		return r, false, ErrUnavailable
	}
	r.Body = []byte(`{"schema":"fixed"}`)
	return r, true, nil
}
func TestChangedBodyRepairRetainsPreparedContext(t *testing.T) {
	calls := 0
	h, s, _, _, u := harness(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "bad") {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"schema"}`)
			return
		}
		io.WriteString(w, fixture(Chat))
	}, Chat)
	a := &repairFixture{endpoint: s.URL}
	h.c.Adapters = map[Wire]Adapter{Chat: a}
	w := call(h, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 || calls != 2 || a.repairs != 1 || u.records[0].Sends != 2 {
		t.Fatalf("%d %s calls=%d repairs=%d", w.Code, w.Body, calls, a.repairs)
	}
}

func TestReceiptBindsSignedTextAndCalls(t *testing.T) {
	first, _ := signedReplayTokens(object{"type": "thinking", "thinking": "original", "signature": "sig"})
	changed, _ := signedReplayTokens(object{"type": "thinking", "thinking": "changed", "signature": "sig"})
	if len(first) != 1 || len(changed) != 1 || first[0] == changed[0] {
		t.Fatal("signature transferable to changed text")
	}
	first, _ = signedReplayTokens(object{"id": "call1", "function": object{"name": "read", "arguments": "{}"}, "extra_content": object{"google": object{"thought_signature": "sig"}}})
	changed, _ = signedReplayTokens(object{"id": "call1", "function": object{"name": "write", "arguments": "{}"}, "extra_content": object{"google": object{"thought_signature": "sig"}}})
	if len(first) != 1 || len(changed) != 1 || first[0] == changed[0] {
		t.Fatal("signature transferable to changed tool")
	}
	for _, v := range []object{{"type": "reasoning", "encrypted_content": "native"}, {"type": "reasoning.encrypted", "data": "native"}, {"type": "thinking", "thinking": "text", "signature": encodeReasoning(reasoningEnvelope{Kiro: "sig"})}, {"type": "reasoning", "encrypted_content": encodeReasoning(reasoningEnvelope{Native: object{"enc": "native"}})}} {
		tokens, e := signedReplayTokens(v)
		if e != nil || len(tokens) != 1 {
			t.Fatalf("missed native token: %v %v", v, e)
		}
	}
}

func TestCompactionUsesSelectedRouteAndBudget(t *testing.T) {
	h, _, _, _, u := harness(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `"tools"`) || !strings.Contains(string(b), "context checkpoint") {
			t.Errorf("bad compact request %s", b)
		}
		io.WriteString(w, fixture(Chat))
	}, Chat)
	w := call(h, "/v1/responses/compact", `{"model":"m","input":[{"role":"user","content":"original task"}],"tools":[{"type":"function","name":"unused","parameters":{}}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "original task") || !strings.Contains(w.Body.String(), checkpointPrefix) || u.records[0].Sends != 1 {
		t.Fatalf("%d %s %+v", w.Code, w.Body, u.records)
	}
}
func Test507IsNotTransient(t *testing.T) {
	calls := 0
	h, _, _, _, u := harness(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(507); io.WriteString(w, `{}`) }, Chat)
	router := h.c.Router.(testRouter)
	router.route.Targets[0].Retry.TransientSends = 3
	h.c.Router = router
	w := call(h, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 507 || calls != 1 || u.records[0].Outcome != OutcomeUpstreamError {
		t.Fatalf("%d calls=%d %+v", w.Code, calls, u.records)
	}
}

func TestSignedToolReceiptCrossWireCanonicalIdentity(t *testing.T) {
	forms := []object{
		{"id": "c", "function": object{"name": "ns__f", "arguments": "{\"n\":1234567890123456789}"}, "signature": "sig"},
		{"type": "function_call", "call_id": "c", "name": "f", "namespace": "ns", "arguments": "{\"n\":1234567890123456789}", "signature": "sig"},
		{"type": "tool_use", "id": "c", "name": "ns__f", "input": object{"n": json.Number("1234567890123456789")}, "signature": "sig"},
	}
	var expected string
	for _, form := range forms {
		tokens, e := signedReplayTokens(form)
		if e != nil || len(tokens) != 1 {
			t.Fatal(e, tokens)
		}
		if expected == "" {
			expected = tokens[0]
		} else if expected != tokens[0] {
			t.Fatal("cross-wire tool binding changed")
		}
	}
}
