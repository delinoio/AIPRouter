// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference/providers/zed"
	"github.com/delinoio/AIPRouter/packages/aiprouter/pricing"
	"github.com/delinoio/AIPRouter/packages/aiprouter/providers"
	"github.com/delinoio/AIPRouter/packages/aiprouter/store"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
	"google.golang.org/protobuf/proto"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// An adapter is allocated per HTTP request or WebSocket. It never caches authorization.
// WebSocket execution admits one active turn at a time; each turn replaces its lease.
type inferenceAdapter struct {
	s           *store.Store
	telemetry   *telemetry
	mu          sync.Mutex
	lease       store.Lease
	connections map[string]providerData
	prices      *pricing.Snapshot
}

func inferenceError(e error) error {
	if e == nil {
		return nil
	}
	var limit *store.LimitError
	if errors.As(e, &limit) {
		return &inference.Error{Status: 429, Code: "rate_limited", RetryAfter: limit.RetryAfter}
	}
	if errors.Is(e, store.ErrForbidden) || errors.Is(e, store.ErrNotFound) || errors.Is(e, store.ErrExpired) {
		return inference.ErrUnauthorized
	}
	return inference.ErrUnavailable
}
func (a *inferenceAdapter) Admit(ctx context.Context, raw string) (inference.Principal, func(), error) {
	l, e := a.s.Admit(ctx, raw)
	if e != nil {
		return inference.Principal{}, nil, inferenceError(e)
	}
	a.mu.Lock()
	a.lease = l
	a.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if e := a.s.Release(ctx, l); e != nil {
				a.telemetry.Event(ctx, "admission_release_failed")
			}
		})
	}
	return inference.Principal{OrganizationID: l.Principal.Scope.OrganizationID, UserID: l.Principal.Scope.UserID, KeyID: l.Principal.KeyID, MembershipGeneration: l.Principal.Scope.Generation}, release, nil
}
func (a *inferenceAdapter) current(p inference.Principal) (store.Lease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	l := a.lease
	if l.Principal.KeyID != p.KeyID || l.Principal.Scope.OrganizationID != p.OrganizationID || l.Principal.Scope.UserID != p.UserID || l.Principal.Scope.Generation != p.MembershipGeneration {
		return l, inference.ErrUnauthorized
	}
	return l, nil
}
func (a *inferenceAdapter) BeforeSend(ctx context.Context, p inference.Principal) error {
	l, e := a.current(p)
	if e != nil {
		return e
	}
	return inferenceError(a.s.CheckDispatch(ctx, l))
}

// Dispatch is called by the transport around its first bounded plaintext write.
// DNS/TCP/TLS and response headers are outside the organization transaction gate.
func (a *inferenceAdapter) Dispatch(ctx context.Context, p inference.Principal, start func(context.Context) error) error {
	l, e := a.current(p)
	if e != nil {
		return e
	}
	return inferenceError(a.s.WithDispatch(ctx, l, start))
}

