// SPDX-License-Identifier: Apache-2.0
package main

import (
	"connectrpc.com/connect"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"github.com/delinoio/AIPRouter/packages/aiprouter/store"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
)

func (m *management) operator(ctx context.Context) (core.Identity, error) {
	i := identity(ctx)
	if !m.auth.config.isOperator(i.Issuer, i.Subject) {
		return core.Identity{}, rpcError(store.ErrForbidden)
	}
	return core.Identity{Issuer: i.Issuer, Subject: i.Subject}, nil
}
func settingsMessage(s store.Settings) *v1.InstanceSettings {
	out := &v1.InstanceSettings{RequestLimits: &v1.RequestLimits{RequestsPerMinute: uint32(s.RPM), ConcurrentRequests: uint32(s.Concurrency)}, Retention: &v1.RetentionSettings{RequestDays: uint32(s.UsageDays), AggregateDays: uint32(s.AggregateDays), AuditDays: uint32(s.AuditDays)}}
	for _, origin := range s.Destinations {
		out.AllowedDestinations = append(out.AllowedDestinations, &v1.AllowedDestination{Origin: origin})
	}
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	out.Revision = hex.EncodeToString(h[:])
	return out
}
func (m *management) GetInstanceSettings(ctx context.Context, r *connect.Request[v1.GetInstanceSettingsRequest]) (*connect.Response[v1.GetInstanceSettingsResponse], error) {
	if _, e := m.operator(ctx); e != nil {
		return nil, e
	}
	s, e := m.store.Settings(ctx)
	return connect.NewResponse(&v1.GetInstanceSettingsResponse{Settings: settingsMessage(s)}), rpcError(e)
}
func (m *management) UpdateInstanceSettings(ctx context.Context, r *connect.Request[v1.UpdateInstanceSettingsRequest]) (*connect.Response[v1.UpdateInstanceSettingsResponse], error) {
	i, e := m.operator(ctx)
	if e != nil {
		return nil, e
	}
	q := r.Msg.Settings
	if q == nil || q.RequestLimits == nil || q.Retention == nil {
		return nil, invalid("complete settings required")
	}
	old, e := m.store.Settings(ctx)
	if e != nil {
		return nil, rpcError(e)
	}
	if r.Msg.ExpectedRevision != "" && r.Msg.ExpectedRevision != settingsMessage(old).Revision {
		return nil, rpcError(store.ErrConflict)
	}
	next := old
	next.RPM = int(q.RequestLimits.RequestsPerMinute)
	next.Concurrency = int(q.RequestLimits.ConcurrentRequests)
	next.UsageDays = int(q.Retention.RequestDays)
	next.AggregateDays = int(q.Retention.AggregateDays)
	next.AuditDays = int(q.Retention.AuditDays)
	next.Destinations = nil
	for _, d := range q.AllowedDestinations {
		if d == nil || !validProviderURL(d.Origin) {
			return nil, invalid("invalid allowed destination")
		}
		if len(d.AllowedCidrs) > 0 {
			return nil, unimplemented()
		}
		next.Destinations = append(next.Destinations, d.Origin)
	}
	e = m.store.UpdateSettings(ctx, i, next)
	return connect.NewResponse(&v1.UpdateInstanceSettingsResponse{Settings: settingsMessage(next)}), rpcError(e)
}
func (m *management) GetInstanceHealth(ctx context.Context, r *connect.Request[v1.GetInstanceHealthRequest]) (*connect.Response[v1.GetInstanceHealthResponse], error) {
	if _, e := m.operator(ctx); e != nil {
		return nil, e
	}
	state := v1.HealthState_HEALTH_STATE_HEALTHY
	if e := m.store.Ping(ctx); e != nil {
		state = v1.HealthState_HEALTH_STATE_UNAVAILABLE
	}
	return connect.NewResponse(&v1.GetInstanceHealthResponse{Health: &v1.InstanceHealth{Service: v1.HealthState_HEALTH_STATE_HEALTHY, Database: state}}), nil
}
func unsigned(v *int64) *uint64 {
	if v == nil {
		return nil
	}
	n := uint64(*v)
	return &n
}
func summary(a store.Aggregate) *v1.UsageSummary {
	in, out := uint64(a.InputTokens), uint64(a.OutputTokens)
	return &v1.UsageSummary{RequestCount: uint64(a.Requests), KnownUsage: &v1.TokenUsage{InputTokens: &in, OutputTokens: &out}, KnownCosts: []*v1.Money{{Currency: "USD", DecimalAmount: a.Cost}}, MissingUsageCount: uint64(a.MissingUsage), MissingCostCount: uint64(a.MissingCost)}
}
func (m *management) GetInstanceUsage(ctx context.Context, r *connect.Request[v1.GetInstanceUsageRequest]) (*connect.Response[v1.GetInstanceUsageResponse], error) {
	i, e := m.operator(ctx)
	if e != nil {
		return nil, e
	}
	if r.Msg.TimeRange != nil {
		return nil, unimplemented()
	}
	a, e := m.store.InstanceAggregate(ctx, i)
	return connect.NewResponse(&v1.GetInstanceUsageResponse{Summary: summary(a)}), rpcError(e)
}
func (m *management) ListUsage(ctx context.Context, r *connect.Request[v1.ListUsageRequest]) (*connect.Response[v1.ListUsageResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	if r.Msg.TimeRange != nil || r.Msg.ModelId != nil || r.Msg.GetPage().GetPageToken() != "" {
		return nil, unimplemented()
	}
	user := sc.UserID
	if r.Msg.UserId != nil {
		user = r.Msg.GetUserId()
	}
	rows, e := m.store.ListUsage(ctx, sc, user, int(r.Msg.GetPage().GetPageSize()))
	if e != nil {
		return nil, rpcError(e)
	}
	out := &v1.ListUsageResponse{}
	for _, u := range rows {
		record := &v1.UsageRecord{Id: u.ID, OrganizationId: u.OrganizationID, ModelId: u.Model, ProviderId: u.Provider, OccurredAt: timestamp(u.CreatedAt), Usage: &v1.TokenUsage{InputTokens: unsigned(u.InputTokens), OutputTokens: unsigned(u.OutputTokens)}, RequestId: u.ID}
		if u.UserID != "" {
			id := u.UserID
			record.UserId = &id
		}
		if u.Cost != nil {
			record.EstimatedCost = &v1.Money{Currency: "USD", DecimalAmount: *u.Cost}
		}
		if u.ErrorCode != "" {
			code := u.ErrorCode
			record.ErrorCode = &code
		}
		out.Records = append(out.Records, record)
	}
	return connect.NewResponse(out), nil
}
func (m *management) GetUsageSummary(ctx context.Context, r *connect.Request[v1.GetUsageSummaryRequest]) (*connect.Response[v1.GetUsageSummaryResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	if r.Msg.TimeRange != nil || r.Msg.ModelId != nil {
		return nil, unimplemented()
	}
	user := sc.UserID
	if r.Msg.UserId != nil {
		user = r.Msg.GetUserId()
	}
	a, e := m.store.AggregateUsage(ctx, sc, user)
	return connect.NewResponse(&v1.GetUsageSummaryResponse{Summary: summary(a)}), rpcError(e)
}
func (m *management) ListDailyUsage(ctx context.Context, r *connect.Request[v1.ListDailyUsageRequest]) (*connect.Response[v1.ListDailyUsageResponse], error) {
	if _, e := m.scope(ctx, r.Msg.OrganizationId); e != nil {
		return nil, e
	}
	return nil, unimplemented()
}
func auditMessage(a store.Audit) *v1.AuditRecord {
	out := &v1.AuditRecord{Id: a.ID, OccurredAt: timestamp(a.CreatedAt), ResourceId: a.TargetID}
	if a.ActorID != "" {
		out.ActorUserId = &a.ActorID
	}
	return out
}
func (m *management) ListOrganizationAudit(ctx context.Context, r *connect.Request[v1.ListOrganizationAuditRequest]) (*connect.Response[v1.ListOrganizationAuditResponse], error) {
	sc, e := m.scope(ctx, r.Msg.OrganizationId)
	if e != nil {
		return nil, e
	}
	if r.Msg.TimeRange != nil || r.Msg.GetPage().GetPageToken() != "" {
		return nil, unimplemented()
	}
	rows, e := m.store.ListAudits(ctx, sc)
	if e != nil {
		return nil, rpcError(e)
	}
	out := &v1.ListOrganizationAuditResponse{}
	for _, a := range rows {
		out.Records = append(out.Records, auditMessage(a))
	}
	return connect.NewResponse(out), nil
}
func (m *management) ListInstanceAudit(ctx context.Context, r *connect.Request[v1.ListInstanceAuditRequest]) (*connect.Response[v1.ListInstanceAuditResponse], error) {
	i, e := m.operator(ctx)
	if e != nil {
		return nil, e
	}
	if r.Msg.TimeRange != nil || r.Msg.GetPage().GetPageToken() != "" {
		return nil, unimplemented()
	}
	rows, e := m.store.InstanceAudits(ctx, i)
	if e != nil {
		return nil, rpcError(e)
	}
	out := &v1.ListInstanceAuditResponse{}
	for _, a := range rows {
		out.Records = append(out.Records, auditMessage(a))
	}
	return connect.NewResponse(out), nil
}
func (m *management) ListPriceOverrides(ctx context.Context, r *connect.Request[v1.ListPriceOverridesRequest]) (*connect.Response[v1.ListPriceOverridesResponse], error) {
	if _, e := m.operator(ctx); e != nil {
		return nil, e
	}
	s, e := m.store.Settings(ctx)
	if e != nil {
		return nil, rpcError(e)
	}
	out := &v1.ListPriceOverridesResponse{}
	if len(s.Prices) > 0 && string(s.Prices) != "{}" {
		if e = json.Unmarshal(s.Prices, &out.Overrides); e != nil {
			return nil, rpcError(errors.New("invalid price configuration"))
		}
	}
	return connect.NewResponse(out), nil
}
func (m *management) SetPriceOverride(ctx context.Context, r *connect.Request[v1.SetPriceOverrideRequest]) (*connect.Response[v1.SetPriceOverrideResponse], error) {
	if _, e := m.operator(ctx); e != nil {
		return nil, e
	}
	return nil, unimplemented()
}
func (m *management) DeletePriceOverride(ctx context.Context, r *connect.Request[v1.DeletePriceOverrideRequest]) (*connect.Response[v1.DeletePriceOverrideResponse], error) {
	if _, e := m.operator(ctx); e != nil {
		return nil, e
	}
	return nil, unimplemented()
}
