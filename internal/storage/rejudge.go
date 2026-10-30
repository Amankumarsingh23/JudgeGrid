package storage

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/judge"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/jackc/pgx/v5"
	"time"
)

type RejudgeRequest struct {
	JobID        string `json:"evaluation_job_id"`
	SubmissionID string `json:"submission_id"`
	VersionID    int64  `json:"problem_version_id"`
	Ordinal      int64  `json:"ordinal"`
	Replayed     bool   `json:"replayed"`
}

func (r *Repository) requestRejudge(ctx context.Context, principal, key, id string, version int64) (result RejudgeRequest, err error) {
	if principal == "" || len(principal) > 200 || len(key) < 1 || len(key) > 200 || version < 1 {
		return result, domain.ErrInvalid
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return result, e
	}
	defer tx.Rollback(ctx)
	if e := captureTrace(ctx, tx); e != nil {
		return result, e
	}
	var eligible bool
	if e = lockScheduler(ctx, tx); e != nil {
		return result, e
	}
	e = tx.QueryRow(ctx, `SELECT s.status='COMPLETED' AND s.language IN ('cpp17','python3') AND v.problem_id=s.problem_id AND v.version_number>original.version_number AND EXISTS(SELECT 1 FROM problem_test_suites WHERE problem_version_id=v.id) FROM submissions s JOIN problem_versions original ON original.id=s.problem_version_id JOIN problem_versions v ON v.id=$3 WHERE s.id=$1 AND s.principal_id=$2 FOR UPDATE OF s`, id, principal, version).Scan(&eligible)
	if e != nil {
		return result, e
	}
	if !eligible {
		return result, domain.ErrConflict
	}
	result.SubmissionID = id
	result.VersionID = version
	var oldVersion int64
	e = tx.QueryRow(ctx, `SELECT id::text,problem_version_id,ordinal FROM evaluation_jobs WHERE submission_id=$1 AND request_key=$2`, id, key).Scan(&result.JobID, &oldVersion, &result.Ordinal)
	if e == nil {
		if oldVersion != version {
			return result, domain.ErrConflict
		}
		result.Replayed = true
		return result, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return result, e
	}
	e = tx.QueryRow(ctx, `INSERT INTO evaluation_jobs(submission_id,problem_version_id,request_key,operator,ordinal) SELECT $1,$2,$3,$4,COALESCE(max(ordinal),0)+1 FROM evaluation_jobs WHERE submission_id=$1 RETURNING id::text,ordinal`, id, version, key, principal).Scan(&result.JobID, &result.Ordinal)
	if e != nil {
		return result, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO outbox_events(submission_id,job_id,payload_version) VALUES($1,$2,3)`, id, result.JobID); e != nil {
		return result, e
	}
	return result, tx.Commit(ctx)
}
func authorizeJobWork(ctx context.Context, tx pgx.Tx, w judge.Work) error {
	var valid bool
	e := tx.QueryRow(ctx, `SELECT o.run_id=$2::uuid AND o.generation=$3 AND o.owner_id=$4 AND (o.state='DONE' OR(o.state='ACTIVE' AND o.lease_expires_at>clock_timestamp())) AND j.submission_id=$5::uuid AND s.principal_id=$6 FROM evaluation_job_ownership o JOIN evaluation_jobs j ON j.id=o.job_id JOIN submissions s ON s.id=j.submission_id WHERE o.job_id=$1 FOR UPDATE OF o`, w.JobID, w.RunID, w.Generation, w.Owner, w.Submission.ID, w.Submission.PrincipalID).Scan(&valid)
	if e != nil || !valid {
		return ErrOwned
	}
	return setFence(ctx, tx, w)
}
func (r *Repository) renewJob(ctx context.Context, w judge.Work, ttl time.Duration) error {
	if ttl < time.Second || ttl > 5*time.Minute {
		return domain.ErrInvalid
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = authorizeJobWork(ctx, tx, w); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE evaluation_job_ownership SET heartbeat_at=clock_timestamp(),lease_expires_at=clock_timestamp()+$5::bigint*interval '1 millisecond' WHERE job_id=$1 AND run_id=$2 AND generation=$3 AND owner_id=$4 AND state='ACTIVE' AND lease_expires_at>clock_timestamp()`, w.JobID, w.RunID, w.Generation, w.Owner, ttl.Milliseconds())
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrOwned
	}
	return tx.Commit(ctx)
}
func (r *Repository) claimJob(ctx context.Context, principal, owner string, j queue.Job, ttl time.Duration, max int) (w judge.Work, err error) {
	if j.Validate() != nil || principal == "" || owner == "" || len(owner) > 200 || ttl < time.Second || ttl > 5*time.Minute || max < 1 || max > 5 {
		return w, domain.ErrInvalid
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return w, e
	}
	defer tx.Rollback(ctx)
	var valid bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM outbox_events WHERE id=$1 AND submission_id=$2 AND job_id=$3 AND payload_version=3 AND status='PUBLISHED')`, j.EventID, j.SubmissionID, j.JobID).Scan(&valid)
	if e != nil {
		return w, e
	}
	if !valid {
		return w, domain.ErrNotFound
	}
	if e = admitScheduler(ctx, tx, j.JobID, principal); e != nil {
		return w, e
	}
	// Keep submission -> job -> ownership order consistent with completion/failure.
	var lockedSubmission string
	if e = tx.QueryRow(ctx, `SELECT id::text FROM submissions WHERE id=$1 AND principal_id=$2 FOR UPDATE`, j.SubmissionID, principal).Scan(&lockedSubmission); e != nil {
		return w, e
	}
	var state string
	var version int64
	e = tx.QueryRow(ctx, `SELECT j.state,j.problem_version_id FROM evaluation_jobs j JOIN submissions s ON s.id=j.submission_id WHERE j.id=$1 AND j.submission_id=$2 AND s.principal_id=$3 FOR UPDATE OF j`, j.JobID, j.SubmissionID, principal).Scan(&state, &version)
	if e != nil {
		return w, e
	}
	w.JobID = j.JobID
	if state == "COMPLETED" || state == "FAILED" {
		summary, e := scanSummary(tx.QueryRow(ctx, `SELECT `+summaryColumns+` FROM evaluation_runs WHERE job_id=$1 ORDER BY generation DESC LIMIT 1`, j.JobID))
		if e != nil {
			return w, e
		}
		w.Existing = &summary
		return w, tx.Commit(ctx)
	}
	w.Submission, e = scanSubmission(tx.QueryRow(ctx, `SELECT `+columns+` FROM submissions s JOIN problem_versions v ON v.id=s.problem_version_id WHERE s.id=$1 AND s.principal_id=$2`, j.SubmissionID, principal))
	if e != nil {
		return w, e
	}
	// Work uses target metadata; the stored submission and original version never change.
	w.Submission.ProblemVersionID = version
	if e = tx.QueryRow(ctx, `SELECT version_number,time_limit_ms,memory_limit_kb FROM problem_versions WHERE id=$1`, version).Scan(&w.Submission.ProblemVersion, &w.TimeLimitMS, &w.MemoryLimitKB); e != nil {
		return w, e
	}
	w.Reference, e = loadReference(ctx, tx, version)
	if e != nil {
		return w, e
	}
	var oldRun, oldState string
	var oldGen, oldMax int
	var due bool
	e = tx.QueryRow(ctx, `SELECT run_id::text,state,generation,max_attempts,((state='ACTIVE' AND lease_expires_at<=clock_timestamp()) OR(state='RETRY' AND next_attempt_at<=clock_timestamp())) FROM evaluation_job_ownership WHERE job_id=$1 FOR UPDATE`, j.JobID).Scan(&oldRun, &oldState, &oldGen, &oldMax, &due)
	exists := e == nil
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return w, e
	}
	if exists {
		if !due {
			return w, ErrOwned
		}
		max = oldMax
		if oldState == "ACTIVE" {
			if _, e = tx.Exec(ctx, `UPDATE evaluation_runs SET status='FAILED',failure_code='lease_expired',finished_at=clock_timestamp() WHERE id=$1 AND status='RUNNING'`, oldRun); e != nil {
				return w, e
			}
		}
		if oldGen >= max {
			if _, e = tx.Exec(ctx, `UPDATE evaluation_job_ownership SET state='EXHAUSTED',failure_code='attempt_limit' WHERE job_id=$1`, j.JobID); e != nil {
				return w, e
			}
			if _, e = tx.Exec(ctx, `UPDATE evaluation_jobs SET state='FAILED' WHERE id=$1`, j.JobID); e != nil {
				return w, e
			}
			w.Existing = &judge.Summary{SubmissionID: j.SubmissionID, Status: "FAILED"}
			return w, tx.Commit(ctx)
		}
	} else if state != "QUEUED" {
		return w, ErrOwned
	}
	w.Generation = oldGen + 1
	w.Owner = owner
	e = tx.QueryRow(ctx, `INSERT INTO evaluation_runs(submission_id,problem_version_id,bundle_sha,evaluator_version,tests_total,generation,owner_id,job_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text`, j.SubmissionID, version, w.Reference.Hash, judge.EvaluatorVersion, len(w.Reference.Cases), w.Generation, owner, j.JobID).Scan(&w.RunID)
	if e != nil {
		return w, e
	}
	if !exists {
		_, e = tx.Exec(ctx, `INSERT INTO evaluation_job_ownership(job_id,submission_id,run_id,event_id,generation,owner_id,state,lease_expires_at,max_attempts) VALUES($1,$2,$3,$4,$5,$6,'ACTIVE',clock_timestamp()+$7::bigint*interval '1 millisecond',$8)`, j.JobID, j.SubmissionID, w.RunID, j.EventID, w.Generation, owner, ttl.Milliseconds(), max)
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE evaluation_jobs SET state='RUNNING' WHERE id=$1 AND state='QUEUED'`, j.JobID)
		}
	} else {
		_, e = tx.Exec(ctx, `UPDATE evaluation_job_ownership SET run_id=$2,generation=$3,owner_id=$4,state='ACTIVE',lease_expires_at=clock_timestamp()+$5::bigint*interval '1 millisecond',heartbeat_at=clock_timestamp(),failure_code=NULL WHERE job_id=$1`, j.JobID, w.RunID, w.Generation, owner, ttl.Milliseconds())
	}
	if e != nil {
		return w, e
	}
	return w, tx.Commit(ctx)
}
func (r *Repository) failJob(ctx context.Context, w judge.Work, code string, retry bool) error {
	switch code {
	case "sandbox_infrastructure", "bundle_verification_failed", "evaluation_canceled", "invalid_configuration":
	default:
		return domain.ErrInvalid
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var lockedSubmission string
	if e = tx.QueryRow(ctx, `SELECT id::text FROM submissions WHERE id=$1 AND principal_id=$2 FOR UPDATE`, w.Submission.ID, w.Submission.PrincipalID).Scan(&lockedSubmission); e != nil {
		return e
	}
	if e = authorizeJobWork(ctx, tx, w); e != nil {
		return e
	}
	var max int
	if e = tx.QueryRow(ctx, `SELECT max_attempts FROM evaluation_job_ownership WHERE job_id=$1`, w.JobID).Scan(&max); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE evaluation_runs SET status='FAILED',failure_code=$2,finished_at=clock_timestamp() WHERE id=$1 AND status='RUNNING'`, w.RunID, code); e != nil {
		return e
	}
	state := "RETRY"
	if !retry || w.Generation >= max {
		state = "EXHAUSTED"
		if _, e = tx.Exec(ctx, `UPDATE evaluation_jobs SET state='FAILED' WHERE id=$1`, w.JobID); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE evaluation_job_ownership SET state=$2,failure_code=$3,next_attempt_at=clock_timestamp()+interval '1 second' WHERE job_id=$1`, w.JobID, state, code); e != nil {
		return e
	}
	e = tx.Commit(ctx)
	if e == nil && state == "EXHAUSTED" {
		recordFailedTerminal(w)
	}
	return e
}
func completeJob(ctx context.Context, db transactionBeginner, w judge.Work, report judge.Report) (judge.Summary, error) {
	if e := judge.ValidateReport(w, report); e != nil {
		return judge.Summary{}, e
	}
	tx, e := db.Begin(ctx)
	if e != nil {
		return judge.Summary{}, e
	}
	defer tx.Rollback(ctx)
	// Serialize selection with request ordering, while keeping heartbeat free during evaluation.
	var sid string
	if e = tx.QueryRow(ctx, `SELECT id::text FROM submissions WHERE id=$1 AND principal_id=$2 FOR UPDATE`, w.Submission.ID, w.Submission.PrincipalID).Scan(&sid); e != nil {
		return judge.Summary{}, e
	}
	if e = authorizeJobWork(ctx, tx, w); e != nil {
		return judge.Summary{}, e
	}
	var state string
	var sha *string
	var target int64
	var storedJob string
	if e = tx.QueryRow(ctx, `SELECT status,result_sha,problem_version_id,job_id::text FROM evaluation_runs WHERE id=$1 AND submission_id=$2 FOR UPDATE`, w.RunID, sid).Scan(&state, &sha, &target, &storedJob); e != nil {
		return judge.Summary{}, e
	}
	if storedJob != w.JobID || target != w.Submission.ProblemVersionID {
		return judge.Summary{}, domain.ErrConflict
	}
	if state == "COMPLETED" {
		if sha == nil || *sha != report.Fingerprint() {
			return judge.Summary{}, domain.ErrConflict
		}
		s, e := scanSummary(tx.QueryRow(ctx, `SELECT `+summaryColumns+` FROM evaluation_runs WHERE id=$1`, w.RunID))
		s.Replayed = true
		if e != nil {
			return s, e
		}
		return s, tx.Commit(ctx)
	}
	if state != "RUNNING" {
		return judge.Summary{}, domain.ErrConflict
	}
	ref, e := loadReference(ctx, tx, target)
	if e != nil {
		return judge.Summary{}, e
	}
	a, _ := json.Marshal(ref)
	b, _ := json.Marshal(w.Reference)
	if string(a) != string(b) {
		return judge.Summary{}, domain.ErrConflict
	}
	passed := 0
	for _, row := range report.Tests {
		v := row.Evidence
		if row.Verdict == "AC" {
			passed++
		}
		if _, e = tx.Exec(ctx, `INSERT INTO evaluation_test_results(evaluation_run_id,problem_version_id,case_id,verdict,output_match,outcome,exit_code,wall_time_ns,oom_killed,cpu_time_ns,peak_memory_bytes,stdout_sha,stderr_sha,output_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, w.RunID, target, row.CaseID, row.Verdict, row.Match, v.Outcome, v.ExitCode, v.WallTimeNS, v.OOMKilled, v.CPUTimeNS, v.PeakMemoryBytes, v.StdoutHash, v.StderrHash, v.OutputBytes); e != nil {
			return judge.Summary{}, e
		}
	}
	var compile any
	if report.Compile != nil {
		compile, _ = json.Marshal(report.Compile)
	}
	var reason any
	if report.CompileReason != "" {
		reason = report.CompileReason
	}
	if _, e = tx.Exec(ctx, `UPDATE evaluation_runs SET status='COMPLETED',final_verdict=$2,runtime_image=$3,execution_policy=$4,compile_evidence=$5,compile_reason=$6,result_sha=$7,tests_run=$8,tests_passed=$9,cache_hit=$10,finished_at=clock_timestamp() WHERE id=$1`, w.RunID, report.Verdict, report.Image, report.Policy, compile, reason, report.Fingerprint(), len(report.Tests), passed, report.CacheHit); e != nil {
		return judge.Summary{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE evaluation_job_ownership SET state='DONE' WHERE job_id=$1`, w.JobID); e != nil {
		return judge.Summary{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE evaluation_jobs SET state='COMPLETED' WHERE id=$1`, w.JobID); e != nil {
		return judge.Summary{}, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO selected_evaluations(submission_id,job_id,run_id,ordinal) SELECT submission_id,id,$2,ordinal FROM evaluation_jobs j WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM evaluation_jobs newer WHERE newer.submission_id=j.submission_id AND newer.ordinal>j.ordinal) ON CONFLICT(submission_id) DO UPDATE SET job_id=EXCLUDED.job_id,run_id=EXCLUDED.run_id,ordinal=EXCLUDED.ordinal,selected_at=clock_timestamp() WHERE selected_evaluations.ordinal<EXCLUDED.ordinal`, w.JobID, w.RunID); e != nil {
		return judge.Summary{}, e
	}
	s, e := scanSummary(tx.QueryRow(ctx, `SELECT `+summaryColumns+` FROM evaluation_runs WHERE id=$1`, w.RunID))
	if e != nil {
		return s, e
	}
	return s, tx.Commit(ctx)
}
func (r *Repository) recoverRejudges(ctx context.Context, principal string, limit int) ([]queue.Job, error) {
	rows, e := r.pool.Query(ctx, `SELECT j.id::text,j.submission_id::text,e.id::text FROM evaluation_jobs j JOIN submissions s ON s.id=j.submission_id JOIN outbox_events e ON e.job_id=j.id LEFT JOIN evaluation_job_ownership o ON o.job_id=j.id WHERE s.principal_id=$1 AND e.status='PUBLISHED' AND((j.state='QUEUED' AND o.job_id IS NULL) OR(j.state='RUNNING' AND((o.state='ACTIVE' AND o.lease_expires_at<=statement_timestamp()) OR(o.state='RETRY' AND o.next_attempt_at<=statement_timestamp())))) ORDER BY j.created_at,j.id LIMIT $2`, principal, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []queue.Job{}
	for rows.Next() {
		j := queue.Job{Kind: queue.RejudgeJob, Attempt: 1}
		if e = rows.Scan(&j.JobID, &j.SubmissionID, &j.EventID); e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// History returns only sanitized summaries, scoped to the authenticated owner.
func (r *Repository) History(ctx context.Context, principal, id string) ([]judge.Summary, error) {
	var visible bool
	if e := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM submissions WHERE id=$1 AND principal_id=$2)`, id, principal).Scan(&visible); e != nil {
		return nil, e
	}
	if !visible {
		return nil, domain.ErrNotFound
	}
	rows, e := r.pool.Query(ctx, `SELECT `+summaryColumns+` FROM evaluation_runs WHERE submission_id=$1 AND EXISTS(SELECT 1 FROM submissions WHERE id=$1 AND principal_id=$2) ORDER BY started_at,id LIMIT 100`, id, principal)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []judge.Summary{}
	for rows.Next() {
		s, e := scanSummary(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
