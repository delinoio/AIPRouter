// SPDX-License-Identifier: Apache-2.0
package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestCompactionHandoffOwnershipExpiryAndReplacement(t *testing.T) {
	s, l, v := nativeFixture(t)
	ctx := context.Background()
	value := CompactionHandoff{AccountID: "native", CredentialVersion: v, Data: []byte(`{"model":"physical","config":"captured"}`)}
	if e := s.PutCompactionHandoff(ctx, l, "lane", value); e != nil {
		t.Fatal(e)
	}
	other := leaseFor(t, s, l.Principal.Scope)
	if _, e := s.GetCompactionHandoff(ctx, other, "lane"); !errors.Is(e, ErrNotFound) {
		t.Fatal("handoff leaked across keys", e)
	}
	if e := s.RotateEncryption(ctx); e != nil {
		t.Fatal(e)
	}
	got, e := s.GetCompactionHandoff(ctx, l, "lane")
	if e != nil || string(got.Data) != string(value.Data) || got.AccountID != value.AccountID {
		t.Fatal(got, e)
	}
	execSQL(t, s, "UPDATE "+s.temp+".compaction_handoffs SET expires_at=clock_timestamp()-interval '1 second'")
	if _, e = s.GetCompactionHandoff(ctx, l, "lane"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if e = s.PutCompactionHandoff(ctx, l, "lane", value); e != nil {
		t.Fatal(e)
	}
	if e = s.PutPrivate(ctx, l.Principal.Scope, "provider", "native", []byte("replacement")); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetCompactionHandoff(ctx, l, "lane"); !errors.Is(e, ErrForbidden) {
		t.Fatal("replacement reused old handoff", e)
	}
	if e = s.PutCompactionHandoff(ctx, l, "lane", value); !errors.Is(e, ErrForbidden) {
		t.Fatal("late old success published", e)
	}
	if e = s.ClearCompactionHandoff(ctx, l, "lane"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetCompactionHandoff(ctx, l, "lane"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
}
func TestCompactionHandoffCapacityAndIdleTouch(t *testing.T) {
	s, l, v := nativeFixture(t)
	ctx := context.Background()
	value := CompactionHandoff{AccountID: "native", CredentialVersion: v, Data: []byte(`{}`)}
	if e := s.PutCompactionHandoff(ctx, l, "lane", value); e != nil {
		t.Fatal(e)
	}
	// Populate eviction candidates without thousands of unrelated authorization transactions.
	execSQL(t, s, "INSERT INTO "+s.temp+".compaction_handoffs(org_id,user_id,key_id,id,generation,value,size,last_used_at,expires_at) SELECT h.org_id,h.user_id,h.key_id,'seed-'||n,h.generation,h.value,h.size,clock_timestamp()-interval '1 hour',h.expires_at FROM "+s.temp+".compaction_handoffs h CROSS JOIN generate_series(1,2048) n WHERE h.id='lane'")
	if e := s.PutCompactionHandoff(ctx, l, "new", value); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.tx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM "+s.temp+".compaction_handoffs").Scan(&count)
	}); e != nil || count != 2048 {
		t.Fatal(count, e)
	}
	execSQL(t, s, "UPDATE "+s.temp+".compaction_handoffs SET expires_at=clock_timestamp()+interval '1 minute' WHERE id='new'")
	if _, e := s.GetCompactionHandoff(ctx, l, "new"); e != nil {
		t.Fatal(e)
	}
	var touched bool
	if e := s.tx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT expires_at>clock_timestamp()+interval '23 hours' FROM "+s.temp+".compaction_handoffs WHERE id='new'").Scan(&touched)
	}); e != nil || !touched {
		t.Fatal(touched, e)
	}
	value.Data = []byte(strings.Repeat("x", 4097))
	if e := s.PutCompactionHandoff(ctx, l, "oversize", value); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
}
