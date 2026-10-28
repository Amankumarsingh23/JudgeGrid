package storage

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) RegisterSandbox(ctx context.Context, p execution.Provenance) error {
	if p.Validate() != nil {
		return execution.ErrInvalid
	}
	var owner string
	err := r.pool.QueryRow(ctx, `SELECT owner_id FROM sandbox_authority WHERE submission_id=$1 AND run_id=$2 AND generation=$3 AND state='ACTIVE' AND lease_expires_at>clock_timestamp()`, p.SubmissionID, p.RunID, p.Generation).Scan(&owner)
	if err != nil {
		return err
	}
	if execution.OwnerHash(owner) != p.OwnerHash {
		return ErrOwned
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO sandbox_executions(execution_id,controller_id,run_id,submission_id,generation,owner_hash,label_created) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.ExecutionID, p.ControllerID, p.RunID, p.SubmissionID, p.Generation, p.OwnerHash, p.Created)
	return err
}
func sandboxLock(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,83217))`, id)
	return err
}
func (r *Repository) ProtectSandbox(ctx context.Context, id string, action func() error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = sandboxLock(ctx, tx, id); err != nil {
		return err
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT x.state='OPEN' AND o.state='ACTIVE' AND o.lease_expires_at>clock_timestamp() AND o.run_id=x.run_id AND o.generation=x.generation FROM sandbox_executions x JOIN sandbox_authority o ON o.run_id=x.run_id WHERE x.execution_id=$1`, id).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrOwned
	}
	if err = action(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) FinishSandbox(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = sandboxLock(ctx, tx, id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE sandbox_executions SET state='CLOSED',finished_at=clock_timestamp() WHERE execution_id=$1 AND state='OPEN'`, id)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReconcileSandbox serializes owned creation and peer reconcilers. Never lock a newer
// generation during old-resource cleanup: that would interfere with its heartbeat.
func (r *Repository) ReconcileSandbox(ctx context.Context, p execution.Provenance, apply bool, action func() error) (string, error) {
	if p.Validate() != nil {
		return "invalid", execution.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "unavailable", err
	}
	defer tx.Rollback(ctx)
	if err = sandboxLock(ctx, tx, p.ExecutionID); err != nil {
		return "unavailable", err
	}
	var actual execution.Provenance
	var state string
	err = tx.QueryRow(ctx, `SELECT execution_id,controller_id,run_id::text,submission_id::text,owner_hash,label_created,generation,state FROM sandbox_executions WHERE execution_id=$1`, p.ExecutionID).Scan(&actual.ExecutionID, &actual.ControllerID, &actual.RunID, &actual.SubmissionID, &actual.OwnerHash, &actual.Created, &actual.Generation, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "unknown", nil
	}
	if err != nil {
		return "unavailable", err
	}
	if actual != p {
		return "mismatch", nil
	}
	if state == "REAPED" {
		return "reaped", nil
	}
	var active bool
	var currentRun string
	var generation int
	if err = tx.QueryRow(ctx, `SELECT run_id::text,generation FROM sandbox_authority WHERE scope_id=(SELECT COALESCE(job_id,submission_id) FROM evaluation_runs WHERE id=$1)`, p.RunID).Scan(&currentRun, &generation); err != nil {
		return "unknown", err
	}
	if generation < p.Generation {
		return "mismatch", nil
	}
	if currentRun == p.RunID && generation == p.Generation {
		if err = tx.QueryRow(ctx, `SELECT state='ACTIVE' AND lease_expires_at>clock_timestamp() AND run_id=$2::uuid AND generation=$3 FROM sandbox_authority WHERE scope_id=(SELECT COALESCE(job_id,submission_id) FROM evaluation_runs WHERE id=$1)`, p.RunID, p.RunID, p.Generation).Scan(&active); err != nil {
			return "unavailable", err
		}
	}
	if active {
		return "active", nil
	}
	if !apply {
		return "eligible", nil
	}
	if action == nil {
		return "invalid", execution.ErrInvalid
	}
	if err = action(); err != nil {
		return "interrupted", err
	}
	if state != "REAPED" {
		if _, err = tx.Exec(ctx, `UPDATE sandbox_executions SET state='REAPED',finished_at=clock_timestamp() WHERE execution_id=$1`, p.ExecutionID); err != nil {
			return "unavailable", err
		}
	}
	return "reaped", tx.Commit(ctx)
}
