package retrieval

import (
	"context"
	"encoding/json"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strconv"
	"time"
)

type Database struct{ Pool *pgxpool.Pool }
type claim struct {
	Index Index
	Token string
	TTL   time.Duration
}

const fields = `a.id,a.repository_id::text,a.snapshot_id,a.analysis_id,a.analysis_manifest,a.policy_id,a.state,a.generation,COALESCE(a.manifest_sha256,''),a.failure_code,a.stats`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Index, error) {
	var a Index
	var b []byte
	e := row.Scan(&a.ID, &a.Repository, &a.Snapshot, &a.Analysis, &a.AnalysisManifest, &a.Policy, &a.State, &a.Generation, &a.Manifest, &a.Failure, &b)
	if e == nil {
		e = json.Unmarshal(b, &a.Stats)
	}
	return a, e
}
func (d Database) Status(ctx context.Context, owner, id string) (Index, error) {
	a, e := scan(d.Pool.QueryRow(ctx, `SELECT `+fields+` FROM retrieval_indexes a JOIN source_repositories r ON r.id=a.repository_id WHERE a.id=$1 AND r.owner=$2`, id, owner))
	if e != nil {
		return Index{}, ErrDenied
	}
	return a, nil
}
func (d Database) request(ctx context.Context, owner string, a analysis.Index, p Policy) (Index, error) {
	if e := p.Validate(); e != nil {
		return Index{}, e
	}
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return Index{}, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `INSERT INTO retrieval_policies(id,body) VALUES($1,$2::jsonb) ON CONFLICT DO NOTHING`, p.ID(), string(canonical(p)))
	if e != nil {
		return Index{}, e
	}
	id := hash([]string{"retrieval-v1", a.Repository, a.Snapshot, a.ID, a.Manifest, p.ID()})
	_, e = tx.Exec(ctx, `INSERT INTO retrieval_indexes(id,repository_id,snapshot_id,analysis_id,analysis_manifest,policy_id) SELECT $1,a.repository_id,a.snapshot_id,a.id,a.manifest_sha256,$3 FROM analysis_indexes a JOIN source_repositories r ON r.id=a.repository_id WHERE a.id=$2 AND a.state='READY' AND a.manifest_sha256=$4 AND r.owner=$5 ON CONFLICT DO NOTHING`, id, a.ID, p.ID(), a.Manifest, owner)
	if e != nil {
		return Index{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Index{}, e
	}
	return d.Status(ctx, owner, id)
}
func (d Database) claim(ctx context.Context, owner, id string, ttl time.Duration, retry bool) (claim, error) {
	if ttl < 100*time.Millisecond || ttl > 30*time.Second {
		return claim{}, ErrBudget
	}
	token := uuid.NewString()
	a, e := scan(d.Pool.QueryRow(ctx, `UPDATE retrieval_indexes a SET state='BUILDING',generation=generation+1,attempts=attempts+1,claim_token=$3,lease_expires_at=clock_timestamp()+$4::interval,retry_at=NULL,failure_code='' WHERE id=$1 AND EXISTS(SELECT 1 FROM source_repositories r WHERE r.id=a.repository_id AND r.owner=$2) AND attempts<5 AND (state='PENDING' OR (state='FAILED' AND $5 AND retry_at<=clock_timestamp()) OR (state IN ('BUILDING','VERIFYING') AND lease_expires_at<=clock_timestamp())) RETURNING `+fields, id, owner, token, ttl.String(), retry))
	if e != nil {
		return claim{}, ErrBusy
	}
	return claim{a, token, ttl}, nil
}
func (d Database) owned(ctx context.Context, c claim) (pgx.Tx, error) {
	tx, e := d.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	var ok bool
	e = tx.QueryRow(ctx, `SELECT true FROM retrieval_indexes WHERE id=$1 AND generation=$2 AND claim_token=$3 AND repository_id=$4 AND policy_id=$5 AND state IN ('BUILDING','VERIFYING') AND lease_expires_at>clock_timestamp() FOR UPDATE`, c.Index.ID, c.Index.Generation, c.Token, c.Index.Repository, c.Index.Policy).Scan(&ok)
	if e != nil {
		tx.Rollback(ctx)
		return nil, ErrFence
	}
	_, e = tx.Exec(ctx, `SELECT set_config('judgegrid.retrieval_token',$1,true),set_config('judgegrid.retrieval_generation',$2,true)`, c.Token, strconv.FormatInt(c.Index.Generation, 10))
	if e != nil {
		tx.Rollback(ctx)
		return nil, e
	}
	return tx, nil
}
func (d Database) renew(ctx context.Context, c claim) error {
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE retrieval_indexes SET lease_expires_at=clock_timestamp()+$2::interval WHERE id=$1`, c.Index.ID, c.TTL.String())
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (d Database) fail(ctx context.Context, c claim, code string) error {
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE retrieval_indexes SET state='FAILED',failure_code=$2,claim_token=NULL,lease_expires_at=NULL,retry_at=clock_timestamp()+interval '1 second' WHERE id=$1`, c.Index.ID, code)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (d Database) cached(ctx context.Context, c claim, p Policy, input string) ([]float32, bool, error) {
	var body, digest string
	e := d.Pool.QueryRow(ctx, `SELECT vector_body,vector_sha256 FROM retrieval_embedding_cache WHERE repository_id=$1 AND model_policy=$2 AND input_sha256=$3`, c.Index.Repository, p.Model, input).Scan(&body, &digest)
	if e == pgx.ErrNoRows {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	var v []float32
	if ingest.Digest([]byte(body)) != digest || json.Unmarshal([]byte(body), &v) != nil || ValidateVector(v) != nil {
		return nil, false, ErrIntegrity
	}
	return v, true, nil
}
func (d Database) cache(ctx context.Context, c claim, p Policy, input string, v []float32) error {
	if e := ValidateVector(v); e != nil {
		return e
	}
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	b := canonical(v)
	_, e = tx.Exec(ctx, `INSERT INTO retrieval_embedding_cache(repository_id,model_policy,input_sha256,vector_body,vector_sha256) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, c.Index.Repository, p.Model, input, string(b), ingest.Digest(b))
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func partition(id string) string    { return "retrieval_v_" + id[:40] }
func vectorText(v []float32) string { return string(canonical(v)) }
func (d Database) publish(ctx context.Context, c claim, m Manifest, p Policy, vectors [][]float32, stats Stats) error {
	if e := validate(m, p); e != nil {
		return e
	}
	if len(vectors) != len(m.Documents) || m.Index != c.Index.ID || m.Policy != c.Index.Policy || m.AnalysisManifest != c.Index.AnalysisManifest || m.Analysis != c.Index.Analysis || m.Repository != c.Index.Repository || m.Snapshot != c.Index.Snapshot {
		return ErrIntegrity
	}
	tx, e := d.owned(ctx, c)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `UPDATE retrieval_indexes SET state='VERIFYING' WHERE id=$1`, c.Index.ID)
	if e != nil {
		return e
	}
	// Only a validated hexadecimal ID enters DDL, always quoted as an identifier.
	name := pgx.Identifier{partition(c.Index.ID)}.Sanitize()
	_, e = tx.Exec(ctx, `CREATE TABLE `+name+` PARTITION OF retrieval_documents FOR VALUES IN ('`+c.Index.ID+`')`)
	if e != nil {
		return e
	}
	for start := 0; start < len(m.Documents); start += 64 {
		batch := &pgx.Batch{}
		for i := start; i < min(start+64, len(m.Documents)); i++ {
			doc := m.Documents[i]
			if ValidateVector(vectors[i]) != nil || hash(vectors[i]) != doc.VectorHash {
				return ErrIntegrity
			}
			batch.Queue(`INSERT INTO retrieval_documents(index_id,chunk_id,payload,embedding) VALUES($1,$2,$3::jsonb,$4::public.vector)`, m.Index, doc.Chunk.ID, string(canonical(doc)), vectorText(vectors[i]))
		}
		results := tx.SendBatch(ctx, batch)
		if e = results.Close(); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `CREATE INDEX ON `+name+` USING hnsw (embedding public.vector_cosine_ops) WITH (m=16,ef_construction=64)`)
	if e != nil {
		return e
	}
	b := canonical(m)
	_, e = tx.Exec(ctx, `UPDATE retrieval_indexes SET state='READY',manifest_sha256=$2,manifest_body=$3,stats=$4::jsonb,claim_token=NULL,lease_expires_at=NULL WHERE id=$1`, m.Index, ingest.Digest(b), string(b), string(canonical(stats)))
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (d Database) manifest(ctx context.Context, owner, id string) (Index, Manifest, Policy, error) {
	a, e := d.Status(ctx, owner, id)
	if e != nil {
		return a, Manifest{}, Policy{}, e
	}
	if a.State != "READY" {
		return a, Manifest{}, Policy{}, ErrBusy
	}
	var b, pb []byte
	e = d.Pool.QueryRow(ctx, `SELECT a.manifest_body,p.body FROM retrieval_indexes a JOIN retrieval_policies p ON p.id=a.policy_id JOIN source_repositories r ON r.id=a.repository_id WHERE a.id=$1 AND r.owner=$2 AND a.state='READY'`, id, owner).Scan(&b, &pb)
	var m Manifest
	var p Policy
	if e != nil || ingest.Digest(b) != a.Manifest || json.Unmarshal(b, &m) != nil || json.Unmarshal(pb, &p) != nil || p.ID() != a.Policy || string(canonical(m)) != string(b) || m.Index != id || m.Repository != a.Repository || m.Snapshot != a.Snapshot || m.Analysis != a.Analysis || m.AnalysisManifest != a.AnalysisManifest || m.Policy != a.Policy {
		return a, m, p, ErrIntegrity
	}
	if e = validate(m, p); e != nil {
		return a, m, p, e
	}
	return a, m, p, nil
}
func (d Database) verifyRows(ctx context.Context, owner string, m Manifest) error {
	rows, e := d.Pool.Query(ctx, `SELECT d.chunk_id,d.payload,d.embedding::text FROM retrieval_documents d JOIN retrieval_indexes i ON i.id=d.index_id JOIN source_repositories r ON r.id=i.repository_id WHERE d.index_id=$1 AND r.owner=$2 AND i.state='READY' ORDER BY d.chunk_id`, m.Index, owner)
	if e != nil {
		return e
	}
	defer rows.Close()
	by := map[string]Document{}
	for _, doc := range m.Documents {
		by[doc.Chunk.ID] = doc
	}
	for rows.Next() {
		var id, vec string
		var b []byte
		if rows.Scan(&id, &b, &vec) != nil {
			return ErrIntegrity
		}
		doc, ok := by[id]
		var v []float32
		var actual Document
		if !ok || json.Unmarshal(b, &actual) != nil || hash(actual) != hash(doc) || json.Unmarshal([]byte(vec), &v) != nil || ValidateVector(v) != nil || hash(v) != doc.VectorHash {
			return ErrIntegrity
		}
		delete(by, id)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if len(by) != 0 {
		return ErrIntegrity
	}
	return nil
}
