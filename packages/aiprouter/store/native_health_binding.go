// SPDX-License-Identifier: Apache-2.0
package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

type NativeHealthBinding struct{ Kind, ID, Version string }

func encodeHealthBindings(b []NativeHealthBinding) []byte {
	if b == nil {
		return []byte(`[]`)
	}
	raw, _ := json.Marshal(b)
	return raw
}
func (s *Store) checkNativeHealthBindings(ctx context.Context, tx *sql.Tx, l Lease, bindings []NativeHealthBinding) error {
	if len(bindings) > 8 {
		return ErrConflict
	}
	sc := l.Principal.Scope
	seen := map[string]bool{}
	for _, b := range bindings {
		key := b.Kind + "/" + b.ID
		if (b.Kind != "route" && b.Kind != "pool") || b.ID == "" || b.Version == "" || seen[key] {
			return ErrConflict
		}
		seen[key] = true
		var valid bool
		if e := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM private_data WHERE org_id=$1 AND user_id=$2 AND kind=$3 AND id=$4 AND version=$5)", sc.OrganizationID, sc.UserID, b.Kind, b.ID, b.Version).Scan(&valid); e != nil {
			return e
		}
		if !valid {
			return ErrForbidden
		}
	}
	return nil
}
func (s *Store) UpdateNativeHealthBound(ctx context.Context, l Lease, k NativeHealthKey, bindings []NativeHealthBinding, merge func(NativeHealth) (NativeHealth, error)) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if e := s.healthAuthority(ctx, tx, l, k); e != nil {
			return e
		}
		if e := s.checkNativeHealthBindings(ctx, tx, l, bindings); e != nil {
			return e
		}
		h, e := s.readHealth(ctx, tx, l, k)
		if e != nil {
			return e
		}
		return s.mergeHealth(ctx, tx, l, k, h, merge, false)
	})
}
