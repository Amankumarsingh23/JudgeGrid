package storage

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func loadReference(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, version int64) (ref bundles.Reference, err error) {
	var count int
	err = db.QueryRow(ctx, `SELECT bundle_sha,comparator,test_count FROM problem_test_suites WHERE problem_version_id=$1`, version).Scan(&ref.Hash, &ref.Comparator, &count)
	if errors.Is(err, pgx.ErrNoRows) {
		return ref, domain.ErrNotFound
	}
	if err != nil {
		return ref, err
	}
	rows, err := db.Query(ctx, `SELECT case_id,ordinal,is_sample,input_sha,expected_sha FROM suite_cases WHERE problem_version_id=$1 ORDER BY ordinal`, version)
	if err != nil {
		return ref, err
	}
	defer rows.Close()
	for rows.Next() {
		var c bundles.CaseMeta
		if err = rows.Scan(&c.ID, &c.Order, &c.Sample, &c.InputHash, &c.ExpectedHash); err != nil {
			return ref, err
		}
		ref.Cases = append(ref.Cases, c)
	}
	if rows.Err() != nil {
		return ref, rows.Err()
	}
	if len(ref.Cases) != count {
		return ref, bundles.ErrIntegrity
	}
	for i, c := range ref.Cases {
		if c.Order != i+1 {
			return ref, bundles.ErrIntegrity
		}
	}
	return ref, nil
}

// BindSuite seals an immutable association. It is a trusted setup operation, never an HTTP endpoint.
func BindSuite(ctx context.Context, pool *pgxpool.Pool, version int64, ref bundles.Reference) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(2349012505)`); err != nil {
		return err
	}
	existing, err := loadReference(ctx, tx, version)
	if err == nil {
		if existing.Hash != ref.Hash || existing.Comparator != ref.Comparator || len(existing.Cases) != len(ref.Cases) {
			return domain.ErrConflict
		}
		for i, c := range ref.Cases {
			if c != existing.Cases[i] {
				return domain.ErrConflict
			}
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	for _, c := range ref.Cases {
		if _, err = tx.Exec(ctx, `INSERT INTO suite_cases(problem_version_id,case_id,ordinal,is_sample,input_sha,expected_sha) VALUES($1,$2,$3,$4,$5,$6)`, version, c.ID, c.Order, c.Sample, c.InputHash, c.ExpectedHash); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO problem_test_suites(problem_version_id,bundle_sha,comparator,test_count) VALUES($1,$2,$3,$4)`, version, ref.Hash, ref.Comparator, len(ref.Cases)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func SeedSuites(ctx context.Context, pool *pgxpool.Pool, store *bundles.Local) error {
	for _, slug := range []string{"two-sum", "maximum"} {
		bundle, _ := bundles.Demo(slug)
		ref, err := store.Put(bundle)
		if err != nil {
			return err
		}
		var version int64
		if err = pool.QueryRow(ctx, `SELECT v.id FROM problem_versions v JOIN problems p ON p.id=v.problem_id WHERE p.slug=$1 AND v.version_number=1`, slug).Scan(&version); err != nil {
			return err
		}
		if err = BindSuite(ctx, pool, version, ref); err != nil {
			return err
		}
	}
	return nil
}
