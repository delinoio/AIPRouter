// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"github.com/delinoio/AIPRouter/packages/aiprouter/store"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
	"google.golang.org/protobuf/proto"
)

func nativeTestQuota(now time.Time, window string, percent float64) nativeQuotaSnapshot {
	return nativeQuotaSnapshot{Windows: []nativeQuotaWindow{{Window: window, Percent: percent, ObservedAt: now}}}
}
func TestNativeQuotaPinnedWindows(t *testing.T) {
	now := time.Now().UTC()
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "99.875")
	h.Set("x-codex-primary-window-minutes", "300")
	h.Set("x-codex-secondary-used-percent", "31.25")
	q := parseNativeQuota("openai", "gpt-6-astra", 200, h, now)
	if len(q.Windows) != 2 || q.Windows[0].Window != "5h" || q.Windows[0].Percent != 99.875 || q.Windows[1].Window != "weekly" {
		t.Fatalf("duration/raw precision: %+v", q)
	}
	if score, known := nativeUsageScore("openai", "", nativeTestQuota(now, "5h", 0), now); known || score != 101 {
		t.Fatal("short-only invented long headroom")
	}
	short := nativeTestQuota(now, "5h", 100)
	if score, known := nativeUsageScore("openai", "", short, now); !known || score != 100 {
		t.Fatal("fresh short exhaustion lost")
	}
	if _, known := nativeUsageScore("openai", "", short, now.Add(6*time.Minute)); known {
		t.Fatal("stale resetless short exhausted")
	}
	reset := now.Add(-time.Second)
	short.Windows[0].ResetAt = &reset
	if _, known := nativeUsageScore("openai", "", short, now); known {
		t.Fatal("elapsed reset ignored")
	}
	h.Set("x-codex-primary-window-minutes", "40320")
	q = parseNativeQuota("openai", "gpt-6-astra", 200, h, now)
	if q.Windows[0].Window != "monthly" {
		t.Fatal("monthly primary classified weekly")
	}
	h = http.Header{}
	h.Set("anthropic-ratelimit-unified-7d_oi-utilization", "0.99991")
	h.Set("anthropic-ratelimit-unified-7d_oi-reset", "broken")
	q = parseNativeQuota("anthropic", "claude-fable-5", 200, h, now)
	if len(q.Windows) != 1 || q.Windows[0].Percent != 99.991 || q.Windows[0].ResetAt == nil || !q.Windows[0].ResetAt.IsZero() {
		t.Fatalf("raw advisory/reset evidence changed: %+v", q)
	}
	if nativeExhausted("anthropic", "claude-sonnet-4", nativeTestQuota(now, "fable", 100), now) {
		t.Fatal("Fable quota leaked into Sonnet")
	}
}
func TestNativeQuotaMergeAndCredits(t *testing.T) {
	now := time.Now().UTC()
	old := nativeTestQuota(now, "weekly", 91)
	merged := mergeNativeQuota(old, nativeTestQuota(now.Add(-time.Minute), "weekly", 2))
	if merged.Windows[0].Percent != 91 {
		t.Fatal("older observation overwrote newer")
	}
	merged = mergeNativeQuota(merged, nativeTestQuota(now.Add(time.Minute), "5h", 3))
	if len(merged.Windows) != 2 || !merged.Windows[0].ObservedAt.Equal(now) {
		t.Fatal("partial observation renewed omitted window")
	}
	q, err := parseNativeQuotaBody("openai", []byte(`{"plan_type":"free","rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":2592000}},"credits":{"has_credits":true,"balance":"10"},"spend_control":{"reached":false}}`), now)
	if err != nil || !q.spendableCredits(now) || len(q.Windows) != 1 || q.Windows[0].Window != "monthly" {
		t.Fatalf("WHAM=%+v err=%v", q, err)
	}
	if q.spendableCredits(now.Add(6 * time.Minute)) {
		t.Fatal("stale credits authorize spending")
	}
	q, err = parseNativeQuotaBody("openai", []byte(`{"credits":{"has_credits":true,"balance":10},"spend_control":{}}`), now)
	if err != nil || q.spendableCredits(now) {
		t.Fatal("malformed control authorized credits")
	}
}
func TestNativeOptionsAndPriorityFallback(t *testing.T) {
	now := time.Now()
	cs := []store.SelectionCandidate{{ID: "a", Priority: 1}, {ID: "b", Priority: 2}, {ID: "c", Priority: 3}}
	pool := &v1.Pool{NativeOptions: &v1.NativePoolOptions{Enabled: proto.Bool(true)}}
	o := nativePoolOptions(pool)
	q := map[string]nativeQuotaSnapshot{"a": nativeTestQuota(now, "weekly", 90), "b": nativeTestQuota(now, "weekly", 20), "c": nativeTestQuota(now, "weekly", 10)}
	if got := o.tier(cs, q, "openai", "", now); len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("priority fallback=%+v", got)
	}
	delete(q, "a")
	if got := o.tier(cs, q, "openai", "", now); got[0].ID != "a" {
		t.Fatal("unknown drained priority")
	}
	for _, id := range []string{"a", "b", "c"} {
		q[id] = nativeTestQuota(now, "weekly", 90)
	}
	if got := o.tier(cs, q, "openai", "", now); len(got) != 3 {
		t.Fatal("all drained emptied lower tiers")
	}
	o.members["a"] = &v1.NativePoolMemberOptions{UseRemainingQuota: true}
	if !o.headroom("openai", "", "a", q["a"], now) {
		t.Fatal("use remaining override ignored")
	}
	o.members["a"] = &v1.NativePoolMemberOptions{AutoSwitchThreshold: proto.Uint32(95)}
	if !o.headroom("openai", "", "a", q["a"], now) {
		t.Fatal("threshold override ignored")
	}
	o.routes = []*v1.NativeModelRoute{{Match: "claude-*", ConnectionIds: []string{"c", "b"}}}
	got := o.route(cs, "claude-fable-5")
	if len(got) != 2 || got[0].ID != "c" {
		t.Fatal("declared route order lost")
	}
	o.routes[0].ConnectionIds = []string{"missing"}
	if len(o.route(cs, "claude-fable-5")) != 0 {
		t.Fatal("strict route widened")
	}
	o.routes[0].Fallback = true
	if len(o.route(cs, "claude-fable-5")) != 3 {
		t.Fatal("empty fallback did not widen")
	}
	if nativePoolOptions(&v1.Pool{}).enabled {
		t.Fatal("imported absent settings enabled native pool")
	}
}
func TestNativeDurableQuotaAndCompletionIsolation(t *testing.T) {
	a, sc, key := poolRoutingFixture(t, v1.PoolStrategy_POOL_STRATEGY_PRIORITY)
	p := admitRouting(t, a, key)
	ctx := context.Background()
	lease, err := a.current(p)
	if err != nil {
		t.Fatal(err)
	}
	_, version, err := a.s.GetPrivateVersion(ctx, sc, "provider", "a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = a.writeNativeQuota(ctx, p, "a", version, nativeTestQuota(now, "weekly", 33)); err != nil {
		t.Fatal(err)
	}
	other := &inferenceAdapter{s: a.s, lease: a.lease}
	q, found, err := other.readNativeQuota(ctx, p, "a", version)
	if err != nil || !found || q.Windows[0].Percent != 33 {
		t.Fatalf("cross-instance quota=%+v %v", q, err)
	}
	makeState := func(group, target string) nativeSelectionState {
		snap, e := a.s.GetNativeAffinity(ctx, lease, group, "config", "session")
		if e != nil {
			t.Fatal(e)
		}
		return nativeSelectionState{Group: group, Version: "config", Session: "session", Epoch: snap.Epoch, TargetProvider: target, Model: "same", Candidates: map[string]string{"a": version}}
	}
	good, bad := makeState("native/openai/good", "target-good"), makeState("native/anthropic/bad", "target-bad")
	a.native = map[string]nativeSelectionState{"good": good, "bad": bad}
	a.versions = map[string]string{"a": version}
	record := inference.UsageRecord{Provider: "target-good", Model: "same", AccountID: "a", Outcome: inference.OutcomeSuccess}
	if err = a.finishNative(ctx, p, record); err != nil {
		t.Fatal(err)
	}
	saved, err := a.s.GetNativeAffinity(ctx, lease, good.Group, good.Version, good.Session)
	if err != nil || saved.AccountID != "a" {
		t.Fatalf("successful pin=%+v %v", saved, err)
	}
	saved, err = a.s.GetNativeAffinity(ctx, lease, bad.Group, bad.Version, bad.Session)
	if err != nil || saved.AccountID != "" {
		t.Fatal("same-model fallback polluted other provider affinity")
	}
	if err = a.s.FinishNativeAffinity(ctx, lease, good.Group, good.Version, good.Session, "a", version, good.Epoch, false); err != nil {
		t.Fatal(err)
	}
	if err = a.finishNative(ctx, p, record); err != nil {
		t.Fatal(err)
	}
	saved, err = a.s.GetNativeAffinity(ctx, lease, good.Group, good.Version, good.Session)
	if err != nil || saved.AccountID != "" {
		t.Fatal("late success resurrected refusal")
	}
	d, err := a.provider(ctx, sc, "a")
	if err != nil {
		t.Fatal(err)
	}
	d.Secret = "replacement"
	putRoutingProvider(t, a, sc, d)
	if _, _, err = other.readNativeQuota(ctx, p, "a", version); err == nil {
		t.Fatal("old generation read quota")
	}
	raw, _ := json.Marshal(nativeTestQuota(now, "weekly", 1))
	if err = a.s.UpdateNativeQuota(ctx, lease, "a", version, func([]byte) ([]byte, error) { return raw, nil }); err == nil {
		t.Fatal("old generation wrote quota")
	}
}
func TestNativePreselectStrategies(t *testing.T) {
	for _, strategy := range []v1.NativePoolStrategy{v1.NativePoolStrategy_NATIVE_POOL_STRATEGY_QUOTA, v1.NativePoolStrategy_NATIVE_POOL_STRATEGY_ROUND_ROBIN, v1.NativePoolStrategy_NATIVE_POOL_STRATEGY_FILL_FIRST, v1.NativePoolStrategy_NATIVE_POOL_STRATEGY_RESET_FIRST} {
		t.Run(strategy.String(), func(t *testing.T) {
			a, sc, key := poolRoutingFixture(t, v1.PoolStrategy_POOL_STRATEGY_PRIORITY)
			p := admitRouting(t, a, key)
			ctx := context.Background()
			target := inference.Target{Provider: "logical", Model: "gpt-6-astra", Auth: inference.OAuth}
			req := store.SelectionRequest{GroupID: "pool", Version: "config", Model: target.Model, Strategy: store.SelectionPriority}
			for _, id := range []string{"a", "b", "c"} {
				_, v, e := a.s.GetPrivateVersion(ctx, sc, "provider", id)
				if e != nil {
					t.Fatal(e)
				}
				req.Candidates = append(req.Candidates, store.SelectionCandidate{ID: id, Version: v})
			}
			a.selections = map[string]routeSelection{selectionKey(target): {pool: &v1.Pool{NativeOptions: &v1.NativePoolOptions{Enabled: proto.Bool(true), Strategy: strategy, StickyLimit: proto.Uint32(2)}}}}
			now := time.Now()
			for i, c := range req.Candidates {
				q := nativeTestQuota(now, "weekly", float64(30-i*10))
				reset := now.Add(time.Duration(i+1) * time.Hour)
				q.Windows[0].ResetAt = &reset
				if e := a.writeNativeQuota(ctx, p, c.ID, c.Version, q); e != nil {
					t.Fatal(e)
				}
			}
			got, e := a.preselectNative(ctx, p, target, "openai", req)
			if e != nil {
				t.Fatal(e)
			}
			want := "c"
			if strategy == v1.NativePoolStrategy_NATIVE_POOL_STRATEGY_FILL_FIRST || strategy == v1.NativePoolStrategy_NATIVE_POOL_STRATEGY_RESET_FIRST || strategy == v1.NativePoolStrategy_NATIVE_POOL_STRATEGY_ROUND_ROBIN {
				want = "a"
			}
			if got.Candidates[0].ID != want {
				t.Fatalf("first=%s want=%s", got.Candidates[0].ID, want)
			}
			if strategy == v1.NativePoolStrategy_NATIVE_POOL_STRATEGY_ROUND_ROBIN && got.Strategy != store.SelectionRoundRobin {
				t.Fatal("RR not delegated to durable selector")
			}
		})
	}
}
