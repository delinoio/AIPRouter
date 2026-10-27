// SPDX-License-Identifier: Apache-2.0
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"time"
)

type Settings struct {
	RPM, Concurrency, UsageDays, AggregateDays, AuditDays int
	Destinations                                          []string
	Prices                                                json.RawMessage
}

func (s *Store) Settings(ctx context.Context) (v Settings, e error) {
	e = s.tx(ctx, func(tx *sql.Tx) error {
		var dest []byte
		e := tx.QueryRowContext(ctx, "SELECT rpm,concurrency,usage_days,aggregate_days,audit_days,destinations,prices FROM settings WHERE singleton").Scan(&v.RPM, &v.Concurrency, &v.UsageDays, &v.AggregateDays, &v.AuditDays, &dest, &v.Prices)
		if e != nil {
			return e
		}
		return json.Unmarshal(dest, &v.Destinations)
	})
	return
}
func (s *Store) IsOperator(i core.Identity) bool { return s.operators[i] }
func (s *Store) UpdateSettings(ctx context.Context, identity core.Identity, v Settings) error {
	if !s.IsOperator(identity) {
		return ErrForbidden
	}
	if v.RPM < 1 || v.RPM > 6000 || v.Concurrency < 1 || v.Concurrency > 100 || v.UsageDays < 1 || v.UsageDays > 90 || v.AggregateDays < 30 || v.AggregateDays > 730 || v.AuditDays < 30 || v.AuditDays > 365 {
		return ErrConflict
	}
	if len(v.Prices) == 0 {
		v.Prices = json.RawMessage(`{}`)
	}
	if !json.Valid(v.Prices) {
		return ErrConflict
	}
	d, e := json.Marshal(v.Destinations)
	if e != nil {
		return e
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "UPDATE settings SET rpm=$1,concurrency=$2,usage_days=$3,aggregate_days=$4,audit_days=$5,destinations=$6,prices=$7 WHERE singleton", v.RPM, v.Concurrency, v.UsageDays, v.AggregateDays, v.AuditDays, d, v.Prices)
		if e != nil {
			return e
		}
		for _, policy := range []struct {
			table, created string
			days           int
		}{{"usage", "created_at", v.UsageDays}, {"daily_usage", "day::timestamptz", v.AggregateDays}, {"audits", "created_at", v.AuditDays}} {
			if _, e = tx.ExecContext(ctx, "UPDATE "+policy.table+" SET expires_at=LEAST(expires_at,"+policy.created+"+$1*interval '1 day')", policy.days); e != nil {
				return e
			}
		}
		var actor string
		e = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE issuer=$1 AND subject=$2", identity.Issuer, identity.Subject).Scan(&actor)
		if e != nil && e != sql.ErrNoRows {
			return e
		}
		return s.audit(ctx, tx, "", actor, "instance.settings_changed", "")
	})
}

type Usage struct {
	ID, OrganizationID, UserID, Model, Provider, ErrorCode string
	InputTokens, OutputTokens                              *int64
	Cost                                                   *string
	Price                                                  json.RawMessage
	Provenance                                             string
	CreatedAt                                              time.Time
}
type Aggregate struct {
	Requests, InputTokens, OutputTokens, MissingUsage, MissingCost int64
	Cost                                                           string
}

