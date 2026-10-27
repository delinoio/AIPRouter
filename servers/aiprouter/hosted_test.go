// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/delinoio/AIPRouter/packages/aiprouter/artifacts"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference/hosted"
	"github.com/delinoio/AIPRouter/packages/aiprouter/store"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestHostedPhysicalBudgetAndIndependentReceipts(t *testing.T) {
	var writes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"usage":{"input_tokens":2,"output_tokens":3}}`)
	}))
	defer srv.Close()
	var mu sync.Mutex
	receipts := map[string]store.Usage{}
	tr := &hostedTransport{base: srv.URL, policy: inference.DestinationPolicy{ApprovedOrigins: []string{srv.URL}}, gate: func(c context.Context, f func(context.Context) error) error { return f(c) }, record: func(_ context.Context, id string, u store.Usage) error {
		mu.Lock()
		defer mu.Unlock()
		receipts[id] = u
		return nil
	}}
	var sends int
	ctx := inference.WithPhysicalSendBudget(context.Background(), func(c context.Context, start func(context.Context) error) error {
		if sends == 2 {
			return inference.ErrUnavailable
		}
		sends++
		return start(c)
	})
	for i := 0; i < 3; i++ {
		_, e := tr.Do(ctx, inference.AdapterRequest{Method: "POST", URL: srv.URL})
		if (e != nil) != (i == 2) {
			t.Fatalf("send %d: %v", i, e)
		}
	}
	if writes.Load() != 2 || sends != 2 || len(receipts) != 2 {
		t.Fatalf("writes=%d budget=%d receipts=%d", writes.Load(), sends, len(receipts))
	}
	for _, u := range receipts {
		if u.InputTokens == nil || *u.InputTokens != 2 || u.OutputTokens == nil || *u.OutputTokens != 3 {
			t.Fatal("known usage lost")
		}
	}
}
func TestHostedAmbiguousCancellationAndReceiptFailure(t *testing.T) {
	for _, kind := range []string{"ambiguous", "cancel", "record-failure"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "ambiguous" {
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
					return
				}
				if kind == "cancel" {
					cancel()
					<-r.Context().Done()
					return
				}
				io.WriteString(w, `{}`)
			}))
			defer srv.Close()
			var got []store.Usage
			tr := &hostedTransport{base: srv.URL, policy: inference.DestinationPolicy{ApprovedOrigins: []string{srv.URL}}, gate: func(c context.Context, f func(context.Context) error) error { return f(c) }, record: func(c context.Context, _ string, u store.Usage) error {
				if c.Err() != nil {
					t.Error("accounting cleanup canceled")
				}
				got = append(got, u)
				if kind == "record-failure" {
					return errors.New("db unavailable")
				}
				return nil
			}}
			_, e := tr.Do(ctx, inference.AdapterRequest{Method: "POST", URL: srv.URL})
			if e == nil || len(got) != 1 {
				t.Fatalf("err=%v receipts=%d", e, len(got))
			}
			if kind == "ambiguous" && (!errors.Is(e, inference.ErrAmbiguous) || got[0].Outcome != store.OutcomeUnknown) {
				t.Fatalf("ambiguous: %v %+v", e, got)
			}
			if kind == "cancel" && got[0].Outcome != store.OutcomeCanceled {
				t.Fatal("cancellation outcome lost")
			}
			if got[0].InputTokens != nil {
				t.Fatal("unknown usage became zero")
			}
		})
	}
}
func TestHostedImageMappingAndNativePolicy(t *testing.T) {
	body, path, e := hostedMediaBody(map[string]any{"prompt": "draw", "size": "1920x1080", "quality": "hd", "image_url": "https://input.test/a"}, nil, "image-model", false)
	if e != nil || path != "/images/edits" || body["aspect_ratio"] != "16:9" || body["resolution"] != "2k" {
		t.Fatalf("%+v %s %v", body, path, e)
	}
	body, _, e = hostedMediaBody(map[string]any{"prompt": "draw", "aspect_ratio": "auto", "size": "1920x1080"}, nil, "image-model", false)
	if e != nil || body["aspect_ratio"] != nil {
		t.Fatal("explicit auto did not override size")
	}
	cfg := hostedWrapperConfig(&v1.HostedSettings{Images: &v1.HostedImageSettings{Target: &v1.HostedHelperTarget{}, BridgeEnabled: true, VideoBridgeEnabled: true}}, inference.AdapterInput{Target: inference.Target{BaseURL: "https://api.openai.com/v1"}}, nil)
	if !cfg.NativeTools[hosted.Image] || cfg.EnableVideo {
		t.Fatal("native model media overridden")
	}
}
func TestHostedSSETerminalUsage(t *testing.T) {
	b := []byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":9}}}\n\n")
	u := hostedReportedUsage(b, "text/event-stream")
	if u.InputTokens == nil || *u.InputTokens != 7 || u.OutputTokens == nil || *u.OutputTokens != 9 {
		t.Fatal(u)
	}
	u = hostedReportedUsage([]byte("data: {\"usage\":{\"input_tokens\":7}}\n\n"), "text/event-stream")
	if u.InputTokens != nil {
		t.Fatal("partial stream confirmed")
	}
}

func TestHostedResolverMediaOwnerFenceAndVideoLifecycle(t *testing.T) {
	auth, sc, key := dispatchFixture(t)
	ctx := context.Background()
	p, release, e := auth.Admit(ctx, key)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	d := providerData{Metadata: &v1.ProviderConnection{Id: "helper", ProviderId: "xai", AuthKind: v1.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY, State: v1.ConnectionState_CONNECTION_STATE_READY}, Secret: "helper-secret"}
	putRoutingProvider(t, auth, sc, d)
	cfg := &v1.HostedSettings{Images: &v1.HostedImageSettings{Target: &v1.HostedHelperTarget{Selection: &v1.HostedHelperTarget_ConnectionId{ConnectionId: "helper"}}, VideoBridgeEnabled: true}}
	b, _ := protojson.Marshal(cfg)
	if e = auth.s.PutPrivate(ctx, sc, "hosted-helpers", "settings", b); e != nil {
		t.Fatal(e)
	}
	objects := &objectFixture{objects: map[string][]byte{}}
	r := &hostedResolver{auth: auth, artifacts: artifacts.New(auth.s, objects)}
	req := hosted.OperationRequest{Principal: p, RequestID: "turn", CallID: "video", Kind: hosted.Video, Arguments: json.RawMessage(`{"prompt":"fixture"}`)}
	executor, e := r.Resolve(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	op := executor.(*hostedOperation)
	if op.credential.Secret != "helper-secret" || op.target.BaseURL != "https://api.x.ai/v1" {
		t.Fatal("helper did not select pinned independent credential")
	}
	var polls int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/videos/generations":
			if r.Header.Get("Authorization") != "Bearer helper-secret" {
				t.Error("wrong credential")
			}
			io.WriteString(w, `{"request_id":"job"}`)
		case "/videos/job":
			polls++
			if polls == 1 {
				io.WriteString(w, `{"status":"processing"}`)
			} else {
				json.NewEncoder(w).Encode(map[string]any{"status": "completed", "video": map[string]any{"url": srv.URL + "/download"}})
			}
		case "/download":
			if r.Header.Get("Authorization") != "" {
				t.Error("credential leaked to download")
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Write([]byte{1, 2, 3})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	op.transport.base = srv.URL
	op.transport.policy.ApprovedOrigins = []string{srv.URL}
	var delays []time.Duration
	op.wait = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	sends := 0
	ctx = inference.WithPhysicalSendBudget(ctx, func(c context.Context, start func(context.Context) error) error { sends++; return start(c) })
	result, e := op.Execute(ctx, req)
	if e != nil {
		t.Fatal(e)
	}
	if sends != 4 || len(result.Artifacts) != 1 || result.Artifacts[0].ContentType != "video/mp4" || len(objects.objects) != 1 || len(delays) != 2 || delays[0] != 5*time.Second || delays[1] != 7500*time.Millisecond {
		t.Fatalf("sends=%d result=%+v delays=%v", sends, result, delays)
	}
	// Replacing the helper account must fence a resolved operation at physical write.
	d.Secret = "new-secret"
	putRoutingProvider(t, auth, sc, d)
	if _, e = op.Execute(ctx, req); e == nil {
		t.Fatal("replaced helper credential sent")
	}
	if sends != 4 {
		t.Fatal("denied send charged budget")
	}
	bad := req
	bad.Principal.UserID = "other"
	if _, e = op.Execute(ctx, bad); !errors.Is(e, inference.ErrUnauthorized) {
		t.Fatal("operation principal rebound")
	}
}
func TestHostedPoolJSONAndSettingsOwnership(t *testing.T) {
	auth, sc, _ := dispatchFixture(t)
	ctx := context.Background()
	d := providerData{Metadata: &v1.ProviderConnection{Id: "helper", ProviderId: "xai", AuthKind: v1.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY, State: v1.ConnectionState_CONNECTION_STATE_READY}, Secret: "fixture"}
	putRoutingProvider(t, auth, sc, d)
	pool := &v1.Pool{Id: "helpers", Members: []*v1.PoolMember{{ConnectionId: "helper", Weight: 1}}}
	b, _ := json.Marshal(pool)
	if e := auth.s.PutPrivate(ctx, sc, "pool", "helpers", b); e != nil {
		t.Fatal(e)
	}
	cfg := &v1.HostedSettings{Images: &v1.HostedImageSettings{Target: &v1.HostedHelperTarget{Selection: &v1.HostedHelperTarget_PoolId{PoolId: "helpers"}}, BridgeEnabled: true}}
	if e := validateHostedSettings(ctx, auth.s, sc, cfg); e != nil {
		t.Fatal(e)
	}
	cfg.Images.Target.Selection = &v1.HostedHelperTarget_ConnectionId{ConnectionId: "someone-else"}
	if e := validateHostedSettings(ctx, auth.s, sc, cfg); e == nil {
		t.Fatal("foreign target accepted")
	}
}

func TestHostedStandardAdapterKeepsNonChatPaths(t *testing.T) {
	for _, op := range []inference.Operation{inference.ImageGeneration, inference.Search, inference.Compact, inference.CountTokens} {
		r, e := (hostedStandardAdapter{}).Build(context.Background(), inference.AdapterInput{Operation: op, Wire: inference.Chat, Target: inference.Target{Wire: inference.Responses, BaseURL: "https://fixture.test/v1", Model: "m"}, Body: json.RawMessage(`{"query":"q"}`)})
		if e != nil || strings.HasSuffix(r.URL, "/responses") {
			t.Fatalf("%s %s %v", op, r.URL, e)
		}
	}
}
