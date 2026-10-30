package storage

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool         *pgxpool.Pool
	backlogLimit int
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, backlogLimit: 10000}
}
func NewRepositoryWithBacklog(pool *pgxpool.Pool, limit int) *Repository {
	return &Repository{pool: pool, backlogLimit: limit}
}

const columns = `s.id::text,s.problem_id,s.problem_version_id,v.version_number,s.principal_id,s.language,s.source_code,s.status,s.verdict,s.state_version,s.created_at,s.updated_at`

type scanner interface{ Scan(...any) error }

func scanSubmission(row scanner) (s domain.Submission, err error) {
	err = row.Scan(&s.ID, &s.ProblemID, &s.ProblemVersionID, &s.ProblemVersion, &s.PrincipalID, &s.Language, &s.SourceCode, &s.Status, &s.Verdict, &s.StateVersion, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.ErrNotFound
	}
	return
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (r *Repository) create(ctx context.Context, principal, key string, req domain.CreateRequest) (domain.Submission, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Submission{}, false, err
	}
	defer tx.Rollback(ctx)
	if e := captureTrace(ctx, tx); e != nil {
		return domain.Submission{}, false, e
	}
	if err = lockScheduler(ctx, tx); err != nil {
		return domain.Submission{}, false, err
	}
	// Lock this principal/key pair before checking for a replay. Hash collisions only serialize unrelated requests.
	lockKey, _ := json.Marshal([]string{principal, key})
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(lockKey)); err != nil {
		return domain.Submission{}, false, err
	}
	var fingerprint, id string
	err = tx.QueryRow(ctx, `SELECT request_fingerprint,submission_id::text FROM submission_idempotency WHERE principal_id=$1 AND idempotency_key=$2`, principal, key).Scan(&fingerprint, &id)
	if err == nil {
		if fingerprint != req.Fingerprint() {
			return domain.Submission{}, false, domain.ErrConflict
		}
		s, err := scanSubmission(tx.QueryRow(ctx, `SELECT `+columns+` FROM submissions s JOIN problem_versions v ON v.id=s.problem_version_id WHERE s.id=$1 AND s.principal_id=$2`, id, principal))
		if err != nil {
			return domain.Submission{}, false, err
		}
		return s, true, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Submission{}, false, err
	}
	var versionID *int64
	err = tx.QueryRow(ctx, `SELECT current_version_id FROM problems WHERE id=$1 FOR SHARE`, req.ProblemID).Scan(&versionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Submission{}, false, domain.ErrNotFound
	}
	if err != nil {
		return domain.Submission{}, false, err
	}
	if versionID == nil {
		return domain.Submission{}, false, domain.ErrConflict
	}
	id = newID()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(2349012506)`); err != nil {
		return domain.Submission{}, false, err
	}
	var backlog int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM(
 (SELECT id FROM outbox_events WHERE status='PENDING' ORDER BY created_at,id LIMIT $1)
 UNION ALL (SELECT id FROM outbox_events WHERE status='CLAIMED' ORDER BY created_at,id LIMIT $1)
 UNION ALL (SELECT id FROM outbox_events WHERE status='QUARANTINED' ORDER BY created_at,id LIMIT $1)
 LIMIT $1) bounded`, r.backlogLimit).Scan(&backlog)
	if err != nil {
		return domain.Submission{}, false, err
	}
	if r.backlogLimit < 1 || backlog >= r.backlogLimit {
		return domain.Submission{}, false, domain.ErrBacklog
	}
	_, err = tx.Exec(ctx, `INSERT INTO submissions(id,problem_id,problem_version_id,principal_id,language,source_code) VALUES($1,$2,$3,$4,$5,$6)`, id, req.ProblemID, *versionID, principal, req.Language, req.SourceCode)
	if err != nil {
		return domain.Submission{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO submission_idempotency(principal_id,idempotency_key,request_fingerprint,submission_id) VALUES($1,$2,$3,$4)`, principal, key, req.Fingerprint(), id)
	if err != nil {
		return domain.Submission{}, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(submission_id) VALUES($1)`, id); err != nil {
		return domain.Submission{}, false, err
	}
	s, err := scanSubmission(tx.QueryRow(ctx, `SELECT `+columns+` FROM submissions s JOIN problem_versions v ON v.id=s.problem_version_id WHERE s.id=$1`, id))
	if err != nil {
		return domain.Submission{}, false, err
	}
	return s, false, tx.Commit(ctx)
}

