package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Database struct{ Pool *pgxpool.Pool }

func (d Database) KnownCommits(ctx context.Context, repo, format string) ([]string, error) {
	rows, e := d.Pool.Query(ctx, `SELECT DISTINCT commit_oid FROM source_snapshots WHERE repository_id=$1 AND object_format=$2 AND state='READY' ORDER BY commit_oid LIMIT 16`, repo, format)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (d Database) Register(ctx context.Context, owner, name, location, origin string, t Trust) (Repository, error) {
	if len(owner) < 1 || len(owner) > 200 || !namePattern.MatchString(name) {
		return Repository{}, ErrDenied
	}
	p, e := t.Local(location)
	if e != nil {
		return Repository{}, e
	}
	if origin != "" && !t.Remote(origin) {
		return Repository{}, ErrDenied
	}
	_, e = d.Pool.Exec(ctx, `INSERT INTO source_repositories(owner,name,location,origin) VALUES($1,$2,$3,$4) ON CONFLICT(owner,name) DO NOTHING`, owner, name, p, origin)
	if e != nil {
		return Repository{}, e
	}
	var r Repository
	e = d.Pool.QueryRow(ctx, `SELECT id::text,owner,name,location,origin FROM source_repositories WHERE owner=$1 AND name=$2`, owner, name).Scan(&r.ID, &r.Owner, &r.Name, &r.Location, &r.Origin)
	if e != nil || r.Location != p || r.Origin != origin {
		return Repository{}, ErrDenied
	}
	return r, nil
}
func (d Database) Repository(ctx context.Context, owner, id string) (Repository, error) {
	if !uuidPattern.MatchString(id) {
		return Repository{}, ErrDenied
	}
	var r Repository
	e := d.Pool.QueryRow(ctx, `SELECT id::text,owner,name,location,origin FROM source_repositories WHERE owner=$1 AND id=$2`, owner, id).Scan(&r.ID, &r.Owner, &r.Name, &r.Location, &r.Origin)
	if e != nil {
		return r, ErrDenied
	}
	return r, nil
}

const snapshotFields = `s.id,s.repository_id::text,s.object_format,s.commit_oid,s.tree_oid,s.policy_id,s.state,s.generation,COALESCE(s.manifest_sha256,''),s.failure_code,s.stats`

type scanner interface{ Scan(...any) error }

func scanSnapshot(row scanner) (Snapshot, error) {
	var s Snapshot
	var b []byte
	e := row.Scan(&s.ID, &s.Repository, &s.Format, &s.Commit, &s.Tree, &s.Policy, &s.State, &s.Generation, &s.Manifest, &s.Failure, &b)
	if e == nil {
		e = json.Unmarshal(b, &s.Stats)
	}
	return s, e
}
func (d Database) Status(ctx context.Context, owner, id string) (Snapshot, error) {
	if !validHash.MatchString(id) {
		return Snapshot{}, ErrDenied
	}
	s, e := scanSnapshot(d.Pool.QueryRow(ctx, `SELECT `+snapshotFields+` FROM source_snapshots s JOIN source_repositories r ON r.id=s.repository_id WHERE s.id=$1 AND r.owner=$2`, id, owner))
	if e != nil {
		return Snapshot{}, ErrDenied
	}
	return s, nil
}
func (d Database) Request(ctx context.Context, r Repository, format, commit, tree string, p Policy) (Snapshot, bool, error) {
	if e := p.Validate(); e != nil {
		return Snapshot{}, false, e
	}
	if !validOID(commit, format) || !validOID(tree, format) {
		return Snapshot{}, false, ErrSource
	}
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return Snapshot{}, false, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `INSERT INTO ingestion_policies(id,body) VALUES($1,$2::jsonb) ON CONFLICT DO NOTHING`, p.ID(), string(canonical(p))); e != nil {
		return Snapshot{}, false, e
	}
	id := snapshotID(r.ID, format, commit, p.ID())
	res, e := tx.Exec(ctx, `INSERT INTO source_snapshots(id,repository_id,policy_id,object_format,commit_oid,tree_oid) SELECT $1,id,$3,$4,$5,$6 FROM source_repositories WHERE id=$2 AND owner=$7 ON CONFLICT DO NOTHING`, id, r.ID, p.ID(), format, commit, tree, r.Owner)
	if e != nil {
		return Snapshot{}, false, e
	}
	s, e := scanSnapshot(tx.QueryRow(ctx, `SELECT `+snapshotFields+` FROM source_snapshots s JOIN source_repositories r ON r.id=s.repository_id WHERE s.id=$1 AND r.owner=$2`, id, r.Owner))
	if e != nil || s.Tree != tree {
		return Snapshot{}, false, ErrIntegrity
	}
	if e = tx.Commit(ctx); e != nil {
		return Snapshot{}, false, e
	}
	return s, res.RowsAffected() == 1, nil
}
func (d Database) Claim(ctx context.Context, owner, id string, ttl time.Duration, retry bool) (Claim, error) {
	if ttl < 100*time.Millisecond || ttl > 30*time.Second {
		return Claim{}, ErrBudget
	}
	token := uuid.NewString()
	s, e := scanSnapshot(d.Pool.QueryRow(ctx, `UPDATE source_snapshots s SET state='INGESTING',generation=generation+1,attempts=attempts+1,claim_token=$3,lease_expires_at=clock_timestamp()+$4::interval,retry_at=NULL,failure_code='',updated_at=clock_timestamp()
 WHERE s.id=$1 AND EXISTS(SELECT 1 FROM source_repositories r WHERE r.id=s.repository_id AND r.owner=$2) AND attempts<5
 AND (state='PENDING' OR (state='FAILED' AND $5 AND retry_at<=clock_timestamp()) OR (state IN ('INGESTING','VERIFYING') AND lease_expires_at<=clock_timestamp())) RETURNING `+snapshotFields, id, owner, token, ttl.String(), retry))
	if e != nil {
		return Claim{}, ErrBusy
	}
	return Claim{s, token, ttl}, nil
}
func (d Database) owned(ctx context.Context, c Claim) (pgx.Tx, error) {
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	var ok bool
	e = tx.QueryRow(ctx, `SELECT true FROM source_snapshots WHERE id=$1 AND generation=$2 AND claim_token=$3 AND repository_id=$4 AND policy_id=$5 AND object_format=$6 AND commit_oid=$7 AND tree_oid=$8 AND state IN ('INGESTING','VERIFYING') AND lease_expires_at>clock_timestamp() FOR UPDATE`, c.Snapshot.ID, c.Snapshot.Generation, c.Token, c.Snapshot.Repository, c.Snapshot.Policy, c.Snapshot.Format, c.Snapshot.Commit, c.Snapshot.Tree).Scan(&ok)
	if e != nil {
		tx.Rollback(ctx)
		return nil, ErrFence
	}
	_, e = tx.Exec(ctx, `SELECT set_config('judgegrid.ingest_token',$1,true),set_config('judgegrid.ingest_generation',$2,true)`, c.Token, stringInt(c.Snapshot.Generation))
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
	_, e = tx.Exec(ctx, `UPDATE source_snapshots SET lease_expires_at=clock_timestamp()+$2::interval,updated_at=clock_timestamp() WHERE id=$1`, c.Snapshot.ID, c.TTL.String())
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (d Database) Verifying(ctx context.Context, c Claim) error {
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE source_snapshots SET state='VERIFYING',updated_at=clock_timestamp() WHERE id=$1 AND state='INGESTING'`, c.Snapshot.ID)
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
	_, e = tx.Exec(ctx, `UPDATE source_snapshots SET state='FAILED',claim_token=NULL,lease_expires_at=NULL,retry_at=clock_timestamp()+interval '1 second',failure_code=$2,updated_at=clock_timestamp() WHERE id=$1`, c.Snapshot.ID, code)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (d Database) Blob(ctx context.Context, s Snapshot, oid string) (string, int64, bool, error) {
	var digest string
	var n int64
	e := d.Pool.QueryRow(ctx, `SELECT raw_sha256,byte_size FROM source_blobs WHERE repository_id=$1 AND policy_id=$2 AND object_format=$3 AND git_oid=$4`, s.Repository, s.Policy, s.Format, oid).Scan(&digest, &n)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", 0, false, nil
	}
	return digest, n, e == nil, e
}
func (d Database) RecordBlob(ctx context.Context, c Claim, oid, digest string, n int64) error {
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `INSERT INTO source_blobs(repository_id,policy_id,object_format,git_oid,raw_sha256,byte_size) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, c.Snapshot.Repository, c.Snapshot.Policy, c.Snapshot.Format, oid, digest, n)
	if e != nil {
		return e
	}
	var got string
	var size int64
	e = tx.QueryRow(ctx, `SELECT raw_sha256,byte_size FROM source_blobs WHERE repository_id=$1 AND policy_id=$2 AND object_format=$3 AND git_oid=$4`, c.Snapshot.Repository, c.Snapshot.Policy, c.Snapshot.Format, oid).Scan(&got, &size)
	if e != nil {
		return e
	}
	if got != digest || size != n {
		return ErrIntegrity
	}
	return tx.Commit(ctx)
}
func (d Database) Publish(ctx context.Context, c Claim, m Manifest, stats Stats) error {
	if e := validateManifest(c.Snapshot, m); e != nil {
		return e
	}
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	batch := &pgx.Batch{}
	for _, f := range m.Entries {
		batch.Queue(`INSERT INTO source_files(snapshot_id,path,file_id,git_mode,git_type,git_oid,byte_size,raw_sha256,language,reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, m.Snapshot, f.Path, f.ID, f.Mode, f.Type, f.OID, f.Size, f.SHA256, f.Language, f.Reason)
	}
	results := tx.SendBatch(ctx, batch)
	if e = results.Close(); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE source_snapshots SET state='READY',manifest_sha256=$2,stats=$3::jsonb,claim_token=NULL,lease_expires_at=NULL,retry_at=NULL,failure_code='',updated_at=clock_timestamp() WHERE id=$1`, m.Snapshot, Digest(canonical(m)), string(canonical(stats)))
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (d Database) Files(ctx context.Context, owner, id string) ([]Entry, error) {
	s, e := d.Status(ctx, owner, id)
	if e != nil {
		return nil, e
	}
	if s.State != "READY" {
		return nil, ErrBusy
	}
	rows, e := d.Pool.Query(ctx, `SELECT file_id,path,git_mode,git_type,git_oid,byte_size,raw_sha256,language,reason FROM source_files WHERE snapshot_id=$1 ORDER BY path COLLATE "C" LIMIT 20001`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		var f Entry
		if e = rows.Scan(&f.ID, &f.Path, &f.Mode, &f.Type, &f.OID, &f.Size, &f.SHA256, &f.Language, &f.Reason); e != nil {
			return nil, e
		}
		entries = append(entries, f)
		if len(entries) > 20000 {
			return nil, ErrBudget
		}
	}
	return entries, rows.Err()
}
func (d Database) Policy(ctx context.Context, id string) (Policy, error) {
	var b []byte
	var p Policy
	e := d.Pool.QueryRow(ctx, `SELECT body FROM ingestion_policies WHERE id=$1`, id).Scan(&b)
	if e != nil {
		return p, e
	}
	if e = json.Unmarshal(b, &p); e != nil {
		return p, ErrIntegrity
	}
	if p.ID() != id {
		return p, ErrIntegrity
	}
	return p, p.Validate()
}
