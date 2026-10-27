// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"bytes"
	"context"
	"encoding/json"
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
	return x.do(ctx, r, false)
}
func (x *requestExecutor) do(ctx context.Context, r AdapterRequest, reserved bool) (*http.Response, error) {
	var policyErr error
	r, policyErr = ApplyRequestPolicy(x.target, r)
	if policyErr != nil {
		return nil, policyErr
	}
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
	if !reserved && x.record.Sends+x.reserved >= x.maxSends {
		x.mu.Unlock()
		return nil, &Error{429, "request_send_budget_exhausted", 0}
	}
	if !reserved {
		x.reserved++
	}
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

func (x *requestExecutor) RecordUsage(u Usage) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.record.IntermediateUsage) >= x.record.Sends {
		return
	}
	copyToken := func(p *int64) *int64 {
		if p == nil || *p < 0 {
			return nil
		}
		v := *p
		return &v
	}
	u.InputTokens = copyToken(u.InputTokens)
	u.OutputTokens = copyToken(u.OutputTokens)
	u.CacheReadTokens = copyToken(u.CacheReadTokens)
	u.CacheWriteTokens = copyToken(u.CacheWriteTokens)
	x.record.IntermediateUsage = append(x.record.IntermediateUsage, u)
}
func aggregateIntermediateUsage(rec *UsageRecord) {
	add := func(a, b *int64) *int64 {
		if a == nil || b == nil || *a < 0 || *b < 0 || *a > int64(^uint64(0)>>1)-*b {
			return nil
		}
		v := *a + *b
		return &v
	}
	for _, u := range rec.IntermediateUsage {
		rec.Usage.InputTokens = add(rec.Usage.InputTokens, u.InputTokens)
		rec.Usage.OutputTokens = add(rec.Usage.OutputTokens, u.OutputTokens)
		rec.Usage.CacheReadTokens = add(rec.Usage.CacheReadTokens, u.CacheReadTokens)
		rec.Usage.CacheWriteTokens = add(rec.Usage.CacheWriteTokens, u.CacheWriteTokens)
	}
}

func (x *requestExecutor) RecordContinuationHistory(raw json.RawMessage, wire Wire) error {
	if len(raw) > 32<<20 {
		return &Error{502, "continuation_too_large", 0}
	}
	body, e := decode(raw)
	if e != nil {
		return e
	}
	if wire != Responses {
		body, e = translateRequest(body, wire, Responses)
		if e != nil {
			return e
		}
	}
	encoded, e := json.Marshal(body["input"])
	if e != nil {
		return e
	}
	if len(encoded) > 32<<20 {
		return &Error{502, "continuation_too_large", 0}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.record.continuationInput = body["input"]
	return nil
}

func (x *requestExecutor) Recover(ctx context.Context, n int, run func(AdapterExecutor) (*http.Response, error)) (*http.Response, error) {
	if n < 1 || n > x.maxSends || run == nil {
		return nil, &Error{429, "request_send_budget_exhausted", 0}
	}
	lease := &recoveryExecutor{requestExecutor: x}
	// Dispatch holds the authoritative fence only for reservation, never callback I/O.
	e := x.h.c.Authorizer.(Dispatcher).Dispatch(ctx, x.p, func(gated context.Context) error {
		if e := gated.Err(); e != nil {
			return e
		}
		x.mu.Lock()
		defer x.mu.Unlock()
		if x.record.Sends+x.reserved+n > x.maxSends {
			return &Error{429, "request_send_budget_exhausted", 0}
		}
		x.reserved += n
		lease.remaining = n
		return nil
	})
	if e != nil {
		return nil, e
	}
	defer lease.release()
	return run(lease)
}

type recoveryExecutor struct {
	*requestExecutor
	leaseMu   sync.Mutex
	remaining int
}

func (x *recoveryExecutor) Do(ctx context.Context, r AdapterRequest) (*http.Response, error) {
	x.leaseMu.Lock()
	if x.remaining < 1 {
		x.leaseMu.Unlock()
		return nil, &Error{429, "request_send_budget_exhausted", 0}
	}
	x.remaining--
	x.leaseMu.Unlock()
	return x.requestExecutor.do(ctx, r, true)
}
func (x *recoveryExecutor) release() {
	x.leaseMu.Lock()
	defer x.leaseMu.Unlock()
	x.mu.Lock()
	defer x.mu.Unlock()
	x.reserved -= x.remaining
	x.remaining = 0
}

func (x *requestExecutor) AuthorizeRecovery(ctx context.Context, reason AdapterRecoveryReason) error {
	switch reason {
	case RecoveryInvalidArgument, RecoveryToolResultEcho, RecoveryRoutingCommentary, RecoveryContextOverflow:
	default:
		return ErrUnsupported
	}
	if e := x.h.guard(ctx, x.p); e != nil {
		return e
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.record.Sends+x.reserved >= x.maxSends {
		return &Error{429, "request_send_budget_exhausted", 0}
	}
	return nil
}
