// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"bytes"
	"context"
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
}

func (x *requestExecutor) Do(ctx context.Context, r AdapterRequest) (*http.Response, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.record.Sends >= x.maxSends {
		return nil, &Error{429, "request_send_budget_exhausted", 0}
	}
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
	sent := gateClient(client, ctx, x.p, x.h.c.Authorizer, func() { x.record.Sends++; x.h.event(ctx, "upstream_send") })
	resp, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if sent.Load() {
			x.record.UnmeasuredAttempts++
			return nil, ErrAmbiguous
		}
		return nil, e
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		resp.Body.Close()
		return nil, ErrDestination
	}
	return resp, nil
}
