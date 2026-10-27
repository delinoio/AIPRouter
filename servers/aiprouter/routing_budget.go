// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"time"

	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"github.com/delinoio/AIPRouter/packages/aiprouter/store"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
)

// These allowances cover bounded support exchanges, not extra retry attempts.
// All exchanges still consume the original ledger and current dispatch guard.
func (a *inferenceAdapter) hostedExchangeAllowance(ctx context.Context, p inference.Principal, mainBudget int) (int, error) {
	settings, err := readHostedSettings(ctx, a.s, scopeOf(p))
	if err != nil {
		return 0, inferenceError(err)
	}
	extra := 0
	if s := settings.Search; s != nil && s.Target != nil && (s.Enabled == nil || s.GetEnabled()) {
		n := 3
		if s.MaxSearchesPerTurn != nil {
			n = int(s.GetMaxSearchesPerTurn())
		}
		// One operation, plus at most one credential refresh and discovery.
		extra += n * 3
	}
	if s := settings.Images; s != nil && s.Target != nil {
		if s.BridgeEnabled {
			extra += 10 * (1 + 4 + 2)
		} // submit, four downloads, refresh/discovery
		if s.VideoBridgeEnabled {
			timeout := 300 * time.Second
			if s.VideoTimeoutMs != nil {
				timeout = time.Duration(*s.VideoTimeoutMs) * time.Millisecond
			}
			polls := 0
			for elapsed, delay := time.Duration(0), 5*time.Second; elapsed+delay < timeout; {
				elapsed += delay
				polls++
				delay = delay * 3 / 2
				if delay > 15*time.Second {
					delay = 15 * time.Second
				}
			}
			extra += 3 * (1 + polls + 1 + 2)
		}
	}
	if s := settings.Vision; s != nil && (s.GetTarget() != nil || s.GetRouteModel() != "") && (s.Enabled == nil || s.GetEnabled()) {
		n := 8
		if s.MaxDescriptionsPerTurn != nil {
			n = int(s.GetMaxDescriptionsPerTurn())
		}
		sends := 1
		if s.GetRouteModel() != "" {
			sends, err = a.routedHelperBudget(ctx, p, s.GetRouteModel())
			if err != nil {
				return 0, err
			}
		}
		extra += n * (sends + 2)
	}
	if extra > 0 {
		extra += 3 * mainBudget
	} // Each helper round repeats the original adapter preflights and bounded exchanges.
	return extra, nil
}

// Inspect candidates without selecting: sizing must not advance RR/weighted
// receipts or recurse through hosted settings on a routed vision helper.
func (a *inferenceAdapter) routedHelperBudget(ctx context.Context, p inference.Principal, model string) (int, error) {
	routes, err := a.routes(ctx, scopeOf(p))
	if err != nil {
		return 0, err
	}
	var chosen *v1.Route
	for _, r := range routes {
		if routeAliasMatches(r.ModelId, model) {
			if chosen != nil {
				return 0, inference.ErrUnsupported
			}
			chosen = r
		}
	}
	if chosen == nil || chosen.Strategy == v1.RoutingStrategy_ROUTING_STRATEGY_JEV {
		return 0, inference.ErrUnsupported
	}
	maxSends := 4
	for _, rt := range chosen.Targets {
		members, _, err := a.targetMembers(ctx, scopeOf(p), rt)
		if err != nil {
			return 0, err
		}
		n := len(members)
		if n > 6 {
			n = 6
		}
		for _, member := range members {
			d, err := a.provider(ctx, scopeOf(p), member.ConnectionId)
			if errors.Is(err, inference.ErrNoCredential) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return 0, inferenceError(err)
			}
			sends := 4
			if d.Metadata.ProviderId == "cursor" {
				sends = cursorPhysicalExchangeBudget
			} else if d.Metadata.AuthKind == v1.ProviderAuthKind_PROVIDER_AUTH_KIND_OAUTH {
				sends = n * 3
				if d.Metadata.ProviderId == "github-copilot" {
					sends = n * 9
				}
			}
			if sends > maxSends {
				maxSends = sends
			}
		}
	}
	return maxSends, nil
}
