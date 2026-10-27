// SPDX-License-Identifier: Apache-2.0
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func leaseFor(t *testing.T, s *Store, sc core.Scope) Lease {
	t.Helper()
	_, raw, e := s.IssueKey(context.Background(), sc, "work", nil)
	if e != nil {
		t.Fatal(e)
	}
	l, e := s.Admit(context.Background(), raw)
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func TestProviderFlowClaimTargetFenceAndReceipt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, sc := tenant(t, s)
	if e := s.PutPrivate(ctx, sc, "provider", "p", []byte("old")); e != nil {
		t.Fatal(e)
	}
	_, version, e := s.GetPrivateVersion(ctx, sc, "provider", "p")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SaveProviderFlowBound(ctx, sc, "f", "provider", "p", version, []byte("oauth secret"), time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	var winners atomic.Int32
	var claim string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c, e := s.ClaimProviderFlow(ctx, sc, "f")
			if e == nil {
				winners.Add(1)
				mu.Lock()
				claim = c
				mu.Unlock()
			} else if !errors.Is(e, ErrBusy) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal(winners.Load())
	}
	if e = s.PutPrivate(ctx, sc, "provider", "p", []byte("replacement")); e != nil {
		t.Fatal(e)
	}
	if e = s.WithProviderFlowDispatch(ctx, sc, "f", claim, func(context.Context) error { t.Fatal("stale reconnect dispatched"); return nil }); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e = s.CompleteProviderFlow(ctx, sc, "f", claim, "p", []byte("stale")); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e = s.CancelProviderFlow(ctx, sc, "f"); e != nil {
		t.Fatal(e)
	}
	r, e := s.ProviderFlowReceipt(ctx, sc, "f")
	if e != nil || r.Status != "canceled" {
		t.Fatal(r, e)
	}
	if _, e = s.GetProviderFlow(ctx, sc, "f"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if e = s.SaveProviderFlow(ctx, sc, "new", []byte("new-secret"), time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	_, claim, e = s.ClaimProviderFlow(ctx, sc, "new")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteProviderFlow(ctx, sc, "new", claim, "new-provider", []byte("credential")); e != nil {
		t.Fatal(e)
	}
	r, e = s.ProviderFlowReceipt(ctx, sc, "new")
	if e != nil || r.Status != "completed" {
		t.Fatal(r, e)
	}
	if _, _, e = s.ClaimProviderFlow(ctx, sc, "new"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
}
func TestRefreshExclusiveUncertainAndBrowserRecovery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, sc := tenant(t, s)
	l := leaseFor(t, s, sc)
	if e := s.PutPrivate(ctx, sc, "provider", "p", []byte("rotate")); e != nil {
		t.Fatal(e)
	}
	var winners atomic.Int32
	var claim, version string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, v, c, e := s.ClaimPrivateRefresh(ctx, l, "provider", "p")
			if e == nil {
				winners.Add(1)
				mu.Lock()
				claim = c
				version = v
				mu.Unlock()
			} else if !errors.Is(e, ErrBusy) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal(winners.Load())
	}
	if e := s.WithAccountDispatch(ctx, l, "p", version, func(context.Context) error { t.Fatal("old credential dispatched during refresh"); return nil }); !errors.Is(e, ErrBusy) {
		t.Fatal(e)
	}
	if e := s.MarkPrivateRefreshUncertain(ctx, l, "provider", "p", claim); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e := s.ClaimScopePrivateRefresh(ctx, sc, "provider", "p"); !errors.Is(e, ErrReauthentication) {
		t.Fatal(e)
	}
	if _, e := s.GetPrivate(ctx, sc, "provider", "p"); !errors.Is(e, ErrReauthentication) {
		t.Fatal(e)
	}
	if _, _, e := s.GetPrivateVersionForManagement(ctx, sc, "provider", "p"); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveProviderFlowBound(ctx, sc, "reconnect", "provider", "p", version, []byte("flow"), time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	_, c, e := s.ClaimProviderFlow(ctx, sc, "reconnect")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteProviderFlow(ctx, sc, "reconnect", c, "p", []byte("fresh")); e != nil {
		t.Fatal(e)
	}
	value, v, c, e := s.ClaimScopePrivateRefresh(ctx, sc, "provider", "p")
	if e != nil || string(value) != "fresh" {
		t.Fatal(string(value), e)
	}
	if e = s.CompleteScopePrivateRefresh(ctx, sc, "provider", "p", v, c, []byte("renewed")); e != nil {
		t.Fatal(e)
	}
	if e = s.WithAccountDispatch(ctx, l, "p", version, func(context.Context) error { t.Fatal("stale version send"); return nil }); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
}
func TestSettingsCASAndAtomicPrices(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	i := core.Identity{Issuer: "issuer", Subject: "operator"}
	v, e := s.Settings(ctx)
	if e != nil || v.Revision < 1 {
		t.Fatal(v, e)
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for n := range 8 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, e := s.PutPriceOverride(ctx, i, v.Revision, fmt.Sprint(n), json.RawMessage(`{"input":"1"}`))
			if e == nil {
				winners.Add(1)
			} else if !errors.Is(e, ErrConflict) {
				t.Error(e)
			}
		}(n)
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal(winners.Load())
	}
	next, e := s.Settings(ctx)
	if e != nil || next.Revision != v.Revision+1 {
		t.Fatal(next, e)
	}
	if _, e = s.UpdateSettingsCAS(ctx, i, v.Revision, v); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	rows, _, e := s.QueryInstanceAudits(ctx, i, AuditFilter{Action: "price.override_set", Outcome: AuditSucceeded})
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
}
func TestDurableSelectionAffinityAndVersionCooldown(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, sc := tenant(t, s)
	l := leaseFor(t, s, sc)
	var candidates []SelectionCandidate
	for n := range 2 {
		id := fmt.Sprint(n)
		if e := s.PutPrivate(ctx, sc, "provider", id, []byte("credential")); e != nil {
			t.Fatal(e)
		}
		_, v, e := s.GetPrivateVersion(ctx, sc, "provider", id)
		if e != nil {
			t.Fatal(e)
		}
		candidates = append(candidates, SelectionCandidate{ID: id, Version: v, Weight: n + 1})
	}
	r := SelectionRequest{GroupID: "pool", Version: "v1", Model: "m", Strategy: SelectionWeighted, Candidates: candidates}
	counts := map[string]int{}
	for range 30 {
		receipt, e := s.SelectAccount(ctx, l, r)
		if e != nil {
			t.Fatal(e)
		}
		counts[receipt.CandidateID]++
	}
	if counts["0"] != 10 || counts["1"] != 20 {
		t.Fatal(counts)
	}
	r.Strategy = SelectionRoundRobin
	r.AffinityKey = "conversation"
	first, e := s.SelectAccount(ctx, l, r)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.SelectAccount(ctx, l, r)
	if e != nil || again.CandidateID != first.CandidateID {
		t.Fatal(again, e)
	}
	idx := 0
	if first.CandidateID == "1" {
		idx = 1
	}
	if e = s.SetAccountCooldownVersion(ctx, l, first.CandidateID, candidates[idx].Version, "m", time.Now().Add(31*24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	again, e = s.SelectAccount(ctx, l, r)
	if e != nil || again.CandidateID == first.CandidateID {
		t.Fatal("affinity ignored cooldown", again, e)
	}
	if e = s.PutPrivate(ctx, sc, "provider", first.CandidateID, []byte("new")); e != nil {
		t.Fatal(e)
	}
	if e = s.SetAccountCooldownVersion(ctx, l, first.CandidateID, candidates[idx].Version, "m", time.Now().Add(time.Hour)); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	r.Candidates = []SelectionCandidate{candidates[idx]}
	if _, e = s.SelectAccount(ctx, l, r); !errors.Is(e, ErrNotFound) {
		t.Fatal("stale candidate selected", e)
	}
}
func TestUsageExactFiltersDailyPagesAndAuditIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, sc := tenant(t, s)
	zero := int64(0)
	cost := "0"
	start := time.Now().Add(-time.Minute)
	for n := range 3 {
		l := leaseFor(t, s, sc)
		outcome := OutcomeSucceeded
		if n == 2 {
			outcome = OutcomeUpstreamError
		}
		if e := s.RecordUsage(ctx, l, Usage{Model: fmt.Sprint(n), Provider: "provider", Outcome: outcome, InputTokens: &zero, PriceKnown: true, Cost: &cost}); e != nil {
			t.Fatal(e)
		}
	}
	end := time.Now().Add(time.Minute)
	a, e := s.QueryAggregate(ctx, sc, UsageFilter{UserID: sc.UserID, Start: &start, End: &end, Provider: "provider"})
	if e != nil || a.Requests != 3 || a.FailedRequests != 1 || a.KnownInput != 3 || a.KnownOutput != 0 || a.MissingPrice != 0 {
		t.Fatal(a, e)
	}
	rows, next, e := s.QueryUsage(ctx, sc, UsageFilter{UserID: sc.UserID, Provider: "provider", Limit: 1})
	if e != nil || len(rows) != 1 || next == "" {
		t.Fatal(rows, next, e)
	}
	if _, _, e = s.QueryUsage(ctx, sc, UsageFilter{UserID: sc.UserID, Provider: "wrong", Limit: 1, After: next}); !errors.Is(e, ErrConflict) {
		t.Fatal("cursor filter rebinding", e)
	}
	days, next, e := s.QueryDailyUsagePage(ctx, sc, UsageFilter{UserID: sc.UserID, Limit: 1})
	if e != nil || len(days) != 1 || next == "" {
		t.Fatal(days, next, e)
	}
	days, _, e = s.QueryDailyUsagePage(ctx, sc, UsageFilter{UserID: sc.UserID, Limit: 1, After: next})
	if e != nil || len(days) != 1 {
		t.Fatal(days, e)
	}
	old := time.Now().Add(-60 * 24 * time.Hour)
	if _, e = s.QueryAggregate(ctx, sc, UsageFilter{UserID: sc.UserID, Start: &old}); !errors.Is(e, ErrRangeUnavailable) {
		t.Fatal(e)
	}
	i := core.Identity{Issuer: "issuer", Subject: "operator"}
	if _, e = s.QueryInstanceAggregate(ctx, i, UsageFilter{UserID: sc.UserID}); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	audits, _, e := s.QueryInstanceAudits(ctx, i, AuditFilter{})
	if e != nil || len(audits) != 0 {
		t.Fatal("organization audit leaked", audits, e)
	}
	h, e := s.OperationalHealth(ctx, i)
	if e != nil || h.UsageRecording.LastSuccess == nil {
		t.Fatal(h, e)
	}
}
func TestProviderStateAtomicMergeKeyFenceAndExpiry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, sc := tenant(t, s)
	l := leaseFor(t, s, sc)
	policy := ProviderStatePolicy{time.Hour, 256, 2 << 20, 64 << 20}
	var wg sync.WaitGroup
	for n := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			e := s.UpdateProviderState(ctx, l.Principal, "google", "session", policy, func(old []byte) ([]byte, error) {
				v := map[string]bool{}
				if old != nil {
					if e := json.Unmarshal(old, &v); e != nil {
						return nil, e
					}
				}
				v[fmt.Sprint(n)] = true
				return json.Marshal(v)
			})
			if e != nil {
				t.Error(e)
			}
		}(n)
	}
	wg.Wait()
	got, e := s.GetProviderState(ctx, l.Principal, "google", "session")
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]bool
	if e = json.Unmarshal(got.Data, &v); e != nil || len(v) != 20 {
		t.Fatal(v, e)
	}
	other := leaseFor(t, s, sc)
	if _, e = s.GetProviderState(ctx, other.Principal, "google", "session"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if e = s.RevokeKey(ctx, sc, l.Principal.KeyID); e != nil {
		t.Fatal(e)
	}
	if e = s.UpdateProviderState(ctx, l.Principal, "google", "session", policy, func([]byte) ([]byte, error) { t.Fatal("merge ran after revocation"); return nil, nil }); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	if _, e = s.GetProviderState(ctx, l.Principal, "google", "session"); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
}

