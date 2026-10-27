// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"github.com/delinoio/AIPRouter/packages/aiprouter/providers"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
	"sync/atomic"
	"time"
)

func providerConnection(d providerData) providers.Connection {
	c := providers.Connection{Provider: d.Metadata.ProviderId, BaseURL: d.Metadata.BaseUrl, APIKey: d.Secret, Credential: d.Credential}
	if c.Provider == "custom" {
		c.Wire = providers.Chat
	}
	switch d.Metadata.AuthKind {
	case v1.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY:
		c.Auth = providers.Key
	case v1.ProviderAuthKind_PROVIDER_AUTH_KIND_OAUTH:
		if p, ok := providers.Lookup(c.Provider); ok {
			c.Auth = p.Auth
		}
		if c.Provider == "google-vertex" && c.Credential != nil && c.Credential.Google != nil {
			c.Auth = providers.OAuth
		}
	case v1.ProviderAuthKind_PROVIDER_AUTH_KIND_NO_AUTH:
		c.Auth = providers.Local
	}
	return c
}
func catalogMessage(p providers.Preset) *v1.ProviderDefinition {
	auth := v1.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY
	switch p.Auth {
	case providers.OAuth, providers.Forward:
		auth = v1.ProviderAuthKind_PROVIDER_AUTH_KIND_OAUTH
	case providers.Local:
		auth = v1.ProviderAuthKind_PROVIDER_AUTH_KIND_NO_AUTH
	}
	shapes := []v1.ApiShape{v1.ApiShape_API_SHAPE_MODELS}
	switch p.Wire {
	case providers.Responses:
		shapes = append(shapes, v1.ApiShape_API_SHAPE_RESPONSES)
	case providers.Anthropic:
		shapes = append(shapes, v1.ApiShape_API_SHAPE_ANTHROPIC_MESSAGES)
	default:
		shapes = append(shapes, v1.ApiShape_API_SHAPE_CHAT_COMPLETIONS)
	}
	auths := []v1.ProviderAuthKind{auth}
	if p.AllowKeyAuthOverride && auth != v1.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY {
		auths = append(auths, v1.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY)
	}
	return &v1.ProviderDefinition{Id: p.ID, DisplayName: p.Label, AuthKinds: auths, ApiShapes: shapes}
}
func providerModel(p providers.Preset, id string) *v1.Model {
	out := &v1.Model{Id: id, DisplayName: id, ApiShapes: catalogMessage(p).ApiShapes}
	if n := p.ModelContextWindows[id]; n > 0 {
		v := uint64(n)
		out.ContextLimit = &v
	} else if p.ContextWindow > 0 {
		v := uint64(p.ContextWindow)
		out.ContextLimit = &v
	}
	if n := p.ModelMaxOutputTokens[id]; n > 0 {
		v := uint64(n)
		out.OutputLimit = &v
	} else if p.DefaultMaxOutputTokens > 0 {
		v := uint64(p.DefaultMaxOutputTokens)
		out.OutputLimit = &v
	}
	return out
}

// discoverProviderModels serializes catalog calls with refresh. Some providers
// renew an account token during discovery; their result must use the original CAS.
func (m *management) discoverProviderModels(ctx context.Context, sc core.Scope, id string) (providerData, error) {
	var d providerData
	raw, version, claim, err := m.store.ClaimScopePrivateRefresh(ctx, sc, string(providerKind), id)
	if err != nil {
		return d, err
	}
	var sent atomic.Bool
	complete := false
	defer func() {
		if complete {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Zed discovery can rotate its LLM token. A failed/ambiguous call must not
		// leave its previous token eligible for a later rotating exchange.
		if sent.Load() && d.Metadata != nil && d.Metadata.ProviderId == "zed" {
			_ = m.store.MarkScopePrivateRefreshUncertain(cleanup, sc, string(providerKind), id, claim)
		} else {
			_ = m.store.ReleaseScopePrivateRefresh(cleanup, sc, string(providerKind), id, claim)
		}
	}()
	if err = json.Unmarshal(raw, &d); err != nil {
		return providerData{}, err
	}
	if d.Metadata == nil || d.Metadata.State != v1.ConnectionState_CONNECTION_STATE_READY {
		return providerData{}, providers.ErrReauthenticate
	}
	executor := &providerExecutor{store: m.store, scope: sc, test: m.providerExecutor, onSend: func() { sent.Store(true) }, gate: func(c context.Context, start func(context.Context) error) error {
		return m.store.WithScopePrivateRefreshDispatch(c, sc, string(providerKind), id, claim, start)
	}}
	catalog, fresh, err := providers.NewAuth(executor).Discover(ctx, sc, providerConnection(d))
	if err != nil {
		return providerData{}, err
	}
	if fresh != nil {
		d.Credential = fresh
	}
	d.CodexCatalog = catalog.CodexCatalog
	d.Models = &catalog
	d.ModelsDiscoveredAt = time.Now()
	raw, err = json.Marshal(d)
	if err != nil {
		return providerData{}, err
	}
	if err = m.store.CompleteScopePrivateRefresh(ctx, sc, string(providerKind), id, version, claim, raw); err != nil {
		return providerData{}, err
	}
	complete = true
	return d, nil
}
