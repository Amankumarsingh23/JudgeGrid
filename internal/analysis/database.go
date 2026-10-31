package analysis

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Database struct{ Pool *pgxpool.Pool }

const fields = `a.id,a.snapshot_id,a.repository_id::text,a.policy_id,a.state,a.generation,COALESCE(a.manifest_sha256,''),a.failure_code,a.stats`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Index, error) {
	var a Index
	var b []byte
	e := row.Scan(&a.ID, &a.Snapshot, &a.Repository, &a.Policy, &a.State, &a.Generation, &a.Manifest, &a.Failure, &b)
	if e == nil {
		e = json.Unmarshal(b, &a.Stats)
	}
	return a, e
}
func (d Database) Status(ctx context.Context, owner, id string) (Index, error) {
	a, e := scan(d.Pool.QueryRow(ctx, `SELECT `+fields+` FROM analysis_indexes a JOIN source_repositories r ON r.id=a.repository_id WHERE a.id=$1 AND r.owner=$2`, id, owner))
	if e != nil {
		return Index{}, ErrDenied
	}
	return a, nil
}
func (d Database) Request(ctx context.Context, owner string, s ingest.Snapshot, p Policy) (Index, error) {
	if e := p.Validate(); e != nil {
		return Index{}, e
	}
	if s.State != "READY" {
		return Index{}, ErrDenied
	}
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return Index{}, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `INSERT INTO analysis_policies(id,parser_identity,body) VALUES($1,$2,$3::jsonb) ON CONFLICT DO NOTHING`, p.ID(), p.Parser, string(canonical(p)))
	if e != nil {
		return Index{}, e
	}
	id := indexID(s.ID, p.ID())
	_, e = tx.Exec(ctx, `INSERT INTO analysis_indexes(id,snapshot_id,repository_id,policy_id,source_manifest) SELECT $1,s.id,s.repository_id,$3,s.manifest_sha256 FROM source_snapshots s JOIN source_repositories r ON r.id=s.repository_id WHERE s.id=$2 AND s.repository_id=$4 AND s.state='READY' AND s.manifest_sha256=$5 AND r.owner=$6 ON CONFLICT DO NOTHING`, id, s.ID, p.ID(), s.Repository, s.Manifest, owner)
	if e != nil {
		return Index{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Index{}, e
	}
	return d.Status(ctx, owner, id)
}
func (d Database) Claim(ctx context.Context, owner, id string, ttl time.Duration, retry bool) (Claim, error) {
	if ttl < 100*time.Millisecond || ttl > 30*time.Second {
		return Claim{}, ErrBudget
	}
	token := uuid.NewString()
	a, e := scan(d.Pool.QueryRow(ctx, `UPDATE analysis_indexes a SET state='ANALYZING',generation=generation+1,attempts=attempts+1,claim_token=$3,lease_expires_at=clock_timestamp()+$4::interval,retry_at=NULL,failure_code='' WHERE id=$1 AND EXISTS(SELECT 1 FROM source_repositories r WHERE r.id=a.repository_id AND r.owner=$2) AND attempts<5 AND (state='PENDING' OR (state='FAILED' AND $5 AND retry_at<=clock_timestamp()) OR (state IN ('ANALYZING','VERIFYING') AND lease_expires_at<=clock_timestamp())) RETURNING `+fields, id, owner, token, ttl.String(), retry))
	if e != nil {
		return Claim{}, ErrBusy
	}
	return Claim{a, token, ttl}, nil
}
func (d Database) owned(ctx context.Context, c Claim) (pgx.Tx, error) {
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	var ok bool
	e = tx.QueryRow(ctx, `SELECT true FROM analysis_indexes WHERE id=$1 AND generation=$2 AND claim_token=$3 AND repository_id=$4 AND policy_id=$5 AND state IN ('ANALYZING','VERIFYING') AND lease_expires_at>clock_timestamp() FOR UPDATE`, c.Index.ID, c.Index.Generation, c.Token, c.Index.Repository, c.Index.Policy).Scan(&ok)
	if e != nil {
		tx.Rollback(ctx)
		return nil, ErrFence
	}
	_, e = tx.Exec(ctx, `SELECT set_config('judgegrid.analysis_token',$1,true),set_config('judgegrid.analysis_generation',$2,true)`, c.Token, strconv.FormatInt(c.Index.Generation, 10))
	if e != nil {
		tx.Rollback(ctx)
		return nil, e
	}
	return tx, nil
}
func (d Database) Renew(ctx context.Context, c Claim) error {
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE analysis_indexes SET lease_expires_at=clock_timestamp()+$2::interval WHERE id=$1`, c.Index.ID, c.TTL.String())
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (d Database) Fail(ctx context.Context, c Claim, code string) error {
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE analysis_indexes SET state='FAILED',failure_code=$2,claim_token=NULL,lease_expires_at=NULL,retry_at=clock_timestamp()+interval '1 second' WHERE id=$1`, c.Index.ID, code)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (d Database) Progress(ctx context.Context, c Claim, sourceID, state, code string) error {
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `INSERT INTO analysis_file_attempts(index_id,generation,source_file_id,state,failure_code) VALUES($1,$2,$3,$4,$5) ON CONFLICT(index_id,generation,source_file_id) DO UPDATE SET state=EXCLUDED.state,failure_code=EXCLUDED.failure_code`, c.Index.ID, c.Index.Generation, sourceID, state, code)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (d Database) Cached(ctx context.Context, c Claim, key string) (Facts, bool, error) {
	var body, digest string
	e := d.Pool.QueryRow(ctx, `SELECT body,sha256 FROM analysis_cache WHERE repository_id=$1 AND policy_id=$2 AND identity=$3`, c.Index.Repository, c.Index.Policy, key).Scan(&body, &digest)
	if e == pgx.ErrNoRows {
		return Facts{}, false, nil
	}
	if e != nil {
		return Facts{}, false, e
	}
	if ingest.Digest([]byte(body)) != digest {
		return Facts{}, false, ErrIntegrity
	}
	var f Facts
	e = json.Unmarshal([]byte(body), &f)
	return f, true, e
}
func (d Database) Cache(ctx context.Context, c Claim, key string, f Facts) error {
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	b := canonical(f)
	_, e = tx.Exec(ctx, `INSERT INTO analysis_cache(repository_id,policy_id,identity,sha256,body) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, c.Index.Repository, c.Index.Policy, key, ingest.Digest(b), string(b))
	if e != nil {
		return e
	}
	var digest string
	e = tx.QueryRow(ctx, `SELECT sha256 FROM analysis_cache WHERE repository_id=$1 AND policy_id=$2 AND identity=$3`, c.Index.Repository, c.Index.Policy, key).Scan(&digest)
	if e != nil {
		return e
	}
	if digest != ingest.Digest(b) {
		return ErrIntegrity
	}
	return tx.Commit(ctx)
}

func (d Database) Publish(ctx context.Context, c Claim, m Manifest, stats Stats, p Policy) error {
	if m.Index != c.Index.ID || m.Snapshot != c.Index.Snapshot || m.Repository != c.Index.Repository || m.Policy != c.Index.Policy {
		return ErrIntegrity
	}
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	// Recheck the complete immutable source inventory inside the fenced boundary;
	// callers cannot publish an index with a subset of the snapshot's files.
	if e = p.Validate(); e != nil {
		return e
	}
	var sourceManifest, commit string
	e = tx.QueryRow(ctx, `SELECT manifest_sha256,commit_oid FROM source_snapshots WHERE id=$1 AND repository_id=$2 AND state='READY'`, c.Index.Snapshot, c.Index.Repository).Scan(&sourceManifest, &commit)
	if e != nil || sourceManifest != m.SourceManifest {
		return ErrIntegrity
	}
	inventory := ingest.Manifest{Snapshot: c.Index.Snapshot, Repository: c.Index.Repository, Commit: commit}
	sourceRows, e := tx.Query(ctx, `SELECT file_id,path,git_mode,git_type,git_oid,byte_size,raw_sha256,language,reason FROM source_files WHERE snapshot_id=$1 ORDER BY path COLLATE "C"`, c.Index.Snapshot)
	if e != nil {
		return e
	}
	for sourceRows.Next() {
		var f ingest.Entry
		if e = sourceRows.Scan(&f.ID, &f.Path, &f.Mode, &f.Type, &f.OID, &f.Size, &f.SHA256, &f.Language, &f.Reason); e != nil {
			sourceRows.Close()
			return e
		}
		inventory.Entries = append(inventory.Entries, f)
		if len(inventory.Entries) > p.MaxFiles {
			sourceRows.Close()
			return ErrBudget
		}
	}
	sourceRows.Close()
	if e = sourceRows.Err(); e != nil {
		return e
	}
	if e = validateManifest(m, inventory, p); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE analysis_indexes SET state='VERIFYING' WHERE id=$1`, c.Index.ID)
	if e != nil {
		return e
	}
	// Everything is bounded before this short publication transaction. Batch send
	// size is capped, and READY plus all rows commit atomically.
	type row struct {
		table, id, file string
		value           any
	}
	rows := []row{}
	for _, f := range m.Files {
		rows = append(rows, row{"analysis_files", f.ID, f.SourceID, f})
	}
	for _, s := range m.Symbols {
		rows = append(rows, row{"analysis_symbols", s.ID, s.File, s})
	}
	for _, s := range m.Imports {
		rows = append(rows, row{"analysis_imports", s.ID, s.File, s})
	}
	for _, s := range m.Calls {
		rows = append(rows, row{"analysis_calls", s.ID, s.File, s})
	}
	for _, s := range m.Edges {
		rows = append(rows, row{"analysis_edges", s.ID, s.File, s})
	}
	for _, s := range m.Chunks {
		rows = append(rows, row{"analysis_chunks", s.ID, s.File, s})
	}
	if len(rows) > p.MaxRows {
		return ErrBudget
	}
	for start := 0; start < len(rows); {
		table := rows[start].table
		col := "file_id"
		if table == "analysis_files" {
			col = "source_file_id"
		}
		end := start
		values := [][]any{}
		for end < len(rows) && end-start < p.Batch && rows[end].table == table {
			r := rows[end]
			values = append(values, []any{m.Index, r.id, r.file, string(canonical(r.value))})
			end++
		}
		// COPY preserves row triggers and foreign keys while avoiding thousands
		// of individual extended-query messages inside the publication lease.
		if _, e = tx.CopyFrom(ctx, pgx.Identifier{table}, []string{"index_id", "id", col, "payload"}, pgx.CopyFromRows(values)); e != nil {
			return e
		}
		start = end
	}
	body := canonical(m)
	_, e = tx.Exec(ctx, `UPDATE analysis_indexes SET state='READY',manifest_sha256=$2,manifest_body=$3,stats=$4::jsonb,claim_token=NULL,lease_expires_at=NULL WHERE id=$1`, m.Index, ingest.Digest(body), string(body), string(canonical(stats)))
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (d Database) Manifest(ctx context.Context, owner, id string) (Index, Manifest, error) {
	a, e := d.Status(ctx, owner, id)
	if e != nil {
		return a, Manifest{}, e
	}
	if a.State != "READY" {
		return a, Manifest{}, ErrDenied
	}
	var body string
	e = d.Pool.QueryRow(ctx, `SELECT a.manifest_body FROM analysis_indexes a JOIN source_repositories r ON r.id=a.repository_id WHERE a.id=$1 AND r.owner=$2 AND a.state='READY'`, id, owner).Scan(&body)
	if e != nil {
		return a, Manifest{}, ErrDenied
	}
	if ingest.Digest([]byte(body)) != a.Manifest {
		return a, Manifest{}, ErrIntegrity
	}
	var m Manifest
	e = json.Unmarshal([]byte(body), &m)
	if e != nil {
		return a, m, ErrIntegrity
	}
	if m.Index != a.ID || m.Policy != a.Policy || m.Repository != a.Repository || m.Snapshot != a.Snapshot || m.Schema != 1 {
		return a, m, ErrIntegrity
	}
	return a, m, nil
}
