package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/judge"
	"github.com/Amankumarsingh23/JudgeGrid/internal/submission"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type evaluationSession struct {
	mu                 sync.Mutex
	connection         *pgxpool.Conn
	principal, id, key string
	closed             bool
}

func (r *Repository) Acquire(ctx context.Context, principal, id string) (judge.Session, error) {
	if strings.TrimSpace(principal) == "" {
		return nil, domain.ErrUnauthorized
	}
	if !submission.ValidID(id) {
		return nil, domain.ErrNotFound
	}
	id = strings.ToLower(id)
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var visible bool
	err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM submissions WHERE id=$1 AND principal_id=$2)`, id, principal).Scan(&visible)
	if err != nil || !visible {
		conn.Release()
		if err != nil {
			return nil, err
		}
		return nil, domain.ErrNotFound
	}
	key := "judgegrid.local-evaluation:" + id
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked); err != nil || !locked {
		conn.Release()
		if err != nil {
			return nil, err
		}
		return nil, domain.ErrConflict
	}
	return &evaluationSession{connection: conn, principal: principal, id: id, key: key}, nil
}
func (s *evaluationSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := s.connection.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, s.key)
	if err != nil {
		_ = s.connection.Conn().Close(ctx)
	}
	s.connection.Release()
	return err
}

const summaryColumns = `id::text,submission_id::text,problem_version_id,generation,status,final_verdict,failure_code,tests_run,tests_passed,tests_total,evaluator_version,runtime_image,started_at,finished_at`

func scanSummary(row scanner) (summary judge.Summary, err error) {
	err = row.Scan(&summary.RunID, &summary.SubmissionID, &summary.ProblemVersionID, &summary.Generation, &summary.Status, &summary.Verdict, &summary.FailureCode, &summary.TestsRun, &summary.TestsPassed, &summary.TestsTotal, &summary.Evaluator, &summary.Image, &summary.StartedAt, &summary.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.ErrNotFound
	}
	return
}
func (s *evaluationSession) Begin(ctx context.Context) (work judge.Work, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return work, domain.ErrConflict
	}
	tx, err := s.connection.Begin(ctx)
	if err != nil {
		return work, err
	}
	defer tx.Rollback(ctx)
	work.Submission, err = scanSubmission(tx.QueryRow(ctx, `SELECT `+columns+` FROM submissions s JOIN problem_versions v ON v.id=s.problem_version_id WHERE s.id=$1 AND s.principal_id=$2 FOR UPDATE OF s`, s.id, s.principal))
	if err != nil {
		return work, err
	}
	if work.Submission.Status == domain.Completed || work.Submission.Status == domain.Failed {
		summary, e := scanSummary(tx.QueryRow(ctx, `SELECT `+summaryColumns+` FROM evaluation_runs WHERE submission_id=$1 ORDER BY generation DESC LIMIT 1`, s.id))
		if e != nil {
			return work, domain.ErrConflict
		}
		work.Existing = &summary
		return work, tx.Commit(ctx)
	}
	if work.Submission.Status != domain.Queued {
		return work, domain.ErrConflict
	}
	work.Reference, err = loadReference(ctx, tx, work.Submission.ProblemVersionID)
	if err != nil {
		return work, err
	}
	if err = tx.QueryRow(ctx, `SELECT time_limit_ms,memory_limit_kb FROM problem_versions WHERE id=$1`, work.Submission.ProblemVersionID).Scan(&work.TimeLimitMS, &work.MemoryLimitKB); err != nil {
		return work, err
	}
	if err = tx.QueryRow(ctx, `UPDATE submissions SET status='RUNNING',state_version=state_version+1,updated_at=clock_timestamp() WHERE id=$1 AND status='QUEUED' AND state_version=$2 RETURNING state_version`, s.id, work.Submission.StateVersion).Scan(&work.StateVersion); err != nil {
		return work, err
	}
	work.Owner = "local-" + newID()
	work.Generation = 1
	work.Submission.Status = domain.Running
	err = tx.QueryRow(ctx, `INSERT INTO evaluation_runs(submission_id,problem_version_id,bundle_sha,evaluator_version,tests_total,owner_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING id::text`, s.id, work.Submission.ProblemVersionID, work.Reference.Hash, judge.EvaluatorVersion, len(work.Reference.Cases), work.Owner).Scan(&work.RunID)
	if err != nil {
		return work, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO execution_ownership(submission_id,run_id,generation,owner_id,mode,state,lease_expires_at,max_attempts) VALUES($1,$2,1,$3,'local','ACTIVE',clock_timestamp()+interval '180 seconds',1)`, s.id, work.RunID, work.Owner); err != nil {
		return work, err
	}
	return work, tx.Commit(ctx)
}

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

