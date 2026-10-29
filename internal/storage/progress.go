package storage

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/progress"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) ProgressSnapshot(ctx context.Context, principal, id string) (s progress.Snapshot, e error) {
	tx, e := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return s, e
	}
	defer tx.Rollback(ctx)
	e = tx.QueryRow(ctx, `SELECT s.id::text,s.status,COALESCE(r.final_verdict,s.verdict),CASE WHEN x.run_id IS NOT NULL THEN s.verdict END,s.problem_version_id,COALESCE(r.problem_version_id,s.problem_version_id),x.run_id::text,j.id::text,j.state FROM submissions s LEFT JOIN selected_evaluations x ON x.submission_id=s.id LEFT JOIN evaluation_runs r ON r.id=x.run_id LEFT JOIN LATERAL(SELECT id,state FROM evaluation_jobs WHERE submission_id=s.id ORDER BY ordinal DESC LIMIT 1)j ON true WHERE s.id=$1 AND s.principal_id=$2`, id, principal).Scan(&s.SubmissionID, &s.Status, &s.Verdict, &s.OriginalVerdict, &s.OriginalVersion, &s.CurrentVersion, &s.SelectedRun, &s.JobID, &s.JobStatus)
	if errors.Is(e, pgx.ErrNoRows) {
		return s, domain.ErrNotFound
	}
	if e != nil {
		return s, e
	}
	if e = tx.QueryRow(ctx, `SELECT COALESCE((SELECT seq FROM public_event_counters WHERE submission_id=$1),0),COALESCE((SELECT min(seq) FROM public_events WHERE submission_id=$1),0)`, id).Scan(&s.Cursor, &s.First); e != nil {
		return s, e
	}
	return s, tx.Commit(ctx)
}
func (r *Repository) ProgressEvents(ctx context.Context, principal, id string, after, upper int64, limit int) ([]progress.Event, error) {
	if after < 0 || upper < 0 || limit < 1 || limit > progress.PageSize {
		return nil, domain.ErrInvalid
	}
	rows, e := r.pool.Query(ctx, `SELECT e.seq,e.kind,e.payload,e.created_at FROM public_events e JOIN submissions s ON s.id=e.submission_id WHERE e.submission_id=$1 AND s.principal_id=$2 AND e.seq>$3 AND($4::bigint=0 OR e.seq<=$4) ORDER BY e.seq LIMIT $5`, id, principal, after, upper, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []progress.Event{}
	for rows.Next() {
		var event progress.Event
		if e = rows.Scan(&event.Sequence, &event.Kind, &event.Data, &event.At); e != nil {
			return nil, e
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

// Observation is fenced, phase-only, and serialized with the submission journal.
func (r *Repository) ObserveSandboxPhase(ctx context.Context, p execution.Provenance, kind string) error {
	if p.Validate() != nil || (kind != "preparing" && kind != "compiling" && kind != "running") {
		return domain.ErrInvalid
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var sid string
	if e = tx.QueryRow(ctx, `SELECT id::text FROM submissions WHERE id=$1 FOR UPDATE`, p.SubmissionID).Scan(&sid); e != nil {
		return e
	}
	var owner string
	var job *string
	if e = tx.QueryRow(ctx, `SELECT a.owner_id,r.job_id::text FROM sandbox_authority a JOIN evaluation_runs r ON r.id=a.run_id WHERE a.run_id=$1 AND a.submission_id=$2 AND a.generation=$3 AND a.state='ACTIVE' AND a.lease_expires_at>clock_timestamp()`, p.RunID, p.SubmissionID, p.Generation).Scan(&owner, &job); e != nil {
		return e
	}
	if execution.OwnerHash(owner) != p.OwnerHash {
		return ErrOwned
	}
	if _, e = tx.Exec(ctx, `SELECT set_config('judgegrid.execution_owner',$1,true),set_config('judgegrid.execution_generation',$2,true),set_config('judgegrid.execution_run',$3,true)`, owner, fmtGeneration(p.Generation), p.RunID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `SELECT assert_execution_fence($1,$2)`, p.SubmissionID, p.RunID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `SELECT append_public_event($1,$2,jsonb_build_object('status','RUNNING','evaluation_job_id',$3::text,'evaluation_run_id',$4::text,'generation',$5::int))`, p.SubmissionID, kind, job, p.RunID, p.Generation); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
