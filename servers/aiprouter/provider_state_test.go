// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestProviderStateAtomicMergeAndRevocation(t *testing.T) {
	s := serverStore(t)
	ctx := context.Background()
	u, _ := testIdentity(t, s, "state-owner")
	org, e := s.CreateOrganization(ctx, u.ID, "state fixture")
	if e != nil {
		t.Fatal(e)
	}
	sc, _, e := s.Membership(ctx, org.ID, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	key, _, e := s.IssueKey(ctx, sc, "state fixture", nil)
	if e != nil {
		t.Fatal(e)
	}
	p := inference.Principal{OrganizationID: org.ID, UserID: u.ID, KeyID: key.ID, MembershipGeneration: sc.Generation}
	a := &inferenceAdapter{s: s}
	if _, found, e := a.LookupProviderState(ctx, p, "devin", "missing"); e != nil || found {
		t.Fatalf("absent: %v %v", found, e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- a.UpdateProviderState(ctx, p, "devin", "merge", time.Now().Add(time.Hour), func(current []byte) ([]byte, error) {
				n := 0
				if len(current) > 0 {
					var e error
					n, e = strconv.Atoi(string(current))
					if e != nil {
						return nil, e
					}
				}
				return []byte(strconv.Itoa(n + 1)), nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	value, found, e := a.LookupProviderState(ctx, p, "devin", "merge")
	if e != nil || !found || string(value.Data) != "12" {
		t.Fatalf("lost update: %q %v %v", value.Data, found, e)
	}
	conflict := errors.New("claim occupied")
	if e = a.UpdateProviderState(ctx, p, "devin", "merge", time.Now().Add(time.Hour), func([]byte) ([]byte, error) { return nil, conflict }); !errors.Is(e, conflict) {
		t.Fatalf("callback identity lost: %v", e)
	}
	if e = a.SaveProviderState(ctx, p, "devin", "oversize", inference.ProviderState{Data: make([]byte, 4097), ExpiresAt: time.Now().Add(time.Hour)}); e == nil {
		t.Fatal("oversized state accepted")
	}
	if e = s.RevokeKey(ctx, sc, key.ID); e != nil {
		t.Fatal(e)
	}
	if _, found, e = a.LookupProviderState(ctx, p, "devin", "merge"); e == nil || found {
		t.Fatalf("revocation treated as absence: %v %v", found, e)
	}
	called := false
	if e = a.UpdateProviderState(ctx, p, "devin", "merge", time.Now().Add(time.Hour), func([]byte) ([]byte, error) { called = true; return []byte("resurrected"), nil }); e == nil || called {
		t.Fatalf("revoked merge callback: %v %v", called, e)
	}
}
