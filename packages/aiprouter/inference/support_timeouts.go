// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"context"
	"net"
	"time"
)

// SupportTimeouts adds phase deadlines without replacing the parent's total deadline.
// Connect covers DNS/TCP/targetTLS; Idle covers first headers and subsequent reads.
// Nonpositive values leave that phase's existing defaults in place.
type SupportTimeouts struct{ Connect, Idle time.Duration }
type supportTimeoutKey struct{}

func WithSupportTimeouts(ctx context.Context, timeouts SupportTimeouts) context.Context {
	return context.WithValue(ctx, supportTimeoutKey{}, timeouts)
}
func supportConnectContext(ctx context.Context) (context.Context, context.CancelFunc) {
	options, _ := ctx.Value(supportTimeoutKey{}).(SupportTimeouts)
	if options.Connect > 0 {
		return context.WithTimeout(ctx, options.Connect)
	}
	return ctx, func() {}
}

type supportIdleConn struct {
	net.Conn
	idle time.Duration
}

func (c *supportIdleConn) Read(p []byte) (int, error) {
	n, e := c.Conn.Read(p)
	if n > 0 {
		c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	}
	return n, e
}
func withSupportIdle(ctx context.Context, c net.Conn) net.Conn {
	options, _ := ctx.Value(supportTimeoutKey{}).(SupportTimeouts)
	if options.Idle <= 0 {
		return c
	}
	c.SetReadDeadline(time.Now().Add(options.Idle))
	return &supportIdleConn{Conn: c, idle: options.Idle}
}
