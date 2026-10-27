// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

type ProviderState struct {
	Data      []byte
	ExpiresAt time.Time
}

// ProviderStates stores encrypted temporary provider state. Implementations must atomically
// enforce live membership/generation and key revocation, TTL, and oldest-first limits per
// org/user/namespace. Cursor namespace: 15min,64 entries,16MiB total. No backup inclusion.
type ProviderStates interface {
	LoadProviderState(context.Context, Principal, string, string) (ProviderState, error)
	SaveProviderState(context.Context, Principal, string, string, ProviderState) error
}

// AdapterStateStore is already bound to principal, provider, exact account, model and
// credential digest. IDs must be public conversation IDs, never raw credential values.
type AdapterStateStore interface {
	Load(context.Context, string) ([]byte, error)
	Save(context.Context, string, []byte, time.Duration) error
}
type scopedAdapterState struct {
	h *Handler
	p Principal
	t Target
	c Credential
}

func (s scopedAdapterState) key(id string) string {
	sum := sha256.Sum256([]byte(s.t.Provider + "\x00" + s.t.BaseURL + "\x00" + s.t.Model + "\x00" + s.c.AccountID + "\x00" + s.c.Secret + "\x00" + id))
	return hex.EncodeToString(sum[:])
}
func (s scopedAdapterState) Load(ctx context.Context, id string) ([]byte, error) {
	if s.h.c.ProviderStates == nil {
		return nil, ErrContinuation
	}
	if e := s.h.guard(ctx, s.p); e != nil {
		return nil, e
	}
	state, e := s.h.c.ProviderStates.LoadProviderState(ctx, s.p, string(s.t.Wire), s.key(id))
	if e != nil || !state.ExpiresAt.After(time.Now()) {
		return nil, ErrContinuation
	}
	return state.Data, nil
}
func (s scopedAdapterState) Save(ctx context.Context, id string, data []byte, ttl time.Duration) error {
	if s.h.c.ProviderStates == nil {
		return ErrContinuation
	}
	if len(data) > 16<<20 || ttl <= 0 || ttl > 15*time.Minute {
		return &Error{400, "invalid_provider_state", 0}
	}
	if e := s.h.guard(ctx, s.p); e != nil {
		return e
	}
	return s.h.c.ProviderStates.SaveProviderState(ctx, s.p, string(s.t.Wire), s.key(id), ProviderState{Data: data, ExpiresAt: time.Now().Add(ttl)})
}
