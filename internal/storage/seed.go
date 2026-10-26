package storage

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Seed is an explicit local CLI operation, with no HTTP mutation endpoint.
func Seed(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(2349012503)`); err != nil {
		return err
	}
	for _, p := range []struct{ slug, title, statement string }{
		{"two-sum", "Two Sum", "Read two integers and print their sum."},
		{"maximum", "Maximum", "Read three integers and print their maximum."},
	} {
		var id, versionID int64
		if err = tx.QueryRow(ctx, `INSERT INTO problems(slug,title) VALUES($1,$2) ON CONFLICT(slug) DO UPDATE SET slug=EXCLUDED.slug RETURNING id`, p.slug, p.title).Scan(&id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO problem_versions(problem_id,version_number,statement,time_limit_ms,memory_limit_kb) VALUES($1,1,$2,2000,262144) ON CONFLICT(problem_id,version_number) DO NOTHING`, id, p.statement); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `SELECT id FROM problem_versions WHERE problem_id=$1 AND version_number=1`, id).Scan(&versionID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE problems SET current_version_id=$2 WHERE id=$1 AND current_version_id IS NULL`, id, versionID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
