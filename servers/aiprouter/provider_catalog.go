// SPDX-License-Identifier: Apache-2.0
package main

import (
	"github.com/delinoio/AIPRouter/packages/aiprouter/providers"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
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