func (s *evaluationSession) Complete(ctx context.Context, work judge.Work, report judge.Report) (judge.Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || work.Submission.ID != s.id || work.Submission.PrincipalID != s.principal {
		return judge.Summary{}, domain.ErrConflict
	}
	return completeEvaluation(ctx, s.connection, work, report)
}

// CommitEvaluation exposes the same guarded finalizer for trusted persistence/retry tests.
// The local service uses its pinned session connection instead; this is never an HTTP route.
func (r *Repository) CommitEvaluation(ctx context.Context, work judge.Work, report judge.Report) (judge.Summary, error) {
	return completeEvaluation(ctx, r.pool, work, report)
}
func completeEvaluation(ctx context.Context, db transactionBeginner, work judge.Work, report judge.Report) (judge.Summary, error) {
	if err := judge.ValidateReport(work, report); err != nil {
		return judge.Summary{}, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return judge.Summary{}, err
	}
	defer tx.Rollback(ctx)
	if err = authorizeWork(ctx, tx, work); err != nil {
		return judge.Summary{}, err
	}
	var status string
	var storedHash *string
	var version int64
	var submissionID, hash, evaluator string
	err = tx.QueryRow(ctx, `SELECT r.status,r.result_sha,r.problem_version_id,r.submission_id::text,r.bundle_sha,r.evaluator_version FROM evaluation_runs r JOIN submissions s ON s.id=r.submission_id WHERE r.id=$1 AND s.principal_id=$2 FOR UPDATE OF r`, work.RunID, work.Submission.PrincipalID).Scan(&status, &storedHash, &version, &submissionID, &hash, &evaluator)
	if errors.Is(err, pgx.ErrNoRows) {
		return judge.Summary{}, domain.ErrNotFound
	}
	if err != nil {
		return judge.Summary{}, err
	}
	if version != work.Submission.ProblemVersionID || submissionID != work.Submission.ID || hash != work.Reference.Hash || evaluator != judge.EvaluatorVersion {
		return judge.Summary{}, domain.ErrConflict
	}
	fingerprint := report.Fingerprint()
	if status == "COMPLETED" {
		if storedHash == nil || *storedHash != fingerprint {
			return judge.Summary{}, domain.ErrConflict
		}
		summary, err := scanSummary(tx.QueryRow(ctx, `SELECT `+summaryColumns+` FROM evaluation_runs WHERE id=$1`, work.RunID))
		summary.Replayed = true
		if err != nil {
			return summary, err
		}
		return summary, tx.Commit(ctx)
	}
	if status != "RUNNING" {
		return judge.Summary{}, domain.ErrConflict
	}
	// Verify the exact sealed metadata before accepting case IDs or aggregation from a caller.
	ref, err := loadReference(ctx, tx, version)
	if err != nil {
		return judge.Summary{}, err
	}
	if ref.Hash != work.Reference.Hash || ref.Comparator != work.Reference.Comparator || len(ref.Cases) != len(work.Reference.Cases) {
		return judge.Summary{}, domain.ErrConflict
	}
	for i, c := range ref.Cases {
		if c != work.Reference.Cases[i] {
			return judge.Summary{}, domain.ErrConflict
		}
	}
	stateVersion := work.StateVersion
	if work.Submission.Status != domain.Evaluating {
		tag, err := tx.Exec(ctx, `UPDATE submissions SET status='EVALUATING',state_version=state_version+1,updated_at=clock_timestamp() WHERE id=$1 AND principal_id=$2 AND status='RUNNING' AND state_version=$3`, submissionID, work.Submission.PrincipalID, work.StateVersion)
		if err != nil {
			return judge.Summary{}, err
		}
		if tag.RowsAffected() != 1 {
			return judge.Summary{}, domain.ErrConflict
		}
		stateVersion++
	}
	passed := 0
	for _, row := range report.Tests {
		e := row.Evidence
		if row.Verdict == "AC" {
			passed++
		}
		_, err = tx.Exec(ctx, `INSERT INTO evaluation_test_results(evaluation_run_id,problem_version_id,case_id,verdict,output_match,outcome,exit_code,wall_time_ns,oom_killed,cpu_time_ns,peak_memory_bytes,stdout_sha,stderr_sha,output_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, work.RunID, version, row.CaseID, row.Verdict, row.Match, e.Outcome, e.ExitCode, e.WallTimeNS, e.OOMKilled, e.CPUTimeNS, e.PeakMemoryBytes, e.StdoutHash, e.StderrHash, e.OutputBytes)
		if err != nil {
			return judge.Summary{}, err
		}
	}
	var compile any
	if report.Compile != nil {
		data, _ := json.Marshal(report.Compile)
		compile = data
	}
	var reason any
	if report.CompileReason != "" {
		reason = report.CompileReason
	}
	_, err = tx.Exec(ctx, `UPDATE evaluation_runs SET status='COMPLETED',final_verdict=$2,runtime_image=$3,execution_policy=$4,compile_evidence=$5,compile_reason=$6,result_sha=$7,tests_run=$8,tests_passed=$9,finished_at=clock_timestamp() WHERE id=$1 AND status='RUNNING'`, work.RunID, report.Verdict, report.Image, report.Policy, compile, reason, fingerprint, len(report.Tests), passed)
	if err != nil {
		return judge.Summary{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE submissions SET status='COMPLETED',verdict=$2,state_version=state_version+1,updated_at=clock_timestamp() WHERE id=$1 AND status='EVALUATING' AND state_version=$3`, submissionID, report.Verdict, stateVersion)
	if err != nil {
		return judge.Summary{}, err
	}
	if tag.RowsAffected() != 1 {
		return judge.Summary{}, domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE execution_ownership SET state='DONE' WHERE submission_id=$1`, submissionID); err != nil {
		return judge.Summary{}, err
	}
	summary, err := scanSummary(tx.QueryRow(ctx, `SELECT `+summaryColumns+` FROM evaluation_runs WHERE id=$1`, work.RunID))
	if err != nil {
		return summary, err
	}
	return summary, tx.Commit(ctx)
}
func (s *evaluationSession) Fail(ctx context.Context, work judge.Work, code string) (judge.Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || work.Submission.ID != s.id {
		return judge.Summary{}, domain.ErrConflict
	}
	switch code {
	case "sandbox_infrastructure", "bundle_verification_failed", "evaluation_canceled", "invalid_configuration":
	default:
		return judge.Summary{}, judge.ErrConfiguration
	}
	tx, err := s.connection.Begin(ctx)
	if err != nil {
		return judge.Summary{}, err
	}
	defer tx.Rollback(ctx)
	if err = authorizeWork(ctx, tx, work); err != nil {
		return judge.Summary{}, err
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM evaluation_runs WHERE id=$1 AND submission_id=$2 FOR UPDATE`, work.RunID, s.id).Scan(&status)
	if err != nil {
		return judge.Summary{}, err
	}
	if status != "RUNNING" {
		return judge.Summary{}, domain.ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE submissions SET status='FAILED',failure_reason=$2,state_version=state_version+1,updated_at=clock_timestamp() WHERE id=$1 AND status='RUNNING' AND state_version=$3`, s.id, code, work.StateVersion)
	if err != nil {
		return judge.Summary{}, err
	}
	if tag.RowsAffected() != 1 {
		return judge.Summary{}, domain.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE evaluation_runs SET status='FAILED',failure_code=$2,finished_at=clock_timestamp() WHERE id=$1`, work.RunID, code)
	if err != nil {
		return judge.Summary{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE execution_ownership SET state='EXHAUSTED',failure_code=$2 WHERE submission_id=$1`, s.id, code); err != nil {
		return judge.Summary{}, err
	}
	summary, err := scanSummary(tx.QueryRow(ctx, `SELECT `+summaryColumns+` FROM evaluation_runs WHERE id=$1`, work.RunID))
	if err != nil {
		return summary, err
	}
	return summary, tx.Commit(ctx)
}
