// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"
)

type requestExecutor struct {
	h        *Handler
	p        Principal
	target   Target
	record   *UsageRecord
	maxSends int
	mu       sync.Mutex
	reserved int
}

func (x *requestExecutor) Do(ctx context.Context, r AdapterRequest) (*http.Response, error) {
	cancel := func() {}
	if r.AttemptTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, r.AttemptTimeout)
	}
	keepDeadline := false
	defer func() {
		if !keepDeadline {
			cancel()
		}
	}()
	x.mu.Lock()
	if x.record.Sends+x.reserved >= x.maxSends {
		x.mu.Unlock()
		return nil, &Error{429, "request_send_budget_exhausted", 0}
	}
	x.reserved++
	x.mu.Unlock()
	claimed := false
	defer func() {
		if !claimed {
			x.mu.Lock()
			x.reserved--
			x.mu.Unlock()
		}
	}()
	u, e := url.Parse(r.URL)
	base, _ := url.Parse(x.target.BaseURL)
	if e != nil || u.User != nil || u.Fragment != "" || origin(u) != origin(base) {
		return nil, ErrDestination
	}
	method := r.Method
	if method == "" {
		method = "POST"
	}
	if method != "POST" && method != "GET" {
		return nil, ErrUnsupported
	}
	client, _, e := x.h.c.Destinations.client(x.target.BaseURL)
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, method, r.URL, bytes.NewReader(r.Body))
	if e != nil {
		return nil, e
	}
	req.Header = r.Headers.Clone()
	sent := gateClient(client, ctx, x.p, x.h.c.Authorizer, func() {
		x.mu.Lock()
		x.reserved--
		x.record.Sends++
		claimed = true
		x.mu.Unlock()
		x.h.event(ctx, "upstream_send")
	})
	resp, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if sent.Load() {
			x.mu.Lock()
			x.record.UnmeasuredAttempts++
			x.mu.Unlock()
			return nil, ErrAmbiguous
		}
		return nil, e
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != 304 {
		resp.Body.Close()
		return nil, ErrDestination
	}
	resp.Body = &executorBody{ReadCloser: resp.Body, cancel: cancel}
	keepDeadline = true
	return resp, nil
}

// A deadline remains active through response consumption, not merely until headers.
type executorBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *executorBody) Close() error { defer b.cancel(); return b.ReadCloser.Close() }

func (x *requestExecutor) RecordUnmeasuredAttempt() {
	x.mu.Lock()
	defer x.mu.Unlock()
	// A helper may only mark an already sent physical exchange.
	if x.record.UnmeasuredAttempts < x.record.Sends {
		x.record.UnmeasuredAttempts++
	}
}
