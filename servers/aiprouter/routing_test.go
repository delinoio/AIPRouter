// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"github.com/delinoio/AIPRouter/packages/aiprouter/providers"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
)

func poolRoutingFixture(t *testing.T, strategy v1.PoolStrategy) (*inferenceAdapter, core.Scope, string) {
	t.Helper()
	a, sc, key := dispatchFixture(t)
	for _, id := range []string{"a", "b", "c"} {
		d := providerData{Metadata: &v1.ProviderConnection{Id: id, OrganizationId: sc.OrganizationID, ProviderId: "custom", BaseUrl: "https://provider.example/v1", AuthKind: v1.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY, State: v1.ConnectionState_CONNECTION_STATE_READY}, Secret: "secret-" + id}
		putRoutingProvider(t, a, sc, d)
	}
	pool := &v1.Pool{Id: "pool", Strategy: strategy, Members: []*v1.PoolMember{{ConnectionId: "a", Priority: 1}, {ConnectionId: "b", Priority: 2}, {ConnectionId: "c", Priority: 3}}}
	b, _ := json.Marshal(pool)
	if e := a.s.PutPrivate(context.Background(), sc, string(poolKind), pool.Id, b); e != nil {
		t.Fatal(e)
	}
	putRoutingRoute(t, a, sc, &v1.Route{Id: "r", ModelId: "model", Enabled: true, Strategy: v1.RoutingStrategy_ROUTING_STRATEGY_DIRECT, Targets: []*v1.RouteTarget{{Target: &v1.RouteTarget_PoolId{PoolId: "pool"}, ModelId: "upstream"}}})
	return a, sc, key
}
func TestRoutingPoolStrategiesPersistAcrossInstances(t *testing.T) {
	for _, strategy := range []v1.PoolStrategy{v1.PoolStrategy_POOL_STRATEGY_PRIORITY, v1.PoolStrategy_POOL_STRATEGY_ROUND_ROBIN, v1.PoolStrategy_POOL_STRATEGY_LEAST_RECENTLY_USED} {
		t.Run(strategy.String(), func(t *testing.T) {
			a, _, key := poolRoutingFixture(t, strategy)
			got := []string{}
			for i := 0; i < 4; i++ {
				instance := &inferenceAdapter{s: a.s, telemetry: a.telemetry}
				p, release, e := instance.Admit(context.Background(), key)
				if e != nil {
					t.Fatal(e)
				}
				r, e := instance.Resolve(context.Background(), p, "model", inference.Generate)
				if e != nil {
					t.Fatal(e)
				}
				c, e := instance.Select(context.Background(), p, r.Targets[0], "")
				release()
				if e != nil {
					t.Fatal(e)
				}
				if c.Secret != "secret-"+c.AccountID {
					t.Fatal("rotated account kept another secret")
				}
				got = append(got, c.AccountID)
			}
			want := []string{"a", "b", "c", "a"}
			if strategy == v1.PoolStrategy_POOL_STRATEGY_PRIORITY {
				want = []string{"a", "a", "a", "a"}
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("selection=%v want=%v", got, want)
				}
			}
		})
	}
}
func TestRoutingCooldownAndCredentialReplacementFence(t *testing.T) {
	a, sc, key := poolRoutingFixture(t, v1.PoolStrategy_POOL_STRATEGY_PRIORITY)
	p := admitRouting(t, a, key)
	ctx := context.Background()
	r, e := a.Resolve(ctx, p, "model", inference.Generate)
	if e != nil {
		t.Fatal(e)
	}
	target := r.Targets[0]
	first, e := a.Select(ctx, p, target, "")
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Failed(ctx, p, target, first, 429, time.Minute); e != nil {
		t.Fatal(e)
	}
	second, e := a.Select(ctx, p, target, first.AccountID)
	if e != nil || second.AccountID == first.AccountID {
		t.Fatalf("cooldown rotation=%v %v", second.AccountID, e)
	}
	d, e := a.provider(ctx, sc, second.AccountID)
	if e != nil {
		t.Fatal(e)
	}
	d.Secret = "replacement"
	putRoutingProvider(t, a, sc, d)
	sent := false
	e = a.Dispatch(ctx, p, func(context.Context) error { sent = true; return nil })
	if e == nil || sent {
		t.Fatal("replaced credential physically dispatched")
	}
	if e = a.Failed(ctx, p, target, second, 429, time.Hour); e == nil {
		t.Fatal("late failure quarantined new credential")
	}
	until, e := a.s.AccountCooldown(ctx, sc, second.AccountID, target.Model)
	if e != nil || until != nil {
		t.Fatalf("new credential inherited old cooldown: %v %v", until, e)
	}
}
func TestRoutingWeightedTargetsPreserveModelIdentity(t *testing.T) {
	a, sc, key := dispatchFixture(t)
	saveRoutingFixture(t, a, sc, "account", "model")
	r := &v1.Route{Id: "route-account", ModelId: "model", Enabled: true, Strategy: v1.RoutingStrategy_ROUTING_STRATEGY_WEIGHTED, Targets: []*v1.RouteTarget{{Target: &v1.RouteTarget_ConnectionId{ConnectionId: "account"}, ModelId: "one", Weight: 3}, {Target: &v1.RouteTarget_ConnectionId{ConnectionId: "account"}, ModelId: "two", Weight: 1}}}
	putRoutingRoute(t, a, sc, r)
	counts := map[string]int{}
	for i := 0; i < 8; i++ {
		instance := &inferenceAdapter{s: a.s, telemetry: a.telemetry}
		p, release, e := instance.Admit(context.Background(), key)
		if e != nil {
			t.Fatal(e)
		}
		route, e := instance.Resolve(context.Background(), p, "model", inference.Generate)
		if e != nil {
			t.Fatal(e)
		}
		_, e = instance.Select(context.Background(), p, route.Targets[0], "")
		release()
		if e != nil {
			t.Fatal(e)
		}
		counts[route.Targets[0].Model]++
	}
	if counts["one"] != 6 || counts["two"] != 2 {
		t.Fatalf("weighted route choices=%v", counts)
	}
}
func TestRoutingPinnedKeyPolicyAndCursorBudget(t *testing.T) {
	tests := []struct {
		base  string
		wire  providers.Wire
		auth  inference.AuthMode
		retry int
	}{{"https://api.commandcode.ai", providers.Chat, inference.APIKey, 6}, {"https://api.commandcode.ai/provider/v1", providers.Chat, inference.APIKey, 6}, {"https://api.commandcode.ai/v1", providers.Chat, inference.APIKey, 0}, {"https://api.mistral.ai/v1", providers.Chat, inference.APIKey, 6}, {"https://api.mistral.ai/v1", providers.Responses, inference.APIKey, 0}, {"https://opencode.ai/zen/go/v1", providers.Chat, inference.OAuth, 0}}
	for _, tt := range tests {
		p := providerRetryPolicy(providers.Route{BaseURL: tt.base, Wire: tt.wire}, tt.auth)
		if p.RateLimitRetries != tt.retry {
			t.Fatalf("retry policy %s: %d", tt.base, p.RateLimitRetries)
		}
	}
	r := providers.Route{Wire: providers.Chat, Preset: providers.Preset{Metadata: json.RawMessage(`{"retryOn429":{},"transientRetryOn5xx":{}}`)}}
	p := providerRetryPolicy(r, inference.APIKey)
	if p.RateLimitRetries != 3 || p.TransientSends != 3 || p.Interval != 5*time.Second {
		t.Fatalf("default explicit policy=%+v", p)
	}
	if cursorPhysicalExchangeBudget <= 4 || cursorPhysicalExchangeBudget > 1024 {
		t.Fatal("Cursor duplex exchange bound invalid")
	}
}
func TestRoutingBoundCredentialsNeverRotate(t *testing.T) {
	a, _, key := poolRoutingFixture(t, v1.PoolStrategy_POOL_STRATEGY_ROUND_ROBIN)
	p := admitRouting(t, a, key)
	r, e := a.Resolve(context.Background(), p, "model", inference.Generate)
	if e != nil {
		t.Fatal(e)
	}
	c, e := a.SelectBound(context.Background(), p, r.Targets[0], "b")
	if e != nil || c.AccountID != "b" {
		t.Fatalf("bound credential=%v %v", c.AccountID, e)
	}
	if _, e = a.SelectBound(context.Background(), p, r.Targets[0], "outsider"); !errors.Is(e, inference.ErrContinuation) {
		t.Fatalf("outsider=%v", e)
	}
}

func TestRoutingGenericAffinityRequiresOptInAndProviderBoundary(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "anthropic2"} {
		if genericAffinityEnabled(&v1.Pool{AffinityEnabled: true}, inference.OAuth, provider) {
			t.Fatalf("generic affinity enabled for %s", provider)
		}
	}
	for _, auth := range []inference.AuthMode{inference.APIKey, inference.Anonymous} {
		if genericAffinityEnabled(&v1.Pool{AffinityEnabled: true}, auth, "custom") {
			t.Fatal("non-OAuth affinity enabled")
		}
	}
	if genericAffinityEnabled(&v1.Pool{}, inference.OAuth, "nous") || genericAffinityEnabled(nil, inference.OAuth, "nous") {
		t.Fatal("default affinity enabled")
	}
	if !genericAffinityEnabled(&v1.Pool{AffinityEnabled: true}, inference.OAuth, "nous") {
		t.Fatal("explicit generic affinity ignored")
	}
}
