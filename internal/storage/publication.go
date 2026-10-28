package storage

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/jackc/pgx/v5"
	"strings"
)

type Publication struct {
	ProblemID     int64          `json:"problem_id"`
	Statement     string         `json:"statement"`
	TimeLimitMS   int            `json:"time_limit_ms"`
	MemoryLimitKB int            `json:"memory_limit_kb"`
	Bundle        bundles.Bundle `json:"bundle"`
}

// PublishVersion retains a durable STAGED record before any remote write. Repeating the
// same operator/key resumes verification and activation; no unknown object is deleted.
func (r *Repository) PublishVersion(ctx context.Context, operator, key string, p Publication, store bundles.Publisher) (int64, error) {
	if operator == "" || len(operator) > 200 || len(key) < 1 || len(key) > 200 || p.ProblemID < 1 || strings.TrimSpace(p.Statement) == "" || len(p.Statement) > 65536 || p.TimeLimitMS < 50 || p.TimeLimitMS > 30000 || p.MemoryLimitKB < 32768 || p.MemoryLimitKB > 524288 {
		return 0, domain.ErrInvalid
	}
	ref, e := p.Bundle.Reference()
	if e != nil {
		return 0, e
	}
	metadata, _ := json.Marshal(struct {
		Statement    string
		Time, Memory int
	}{p.Statement, p.TimeLimitMS, p.MemoryLimitKB})
	_, e = r.pool.Exec(ctx, `INSERT INTO bundle_publications(operator,idempotency_key,problem_id,bundle_sha,metadata) VALUES($1,$2,$3,$4,$5) ON CONFLICT(operator,idempotency_key) DO NOTHING`, operator, key, p.ProblemID, ref.Hash, metadata)
	if e != nil {
		return 0, e
	}
	var stage string
	var matches bool
	e = r.pool.QueryRow(ctx, `SELECT id::text,problem_id=$3 AND bundle_sha=$4 AND metadata=$5::jsonb FROM bundle_publications WHERE operator=$1 AND idempotency_key=$2`, operator, key, p.ProblemID, ref.Hash, metadata).Scan(&stage, &matches)
	if e != nil {
		return 0, e
	}
	if !matches {
		return 0, domain.ErrConflict
	}
	uploaded, e := store.Put(p.Bundle)
	if e != nil {
		return 0, e
	}
	if uploaded.Hash != ref.Hash {
		return 0, bundles.ErrIntegrity
	}
	read, e := store.Load(ref.Hash)
	if e != nil {
		return 0, e
	}
	if e = read.Verify(ref); e != nil {
		return 0, e
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback(ctx)
	var state string
	var version *int64
	if e = tx.QueryRow(ctx, `SELECT state,version_id FROM bundle_publications WHERE id=$1 FOR UPDATE`, stage).Scan(&state, &version); e != nil {
		return 0, e
	}
	if state == "ACTIVE" {
		return *version, tx.Commit(ctx)
	}
	var problem int64
	if e = tx.QueryRow(ctx, `SELECT id FROM problems WHERE id=$1 FOR UPDATE`, p.ProblemID).Scan(&problem); e != nil {
		return 0, e
	}
	var number int
	if e = tx.QueryRow(ctx, `SELECT COALESCE(max(version_number),0)+1 FROM problem_versions WHERE problem_id=$1`, p.ProblemID).Scan(&number); e != nil {
		return 0, e
	}
	var id int64
	if e = tx.QueryRow(ctx, `INSERT INTO problem_versions(problem_id,version_number,statement,time_limit_ms,memory_limit_kb) VALUES($1,$2,$3,$4,$5) RETURNING id`, p.ProblemID, number, p.Statement, p.TimeLimitMS, p.MemoryLimitKB).Scan(&id); e != nil {
		return 0, e
	}
	if e = bindSuiteTx(ctx, tx, id, ref); e != nil {
		return 0, e
	}
	if _, e = tx.Exec(ctx, `UPDATE problems SET current_version_id=$2 WHERE id=$1`, p.ProblemID, id); e != nil {
		return 0, e
	}
	if _, e = tx.Exec(ctx, `UPDATE bundle_publications SET state='ACTIVE',version_id=$2 WHERE id=$1`, stage, id); e != nil {
		return 0, e
	}
	return id, tx.Commit(ctx)
}
func bindSuiteTx(ctx context.Context, tx pgx.Tx, version int64, ref bundles.Reference) error {
	existing, e := loadReference(ctx, tx, version)
	if e == nil {
		if existing.Hash != ref.Hash {
			return domain.ErrConflict
		}
		return nil
	}
	if !errors.Is(e, domain.ErrNotFound) {
		return e
	}
	for _, c := range ref.Cases {
		if _, e = tx.Exec(ctx, `INSERT INTO suite_cases(problem_version_id,case_id,ordinal,is_sample,input_sha,expected_sha) VALUES($1,$2,$3,$4,$5,$6)`, version, c.ID, c.Order, c.Sample, c.InputHash, c.ExpectedHash); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO problem_test_suites(problem_version_id,bundle_sha,comparator,test_count) VALUES($1,$2,$3,$4)`, version, ref.Hash, ref.Comparator, len(ref.Cases))
	return e
}