func TestSelfRevocationIdempotenceDeliveryAndNewStateRotation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, sc := tenant(t, s)
	k, raw, e := s.IssueKey(ctx, sc, "self", nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeOwnKey(ctx, raw, k.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeOwnKey(ctx, raw, k.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeOwnKey(ctx, raw, "other"); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	i, e := s.Invite(ctx, sc, "delivery@example.com")
	if e != nil || i.OrganizationName == "" || i.DeliveryState != "queued" {
		t.Fatal(i, e)
	}
	m, e := s.ClaimMail(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.WithMailAttempt(ctx, m, func(context.Context, Mail) MailOutcome { return MailSuccess }); e != nil {
		t.Fatal(e)
	}
	list, e := s.ListInvitations(ctx, sc)
	if e != nil || len(list) != 1 || list[0].DeliveryState != "sent" {
		t.Fatal(list, e)
	}
	execSQL(t, s, "UPDATE invitations SET created_at=clock_timestamp()-interval '2 minutes'")
	if _, e = s.ResendInvitation(ctx, sc, i.ID); e != nil {
		t.Fatal(e)
	}
	audits, _, e := s.QueryAudits(ctx, sc, AuditFilter{Action: "invitation.resent", Outcome: AuditSucceeded})
	if e != nil || len(audits) != 1 {
		t.Fatal(audits, e)
	}
	l := leaseFor(t, s, sc)
	policy := ProviderStatePolicy{time.Hour, 4, 4096, 8192}
	if e = s.PutProviderState(ctx, l.Principal, "wire", "state", ProviderState{Data: []byte("secret")}, policy); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveProviderFlow(ctx, sc, "rotate", []byte("flow-secret"), time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	key := make([]byte, 32)
	key[0] = 8
	s.keys, _ = core.NewEnvelope("new", map[string][]byte{"v1": make([]byte, 32), "new": key})
	if e = s.RotateEncryption(ctx); e != nil {
		t.Fatal(e)
	}
	s.keys, _ = core.NewEnvelope("new", map[string][]byte{"new": key})
	if p, e := s.GetProviderState(ctx, l.Principal, "wire", "state"); e != nil || string(p.Data) != "secret" {
		t.Fatal(p, e)
	}
	if p, e := s.GetProviderFlow(ctx, sc, "rotate"); e != nil || string(p) != "flow-secret" {
		t.Fatal(string(p), e)
	}
}
func TestProviderFlowRemovalAndScopeDispatchFence(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, owner := tenant(t, s)
	u := user(t, s, "oauth-member")
	sc := addMember(t, s, owner.OrganizationID, u)
	if e := s.SaveProviderFlow(ctx, sc, "f", []byte("pending"), time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	_, claim, e := s.ClaimProviderFlow(ctx, sc, "f")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.RemoveMember(ctx, owner, u.ID); e != nil {
		t.Fatal(e)
	}
	addMember(t, s, owner.OrganizationID, u)
	if e = s.WithScopeDispatch(ctx, sc, func(context.Context) error { t.Fatal("removed scope dispatch"); return nil }); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	if e = s.CompleteProviderFlow(ctx, sc, "f", claim, "new", []byte("late")); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
}
