package storage

import (
	"context"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"math"
)

// Each decision commits independently of the request's idempotency/work transaction,
// so invalid/replayed requests cannot roll back token consumption.
func (r *Repository) consumeBucket(ctx context.Context, key string, burst int, rate float64) error {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `INSERT INTO request_buckets(bucket,tokens,updated_at) VALUES($1,$2,clock_timestamp()) ON CONFLICT DO NOTHING`, key, burst); e != nil {
		return e
	}
	var tokens float64
	if e = tx.QueryRow(ctx, `SELECT LEAST($2::double precision,tokens+GREATEST(0,EXTRACT(EPOCH FROM clock_timestamp()-updated_at))*$3::double precision) FROM request_buckets WHERE bucket=$1 FOR UPDATE`, key, burst, rate).Scan(&tokens); e != nil {
		return e
	}
	permitted := tokens >= 1
	if permitted {
		tokens--
	}
	if _, e = tx.Exec(ctx, `UPDATE request_buckets SET tokens=$2,updated_at=clock_timestamp() WHERE bucket=$1`, key, tokens); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	if !permitted {
		return &domain.LimitError{Code: "request_rate", RetryAfter: max(1, int(math.Ceil((1-tokens)/rate)))}
	}
	return nil
}
func (r *Repository) AdmitRequest(ctx context.Context, principal string) error {
	var enabled bool
	var burst int
	var rate float64
	if e := r.pool.QueryRow(ctx, `SELECT enabled,rate_burst,rate_per_second FROM scheduler_policy`).Scan(&enabled, &burst, &rate); e != nil {
		return e
	}
	if !enabled {
		return nil
	}
	return r.consumeBucket(ctx, "submit:"+principal, burst, rate)
}
func (r *Repository) AcquireSSE(ctx context.Context, principal string) (string, error) {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(2349012512)`); e != nil {
		return "", e
	}
	var global, per int
	if e = tx.QueryRow(ctx, `SELECT sse_global,sse_principal FROM scheduler_policy`).Scan(&global, &per); e != nil {
		return "", e
	}
	var full bool
	if e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM sse_connections WHERE expires_at>clock_timestamp())>=$1 OR(SELECT count(*) FROM sse_connections WHERE principal=$2 AND expires_at>clock_timestamp())>=$3`, global, principal, per).Scan(&full); e != nil {
		return "", e
	}
	if full {
		return "", &domain.LimitError{Code: "sse_capacity", RetryAfter: 5}
	}
	if _, e = tx.Exec(ctx, `DELETE FROM sse_connections WHERE id IN(SELECT id FROM sse_connections WHERE expires_at<=clock_timestamp() ORDER BY expires_at LIMIT 128)`); e != nil {
		return "", e
	}
	var id string
	if e = tx.QueryRow(ctx, `INSERT INTO sse_connections(principal,expires_at) VALUES($1,clock_timestamp()+interval '30 seconds') RETURNING id::text`, principal).Scan(&id); e != nil {
		return "", e
	}
	return id, tx.Commit(ctx)
}
func (r *Repository) RenewSSE(ctx context.Context, principal, id string) error {
	tag, e := r.pool.Exec(ctx, `UPDATE sse_connections SET expires_at=clock_timestamp()+interval '30 seconds' WHERE id=$1 AND principal=$2 AND expires_at>clock_timestamp()`, id, principal)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrUnauthorized
	}
	return nil
}
func (r *Repository) ReleaseSSE(ctx context.Context, principal, id string) error {
	_, e := r.pool.Exec(ctx, `DELETE FROM sse_connections WHERE id=$1 AND principal=$2`, id, principal)
	return e
}
