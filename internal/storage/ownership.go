package storage

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/judge"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/jackc/pgx/v5"
	"strconv"
	"time"
)

var ErrOwned = errors.New("execution busy, expired or uncertain")

func setFence(ctx context.Context, tx pgx.Tx, w judge.Work) error {
	_, err := tx.Exec(ctx, `SELECT set_config('judgegrid.execution_owner',$1,true),set_config('judgegrid.execution_generation',$2,true),set_config('judgegrid.execution_run',$3,true)`, w.Owner, fmtGeneration(w.Generation), w.RunID)
	return err
}
func fmtGeneration(n int) string { return strconv.Itoa(n) }
func authorizeWork(ctx context.Context, tx pgx.Tx, w judge.Work) error {
	var principal string
	if err := tx.QueryRow(ctx, `SELECT principal_id FROM submissions WHERE id=$1 FOR UPDATE`, w.Submission.ID).Scan(&principal); err != nil {
		return err
	}
	if principal != w.Submission.PrincipalID {
		return domain.ErrNotFound
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT run_id=$2::uuid AND generation=$3 AND owner_id=$4 AND (state='DONE' OR(state='ACTIVE' AND lease_expires_at>clock_timestamp())) FROM execution_ownership WHERE submission_id=$1 FOR UPDATE`, w.Submission.ID, w.RunID, w.Generation, w.Owner).Scan(&valid)
	if err != nil || !valid {
		return ErrOwned
	}
	return setFence(ctx, tx, w)
}
func (r *Repository) RenewExecution(ctx context.Context, w judge.Work, ttl time.Duration) error {
	if ttl < time.Second || ttl > 5*time.Minute {
		return domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = authorizeWork(ctx, tx, w); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE execution_ownership SET heartbeat_at=clock_timestamp(),lease_expires_at=clock_timestamp()+$5::bigint*interval '1 millisecond' WHERE submission_id=$1 AND run_id=$2 AND generation=$3 AND owner_id=$4 AND state='ACTIVE' AND lease_expires_at>clock_timestamp() AND EXISTS(SELECT 1 FROM submissions WHERE id=$1 AND status IN ('RUNNING','EVALUATING'))`, w.Submission.ID, w.RunID, w.Generation, w.Owner, ttl.Milliseconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrOwned
	}
	return tx.Commit(ctx)
}
func (r *Repository) ClaimExecution(ctx context.Context, principal, owner string, j queue.Job, ttl time.Duration, max int) (w judge.Work, err error) {
	if j.Validate() != nil || j.Kind != queue.SubmissionJob || j.EventID == "" || j.Attempt != 1 || principal == "" || owner == "" || len(owner) > 200 || ttl < time.Second || ttl > 5*time.Minute || max < 1 || max > 5 {
		return w, domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return w, err
	}
	defer tx.Rollback(ctx)
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM outbox_events WHERE id=$1 AND submission_id=$2 AND intent_generation=$3 AND payload_version=2 AND status='PUBLISHED')`, j.EventID, j.SubmissionID, j.Attempt).Scan(&valid); err != nil {
		return w, err
	}
	if !valid {
		return w, domain.ErrNotFound
	}
	w.Submission, err = scanSubmission(tx.QueryRow(ctx, `SELECT `+columns+` FROM submissions s JOIN problem_versions v ON v.id=s.problem_version_id WHERE s.id=$1 AND s.principal_id=$2 FOR UPDATE OF s`, j.SubmissionID, principal))
	if err != nil {
		return w, err
	}
	if w.Submission.Status == domain.Completed || w.Submission.Status == domain.Failed {
		summary, e := scanSummary(tx.QueryRow(ctx, `SELECT `+summaryColumns+` FROM evaluation_runs WHERE submission_id=$1 ORDER BY generation DESC LIMIT 1`, j.SubmissionID))
		if e != nil {
			var reason string
			e = tx.QueryRow(ctx, `SELECT reason FROM execution_rejections WHERE submission_id=$1 AND event_id=$2`, j.SubmissionID, j.EventID).Scan(&reason)
			if e != nil {
				return w, domain.ErrConflict
			}
			summary = judge.Summary{SubmissionID: j.SubmissionID, Status: "FAILED", FailureCode: &reason}
		}
		if summary.Status == "RUNNING" {
			return w, domain.ErrConflict
		}
		if w.Submission.Status == domain.Completed {
			if summary.Status != "COMPLETED" || summary.Verdict == nil || w.Submission.Verdict == nil || *summary.Verdict != *w.Submission.Verdict || summary.ProblemVersionID != w.Submission.ProblemVersionID {
				return w, domain.ErrConflict
			}
			var durable bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM evaluation_runs r WHERE r.id=$1 AND r.submission_id=$2 AND r.status='COMPLETED' AND r.result_sha IS NOT NULL AND (NOT EXISTS(SELECT 1 FROM execution_ownership WHERE submission_id=$2) OR EXISTS(SELECT 1 FROM execution_ownership o WHERE o.submission_id=$2 AND o.run_id=r.id AND o.generation=r.generation AND o.owner_id=r.owner_id AND o.state='DONE')))`, summary.RunID, j.SubmissionID).Scan(&durable); err != nil {
				return w, err
			}
			if !durable {
				return w, domain.ErrConflict
			}
		}
		w.Existing = &summary
		return w, tx.Commit(ctx)
	}
	var oldRun, oldOwner, state, mode string
	var oldGen, oldMax int
	var due bool
	err = tx.QueryRow(ctx, `SELECT run_id::text,generation,owner_id,state,mode,max_attempts,((state='ACTIVE' AND lease_expires_at<=clock_timestamp()) OR(state='RETRY' AND next_attempt_at<=clock_timestamp())) FROM execution_ownership WHERE submission_id=$1 FOR UPDATE`, j.SubmissionID).Scan(&oldRun, &oldGen, &oldOwner, &state, &mode, &oldMax, &due)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return w, err
	}
	hasOwner := err == nil
	if hasOwner {
		if !due || mode != "agent" {
			return w, ErrOwned
		}
		max = oldMax
		if state == "ACTIVE" {
			if _, err = tx.Exec(ctx, `UPDATE evaluation_runs SET status='FAILED',failure_code='lease_expired',finished_at=clock_timestamp() WHERE id=$1 AND status='RUNNING'`, oldRun); err != nil {
				return w, err
			}
		}
		if oldGen >= max {
			if _, err = tx.Exec(ctx, `UPDATE submissions SET status='FAILED',failure_reason='attempt_limit',state_version=state_version+1,updated_at=clock_timestamp() WHERE id=$1`, j.SubmissionID); err != nil {
				return w, err
			}
			if _, err = tx.Exec(ctx, `UPDATE execution_ownership SET state='EXHAUSTED',failure_code='attempt_limit' WHERE submission_id=$1`, j.SubmissionID); err != nil {
				return w, err
			}
			summary := judge.Summary{SubmissionID: j.SubmissionID, Status: "FAILED"}
			w.Existing = &summary
			return w, tx.Commit(ctx)
		}
	} else if w.Submission.Status != domain.Queued {
		return w, ErrOwned
	}
	if !hasOwner && (w.Submission.Language != "cpp17" && w.Submission.Language != "python3") {
		if _, err = tx.Exec(ctx, `INSERT INTO execution_rejections(submission_id,event_id,reason) VALUES($1,$2,'unsupported_language')`, j.SubmissionID, j.EventID); err != nil {
			return w, err
		}
		if _, err = tx.Exec(ctx, `UPDATE submissions SET status='FAILED',failure_reason='unsupported_language',state_version=state_version+1,updated_at=clock_timestamp() WHERE id=$1`, j.SubmissionID); err != nil {
			return w, err
		}
		reason := "unsupported_language"
		w.Existing = &judge.Summary{SubmissionID: j.SubmissionID, Status: "FAILED", FailureCode: &reason}
		return w, tx.Commit(ctx)
	}
	w.Reference, err = loadReference(ctx, tx, w.Submission.ProblemVersionID)
	if err != nil {
		if hasOwner {
			return w, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO execution_rejections(submission_id,event_id,reason) VALUES($1,$2,'bundle_unavailable')`, j.SubmissionID, j.EventID); err != nil {
			return w, err
		}
		if _, err = tx.Exec(ctx, `UPDATE submissions SET status='FAILED',failure_reason='bundle_unavailable',state_version=state_version+1,updated_at=clock_timestamp() WHERE id=$1`, j.SubmissionID); err != nil {
			return w, err
		}
		reason := "bundle_unavailable"
		w.Existing = &judge.Summary{SubmissionID: j.SubmissionID, Status: "FAILED", FailureCode: &reason}
		return w, tx.Commit(ctx)
	}
	if err = tx.QueryRow(ctx, `SELECT time_limit_ms,memory_limit_kb FROM problem_versions WHERE id=$1`, w.Submission.ProblemVersionID).Scan(&w.TimeLimitMS, &w.MemoryLimitKB); err != nil {
		return w, err
	}
	w.StateVersion = w.Submission.StateVersion
	if !hasOwner {
		if err = tx.QueryRow(ctx, `UPDATE submissions SET status='RUNNING',state_version=state_version+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING state_version`, j.SubmissionID).Scan(&w.StateVersion); err != nil {
			return w, err
		}
		w.Submission.Status = domain.Running
	}
	w.Owner = owner
	w.Generation = oldGen + 1
	err = tx.QueryRow(ctx, `INSERT INTO evaluation_runs(submission_id,problem_version_id,bundle_sha,evaluator_version,tests_total,generation,owner_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`, j.SubmissionID, w.Submission.ProblemVersionID, w.Reference.Hash, judge.EvaluatorVersion, len(w.Reference.Cases), w.Generation, owner).Scan(&w.RunID)
	if err != nil {
		return w, err
	}
	if !hasOwner {
		_, err = tx.Exec(ctx, `INSERT INTO execution_ownership(submission_id,run_id,event_id,generation,owner_id,mode,state,lease_expires_at,max_attempts) VALUES($1,$2,$3,$4,$5,'agent','ACTIVE',clock_timestamp()+$6::bigint*interval '1 millisecond',$7)`, j.SubmissionID, w.RunID, j.EventID, w.Generation, owner, ttl.Milliseconds(), max)
	} else {
		_, err = tx.Exec(ctx, `UPDATE execution_ownership SET run_id=$2,generation=$3,owner_id=$4,state='ACTIVE',heartbeat_at=clock_timestamp(),lease_expires_at=clock_timestamp()+$5::bigint*interval '1 millisecond',failure_code=NULL WHERE submission_id=$1`, j.SubmissionID, w.RunID, w.Generation, owner, ttl.Milliseconds())
	}
	if err != nil {
		return w, err
	}
	return w, tx.Commit(ctx)
}

func (r *Repository) FailExecution(ctx context.Context, w judge.Work, code string, retry bool) error {
	switch code {
	case "sandbox_infrastructure", "evaluation_canceled", "bundle_verification_failed", "invalid_configuration":
	default:
		return domain.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = authorizeWork(ctx, tx, w); err != nil {
		return err
	}
	var generation, max int
	if err = tx.QueryRow(ctx, `SELECT generation,max_attempts FROM execution_ownership WHERE submission_id=$1`, w.Submission.ID).Scan(&generation, &max); err != nil {
		return err
	}
	terminal := !retry || generation >= max
	if _, err = tx.Exec(ctx, `UPDATE evaluation_runs SET status='FAILED',failure_code=$2,finished_at=clock_timestamp() WHERE id=$1 AND status='RUNNING'`, w.RunID, code); err != nil {
		return err
	}
	state := "RETRY"
	if terminal {
		state = "EXHAUSTED"
		if _, err = tx.Exec(ctx, `UPDATE submissions SET status='FAILED',failure_reason=$2,state_version=state_version+1,updated_at=clock_timestamp() WHERE id=$1 AND status IN ('RUNNING','EVALUATING')`, w.Submission.ID, code); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE execution_ownership SET state=$2,failure_code=$3,next_attempt_at=clock_timestamp()+interval '1 second' WHERE submission_id=$1`, w.Submission.ID, state, code); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) RecoveryJobs(ctx context.Context, principal string, limit int) ([]queue.Job, error) {
	if limit < 1 || limit > 16 {
		return nil, domain.ErrInvalid
	}
	rows, err := r.pool.Query(ctx, `WITH candidates AS (
 (SELECT o.submission_id FROM execution_ownership o JOIN submissions s ON s.id=o.submission_id WHERE s.principal_id=$1 AND o.mode='agent' AND o.state='ACTIVE' AND o.lease_expires_at<=statement_timestamp() ORDER BY o.lease_expires_at,o.submission_id LIMIT $2)
 UNION (SELECT o.submission_id FROM execution_ownership o JOIN submissions s ON s.id=o.submission_id WHERE s.principal_id=$1 AND o.mode='agent' AND o.state='RETRY' AND o.next_attempt_at<=statement_timestamp() ORDER BY o.next_attempt_at,o.submission_id LIMIT $2)
 UNION (SELECT s.id FROM submissions s JOIN outbox_events e ON e.submission_id=s.id AND e.intent_generation=1 AND e.status='PUBLISHED' WHERE s.principal_id=$1 AND s.status='QUEUED' AND NOT EXISTS(SELECT 1 FROM execution_ownership WHERE submission_id=s.id) ORDER BY s.created_at,s.id LIMIT $2))
 SELECT e.id::text,e.submission_id::text,e.intent_generation FROM candidates c JOIN outbox_events e ON e.submission_id=c.submission_id AND e.intent_generation=1 AND e.status='PUBLISHED' ORDER BY e.created_at,e.id LIMIT $2`, principal, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []queue.Job{}
	for rows.Next() {
		j := queue.Job{Kind: queue.SubmissionJob}
		if err = rows.Scan(&j.EventID, &j.SubmissionID, &j.Attempt); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}
