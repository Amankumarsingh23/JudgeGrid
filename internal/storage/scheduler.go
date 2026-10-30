package storage

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/scheduling"
	"github.com/jackc/pgx/v5"
)

func lockScheduler(ctx context.Context, tx pgx.Tx) error {
	_, e := tx.Exec(ctx, `SELECT scheduler_lock()`)
	return e
}
func admitScheduler(ctx context.Context, tx pgx.Tx, scope, principal string) error {
	_, e := tx.Exec(ctx, `SELECT scheduler_admit($1,$2)`, scope, principal)
	return e
}
func (r *Repository) SchedulerPolicy(ctx context.Context) (p scheduling.Policy, e error) {
	e = r.pool.QueryRow(ctx, `SELECT enabled,global_active,principal_active,pending_limit,rate_burst,rate_per_second,sse_global,sse_principal FROM scheduler_policy`).Scan(&p.Enabled, &p.GlobalActive, &p.PrincipalActive, &p.Pending, &p.Burst, &p.Rate, &p.SSEGlobal, &p.SSEPrincipal)
	if e != nil {
		return p, e
	}
	rows, e := r.pool.Query(ctx, `SELECT principal FROM scheduler_principals WHERE enabled ORDER BY principal`)
	if e != nil {
		return p, e
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			return p, e
		}
		p.Principals = append(p.Principals, s)
	}
	return p, rows.Err()
}
func (r *Repository) ConfigureScheduler(ctx context.Context, p scheduling.Policy) error {
	if e := p.Validate(); e != nil {
		return e
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lockScheduler(ctx, tx); e != nil {
		return e
	}
	if p.Enabled {
		var active int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM sandbox_authority WHERE state='ACTIVE' AND lease_expires_at>clock_timestamp()`).Scan(&active); e != nil {
			return e
		}
		if active > p.GlobalActive {
			return domain.ErrConflict
		}
		var over bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sandbox_authority a JOIN submissions s ON s.id=a.submission_id WHERE a.state='ACTIVE' AND a.lease_expires_at>clock_timestamp() GROUP BY s.principal_id HAVING count(*)>$1) OR EXISTS(SELECT 1 FROM scheduler_jobs WHERE NOT done AND principal=ANY($2::text[]) GROUP BY principal HAVING count(*)>$3)`, p.PrincipalActive, p.Principals, p.Pending).Scan(&over); e != nil {
			return e
		}
		if over {
			return domain.ErrConflict
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE scheduler_principals SET enabled=false WHERE enabled`); e != nil {
		return e
	}
	for _, s := range p.Principals {
		if _, e = tx.Exec(ctx, `INSERT INTO scheduler_principals(principal,enabled) VALUES($1,true) ON CONFLICT(principal) DO UPDATE SET enabled=true`, s); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE scheduler_policy SET enabled=$1,global_active=$2,principal_active=$3,pending_limit=$4,rate_burst=$5,rate_per_second=$6,sse_global=$7,sse_principal=$8,epoch=epoch+1`, p.Enabled, p.GlobalActive, p.PrincipalActive, p.Pending, p.Burst, p.Rate, p.SSEGlobal, p.SSEPrincipal); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (r *Repository) nextScheduled(ctx context.Context, allowed []string) (*scheduling.Candidate, error) {
	if len(allowed) < 1 || len(allowed) > 128 {
		return nil, domain.ErrInvalid
	}
	var enabled bool
	if e := r.pool.QueryRow(ctx, `SELECT enabled FROM scheduler_policy`).Scan(&enabled); e != nil {
		return nil, e
	}
	if !enabled {
		return nil, domain.ErrConflict
	}
	c := &scheduling.Candidate{}
	var scope string
	var jid *string
	e := r.pool.QueryRow(ctx, `SELECT scope_id::text,submission_id::text,job_id::text,principal,event_id::text FROM scheduler_next()`).Scan(&scope, &c.Job.SubmissionID, &jid, &c.Principal, &c.Job.EventID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	permitted := false
	for _, p := range allowed {
		permitted = permitted || p == c.Principal
	}
	if !permitted {
		return nil, nil
	}
	c.Job.Kind = queue.SubmissionJob
	c.Job.Attempt = 1
	if jid != nil {
		c.Job.Kind = queue.RejudgeJob
		c.Job.JobID = *jid
	}
	return c, c.Job.Validate()
}
func (r *Repository) ValidateNotification(ctx context.Context, allowed []string, j queue.Job) error {
	if j.Validate() != nil || j.EventID == "" {
		return domain.ErrInvalid
	}
	var valid bool
	e := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM outbox_events e JOIN submissions s ON s.id=e.submission_id WHERE e.id=$1 AND e.submission_id=$2 AND COALESCE(e.job_id::text,'')=$3 AND e.status='PUBLISHED' AND e.intent_generation=$4 AND s.principal_id=ANY($5::text[]) AND e.payload_version=$6)`, j.EventID, j.SubmissionID, j.JobID, j.Attempt, allowed, map[bool]int{false: 2, true: 3}[j.Kind == queue.RejudgeJob]).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return domain.ErrNotFound
	}
	return nil
}