func (r *Repository) Get(ctx context.Context, principal, id string) (domain.Submission, error) {
	s, e := scanSubmission(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM submissions s JOIN problem_versions v ON v.id=s.problem_version_id WHERE s.id=$1 AND s.principal_id=$2`, id, principal))
	if e != nil {
		return s, e
	}
	return r.displaySelection(ctx, s)
}
func (r *Repository) List(ctx context.Context, principal string, limit int, after *domain.Cursor) ([]domain.Submission, error) {
	query := `SELECT ` + columns + ` FROM submissions s JOIN problem_versions v ON v.id=s.problem_version_id WHERE s.principal_id=$1`
	args := []any{principal, limit}
	if after != nil {
		query += ` AND (s.created_at,s.id)<($3,$4::uuid)`
		args = append(args, after.CreatedAt, after.ID)
	}
	query += ` ORDER BY s.created_at DESC,s.id DESC LIMIT $2`
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.Submission{}
	for rows.Next() {
		s, err := scanSubmission(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range result {
		result[i], err = r.displaySelection(ctx, result[i])
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (r *Repository) displaySelection(ctx context.Context, s domain.Submission) (domain.Submission, error) {
	var verdict domain.Verdict
	var run string
	var version int64
	e := r.pool.QueryRow(ctx, `SELECT r.id::text,r.problem_version_id,r.final_verdict FROM selected_evaluations x JOIN evaluation_runs r ON r.id=x.run_id WHERE x.submission_id=$1 AND r.status='COMPLETED'`, s.ID).Scan(&run, &version, &verdict)
	if errors.Is(e, pgx.ErrNoRows) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	s.OriginalVerdict = s.Verdict
	s.Verdict = &verdict
	s.CurrentEvaluationID = &run
	s.CurrentVersionID = &version
	return s, nil
}
func (r *Repository) Problem(ctx context.Context, slug string) (p domain.Problem, err error) {
	err = r.pool.QueryRow(ctx, `SELECT p.id,p.slug,p.title,v.id,v.version_number,v.statement,v.time_limit_ms,v.memory_limit_kb FROM problems p JOIN problem_versions v ON v.id=p.current_version_id WHERE p.slug=$1`, slug).Scan(&p.ID, &p.Slug, &p.Title, &p.VersionID, &p.Version, &p.Statement, &p.TimeLimitMS, &p.MemoryLimitKB)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.ErrNotFound
	}
	return
}
func (r *Repository) Problems(ctx context.Context) ([]domain.Problem, error) {
	rows, e := r.pool.Query(ctx, `SELECT p.id,p.slug,p.title,v.id,v.version_number,v.statement,v.time_limit_ms,v.memory_limit_kb FROM problems p JOIN problem_versions v ON v.id=p.current_version_id JOIN problem_test_suites t ON t.problem_version_id=v.id ORDER BY p.slug LIMIT 100`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Problem{}
	for rows.Next() {
		var p domain.Problem
		if e = rows.Scan(&p.ID, &p.Slug, &p.Title, &p.VersionID, &p.Version, &p.Statement, &p.TimeLimitMS, &p.MemoryLimitKB); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Transition is internal-only. Future worker claims must additionally carry attempt fencing.
func (r *Repository) Transition(ctx context.Context, id string, t domain.Transition) (domain.Submission, error) {
	if err := t.Validate(); err != nil {
		return domain.Submission{}, err
	}
	var verdict, reason any
	if t.Verdict != "" {
		verdict = string(t.Verdict)
	}
	if t.FailureReason != "" {
		reason = t.FailureReason
	}
	row := r.pool.QueryRow(ctx, `WITH changed AS (
 UPDATE submissions SET status=$2,verdict=$3,failure_reason=$4,state_version=state_version+1,updated_at=clock_timestamp()
 WHERE id=$1 AND status=$5 AND state_version=$6 RETURNING *)
 SELECT `+columns+` FROM changed s JOIN problem_versions v ON v.id=s.problem_version_id`, id, t.To, verdict, reason, t.From, t.ExpectedVersion)
	s, err := scanSubmission(row)
	if errors.Is(err, domain.ErrNotFound) {
		var exists bool
		if e := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM submissions WHERE id=$1)`, id).Scan(&exists); e != nil {
			return s, e
		}
		if exists {
			return s, domain.ErrConflict
		}
	}
	return s, err
}
