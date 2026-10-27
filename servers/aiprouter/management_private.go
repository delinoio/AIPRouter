// SPDX-License-Identifier: Apache-2.0
package main

import (
	"connectrpc.com/connect"
	"context"
	"encoding/json"
	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"github.com/delinoio/AIPRouter/packages/aiprouter/providers"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
	"google.golang.org/protobuf/proto"
	"net/url"
	"time"
)

type privateKind string

const (
	providerKind privateKind = "provider"
	poolKind     privateKind = "pool"
	routeKind    privateKind = "route"
)

// Connection metadata and the secret are encrypted together. Only Metadata is returned by RPCs.
type providerData struct {
	Metadata   *v1.ProviderConnection
	Secret     string
	Credential *providers.Credential
}

func (m *management) put(ctx context.Context, sc core.Scope, kind privateKind, id string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return m.store.PutPrivate(ctx, sc, string(kind), id, b)
}
func (m *management) get(ctx context.Context, sc core.Scope, kind privateKind, id string, v any) error {
	b, e := m.store.GetPrivate(ctx, sc, string(kind), id)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func validProviderURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || u.Scheme == "http")
}
func (m *management) ListProviderCatalog(ctx context.Context, r *connect.Request[v1.ListProviderCatalogRequest]) (*connect.Response[v1.ListProviderCatalogResponse], error) {
	out := &v1.ListProviderCatalogResponse{}
	for _, p := range providers.Catalog() {
		if p.Excluded || p.Deprecated != "" {
			continue
		}
		out.Providers = append(out.Providers, catalogMessage(p))
	}
	out.Providers = append(out.Providers, &v1.ProviderDefinition{Id: "custom", DisplayName: "Custom OpenAI-compatible HTTP provider", AuthKinds: []v1.ProviderAuthKind{v1.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY}, ApiShapes: []v1.ApiShape{v1.ApiShape_API_SHAPE_CHAT_COMPLETIONS}})
	return connect.NewResponse(out), nil
}

