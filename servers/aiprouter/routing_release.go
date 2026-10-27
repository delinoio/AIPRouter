// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"sync"

	"github.com/delinoio/AIPRouter/packages/aiprouter/store"
)

// A completed response is proof that its admission can be released. Keep retry
// work until PostgreSQL acknowledges it; elapsed time is never such proof.
// The host drains HTTP/WebSocket requests before Wait, then calls Close. A hard
// process exit requires the explicit fenced terminated-instance store workflow.
type admissionReleaseQueue struct {
	ctx       context.Context
	cancel    context.CancelFunc
	s         *store.Store
	telemetry *telemetry
	wg        sync.WaitGroup
}

func newAdmissionReleaseQueue(lifetime context.Context, s *store.Store, t *telemetry) *admissionReleaseQueue {
	ctx, cancel := context.WithCancel(lifetime)
	return &admissionReleaseQueue{ctx: ctx, cancel: cancel, s: s, telemetry: t}
}
func (q *admissionReleaseQueue) Submit(lease store.Lease, cleanup func(context.Context)) {
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		if e := q.s.ReleaseUntilRecorded(q.ctx, lease); e != nil {
			q.telemetry.Event(q.ctx, "admission_release_pending_reconciliation")
			return
		}
		if cleanup != nil {
			cleanup(q.ctx)
		}
	}()
}
func (q *admissionReleaseQueue) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { q.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (q *admissionReleaseQueue) Close() { q.cancel() }
