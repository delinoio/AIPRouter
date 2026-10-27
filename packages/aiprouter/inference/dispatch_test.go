// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type gateAuth struct {
	*testAuth
	mu      sync.Mutex
	arrived chan struct{}
	proceed chan struct{}
}

func (a *gateAuth) Dispatch(ctx context.Context, p Principal, start func(context.Context) error) error {
	if a.arrived != nil {
		close(a.arrived)
		select {
		case <-a.proceed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.BeforeSend(ctx, p); e != nil {
		return e
	}
	return start(ctx)
}
func TestRevokedBetweenSelectionAndPhysicalWrite(t *testing.T) {
	h, _, a, _, u := harness(t, func(http.ResponseWriter, *http.Request) { t.Error("revoked request reached upstream") }, Chat)
	g := &gateAuth{testAuth: a, arrived: make(chan struct{}), proceed: make(chan struct{})}
	h.c.Authorizer = g
	done := make(chan int, 1)
	go func() { w := call(h, "/v1/chat/completions", `{"model":"m","messages":[]}`); done <- w.Code }()
	<-g.arrived
	a.denied.Store(true)
	close(g.proceed)
	if code := <-done; code != 401 {
		t.Fatalf("status %d", code)
	}
	if u.records[0].Sends != 0 {
		t.Fatal("unsent request charged as send")
	}
}
func TestSlowResponseDoesNotHoldDispatchGate(t *testing.T) {
	received := make(chan struct{})
	finish := make(chan struct{})
	h, _, a, _, _ := harness(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(received)
		<-finish
		io.WriteString(w, fixture(Chat))
	}, Chat)
	g := &gateAuth{testAuth: a}
	h.c.Authorizer = g
	done := make(chan struct{})
	go func() { defer close(done); call(h, "/v1/chat/completions", `{"model":"m","messages":[]}`) }()
	<-received
	admitted := make(chan struct{})
	go func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		_, release, e := g.Admit(context.Background(), "client-secret")
		if e == nil {
			release()
		}
		close(admitted)
	}()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		close(finish)
		t.Fatal("dispatch gate held while waiting for headers")
	}
	close(finish)
	<-done
}
func TestSSEMultilineAndCRLF(t *testing.T) {
	var got string
	e := readSSE(strings.NewReader(": heartbeat\r\ndata: first\r\ndata: second\r\n\r\n"), func(_ string, b []byte) error { got = string(b); return nil })
	if e != nil || got != "first\nsecond" {
		t.Fatalf("%q %v", got, e)
	}
}
