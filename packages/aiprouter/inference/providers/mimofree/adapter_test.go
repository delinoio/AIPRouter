// SPDX-License-Identifier: Apache-2.0
package mimofree

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
)

type memoryState struct {
	mu   sync.Mutex
	data []byte
	err  error
}

func (s *memoryState) Load(context.Context, string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...), s.err
}
func (s *memoryState) Save(_ context.Context, _ string, b []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append([]byte(nil), b...)
	return s.err
}
func (s *memoryState) Update(ctx context.Context, _ string, ttl time.Duration, f func([]byte) ([]byte, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ttl > time.Hour {
		return errors.New("ttl")
	}
	b, e := f(s.data)
	if e == nil {
		s.data = append([]byte(nil), b...)
	}
	return e
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: &trackedBody{Reader: strings.NewReader(body)}}
}

type executor struct {
	requests      []inference.AdapterRequest
	statuses      []int
	budget        int
	denied        bool
	first         *http.Response
	bootstrapBody string
}

func (x *executor) Do(ctx context.Context, r inference.AdapterRequest) (*http.Response, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if x.denied {
		return nil, inference.ErrUnauthorized
	}
	if len(x.requests) >= x.budget {
		return nil, &inference.Error{Status: 429, Code: "request_send_budget_exhausted"}
	}
	r.Headers = r.Headers.Clone()
	x.requests = append(x.requests, r)
	if r.URL == BootstrapURL {
		b := x.bootstrapBody
		if b == "" {
			b = `{"jwt":"fixture.token.signature"}`
		}
		return response(200, b), nil
	}
	status := 200
	if len(x.statuses) > 0 {
		status = x.statuses[0]
		x.statuses = x.statuses[1:]
	}
	out := response(status, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	if x.first == nil {
		x.first = out
	}
	return out, nil
}
func (x *executor) Recover(ctx context.Context, n int, f func(inference.AdapterExecutor) (*http.Response, error)) (*http.Response, error) {
	if x.denied {
		return nil, inference.ErrUnauthorized
	}
	if len(x.requests)+n > x.budget {
		return nil, &inference.Error{Status: 429, Code: "request_send_budget_exhausted"}
	}
	return f(x)
}
func input() inference.AdapterInput {
	return inference.AdapterInput{Operation: inference.Generate, Wire: inference.Chat, Target: inference.Target{Wire: Wire, BaseURL: ChatURL, Model: "mimo-v2-flash"}, Body: json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`)}
}

func TestCanonicalAndMarker(t *testing.T) {
	for _, s := range []string{"https://evil.example/api/free-ai/openai/chat", ChatURL + "?x=1", ChatURL + "?", ChatURL + "#x", "https://user@api.xiaomimimo.com/api/free-ai/openai/chat", "http://api.xiaomimimo.com/api/free-ai/openai/chat", "https://api.xiaomimimo.com:443/api/free-ai/openai/chat"} {
		in := input()
		in.Target.BaseURL = s
		if _, err := New().Build(context.Background(), in); !errors.Is(err, inference.ErrDestination) {
			t.Errorf("accepted %s: %v", s, err)
		}
	}
	in := input()
	in.Target.BaseURL += "/"
	in.Target.Headers = http.Header{"Authorization": {"leak"}, "Cookie": {"leak"}}
	in.Credential.Secret = "leak"
	req, e := New().Build(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	var body map[string]any
	json.Unmarshal(req.Body, &body)
	messages := body["messages"].([]any)
	if len(messages) != 2 || messages[0].(map[string]any)["content"] != SystemMarker {
		t.Fatal(string(req.Body))
	}
	if req.Headers.Get("Authorization") != "" || req.Headers.Get("Cookie") != "" || req.Headers.Get("X-Mimo-Source") != "mimocode-cli-free" {
		t.Fatal(req.Headers)
	}
	in.Body = req.Body
	req, e = New().Build(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	json.Unmarshal(req.Body, &body)
	if len(body["messages"].([]any)) != 2 {
		t.Fatal("duplicate marker")
	}
}
func TestExecutionRecoveryAndCache(t *testing.T) {
	for _, tc := range []struct {
		name         string
		statuses     []int
		budget, want int
		status       int
	}{{"normal", []int{200}, 4, 2, 200}, {"401 once", []int{401, 200}, 4, 4, 200}, {"second401", []int{401, 401}, 8, 4, 401}, {"403 never", []int{403}, 8, 2, 403}, {"insufficient two-send admission", []int{401}, 3, 2, 401}} {
		t.Run(tc.name, func(t *testing.T) {
			s := &memoryState{}
			x := &executor{statuses: tc.statuses, budget: tc.budget}
			r, w, e := New().Execute(context.Background(), input(), x, s)
			if e != nil {
				t.Fatal(e)
			}
			defer r.Body.Close()
			if w != inference.Chat || r.StatusCode != tc.status || len(x.requests) != tc.want {
				t.Fatalf("wire %s status %d sends %d", w, r.StatusCode, len(x.requests))
			}
			if tc.budget == 3 && x.first.Body.(*trackedBody).closed {
				t.Fatal("denied recovery consumed response")
			}
			affinity := ""
			client := ""
			for _, req := range x.requests {
				if req.URL == BootstrapURL {
					if req.Headers.Get("Authorization") != "" || req.AttemptTimeout != 15*time.Second {
						t.Fatal("bootstrap credentials/deadline")
					}
					var b map[string]string
					json.Unmarshal(req.Body, &b)
					if client != "" && client != b["client"] {
						t.Fatal("client changed")
					}
					client = b["client"]
				} else {
					if req.Headers.Get("Authorization") != "Bearer fixture.token.signature" {
						t.Fatal(req.Headers)
					}
					v := req.Headers.Get("X-Session-Affinity")
					if v == "" || (affinity != "" && affinity != v) {
						t.Fatal("affinity changed")
					}
					affinity = v
				}
			}
			if tc.status == 200 {
				second := &executor{budget: 1}
				r, _, e := New().Execute(context.Background(), input(), second, s)
				if e != nil {
					t.Fatal(e)
				}
				r.Body.Close()
				if len(second.requests) != 1 || second.requests[0].URL != ChatURL {
					t.Fatal("cache not used")
				}
			}
		})
	}
}
func TestBootstrapBoundsAndStateFailure(t *testing.T) {
	for _, b := range []string{`{}`, `{"jwt":"line\nbreak"}`, `{"jwt":"` + strings.Repeat("x", maxJWT+1) + `"}`, strings.Repeat(" ", maxBootstrap+1), "\xff"} {
		x := &executor{budget: 4, bootstrapBody: b}
		_, _, e := New().Execute(context.Background(), input(), x, &memoryState{})
		if e == nil || len(x.requests) != 1 {
			t.Fatalf("bootstrap accepted or chat sent: %v", e)
		}
	}
	for _, s := range []*memoryState{{err: inference.ErrUnauthorized}, {data: []byte(`{}`)}} {
		x := &executor{budget: 4}
		_, _, e := New().Execute(context.Background(), input(), x, s)
		if e == nil || len(x.requests) != 0 {
			t.Fatal("state failure did not fail closed")
		}
	}
}
func TestJWTExpiryAndConcurrentIdentity(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	payload, _ := json.Marshal(map[string]int64{"exp": now.Add(10 * time.Minute).Unix()})
	token := "x." + base64.RawURLEncoding.EncodeToString(payload) + ".x"
	if got := tokenExpiry(token, now); !got.Equal(now.Add(10 * time.Minute)) {
		t.Fatal(got)
	}
	s := &memoryState{}
	var wg sync.WaitGroup
	ids := make(chan string, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := loadSession(context.Background(), s)
			if e != nil {
				t.Error(e)
				return
			}
			ids <- v.Client + v.Affinity
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first != "" && id != first {
			t.Fatal("identity race")
		}
		first = id
	}
}