// RecordUsage freezes supplied price provenance and cost. The lease ID is the
// idempotency key, so replaying completion cannot double-count daily totals.
func (s *Store) RecordUsage(ctx context.Context, l Lease, u Usage) error {
	if u.InputTokens != nil && *u.InputTokens < 0 || u.OutputTokens != nil && *u.OutputTokens < 0 {
		return ErrConflict
	}
	if len(u.Price) == 0 {
		u.Price = json.RawMessage(`null`)
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		sc := l.Principal.Scope
		if e := s.lock(ctx, tx, sc.OrganizationID); e != nil {
			return e
		}
		var ok bool
		e := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM admissions a JOIN users u ON u.id=a.user_id WHERE a.id=$1 AND a.org_id=$2 AND a.user_id=$3 AND a.key_id=$4 AND a.generation=$5)", l.ID, sc.OrganizationID, sc.UserID, l.Principal.KeyID, sc.Generation).Scan(&ok)
		if e != nil {
			return e
		}
		if !ok {
			return ErrForbidden
		}
		reserved, e := tx.ExecContext(ctx, "UPDATE admissions SET usage_recorded=true WHERE id=$1 AND NOT usage_recorded", l.ID)
		if e != nil {
			return e
		}
		changed, _ := reserved.RowsAffected()
		if changed == 0 {
			return nil
		}
		res, e := tx.ExecContext(ctx, `INSERT INTO usage(id,org_id,user_id,model,provider,error_code,input_tokens,output_tokens,cost,price,provenance,expires_at) SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,clock_timestamp()+usage_days*interval '1 day' FROM settings ON CONFLICT(id) DO NOTHING`, l.ID, sc.OrganizationID, sc.UserID, u.Model, u.Provider, u.ErrorCode, u.InputTokens, u.OutputTokens, u.Cost, u.Price, u.Provenance)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO daily_usage(id,org_id,user_id,day,model,requests,input_tokens,output_tokens,cost,missing_usage,missing_cost,expires_at) SELECT $1,$2,$3,(clock_timestamp() AT TIME ZONE 'UTC')::date,$4,1,COALESCE($5::bigint,0),COALESCE($6::bigint,0),COALESCE($7::numeric,0),CASE WHEN $5::bigint IS NULL OR $6::bigint IS NULL THEN 1 ELSE 0 END,CASE WHEN $7::numeric IS NULL THEN 1 ELSE 0 END,clock_timestamp()+aggregate_days*interval '1 day' FROM settings ON CONFLICT(org_id,user_id,day,model) DO UPDATE SET requests=daily_usage.requests+1,input_tokens=daily_usage.input_tokens+excluded.input_tokens,output_tokens=daily_usage.output_tokens+excluded.output_tokens,cost=daily_usage.cost+excluded.cost,missing_usage=daily_usage.missing_usage+excluded.missing_usage,missing_cost=daily_usage.missing_cost+excluded.missing_cost`, core.ID(), sc.OrganizationID, sc.UserID, u.Model, u.InputTokens, u.OutputTokens, u.Cost)
		return e
	})
}
func (s *Store) ListUsage(ctx context.Context, sc core.Scope, user string, limit int) (out []Usage, e error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	e = s.tx(ctx, func(tx *sql.Tx) error {
		r, e := s.guard(ctx, tx, sc)
		if e != nil {
			return e
		}
		if user != sc.UserID && !elevated(r) {
			return ErrForbidden
		}
		rows, e := tx.QueryContext(ctx, "SELECT id,org_id,COALESCE(user_id,''),model,provider,error_code,input_tokens,output_tokens,cost::text,price,provenance,created_at FROM usage WHERE org_id=$1 AND ($2='' OR user_id=$2) AND expires_at>clock_timestamp() ORDER BY created_at DESC LIMIT $3", sc.OrganizationID, user, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var u Usage
			if e = rows.Scan(&u.ID, &u.OrganizationID, &u.UserID, &u.Model, &u.Provider, &u.ErrorCode, &u.InputTokens, &u.OutputTokens, &u.Cost, &u.Price, &u.Provenance, &u.CreatedAt); e != nil {
				return e
			}
			out = append(out, u)
		}
		return rows.Err()
	})
	return
}
func aggregate(ctx context.Context, tx *sql.Tx, org, user string) (a Aggregate, e error) {
	e = tx.QueryRowContext(ctx, "SELECT COALESCE(sum(requests),0),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(cost),0)::text,COALESCE(sum(missing_usage),0),COALESCE(sum(missing_cost),0) FROM daily_usage WHERE ($1='' OR org_id=$1) AND ($2='' OR user_id=$2) AND expires_at>clock_timestamp()", org, user).Scan(&a.Requests, &a.InputTokens, &a.OutputTokens, &a.Cost, &a.MissingUsage, &a.MissingCost)
	return
}
func (s *Store) AggregateUsage(ctx context.Context, sc core.Scope, user string) (a Aggregate, e error) {
	e = s.tx(ctx, func(tx *sql.Tx) error {
		r, e := s.guard(ctx, tx, sc)
		if e != nil {
			return e
		}
		if user != sc.UserID && !elevated(r) {
			return ErrForbidden
		}
		a, e = aggregate(ctx, tx, sc.OrganizationID, user)
		return e
	})
	return
}
func (s *Store) InstanceAggregate(ctx context.Context, i core.Identity) (a Aggregate, e error) {
	if !s.IsOperator(i) {
		return a, ErrForbidden
	}
	e = s.tx(ctx, func(tx *sql.Tx) error { var err error; a, err = aggregate(ctx, tx, "", ""); return err })
	return
}

type Audit struct {
	ID, OrganizationID, ActorID, Action, TargetID string
	CreatedAt                                     time.Time
}

func (s *Store) audits(ctx context.Context, tx *sql.Tx, org string) (out []Audit, e error) {
	rows, e := tx.QueryContext(ctx, "SELECT id,COALESCE(org_id,''),COALESCE(actor_id,''),action,target_id,created_at FROM audits WHERE COALESCE(org_id,'')=$1 AND expires_at>clock_timestamp() ORDER BY created_at DESC LIMIT 1000", org)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var a Audit
		if e = rows.Scan(&a.ID, &a.OrganizationID, &a.ActorID, &a.Action, &a.TargetID, &a.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) ListAudits(ctx context.Context, sc core.Scope) (out []Audit, e error) {
	e = s.tx(ctx, func(tx *sql.Tx) error {
		r, e := s.guard(ctx, tx, sc)
		if e != nil {
			return e
		}
		if !elevated(r) {
			return ErrForbidden
		}
		out, e = s.audits(ctx, tx, sc.OrganizationID)
		return e
	})
	return
}
func (s *Store) InstanceAudits(ctx context.Context, i core.Identity) (out []Audit, e error) {
	if !s.IsOperator(i) {
		return nil, ErrForbidden
	}
	e = s.tx(ctx, func(tx *sql.Tx) error { out, e = s.audits(ctx, tx, ""); return e })
	return
}
func (s *Store) Cleanup(ctx context.Context) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		// Use the same gate as request and device mutations before deleting rows.
		rows, e := tx.QueryContext(ctx, "SELECT id FROM organizations ORDER BY id")
		if e != nil {
			return e
		}
		var ids []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, id := range ids {
			if e = s.lock(ctx, tx, id); e != nil {
				return e
			}
		}

		queries := []string{
			"UPDATE api_keys SET revoked=true WHERE NOT handed_off AND handoff_deadline<=clock_timestamp()",
			"DELETE FROM " + s.temp + ".devices WHERE expires_at<=clock_timestamp()",
			"DELETE FROM " + s.temp + ".states WHERE expires_at<=clock_timestamp()",
			"DELETE FROM " + s.temp + ".login_states WHERE expires_at<=clock_timestamp()",
			"DELETE FROM sessions WHERE created_at<=clock_timestamp()-interval '7 days' OR last_seen<=clock_timestamp()-interval '24 hours'",
			"UPDATE invitations SET status='failed',claim=NULL WHERE status='pending' AND attempts>=3 AND claim_until<=clock_timestamp()",
			"DELETE FROM " + s.temp + ".mail_tokens WHERE invitation_id IN(SELECT id FROM invitations WHERE status<>'pending' OR expires_at<=clock_timestamp())",
			"DELETE FROM invitations WHERE expires_at<=clock_timestamp()",
			"DELETE FROM transfers WHERE expires_at<=clock_timestamp()",
			"DELETE FROM usage WHERE expires_at<=clock_timestamp()",
			"DELETE FROM daily_usage WHERE expires_at<=clock_timestamp()",
			"DELETE FROM audits WHERE expires_at<=clock_timestamp()",
			"DELETE FROM mail_attempts WHERE attempted_at<=clock_timestamp()-interval '1 hour'",
			// Retain admission IDs beyond usage TTL so duplicate completion cannot recreate expired usage.
			"DELETE FROM admissions WHERE released AND created_at<=clock_timestamp()-interval '731 days'",
			"DELETE FROM artifacts WHERE expires_at<=clock_timestamp() OR (state='pending' AND upload_deadline<=clock_timestamp())",
		}
		for _, q := range queries {
			if _, e := tx.ExecContext(ctx, q); e != nil {
				return e
			}
		}
		return nil
	})
}

// InvalidateAfterRestore must run offline, before any listeners or workers start.
func (s *Store) InvalidateAfterRestore(ctx context.Context) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{"UPDATE api_keys SET revoked=true", "DELETE FROM sessions", "UPDATE invitations SET status='canceled',claim=NULL", "DELETE FROM transfers", "DELETE FROM admissions", "DELETE FROM " + s.temp + ".devices", "DELETE FROM " + s.temp + ".mail_tokens", "DELETE FROM " + s.temp + ".login_states", "DELETE FROM " + s.temp + ".states"} {
			if _, e := tx.ExecContext(ctx, q); e != nil {
				return e
			}
		}
		return nil
	})
}
