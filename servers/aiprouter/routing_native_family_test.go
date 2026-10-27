// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"github.com/delinoio/AIPRouter/packages/aiprouter/providers"
	"github.com/delinoio/AIPRouter/packages/aiprouter/store"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
)

func nativeFamilyFixture(t *testing.T) (*inferenceAdapter, inference.Principal, inference.Target, inference.Credential) {
	t.Helper()
	a, sc, key := poolRoutingFixture(t, v1.PoolStrategy_POOL_STRATEGY_PRIORITY)
	ctx := context.Background()
	d, e := a.provider(ctx, sc, "a")
	if e != nil {
		t.Fatal(e)
	}
	d.Metadata.ProviderId = "anthropic"
	d.Metadata.AuthKind = v1.ProviderAuthKind_PROVIDER_AUTH_KIND_OAUTH
	d.Credential = &providers.Credential{Provider: "anthropic", Scope: sc, AccessToken: "fixture", AccountID: "a"}
	putRoutingProvider(t, a, sc, d)
	p := admitRouting(t, a, key)
	_, version, e := a.s.GetPrivateVersion(ctx, sc, "provider", "a")
	if e != nil {
		t.Fatal(e)
	}
	target := inference.Target{Provider: "logical", Auth: inference.OAuth, Model: "claude-fable-5"}
	c := inference.Credential{AccountID: "a", CredentialVersion: version}
	a.activeTarget = target
	a.activeAccount = "a"
	a.activeVersion = version
	a.connections = map[string]providerData{"a": d}
	a.native = map[string]nativeSelectionState{}
	return a, p, target, c
}
func nativeFamilyInstance(a *inferenceAdapter) *inferenceAdapter {
	return &inferenceAdapter{s: a.s, lease: a.lease, connections: a.connections, activeTarget: a.activeTarget, activeAccount: a.activeAccount, activeVersion: a.activeVersion, native: map[string]nativeSelectionState{}}
}
func TestNativeFamilyResetSingleFlightAndSuccess(t *testing.T) {
	a, p, target, c := nativeFamilyFixture(t)
	ctx := context.Background()
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-7d_oi-status", "rejected")
	h.Set("anthropic-ratelimit-unified-7d_oi-reset", strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10))
	if e := a.observeNativeFamily(ctx, p, target, c, 429, h); e != nil {
		t.Fatal(e)
	}
	selected := store.SelectionCandidate{ID: c.AccountID, Version: c.CredentialVersion}
	if allowed, e := a.nativeFamilyEligible(ctx, p, selected, target.Model); e != nil || !allowed {
		t.Fatalf("expired reset remains held: %v %v", allowed, e)
	}
	sent := 0
	if handled, e := a.dispatchNativeFamily(ctx, p, func(context.Context) error { sent++; return nil }); e != nil || !handled || sent != 1 {
		t.Fatalf("first revalidation %v %v", handled, e)
	}
	other := nativeFamilyInstance(a)
	if _, e := other.dispatchNativeFamily(ctx, p, func(context.Context) error { sent++; return nil }); e == nil || sent != 1 {
		t.Fatal("second instance sent during singleflight")
	}
	if e := a.observeNativeFamily(ctx, p, target, c, 200, http.Header{}); e != nil {
		t.Fatal(e)
	}
	if handled, e := other.dispatchNativeFamily(ctx, p, func(context.Context) error { sent++; return nil }); e != nil || !handled || sent != 2 {
		t.Fatalf("success did not reopen family: %v", e)
	}
	if e := other.observeNativeFamily(ctx, p, target, c, 200, http.Header{}); e != nil {
		t.Fatal(e)
	}
	h.Set("anthropic-ratelimit-unified-7d_oi-reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	if e := a.observeNativeFamily(ctx, p, target, c, 429, h); e != nil {
		t.Fatal(e)
	}
	if allowed, e := a.nativeFamilyEligible(ctx, p, selected, target.Model); e != nil || allowed {
		t.Fatal("fresh refusal did not hold family")
	}
	if allowed, e := a.nativeFamilyEligible(ctx, p, selected, "claude-sonnet-4"); e != nil || !allowed {
		t.Fatal("Fable refusal leaked to Sonnet")
	}
}
func TestNativeFamilyLateSuccessCannotClearNewRefusal(t *testing.T) {
	a, p, target, c := nativeFamilyFixture(t)
	ctx := context.Background()
	other := nativeFamilyInstance(a)
	for _, instance := range []*inferenceAdapter{a, other} {
		if _, e := instance.dispatchNativeFamily(ctx, p, func(context.Context) error { return nil }); e != nil {
			t.Fatal(e)
		}
	}
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-7d_oi-status", "rejected")
	if e := other.observeNativeFamily(ctx, p, target, c, 429, h); e != nil {
		t.Fatal(e)
	}
	if e := a.observeNativeFamily(ctx, p, target, c, 200, http.Header{}); e != nil {
		t.Fatal(e)
	}
	lease, e := a.current(p)
	if e != nil {
		t.Fatal(e)
	}
	health, e := a.s.GetNativeHealth(ctx, lease, nativeFamilyKey(c.AccountID, c.CredentialVersion, "fable"))
	if e != nil || !health.ProbeEligible || !health.CooldownUntil.After(health.Now) {
		t.Fatalf("late success erased newer family refusal: %+v %v", health, e)
	}
}
