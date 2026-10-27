// SPDX-License-Identifier: Apache-2.0
package store

import (
	"context"
	"database/sql"
	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"time"
)

type UsageFilter struct {
	UserID, Model, After string
	Start, End           *time.Time
	Limit                int
}
type DailyUsage struct {
	Date          time.Time
	UserID, Model string
	Aggregate
}

func (s *Store) usageGuard(ctx context.Context, tx *sql.Tx, sc core.Scope, f UsageFilter) error {
	r, e := s.guard(ctx, tx, sc)
	if e != nil {
		return e
	}
	if f.UserID != sc.UserID && !elevated(r) {
		return ErrForbidden
	}
	return nil
}
func (s *Store) QueryUsage(ctx context.Context, sc core.Scope, f UsageFilter) (out []Usage, next string, e error) {
	if f.Limit < 1 || f.Limit > 1000 {
		f.Limit = 100
	}
	e = s.tx(ctx, func(tx *sql.Tx) error {
		if e := s.usageGuard(ctx, tx, sc, f); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT id,org_id,COALESCE(user_id,''),model,provider,error_code,input_tokens,output_tokens,cost::text,price,provenance,created_at FROM usage WHERE org_id=$1 AND ($2='' OR user_id=$2) AND ($3='' OR model=$3) AND ($4::timestamptz IS NULL OR created_at >= $4) AND ($5::timestamptz IS NULL OR created_at < $5) AND ($6='' OR id<$6) AND expires_at>clock_timestamp() ORDER BY id DESC LIMIT $7`, sc.OrganizationID, f.UserID, f.Model, f.Start, f.End, f.After, f.Limit+1)
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
		if len(out) > f.Limit {
			out = out[:f.Limit]
			next = out[len(out)-1].ID
		}
		return rows.Err()
	})
	return
}
func (s *Store) QueryDailyUsage(ctx context.Context, sc core.Scope, f UsageFilter) (out []DailyUsage, e error) {
	e = s.tx(ctx, func(tx *sql.Tx) error {
		if e := s.usageGuard(ctx, tx, sc, f); e != nil {
			return e
		}
		var err error
		out, err = queryDaily(ctx, tx, sc.OrganizationID, f)
		return err
	})
	return
}
func queryDaily(ctx context.Context, tx *sql.Tx, org string, f UsageFilter) (out []DailyUsage, e error) {
	rows, e := tx.QueryContext(ctx, `SELECT day,COALESCE(user_id,''),model,sum(requests),sum(input_tokens),sum(output_tokens),sum(cost)::text,sum(missing_usage),sum(missing_cost) FROM daily_usage WHERE ($1='' OR org_id=$1) AND ($2='' OR user_id=$2) AND ($3='' OR model=$3) AND ($4::timestamptz IS NULL OR day>=($4 AT TIME ZONE 'UTC')::date) AND ($5::timestamptz IS NULL OR day<($5 AT TIME ZONE 'UTC')::date) AND expires_at>clock_timestamp() GROUP BY day,user_id,model ORDER BY day,user_id,model`, org, f.UserID, f.Model, f.Start, f.End)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var d DailyUsage
		if e = rows.Scan(&d.Date, &d.UserID, &d.Model, &d.Requests, &d.InputTokens, &d.OutputTokens, &d.Cost, &d.MissingUsage, &d.MissingCost); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) QueryAggregate(ctx context.Context, sc core.Scope, f UsageFilter) (a Aggregate, e error) {
	e = s.tx(ctx, func(tx *sql.Tx) error {
		if e := s.usageGuard(ctx, tx, sc, f); e != nil {
			return e
		}
		var err error
		a, err = queryAggregate(ctx, tx, sc.OrganizationID, f)
		return err
	})
	return
}
func queryAggregate(ctx context.Context, tx *sql.Tx, org string, f UsageFilter) (a Aggregate, e error) {
	e = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(requests),0),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(cost),0)::text,COALESCE(sum(missing_usage),0),COALESCE(sum(missing_cost),0) FROM daily_usage WHERE ($1='' OR org_id=$1) AND ($2='' OR user_id=$2) AND ($3='' OR model=$3) AND ($4::timestamptz IS NULL OR day>=($4 AT TIME ZONE 'UTC')::date) AND ($5::timestamptz IS NULL OR day<($5 AT TIME ZONE 'UTC')::date) AND expires_at>clock_timestamp()`, org, f.UserID, f.Model, f.Start, f.End).Scan(&a.Requests, &a.InputTokens, &a.OutputTokens, &a.Cost, &a.MissingUsage, &a.MissingCost)
	return
}
func (s *Store) QueryInstanceAggregate(ctx context.Context, i core.Identity, f UsageFilter) (a Aggregate, e error) {
	if !s.IsOperator(i) {
		return a, ErrForbidden
	}
	e = s.tx(ctx, func(tx *sql.Tx) error { var err error; a, err = queryAggregate(ctx, tx, "", f); return err })
	return
}