func scopeOf(p inference.Principal) core.Scope {
	return core.Scope{OrganizationID: p.OrganizationID, UserID: p.UserID, Generation: p.MembershipGeneration}
}
func (a *inferenceAdapter) models(ctx context.Context, sc core.Scope) ([]inference.Model, error) {
	ids, e := a.s.ListPrivate(ctx, sc, string(routeKind))
	if e != nil {
		return nil, e
	}
	out := []inference.Model{}
	seen := map[string]bool{}
	for _, id := range ids {
		b, e := a.s.GetPrivate(ctx, sc, string(routeKind), id)
		if e != nil {
			return nil, e
		}
		var route v1.Route
		if e = proto.Unmarshal(b, &route); e != nil {
			return nil, e
		}
		if route.Enabled && !seen[route.ModelId] {
			out = append(out, inference.Model{ID: route.ModelId, Object: "model", OwnedBy: "aiprouter"})
			seen[route.ModelId] = true
		}
	}
	return out, nil
}
func (a *inferenceAdapter) Models(ctx context.Context, p inference.Principal) ([]inference.Model, error) {
	return a.models(ctx, scopeOf(p))
}
func (a *inferenceAdapter) provider(ctx context.Context, sc core.Scope, id string) (providerData, error) {
	var d providerData
	b, e := a.s.GetPrivate(ctx, sc, string(providerKind), id)
	if e != nil {
		return d, e
	}
	e = json.Unmarshal(b, &d)
	if e != nil {
		return d, e
	}
	if d.Metadata == nil || d.Metadata.State != v1.ConnectionState_CONNECTION_STATE_READY {
		return d, inference.ErrNoCredential
	}
	return d, nil
}
func (a *inferenceAdapter) Resolve(ctx context.Context, p inference.Principal, model string, op inference.Operation) (inference.Route, error) {
	sc := scopeOf(p)
	ids, e := a.s.ListPrivate(ctx, sc, string(routeKind))
	if e != nil {
		return inference.Route{}, inferenceError(e)
	}
	var chosen *v1.Route
	for _, id := range ids {
		b, e := a.s.GetPrivate(ctx, sc, string(routeKind), id)
		if e != nil {
			return inference.Route{}, inferenceError(e)
		}
		var route v1.Route
		if e = proto.Unmarshal(b, &route); e != nil {
			return inference.Route{}, inference.ErrUnavailable
		}
		if route.Enabled && route.ModelId == model {
			if chosen != nil {
				return inference.Route{}, &inference.Error{Status: 409, Code: "ambiguous_model_route"}
			}
			chosen = &route
		}
	}
	if chosen == nil {
		return inference.Route{}, &inference.Error{Status: 404, Code: "model_not_found"}
	}
	sort.SliceStable(chosen.Targets, func(i, j int) bool { return chosen.Targets[i].Priority < chosen.Targets[j].Priority })
	a.connections = map[string]providerData{}
	out := inference.Route{}
	for _, target := range chosen.Targets {
		connectionIDs := []string{target.GetConnectionId()}
		if target.GetPoolId() != "" {
			b, e := a.s.GetPrivate(ctx, sc, string(poolKind), target.GetPoolId())
			if e != nil {
				return out, inferenceError(e)
			}
			var pool v1.Pool
			if json.Unmarshal(b, &pool) != nil {
				return out, inference.ErrUnavailable
			}
			sort.SliceStable(pool.Members, func(i, j int) bool { return pool.Members[i].Priority < pool.Members[j].Priority })
			connectionIDs = nil
			for _, member := range pool.Members {
				connectionIDs = append(connectionIDs, member.ConnectionId)
			}
		}
		for _, id := range connectionIDs {
			d, e := a.provider(ctx, sc, id)
			if errors.Is(e, inference.ErrNoCredential) {
				continue
			}
			if e != nil {
				return out, inferenceError(e)
			}
			a.connections[id] = d
			upstreamModel := target.ModelId
			if upstreamModel == "" {
				upstreamModel = model
			}
			resolved, e := providers.Resolve(providerConnection(d), upstreamModel, providers.InboundChat)
			if e != nil {
				return out, inference.ErrUnsupported
			}
			wire := inference.Wire(resolved.Wire)
			switch wire {
			case "google":
				wire = "google-gemini"
			case "cursor":
				wire = "cursor-agent"
			case "zed":
				switch zed.NormalizeProvider("", resolved.Model) {
				case zed.OpenAI:
					wire = zed.WireOpenAI
				case zed.Google:
					wire = zed.WireGoogle
				case zed.XAI:
					wire = zed.WireXAI
				default:
					wire = zed.WireAnthropic
				}
			}
			auth := inference.APIKey
			if d.Metadata.AuthKind == v1.ProviderAuthKind_PROVIDER_AUTH_KIND_NO_AUTH {
				auth = inference.Anonymous
			} else if d.Metadata.AuthKind == v1.ProviderAuthKind_PROVIDER_AUTH_KIND_OAUTH {
				auth = inference.OAuth
			}
			t := inference.Target{Provider: id, BaseURL: resolved.BaseURL, Model: resolved.Model, Wire: wire, Auth: auth, Headers: resolved.Headers}

			if d.Metadata.StallTimeoutSeconds != nil {
				v := time.Duration(*d.Metadata.StallTimeoutSeconds) * time.Second
				t.StallTimeout = &v
			}
			if chosen.Strategy == v1.RoutingStrategy_ROUTING_STRATEGY_FALLBACK {
				t.Retry.FallbackOn429 = true
				t.Retry.FallbackOn5xx = true
			}
			out.Targets = append(out.Targets, t)
		}
	}
	out.MaxSends = len(out.Targets)
	if out.MaxSends == 0 {
		return out, inference.ErrNoCredential
	}
	return out, nil
}
func (a *inferenceAdapter) Select(ctx context.Context, p inference.Principal, t inference.Target, previous string) (inference.Credential, error) {
	if e := a.BeforeSend(ctx, p); e != nil {
		return inference.Credential{}, e
	}
	if previous == t.Provider {
		return inference.Credential{}, inference.ErrNoCredential
	}
	d, e := a.provider(ctx, scopeOf(p), t.Provider)
	if e != nil {
		return inference.Credential{}, inferenceError(e)
	}
	secret := d.Secret
	headers := make(http.Header)
	if d.Credential != nil {
		secret = d.Credential.AccessToken
		if d.Credential.ProjectID != "" {
			headers.Set("X-Aiprouter-Google-Project", d.Credential.ProjectID)
		}
	}
	return inference.Credential{AccountID: t.Provider, Secret: secret, Headers: headers}, nil
}
func (a *inferenceAdapter) Refresh(ctx context.Context, p inference.Principal, t inference.Target, c inference.Credential) (inference.Credential, error) {
	if e := a.BeforeSend(ctx, p); e != nil {
		return c, e
	}
	return c, inference.ErrUnsupported
}
func (a *inferenceAdapter) Load(ctx context.Context, p inference.Principal, id string) (inference.Continuation, error) {
	var c inference.Continuation
	b, e := a.s.GetState(ctx, scopeOf(p), core.Responses, id)
	if errors.Is(e, store.ErrContinuation) {
		return c, inference.ErrContinuation
	}
	if e != nil {
		return c, inferenceError(e)
	}
	e = json.Unmarshal(b, &c)
	return c, e
}
func (a *inferenceAdapter) Save(ctx context.Context, p inference.Principal, id string, c inference.Continuation) error {
	if e := a.BeforeSend(ctx, p); e != nil {
		return e
	}
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	return a.s.PutState(ctx, scopeOf(p), core.Responses, id, b)
}
func (a *inferenceAdapter) Record(ctx context.Context, p inference.Principal, r inference.UsageRecord) error {
	l, e := a.current(p)
	if e != nil {
		return e
	}
	provider := r.Provider
	if d, ok := a.connections[provider]; ok {
		provider = d.Metadata.ProviderId
	}
	code := ""
	if r.Canceled {
		code = "canceled"
	} else if r.Status >= 400 {
		code = "upstream_error"
	}
	u := store.Usage{Model: r.Model, Provider: provider, ErrorCode: code, InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}
	if r.Usage.InputTokens == nil || r.Usage.OutputTokens == nil {
		a.telemetry.Event(ctx, "usage_unknown")
	}
	if a.prices != nil {
		estimate := a.prices.Estimate(pricing.Request{Provider: provider, Model: r.Model, Status: pricing.Reported, Usage: &pricing.Usage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens, CachedInputTokens: r.Usage.CacheReadTokens, CacheCreationInputTokens: r.Usage.CacheWriteTokens}}, time.Now())
		u.Price, _ = json.Marshal(estimate)
		u.Provenance = estimate.SnapshotID
		if estimate.Known && r.UnmeasuredAttempts == 0 {
			cost := strconv.FormatFloat(estimate.Cost.Total, 'f', 12, 64)
			u.Cost = &cost
		} else {
			a.telemetry.Event(ctx, "cost_unknown")
		}
	}
	a.telemetry.logger.InfoContext(ctx, "usage processing", "request_id", requestID(ctx), "inference_request_id", r.RequestID, "usage_id", l.ID)
	e = a.s.RecordUsage(ctx, l, u)
	return e
}

var _ inference.Dispatcher = (*inferenceAdapter)(nil)
