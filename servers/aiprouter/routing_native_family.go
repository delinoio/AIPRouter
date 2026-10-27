// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"github.com/delinoio/AIPRouter/packages/aiprouter/store"
)

// A stale family refusal admits one physical model send, not a usage HTTP probe.
// The permit remains PostgreSQL-owned across instances and credential changes.
type nativeFamilyReceipt struct {
	Account, Version, Family string
	Permit                   store.NativeHealthPermit
	Finished                 bool
}

func nativeFamilyKey(id, version, family string) store.NativeHealthKey {
	return store.NativeHealthKey{AccountID: id, CredentialVersion: version, Model: family, Lane: store.NativeHealthFamily}
}
func (a *inferenceAdapter) nativeFamilyEligible(ctx context.Context, p inference.Principal, c store.SelectionCandidate, model string) (bool, error) {
	family := nativeModelFamily(model)
	if family == "" {
		return true, nil
	}
	lease, err := a.current(p)
	if err != nil {
		return false, err
	}
	health, err := a.s.GetNativeHealth(ctx, lease, nativeFamilyKey(c.ID, c.Version, family))
	if err != nil {
		return false, inferenceError(err)
	}
	return !health.CooldownUntil.After(health.Now) && health.ProbeID == "", nil
}
func (a *inferenceAdapter) dispatchNativeFamily(ctx context.Context, p inference.Principal, start func(context.Context) error) (bool, error) {
	a.mu.Lock()
	target := a.activeTarget
	id, version := a.activeAccount, a.activeVersion
	d := a.connections[id]
	state := a.native[selectionKey(target)]
	a.mu.Unlock()
	if target.Auth != inference.OAuth || d.Metadata == nil || (d.Metadata.ProviderId != "anthropic" && d.Metadata.ProviderId != "anthropic2") {
		return false, nil
	}
	family := nativeModelFamily(target.Model)
	if family == "" {
		return false, nil
	}
	lease, err := a.current(p)
	if err != nil {
		return true, err
	}
	key := nativeFamilyKey(id, version, family)
	health, err := a.s.GetNativeHealth(ctx, lease, key)
	if err != nil {
		return true, inferenceError(err)
	}
	permit, err := a.s.AcquireNativeHealthPermit(ctx, lease, key, health.ProbeEligible)
	if errors.Is(err, store.ErrBusy) {
		return true, nativeHealthWithheld(health.Now, health.CooldownUntil)
	}
	if err != nil {
		return true, inferenceError(err)
	}
	receipt := &nativeFamilyReceipt{Account: id, Version: version, Family: family, Permit: permit}
	state.Family = receipt
	a.mu.Lock()
	if a.native == nil {
		a.native = map[string]nativeSelectionState{}
	}
	a.native[selectionKey(target)] = state
	a.mu.Unlock()
	sent := false
	err = a.s.WithNativeHealthDispatch(ctx, lease, []store.NativeHealthPermit{permit}, func(locked context.Context) error { sent = true; return start(locked) })
	if !sent {
		_ = a.s.ReleaseNativeHealthPermit(context.WithoutCancel(ctx), lease, permit)
	}
	return true, inferenceError(err)
}
func (a *inferenceAdapter) observeNativeFamily(ctx context.Context, p inference.Principal, target inference.Target, c inference.Credential, status int, h http.Header) error {
	a.mu.Lock()
	d := a.connections[c.AccountID]
	receipt := a.native[selectionKey(target)].Family
	a.mu.Unlock()
	if target.Auth != inference.OAuth || d.Metadata == nil || (d.Metadata.ProviderId != "anthropic" && d.Metadata.ProviderId != "anthropic2") {
		return nil
	}
	lease, err := a.current(p)
	if err != nil {
		return err
	}
	rejected := status == 429 && strings.TrimSpace(h.Get("anthropic-ratelimit-unified-7d_oi-status")) == "rejected"
	// The pinned header is attributed only to Fable, even on another model's request.
	if rejected {
		reset := nativeResetHeader(h, "anthropic-ratelimit-unified-7d_oi-reset")
		var resetAt *time.Time
		if reset != "" {
			resetAt = nativeReset(reset)
		}
		reject := func(current store.NativeHealth) (store.NativeHealth, error) {
			current.CooldownSince = current.Now
			current.CooldownUntil = current.Now.Add(30 * time.Minute)
			if resetAt != nil && !resetAt.IsZero() && resetAt.Before(current.CooldownUntil) {
				current.CooldownUntil = *resetAt
			}
			current.ProbeEligible = true
			return current, nil
		}
		if receipt != nil && !receipt.Finished && receipt.Account == c.AccountID && receipt.Version == c.CredentialVersion && receipt.Family == "fable" {
			err = a.s.FinishNativeHealthPermit(ctx, lease, receipt.Permit, reject)
			receipt.Finished = true
			// A newly received refusal remains authoritative even if an earlier
			// successful sibling advanced the admission revision. Only success needs
			// the captured revision to protect a newer refusal from being cleared.
			if errors.Is(err, store.ErrConflict) {
				err = a.s.UpdateNativeHealth(ctx, lease, nativeFamilyKey(c.AccountID, c.CredentialVersion, "fable"), reject)
			}
			if errors.Is(err, store.ErrExpired) {
				return nil
			}
			return inferenceError(err)
		}
		err = a.s.UpdateNativeHealth(ctx, lease, nativeFamilyKey(c.AccountID, c.CredentialVersion, "fable"), reject)
		if err != nil {
			return inferenceError(err)
		}
	}

	if receipt == nil || receipt.Finished || receipt.Account != c.AccountID || receipt.Version != c.CredentialVersion {
		return nil
	}
	err = a.s.FinishNativeHealthPermit(ctx, lease, receipt.Permit, func(current store.NativeHealth) (store.NativeHealth, error) {
		if status >= 200 && status < 300 {
			current.CooldownUntil = time.Time{}
			current.CooldownSince = time.Time{}
			current.ProbeEligible = false
		}
		return current, nil
	})
	receipt.Finished = true
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrExpired) {
		return nil
	}
	return inferenceError(err)
}
