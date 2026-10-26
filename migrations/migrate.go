// Package migrations embeds and applies ordered, checksum-verified SQL migrations.
package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

func Apply(ctx context.Context, pool *pgxpool.Pool) error { return run(ctx, pool, false) }
func Check(ctx context.Context, pool *pgxpool.Pool) error { return run(ctx, pool, true) }
func run(ctx context.Context, pool *pgxpool.Pool, check bool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serialize migration runners, including the initial tracking-table creation.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(2349012502)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return err
	}
	for _, name := range names {
		data, _ := files.ReadFile(name)
		checksum := fmt.Sprintf("%x", sha256.Sum256(data))
		var stored string
		err = tx.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name=$1`, name).Scan(&stored)
		if err == nil {
			if stored != checksum {
				return fmt.Errorf("migration checksum mismatch: %s", name)
			}
			continue
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if check {
			return fmt.Errorf("pending migration: %s", name)
		}
		if _, err = tx.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)`, name, checksum); err != nil {
			return err
		}
	}
	if check {
		return nil
	}
	return tx.Commit(ctx)
}
