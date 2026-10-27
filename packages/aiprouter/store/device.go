// SPDX-License-Identifier: Apache-2.0
package store

import (
	"context"
	"database/sql"
	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"time"
)

type Device struct {
	ID, Code, Secret, Name, Server string
	ExpiresAt                      time.Time
	Interval                       int
}
type DeviceResult struct{ Status, RawKey, KeyID, OrganizationID string }

func (s *Store) StartDevice(ctx context.Context, name, server string) (d Device, e error) {
	d = Device{ID: core.ID(), Code: core.Token()[:12], Secret: core.Token(), Name: name, Server: server, Interval: 5}
	e = s.tx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "INSERT INTO "+s.temp+".devices(id,code,secret_hash,name,server) VALUES($1,$2,$3,$4,$5) RETURNING expires_at", d.ID, d.Code, core.Hash(d.Secret), name, server).Scan(&d.ExpiresAt)
	})
	return
}
func (s *Store) InspectDevice(ctx context.Context, code string) (d Device, e error) {
	e = s.tx(ctx, func(tx *sql.Tx) error {
		return noRows(tx.QueryRowContext(ctx, "SELECT id,code,name,server,expires_at FROM "+s.temp+".devices WHERE code=$1 AND status='pending' AND expires_at>clock_timestamp()", code).Scan(&d.ID, &d.Code, &d.Name, &d.Server, &d.ExpiresAt))
	})
	return
}
func (s *Store) ApproveDevice(ctx context.Context, sc core.Scope, code string, expiry *time.Time) (k Key, e error) {
	e = s.tx(ctx, func(tx *sql.Tx) error {
		if _, e := s.guard(ctx, tx, sc); e != nil {
			return e
		}
		var id, name, status string
		var deadline time.Time
		e := tx.QueryRowContext(ctx, "SELECT id,name,status,expires_at FROM "+s.temp+".devices WHERE code=$1 AND expires_at>clock_timestamp() FOR UPDATE", code).Scan(&id, &name, &status, &deadline)
		if e != nil {
			return noRows(e)
		}
		if status != "pending" {
			return ErrConflict
		}
		var raw string
		k, raw, e = s.issueKey(ctx, tx, sc, name, expiry)
		if e != nil {
			return e
		}
		v, e := s.keys.Encrypt([]byte(raw), "device/"+id)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE api_keys SET handed_off=false,handoff_deadline=$2 WHERE id=$1", k.ID, deadline); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "UPDATE "+s.temp+".devices SET status='approved',org_id=$2,user_id=$3,generation=$4,key_id=$5,value=$6 WHERE id=$1", id, sc.OrganizationID, sc.UserID, sc.Generation, k.ID, v)
		return e
	})
	return
}
func (s *Store) DenyDevice(ctx context.Context, code string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, "UPDATE "+s.temp+".devices SET status='denied' WHERE code=$1 AND status='pending' AND expires_at>clock_timestamp()", code)
		if e != nil {
			return e
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
func (s *Store) PollDevice(ctx context.Context, id, secret string) (out DeviceResult, e error) {
	var resultError error
	e = s.tx(ctx, func(tx *sql.Tx) error {
		if e := s.lockDeviceOrganization(ctx, tx, id, secret); e != nil {
			return e
		}
		var value []byte
		var expires time.Time
		var last *time.Time
		var now time.Time
		var org, user sql.NullString
		var generation sql.NullInt64
		var key sql.NullString
		e := tx.QueryRowContext(ctx, "SELECT status,value,key_id,org_id,user_id,generation,expires_at,last_poll,clock_timestamp() FROM "+s.temp+".devices WHERE id=$1 AND secret_hash=$2 FOR UPDATE", id, core.Hash(secret)).Scan(&out.Status, &value, &key, &org, &user, &generation, &expires, &last, &now)
		if e != nil {
			return noRows(e)
		}
		out.KeyID = key.String
		out.OrganizationID = org.String
		if out.Status == "confirmed" {
			return nil
		}
		if !expires.After(now) {
			return ErrExpired
		}
		if last != nil && now.Sub(*last) < 5*time.Second {
			return &LimitError{5*time.Second - now.Sub(*last)}
		}
		if _, e = tx.ExecContext(ctx, "UPDATE "+s.temp+".devices SET last_poll=clock_timestamp() WHERE id=$1", id); e != nil {
			return e
		}
		if out.Status == "pending" {
			resultError = ErrPending
			return nil
		}
		if out.Status != "approved" {
			return ErrForbidden
		}
		var ok bool
		e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM api_keys k JOIN memberships m ON m.org_id=k.org_id AND m.user_id=k.user_id AND m.generation=k.generation WHERE k.id=$1 AND NOT k.revoked AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()) AND k.handoff_deadline>clock_timestamp())", key.String).Scan(&ok)
		if e != nil {
			return e
		}
		if !ok {
			return ErrForbidden
		}
		p, e := s.keys.Decrypt(value, "device/"+id)
		out.RawKey = string(p)
		return e
	})
	if e == nil {
		e = resultError
	}
	return
}
func (s *Store) ConfirmDevice(ctx context.Context, id, secret, key string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if e := s.lockDeviceOrganization(ctx, tx, id, secret); e != nil {
			return e
		}
		var status string
		var current string
		var valid bool
		e := tx.QueryRowContext(ctx, "SELECT status,COALESCE(key_id,''),expires_at>clock_timestamp() FROM "+s.temp+".devices WHERE id=$1 AND secret_hash=$2 FOR UPDATE", id, core.Hash(secret)).Scan(&status, &current, &valid)
		if e != nil {
			return noRows(e)
		}
		if key != current {
			return ErrForbidden
		}
		if status == "confirmed" {
			return nil
		}
		if !valid {
			return ErrExpired
		}
		if status != "approved" {
			return ErrForbidden
		}
		res, e := tx.ExecContext(ctx, "UPDATE api_keys SET handed_off=true WHERE id=$1 AND NOT revoked AND handoff_deadline>clock_timestamp()", key)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrForbidden
		}
		_, e = tx.ExecContext(ctx, "UPDATE "+s.temp+".devices SET status='confirmed',value=NULL WHERE id=$1", id)
		return e
	})
}
func (s *Store) AbortDevice(ctx context.Context, id, secret string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if e := s.lockDeviceOrganization(ctx, tx, id, secret); e != nil {
			return e
		}
		var key sql.NullString
		e := tx.QueryRowContext(ctx, "SELECT key_id FROM "+s.temp+".devices WHERE id=$1 AND secret_hash=$2 FOR UPDATE", id, core.Hash(secret)).Scan(&key)
		if e != nil {
			return noRows(e)
		}
		if key.Valid {
			if _, e = tx.ExecContext(ctx, "UPDATE api_keys SET revoked=true WHERE id=$1", key.String); e != nil {
				return e
			}
		}
		_, e = tx.ExecContext(ctx, "UPDATE "+s.temp+".devices SET status='denied',value=NULL WHERE id=$1", id)
		return e
	})
}

// Read the organization before locking the device to preserve org -> device -> key
// ordering shared by removal and approval. Pending-device transitions are row locked.
func (s *Store) lockDeviceOrganization(ctx context.Context, tx *sql.Tx, id, secret string) error {
	var org sql.NullString
	if e := tx.QueryRowContext(ctx, "SELECT org_id FROM "+s.temp+".devices WHERE id=$1 AND secret_hash=$2", id, core.Hash(secret)).Scan(&org); e != nil {
		return noRows(e)
	}
	if org.Valid {
		return s.lock(ctx, tx, org.String)
	}
	// Approval can assign an org after this read. Take the device row while still
	// pending; callers cannot update a key that appeared without retrying.
	var current sql.NullString
	if e := tx.QueryRowContext(ctx, "SELECT org_id FROM "+s.temp+".devices WHERE id=$1 FOR UPDATE", id).Scan(&current); e != nil {
		return noRows(e)
	}
	if current.Valid {
		return ErrConflict
	}
	return nil
}