func (m *management) ListProviderConnections(ctx context.Context, r *connect.Request[v1.ListProviderConnectionsRequest]) (*connect.Response[v1.ListProviderConnectionsResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	ids, e := m.store.ListPrivate(ctx, sc, string(providerKind))
	if e != nil {
		return nil, rpcError(e)
	}
	out := &v1.ListProviderConnectionsResponse{}
	for _, id := range ids {
		var d providerData
		if e = m.get(ctx, sc, providerKind, id, &d); e != nil {
			return nil, rpcError(e)
		}
		out.Connections = append(out.Connections, d.Metadata)
	}
	return connect.NewResponse(out), nil
}
func (m *management) CreateProviderConnection(ctx context.Context, r *connect.Request[v1.CreateProviderConnectionRequest]) (*connect.Response[v1.CreateProviderConnectionResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	q := r.Msg
	if q.BaseUrl == "" {
		if preset, ok := providers.Lookup(q.ProviderId); ok {
			q.BaseUrl = preset.BaseURL
		}
	}
	if q.Name == "" || len(q.Name) > 200 || !validProviderURL(q.BaseUrl) {
		return nil, invalid("valid name and provider URL required")
	}
	if q.AuthKind != v1.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY && q.AuthKind != v1.ProviderAuthKind_PROVIDER_AUTH_KIND_NO_AUTH {
		return nil, unimplemented()
	}
	if q.AuthKind == v1.ProviderAuthKind_PROVIDER_AUTH_KIND_API_KEY && q.GetApiKey() == "" {
		return nil, invalid("API key required")
	}
	d := providerData{Metadata: &v1.ProviderConnection{Id: core.ID(), OrganizationId: sc.OrganizationID, ProviderId: q.ProviderId, Name: q.Name, BaseUrl: q.BaseUrl, AuthKind: q.AuthKind, State: v1.ConnectionState_CONNECTION_STATE_READY, CreatedAt: timestamp(time.Now()), StallTimeoutSeconds: q.StallTimeoutSeconds}, Secret: q.GetApiKey()}
	if e = providers.ValidateConnection(providerConnection(d)); e != nil {
		return nil, invalid("provider connection does not match its catalog requirements")
	}
	e = m.put(ctx, sc, providerKind, d.Metadata.Id, d)
	return connect.NewResponse(&v1.CreateProviderConnectionResponse{Connection: d.Metadata}), rpcError(e)
}
func (m *management) UpdateProviderConnection(ctx context.Context, r *connect.Request[v1.UpdateProviderConnectionRequest]) (*connect.Response[v1.UpdateProviderConnectionResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	q := r.Msg
	var d providerData
	if e = m.get(ctx, sc, providerKind, q.ConnectionId, &d); e != nil {
		return nil, rpcError(e)
	}
	if q.Name != nil {
		if q.GetName() == "" || len(q.GetName()) > 200 {
			return nil, invalid("invalid name")
		}
		d.Metadata.Name = q.GetName()
	}
	if q.BaseUrl != nil {
		if !validProviderURL(q.GetBaseUrl()) {
			return nil, invalid("invalid provider URL")
		}
		d.Metadata.BaseUrl = q.GetBaseUrl()
	}
	if q.ApiKey != nil {
		d.Secret = q.GetApiKey()
	}
	if q.Enabled != nil {
		d.Metadata.State = v1.ConnectionState_CONNECTION_STATE_READY
		if !q.GetEnabled() {
			d.Metadata.State = v1.ConnectionState_CONNECTION_STATE_DISABLED
		}
	}
	if q.ResetStallTimeout {
		d.Metadata.StallTimeoutSeconds = nil
	} else if q.StallTimeoutSeconds != nil {
		d.Metadata.StallTimeoutSeconds = q.StallTimeoutSeconds
	}
	if e = providers.ValidateConnection(providerConnection(d)); e != nil {
		return nil, invalid("provider connection does not match its catalog requirements")
	}
	e = m.put(ctx, sc, providerKind, q.ConnectionId, d)
	return connect.NewResponse(&v1.UpdateProviderConnectionResponse{Connection: d.Metadata}), rpcError(e)
}
func (m *management) DeleteProviderConnection(ctx context.Context, r *connect.Request[v1.DeleteProviderConnectionRequest]) (*connect.Response[v1.DeleteProviderConnectionResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	return connect.NewResponse(&v1.DeleteProviderConnectionResponse{}), rpcError(m.store.DeletePrivate(ctx, sc, string(providerKind), r.Msg.ConnectionId))
}
func (m *management) ListProviderModels(ctx context.Context, r *connect.Request[v1.ListProviderModelsRequest]) (*connect.Response[v1.ListProviderModelsResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	var d providerData
	if e = m.get(ctx, sc, providerKind, r.Msg.ConnectionId, &d); e != nil {
		return nil, rpcError(e)
	}
	p, ok := providers.Lookup(d.Metadata.ProviderId)
	if !ok {
		return nil, unimplemented()
	}
	out := &v1.ListProviderModelsResponse{}
	for _, id := range p.Models {
		out.Models = append(out.Models, providerModel(p, id))
	}
	if len(out.Models) == 0 {
		return nil, unimplemented()
	}
	return connect.NewResponse(out), nil
}

func (m *management) BeginProviderOAuth(ctx context.Context, r *connect.Request[v1.BeginProviderOAuthRequest]) (*connect.Response[v1.BeginProviderOAuthResponse], error) {
	if _, e := m.scope(ctx, r.Msg.OrganizationId); e != nil {
		return nil, e
	}
	return nil, unimplemented()
}
func (m *management) GetProviderOAuth(ctx context.Context, r *connect.Request[v1.GetProviderOAuthRequest]) (*connect.Response[v1.GetProviderOAuthResponse], error) {
	if _, e := m.scope(ctx, r.Msg.OrganizationId); e != nil {
		return nil, e
	}
	return nil, unimplemented()
}
func (m *management) CompleteProviderOAuth(ctx context.Context, r *connect.Request[v1.CompleteProviderOAuthRequest]) (*connect.Response[v1.CompleteProviderOAuthResponse], error) {
	if _, e := m.scope(ctx, r.Msg.OrganizationId); e != nil {
		return nil, e
	}
	return nil, unimplemented()
}
func (m *management) CancelProviderOAuth(ctx context.Context, r *connect.Request[v1.CancelProviderOAuthRequest]) (*connect.Response[v1.CancelProviderOAuthResponse], error) {
	if _, e := m.scope(ctx, r.Msg.OrganizationId); e != nil {
		return nil, e
	}
	return nil, unimplemented()
}
func (m *management) validatePool(ctx context.Context, sc core.Scope, p *v1.Pool) error {
	if p.Name == "" || len(p.Members) == 0 || len(p.Members) > 100 {
		return invalid("pool name and 1 to 100 members required")
	}
	if p.Strategy != v1.PoolStrategy_POOL_STRATEGY_PRIORITY {
		return unimplemented()
	}
	seen := map[string]bool{}
	for _, member := range p.Members {
		if member == nil || seen[member.ConnectionId] {
			return invalid("duplicate or missing pool member")
		}
		seen[member.ConnectionId] = true
		var d providerData
		if e := m.get(ctx, sc, providerKind, member.ConnectionId, &d); e != nil {
			return rpcError(e)
		}
	}
	return nil
}
func (m *management) ListPools(ctx context.Context, r *connect.Request[v1.ListPoolsRequest]) (*connect.Response[v1.ListPoolsResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	ids, e := m.store.ListPrivate(ctx, sc, string(poolKind))
	if e != nil {
		return nil, rpcError(e)
	}
	out := &v1.ListPoolsResponse{}
	for _, id := range ids {
		p := &v1.Pool{}
		if e = m.get(ctx, sc, poolKind, id, p); e != nil {
			return nil, rpcError(e)
		}
		out.Pools = append(out.Pools, p)
	}
	return connect.NewResponse(out), nil
}
func (m *management) CreatePool(ctx context.Context, r *connect.Request[v1.CreatePoolRequest]) (*connect.Response[v1.CreatePoolResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	p := &v1.Pool{Id: core.ID(), OrganizationId: sc.OrganizationID, Name: r.Msg.Name, Strategy: r.Msg.Strategy, Members: r.Msg.Members}
	if e = m.validatePool(ctx, sc, p); e != nil {
		return nil, e
	}
	return connect.NewResponse(&v1.CreatePoolResponse{Pool: p}), rpcError(m.put(ctx, sc, poolKind, p.Id, p))
}
func (m *management) UpdatePool(ctx context.Context, r *connect.Request[v1.UpdatePoolRequest]) (*connect.Response[v1.UpdatePoolResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	p := &v1.Pool{}
	if e = m.get(ctx, sc, poolKind, r.Msg.PoolId, p); e != nil {
		return nil, rpcError(e)
	}
	p.Name = r.Msg.Name
	p.Strategy = r.Msg.Strategy
	p.Members = r.Msg.Members
	if e = m.validatePool(ctx, sc, p); e != nil {
		return nil, e
	}
	return connect.NewResponse(&v1.UpdatePoolResponse{Pool: p}), rpcError(m.put(ctx, sc, poolKind, p.Id, p))
}
func (m *management) DeletePool(ctx context.Context, r *connect.Request[v1.DeletePoolRequest]) (*connect.Response[v1.DeletePoolResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	return connect.NewResponse(&v1.DeletePoolResponse{}), rpcError(m.store.DeletePrivate(ctx, sc, string(poolKind), r.Msg.PoolId))
}

// Route uses a Protobuf oneof; encode with proto rather than encoding/json.
func (m *management) putRoute(ctx context.Context, sc core.Scope, p *v1.Route) error {
	b, e := proto.Marshal(p)
	if e != nil {
		return e
	}
	return m.store.PutPrivate(ctx, sc, string(routeKind), p.Id, b)
}
func (m *management) getRoute(ctx context.Context, sc core.Scope, id string) (*v1.Route, error) {
	b, e := m.store.GetPrivate(ctx, sc, string(routeKind), id)
	if e != nil {
		return nil, e
	}
	p := &v1.Route{}
	e = proto.Unmarshal(b, p)
	return p, e
}
func (m *management) validateRoute(ctx context.Context, sc core.Scope, p *v1.Route) error {
	if p.ModelId == "" || len(p.Targets) == 0 || len(p.Targets) > 100 {
		return invalid("model and 1 to 100 targets required")
	}
	if p.Strategy != v1.RoutingStrategy_ROUTING_STRATEGY_DIRECT && p.Strategy != v1.RoutingStrategy_ROUTING_STRATEGY_FALLBACK {
		return unimplemented()
	}
	if p.Strategy == v1.RoutingStrategy_ROUTING_STRATEGY_DIRECT && len(p.Targets) != 1 {
		return invalid("direct route requires one target")
	}
	for _, target := range p.Targets {
		if target == nil {
			return invalid("target required")
		}
		if target.GetConnectionId() != "" {
			var d providerData
			if e := m.get(ctx, sc, providerKind, target.GetConnectionId(), &d); e != nil {
				return rpcError(e)
			}
		} else if target.GetPoolId() != "" {
			var pool v1.Pool
			if e := m.get(ctx, sc, poolKind, target.GetPoolId(), &pool); e != nil {
				return rpcError(e)
			}
		} else {
			return invalid("connection or pool required")
		}
	}
	return nil
}
func (m *management) ListRoutes(ctx context.Context, r *connect.Request[v1.ListRoutesRequest]) (*connect.Response[v1.ListRoutesResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	ids, e := m.store.ListPrivate(ctx, sc, string(routeKind))
	if e != nil {
		return nil, rpcError(e)
	}
	out := &v1.ListRoutesResponse{}
	for _, id := range ids {
		p, e := m.getRoute(ctx, sc, id)
		if e != nil {
			return nil, rpcError(e)
		}
		out.Routes = append(out.Routes, p)
	}
	return connect.NewResponse(out), nil
}
func (m *management) CreateRoute(ctx context.Context, r *connect.Request[v1.CreateRouteRequest]) (*connect.Response[v1.CreateRouteResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	p := &v1.Route{Id: core.ID(), OrganizationId: sc.OrganizationID, ModelId: r.Msg.ModelId, Strategy: r.Msg.Strategy, Targets: r.Msg.Targets, Enabled: r.Msg.Enabled}
	if e = m.validateRoute(ctx, sc, p); e != nil {
		return nil, e
	}
	return connect.NewResponse(&v1.CreateRouteResponse{Route: p}), rpcError(m.putRoute(ctx, sc, p))
}
func (m *management) UpdateRoute(ctx context.Context, r *connect.Request[v1.UpdateRouteRequest]) (*connect.Response[v1.UpdateRouteResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	p, e := m.getRoute(ctx, sc, r.Msg.RouteId)
	if e != nil {
		return nil, rpcError(e)
	}
	p.ModelId = r.Msg.ModelId
	p.Strategy = r.Msg.Strategy
	p.Targets = r.Msg.Targets
	p.Enabled = r.Msg.Enabled
	if e = m.validateRoute(ctx, sc, p); e != nil {
		return nil, e
	}
	return connect.NewResponse(&v1.UpdateRouteResponse{Route: p}), rpcError(m.putRoute(ctx, sc, p))
}
func (m *management) DeleteRoute(ctx context.Context, r *connect.Request[v1.DeleteRouteRequest]) (*connect.Response[v1.DeleteRouteResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	return connect.NewResponse(&v1.DeleteRouteResponse{}), rpcError(m.store.DeletePrivate(ctx, sc, string(routeKind), r.Msg.RouteId))
}
