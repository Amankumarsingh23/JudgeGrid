//go:build integration

package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type lab struct {
	svc                      *Service
	analysis                 *analysis.Service
	source                   *ingest.Service
	db                       *pgxpool.Pool
	repo                     ingest.Repository
	snapshot                 ingest.Snapshot
	index                    analysis.Index
	dir, bucket, url, schema string
}

func setup(t *testing.T) *lab {
	t.Helper()
	ctx := context.Background()
	base := os.Getenv("TEST_DATABASE_URL")
	endpoint := os.Getenv("REPO_TEST_S3_ENDPOINT")
	if base == "" || endpoint == "" {
		t.Fatal("actual PostgreSQL/pgvector and separate source S3 required")
	}
	admin, e := pgxpool.New(ctx, base)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("judgegrid_retrieval_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+quoted); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(base)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 12
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		db.Close()
		_, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		if e != nil {
			t.Error(e)
		}
	})
	if e = migrations.Apply(ctx, db); e != nil {
		t.Fatal(e)
	}
	var version string
	if e = db.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname='vector'`).Scan(&version); e != nil || version != "0.8.2" {
		t.Fatal("pinned extension", version, e)
	}
	bucket := fmt.Sprintf("jg14-%d", time.Now().UnixNano())
	store, e := ingest.OpenS3(endpoint, bucket, os.Getenv("REPO_TEST_S3_ACCESS_KEY"), os.Getenv("REPO_TEST_S3_SECRET_KEY"), false)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "repo")
	if e = os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	l := &lab{db: db, dir: dir, bucket: bucket, url: cfg.ConnString(), schema: schema}
	l.git(t, "init", "--initial-branch=main")
	l.git(t, "config", "core.autocrlf", "false")
	l.write(t, "go.mod", "module example.test/retrieval\n")
	l.write(t, "internal/storage/ownership.go", "package storage\n// renewExecution extends the current worker execution lease.\nfunc renewExecution() { extendLease() }\n// authorizeWork rejects stale worker generations before results are finalized.\nfunc authorizeWork() { rejectStaleGeneration() }\n")
	l.write(t, "internal/buildcache/cache.go", "package buildcache\n// Populate the compilation cache after successful compilation.\nfunc Populate() { storeCompiledBinary() }\n")
	l.write(t, "internal/outbox/publish.go", "package outbox\n// Publish the durable event to the queue stream.\nfunc Publish() { appendQueueEvent() }\n")
	l.write(t, "broken.go", "package broken\nfunc Broken( {\n")
	for i := 0; i < 8; i++ {
		l.write(t, fmt.Sprintf("notes/%d.txt", i), fmt.Sprintf("unrelated fixture document %d with stable plain text\n", i))
	}
	l.git(t, "add", ".")
	l.git(t, "commit", "-m", "retrieval fixture")
	trust := ingest.Trust{Roots: []string{filepath.Dir(dir)}}
	l.source = &ingest.Service{DB: ingest.Database{Pool: db}, Objects: store, Trust: trust}
	l.repo, e = l.source.DB.Register(ctx, "owner", "retrieval-fixture", dir, "", trust)
	if e != nil {
		t.Fatal(e)
	}
	l.snapshot, e = l.source.Ingest(ctx, "owner", l.repo.ID, l.git(t, "rev-parse", "HEAD"), ingest.DefaultPolicy(), ingest.Options{})
	if e != nil {
		t.Fatal(e)
	}
	l.analysis = &analysis.Service{DB: analysis.Database{Pool: db}, Source: l.source, Python: os.Getenv("REPO_ANALYSIS_PYTHON")}
	l.index, e = l.analysis.Analyze(ctx, "owner", l.snapshot.ID, analysis.DefaultPolicy(), false)
	if e != nil {
		t.Fatal(e)
	}
	l.svc = &Service{DB: Database{Pool: db}, Source: l.analysis, Model: testModel(t)}
	return l
}
func (l *lab) git(t *testing.T, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgSign=false", "-C", l.dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatal("fixture git", e, string(b))
	}
	return strings.TrimSpace(string(b))
}
func (l *lab) write(t *testing.T, path, text string) {
	t.Helper()
	p := filepath.Join(l.dir, filepath.FromSlash(path))
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
}
func (l *lab) build(t *testing.T, p Policy) Index {
	t.Helper()
	a, e := l.svc.Build(context.Background(), "owner", l.index.ID, p, false)
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func request(a Index, mode, query string) Request {
	return Request{Index: a.ID, Snapshot: a.Snapshot, Repository: a.Repository, Query: query, Mode: mode, TopK: 5, Depth: 100}
}
func TestS14PublicationRankingAndOwnerBoundary(t *testing.T) {
	l := setup(t)
	ctx := context.Background()
	p := DefaultPolicy()
	a := l.build(t, p)
	if a.State != "READY" || a.Stats.Misses < 1 {
		t.Fatal(a)
	}
	_, m, _, e := l.svc.Ready(ctx, "owner", a.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = l.svc.Verify(ctx, "owner", a.ID); e != nil {
		t.Fatal(e)
	}
	incompatible := *l.svc
	incompatible.Model = wrongSpace{}
	if _, e = incompatible.Search(ctx, "owner", request(a, "dense", "lease")); !errors.Is(e, ErrModel) {
		t.Fatal("incompatible query model policy", e)
	}
	if _, e = incompatible.Build(ctx, "owner", l.index.ID, p, false); !errors.Is(e, ErrModel) {
		t.Fatal("incompatible build model policy", e)
	}
	for _, mode := range []string{"bm25", "dense", "hybrid"} {
		q := request(a, mode, "renewExecution")
		v, e := l.svc.Search(ctx, "owner", q)
		if e != nil || len(v.Candidates) == 0 {
			t.Fatal(mode, e)
		}
		if mode == "bm25" && !strings.Contains(v.Candidates[0].Document.Symbol, "renewExecution") {
			t.Fatal("exact symbol provenance", v)
		}
		r, e := l.svc.Search(ctx, "owner", q)
		v.Seconds = 0
		r.Seconds = 0
		if e != nil || !reflect.DeepEqual(v, r) {
			t.Fatal("S stable order", mode, e)
		}
		q.Repository = "wrong"
		if _, e = l.svc.Search(ctx, "owner", q); !errors.Is(e, ErrDenied) {
			t.Fatal("repository mixing", e)
		}
		q = request(a, mode, "private query")
		q.Snapshot = strings.Repeat("0", 64)
		if _, e = l.svc.Search(ctx, "owner", q); !errors.Is(e, ErrDenied) {
			t.Fatal("snapshot mixing", e)
		}
	}
	for _, doc := range m.Documents {
		if _, e = l.svc.ReadChunk(ctx, "owner", a.ID, doc.Chunk.ID); e != nil {
			t.Fatal(e)
		}
	}
	for _, text := range []string{"internal/storage/ownership.go", "execution lease", "func"} {
		q := request(a, "bm25", text)
		v, e := l.svc.Search(ctx, "owner", q)
		if e != nil || len(v.Candidates) == 0 {
			t.Fatal("F path/phrase/generic", text, e)
		}
		if text == "internal/storage/ownership.go" && v.Candidates[0].Document.Path != text {
			t.Fatal("F path rank", v.Candidates[0].Document.Path)
		}
	}
	repeated, e := l.svc.Build(ctx, "owner", l.index.ID, p, false)
	if e != nil || !repeated.Replayed || repeated.Manifest != a.Manifest {
		t.Fatal("READY replay", e)
	}
	q := p
	q.B = .6
	b := l.build(t, q)
	if b.ID == a.ID || b.Stats.Hits != b.Stats.Documents || b.Stats.Misses != 0 {
		t.Fatal("new ranking identity may reuse compatible input vectors", b)
	}
	counter := &countReader{Reader: l.analysis}
	l.svc.Source = counter
	if _, e = l.svc.DB.Status(ctx, "foreign", a.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("foreign status", e)
	}
	if _, _, _, e = l.svc.Ready(ctx, "foreign", a.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("foreign metadata", e)
	}
	if _, e = l.svc.Search(ctx, "foreign", request(a, "hybrid", "secret")); !errors.Is(e, ErrDenied) {
		t.Fatal("foreign scores", e)
	}
	if _, e = l.svc.ReadChunk(ctx, "foreign", a.ID, m.Documents[0].Chunk.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("foreign source", e)
	}
	if _, e = l.svc.Build(ctx, "foreign", l.index.ID, p, false); !errors.Is(e, ErrDenied) {
		t.Fatal("foreign build", e)
	}
	// Only Build reaches the analysis reader, which itself denies before source I/O.
	if counter.reads.Load() != 1 {
		t.Fatal("denied search reached analysis/source", counter.reads.Load())
	}
	if _, e = l.db.Exec(ctx, `UPDATE retrieval_indexes SET stats='{}' WHERE id=$1`, a.ID); e == nil {
		t.Fatal("immutable READY")
	}
	if _, e = l.db.Exec(ctx, `DELETE FROM retrieval_documents WHERE index_id=$1`, a.ID); e == nil {
		t.Fatal("immutable vectors")
	}
	l.svc.Source = l.analysis
	l.write(t, "internal/storage/ownership.go", "package storage\nfunc ReplacementOnly() {}\n")
	l.git(t, "add", ".")
	l.git(t, "commit", "-m", "new exact commit")
	next, e := l.source.Ingest(ctx, "owner", l.repo.ID, l.git(t, "rev-parse", "HEAD"), ingest.DefaultPolicy(), ingest.Options{})
	if e != nil {
		t.Fatal(e)
	}
	nextAnalysis, e := l.analysis.Analyze(ctx, "owner", next.ID, analysis.DefaultPolicy(), false)
	if e != nil {
		t.Fatal(e)
	}
	nextRetrieval, e := l.svc.Build(ctx, "owner", nextAnalysis.ID, p, false)
	if e != nil {
		t.Fatal(e)
	}
	if nextRetrieval.ID == a.ID || nextRetrieval.Snapshot == a.Snapshot {
		t.Fatal("P commit identity")
	}
	oldResult, e := l.svc.Search(ctx, "owner", request(a, "bm25", "renewExecution"))
	if e != nil || len(oldResult.Candidates) == 0 {
		t.Fatal("old commit history", e)
	}
	newResult, e := l.svc.Search(ctx, "owner", request(nextRetrieval, "bm25", "renewExecution"))
	if e != nil || len(newResult.Candidates) != 0 {
		t.Fatal("P cross-commit chunk leak", e)
	}
}

type countReader struct {
	analysis.Reader
	reads atomic.Int32
}

// A deliberately incompatible backend must be rejected before inference. All
// successful embedding/retrieval demonstrations use the genuine pinned model.
type wrongSpace struct{}

func (wrongSpace) Identity() string { return strings.Repeat("f", 64) }
func (wrongSpace) Embed(context.Context, []string) ([][]float32, error) {
	panic("incompatible embedding backend invoked")
}

func (r *countReader) Ready(ctx context.Context, o, id string) (analysis.Index, analysis.Manifest, error) {
	r.reads.Add(1)
	return r.Reader.Ready(ctx, o, id)
}
func (r *countReader) ReadChunk(ctx context.Context, o, index, id string) (analysis.VerifiedChunk, error) {
	r.reads.Add(1)
	return r.Reader.ReadChunk(ctx, o, index, id)
}
func TestS14ExactCosineHNSWAndSelectiveFallback(t *testing.T) {
	l := setup(t)
	ctx := context.Background()
	a := l.build(t, DefaultPolicy())
	_, m, _, e := l.svc.Ready(ctx, "owner", a.ID)
	if e != nil {
		t.Fatal(e)
	}
	rows, e := l.db.Query(ctx, `SELECT chunk_id,embedding::text FROM retrieval_documents WHERE index_id=$1 ORDER BY chunk_id`, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	vectors := map[string][]float32{}
	for rows.Next() {
		var id, body string
		if e = rows.Scan(&id, &body); e != nil {
			t.Fatal(e)
		}
		var v []float32
		if e = json.Unmarshal([]byte(body), &v); e != nil {
			t.Fatal(e)
		}
		vectors[id] = v
	}
	rows.Close()
	q := vectors[m.Documents[0].Chunk.ID]
	expected := []Candidate{}
	for _, doc := range m.Documents {
		v := vectors[doc.Chunk.ID]
		dot, nq, nv := 0.0, 0.0, 0.0
		for i, x := range v {
			dot += float64(x) * float64(q[i])
			nq += float64(q[i]) * float64(q[i])
			nv += float64(x) * float64(x)
		}
		expected = append(expected, Candidate{Document: doc, Distance: 1 - dot/math.Sqrt(nq*nv), Score: dot / math.Sqrt(nq*nv)})
	}
	sortCandidates(expected)
	exact, _, e := l.svc.DB.dense(ctx, "owner", a, m.Documents, q, len(m.Documents), false)
	if e != nil || len(exact) != len(expected) {
		t.Fatal(e)
	}
	for i, v := range exact {
		if v.Document.Chunk.ID != expected[i].Document.Chunk.ID || math.Abs(v.Distance-expected[i].Distance) > 1e-6 {
			t.Fatal("K independent cosine", i, v, expected[i])
		}
	}
	begin := time.Now()
	ann, fallback, e := l.svc.DB.dense(ctx, "owner", a, m.Documents, q, 5, true)
	if e != nil {
		t.Fatal(e)
	}
	want := map[string]bool{}
	for _, v := range exact[:5] {
		want[v.Document.Chunk.ID] = true
	}
	hits := 0
	for _, v := range ann {
		if want[v.Document.Chunk.ID] {
			hits++
		}
	}
	t.Logf("M actual ANN recall@5=%d/5 fallback=%v duration=%s", hits, fallback, time.Since(begin))
	allowed := []Document{m.Documents[len(m.Documents)-1]}
	filtered, fallback, e := l.svc.DB.dense(ctx, "owner", a, allowed, q, 5, true)
	if e != nil || len(filtered) != 1 || !fallback || filtered[0].Document.Chunk.ID != allowed[0].Chunk.ID {
		t.Fatal("N selective exact fallback", fallback, e)
	}
	// Demonstrate a real HNSW access path against the isolated index partition.
	tx, e := l.db.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SET LOCAL enable_seqscan=off; SET LOCAL enable_sort=off`); e != nil {
		t.Fatal(e)
	}
	plans, e := tx.Query(ctx, `EXPLAIN (COSTS OFF) SELECT chunk_id FROM `+pgx.Identifier{partition(a.ID)}.Sanitize()+` ORDER BY embedding OPERATOR(public.<=>) $1::public.vector LIMIT 5`, vectorText(q))
	if e != nil {
		t.Fatal(e)
	}
	plan := ""
	for plans.Next() {
		var line string
		if e = plans.Scan(&line); e != nil {
			t.Fatal(e)
		}
		plan += line + "\n"
	}
	plans.Close()
	if !strings.Contains(plan, "embedding_idx") {
		t.Fatal("no actual HNSW plan", plan)
	}
	t.Log("actual isolated HNSW Index Scan verified; vector literals omitted")
}
func TestS14RetrievalCrashHelper(t *testing.T) {
	if os.Getenv("S14_CRASH_CHILD") != "1" {
		return
	}
	ctx := context.Background()
	cfg, e := pgxpool.ParseConfig(os.Getenv("S14_CHILD_DB"))
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = os.Getenv("S14_CHILD_SCHEMA")
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	store, e := ingest.OpenS3(os.Getenv("REPO_TEST_S3_ENDPOINT"), os.Getenv("S14_CHILD_BUCKET"), os.Getenv("REPO_TEST_S3_ACCESS_KEY"), os.Getenv("REPO_TEST_S3_SECRET_KEY"), false)
	if e != nil {
		t.Fatal(e)
	}
	source := &analysis.Service{DB: analysis.Database{Pool: db}, Source: &ingest.Service{DB: ingest.Database{Pool: db}, Objects: store}}
	svc := &Service{DB: Database{Pool: db}, Source: source, Model: testModel(t), Lease: time.Second}
	svc.fault = func(stage string, n int) {
		if stage == "embedded" && n > 0 {
			os.Exit(37)
		}
	}
	p := DefaultPolicy()
	p.Batch = 2
	_, _ = svc.Build(ctx, "owner", os.Getenv("S14_CHILD_ANALYSIS"), p, false)
	t.Fatal("crash point not reached")
}
func TestS14CrashRecoveryConcurrencyAndFence(t *testing.T) {
	l := setup(t)
	ctx := context.Background()
	p := DefaultPolicy()
	p.Batch = 2
	r, e := l.svc.DB.request(ctx, "owner", l.index, p)
	if e != nil {
		t.Fatal(e)
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	child := exec.Command(exe, "-test.run=^TestS14RetrievalCrashHelper$", "-test.v")
	child.Env = append(os.Environ(), "S14_CRASH_CHILD=1", "S14_CHILD_DB="+l.url, "S14_CHILD_SCHEMA="+l.schema, "S14_CHILD_BUCKET="+l.bucket, "S14_CHILD_ANALYSIS="+l.index.ID)
	out, e := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.ExitCode() != 37 {
		t.Fatal("actual child death", e, string(out))
	}
	var old claim
	old.Index = r
	old.TTL = time.Second
	if e = l.db.QueryRow(ctx, `SELECT generation,claim_token::text FROM retrieval_indexes WHERE id=$1`, r.ID).Scan(&old.Index.Generation, &old.Token); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = l.svc.Ready(ctx, "owner", r.ID); !errors.Is(e, ErrBusy) {
		t.Fatal("partial READY", e)
	}
	time.Sleep(1100 * time.Millisecond)
	a, e := l.svc.Build(ctx, "owner", l.index.ID, p, true)
	if e != nil || a.Generation != 2 || a.Stats.Hits < 2 {
		t.Fatal("resumable real cache", a, e)
	}
	if e = l.svc.DB.renew(ctx, old); !errors.Is(e, ErrFence) {
		t.Fatal("stale renewal", e)
	}
	v := make([]float32, 384)
	v[0] = 1
	if e = l.svc.DB.cache(ctx, old, p, strings.Repeat("0", 64), v); !errors.Is(e, ErrFence) {
		t.Fatal("stale vector", e)
	}
	_, m, _, e := l.svc.Ready(ctx, "owner", a.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = l.svc.DB.publish(ctx, old, m, p, make([][]float32, len(m.Documents)), Stats{}); !errors.Is(e, ErrFence) {
		t.Fatal("stale publication", e)
	}
	// Hold one genuine builder after embedding; a second owner-authorized claimant
	// cannot compete. Existing verified vectors permit a fast deterministic retry.
	p.RRF++
	entered := make(chan struct{})
	release := make(chan struct{})
	once := atomic.Bool{}
	l.svc.fault = func(stage string, n int) {
		if stage == "embedded" && !once.Swap(true) {
			close(entered)
			<-release
		}
	}
	result := make(chan error, 1)
	go func() { _, e := l.svc.Build(ctx, "owner", l.index.ID, p, false); result <- e }()
	<-entered
	other := &Service{DB: l.svc.DB, Source: l.analysis, Model: l.svc.Model}
	if _, e = other.Build(ctx, "owner", l.index.ID, p, false); !errors.Is(e, ErrBusy) {
		t.Fatal("concurrent builder", e)
	}
	close(release)
	if e = <-result; e != nil {
		t.Fatal(e)
	}
}
func TestS14SourceAndVectorCorruptionTelemetry(t *testing.T) {
	l := setup(t)
	ctx := context.Background()
	p := DefaultPolicy()
	a := l.build(t, p)
	q := request(a, "hybrid", "renewExecution private-query-marker")
	original, e := l.svc.Search(ctx, "owner", q)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("TELEMETRY_ENABLED", "true")
	t.Setenv("TELEMETRY_ADDR", "127.0.0.1:0")
	t.Setenv("TELEMETRY_OTLP_URL", "http://127.0.0.1:1")
	runtime, e := telemetry.Start("retrieval-test")
	if e != nil {
		t.Fatal(e)
	}
	defer runtime.Close()
	observed, e := l.svc.Search(ctx, "owner", q)
	original.Seconds = 0
	observed.Seconds = 0
	if e != nil || !reflect.DeepEqual(original, observed) {
		t.Fatal("collector down changed rankings", e)
	}
	families, e := runtime.Registry.Gather()
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(families)
	for _, secret := range []string{"private-query-marker", l.repo.ID, a.ID, "ownership.go", "renewExecution"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("X private metric label", secret)
		}
	}
	file, e := l.source.ReadFile(ctx, "owner", l.snapshot.ID, "internal/storage/ownership.go")
	if e != nil {
		t.Fatal(e)
	}
	client, e := minio.New(os.Getenv("REPO_TEST_S3_ENDPOINT"), &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("REPO_TEST_S3_ACCESS_KEY"), os.Getenv("REPO_TEST_S3_SECRET_KEY"), ""), Secure: false})
	if e != nil {
		t.Fatal(e)
	}
	key := "repositories/" + l.repo.ID + "/blobs/" + file.Entry.SHA256
	put := func(body []byte) {
		_, e := client.PutObject(ctx, l.bucket, key, bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{DisableMultipart: true})
		if e != nil {
			t.Fatal(e)
		}
	}
	put([]byte("poison"))
	if _, e = l.svc.Search(ctx, "owner", q); !errors.Is(e, ingest.ErrIntegrity) {
		t.Fatal("corrupted source search", e)
	}
	changed := p
	changed.B = .61
	if _, e = l.svc.Build(ctx, "owner", l.index.ID, changed, false); !errors.Is(e, ingest.ErrIntegrity) {
		t.Fatal("corrupt source build", e)
	}
	put(file.Bytes)
	if _, e = l.svc.Search(ctx, "owner", q); e != nil {
		t.Fatal("restored source", e)
	}
	time.Sleep(1100 * time.Millisecond)
	if recovered, e := l.svc.Build(ctx, "owner", l.index.ID, changed, true); e != nil || recovered.State != "READY" {
		t.Fatal("recover source failure", e)
	}
	unavailable, e := ingest.OpenS3("127.0.0.1:1", l.bucket, os.Getenv("REPO_TEST_S3_ACCESS_KEY"), os.Getenv("REPO_TEST_S3_SECRET_KEY"), false)
	if e != nil {
		t.Fatal(e)
	}
	l.svc.Source = &analysis.Service{DB: l.analysis.DB, Source: &ingest.Service{DB: l.source.DB, Objects: unavailable}}
	short, stop := context.WithTimeout(ctx, 500*time.Millisecond)
	_, e = l.svc.Search(short, "owner", q)
	stop()
	if e == nil {
		t.Fatal("actual unreachable storage accepted")
	}
	l.svc.Source = l.analysis
	if _, e = l.svc.Search(ctx, "owner", q); e != nil {
		t.Fatal("outage recovery", e)
	}
	// Privileged backend corruption is detected by independent manifest hashes.
	table := pgx.Identifier{partition(a.ID)}.Sanitize()
	if _, e = l.db.Exec(ctx, "ALTER TABLE "+table+" DISABLE TRIGGER USER"); e != nil {
		t.Fatal(e)
	}
	if _, e = l.db.Exec(ctx, `UPDATE `+table+` SET embedding=$2::public.vector WHERE index_id=$1 AND chunk_id=(SELECT chunk_id FROM `+table+` LIMIT 1)`, a.ID, vectorText(append([]float32{1}, make([]float32, 383)...))); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = l.svc.Ready(ctx, "owner", a.ID); !errors.Is(e, ErrIntegrity) {
		t.Fatal("corrupted stored vector", e)
	}
}

func TestS14ActualRetainedSearchEvidence(t *testing.T) {
	ctx := context.Background()
	base := os.Getenv("S14_RETAINED_DATABASE_URL")
	index := "92609f260e21f8d08329ffbd8ee2d86635536abd266666d29fae036f22b5cc3c"
	owner := "aman-s12"
	bucket := os.Getenv("S13_RETAINED_BUCKET")
	var svc *Service
	if base == "" {
		l := setup(t)
		root := os.Getenv("REPO_REAL_PATH")
		if root == "" {
			t.Fatal("CI authorized exact-commit fixture path required")
		}
		trust := ingest.Trust{Roots: []string{root}}
		l.source.Trust = trust
		repo, e := l.source.DB.Register(ctx, "owner", "pinned-JudgeGrid", root, "", trust)
		if e != nil {
			t.Fatal(e)
		}
		source, e := l.source.Ingest(ctx, "owner", repo.ID, "56ea76e421eca19066362fdecedd4e9aae04521e", ingest.DefaultPolicy(), ingest.Options{})
		if e != nil {
			t.Fatal(e)
		}
		a, e := l.analysis.Analyze(ctx, "owner", source.ID, analysis.DefaultPolicy(), false)
		if e != nil {
			t.Fatal(e)
		}
		svc = l.svc
		index = a.ID
		owner = "owner"
		t.Log("Z CI reconstructs exact pinned commit in its own authorized fixture; not retained local data")
	} else {
		db, e := pgxpool.New(ctx, base)
		if e != nil {
			t.Fatal(e)
		}
		defer db.Close()
		if e = migrations.Apply(ctx, db); e != nil {
			t.Fatal(e)
		}
		store, e := ingest.OpenS3(os.Getenv("REPO_TEST_S3_ENDPOINT"), bucket, os.Getenv("REPO_TEST_S3_ACCESS_KEY"), os.Getenv("REPO_TEST_S3_SECRET_KEY"), false)
		if e != nil {
			t.Fatal(e)
		}
		source := &analysis.Service{DB: analysis.Database{Pool: db}, Source: &ingest.Service{DB: ingest.Database{Pool: db}, Objects: store}}
		svc = &Service{DB: Database{Pool: db}, Source: source, Model: testModel(t)}
		t.Log("Z actual retained READY analysis; no new ingestion")
	}
	a, e := svc.Build(ctx, owner, index, DefaultPolicy(), false)
	if e != nil {
		t.Fatal(e)
	}
	repeat, e := svc.Build(ctx, owner, index, DefaultPolicy(), false)
	if e != nil || repeat.Manifest != a.Manifest || !repeat.Replayed {
		t.Fatal("replay", e)
	}
	_, manifest, policy, e := svc.Ready(ctx, owner, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	queries := []string{"Repository.renewExecution", "where is the execution lease renewed?", "how are stale workers prevented from finalizing an old result?", "where does the compilation cache get populated?", "where is the outbox event published?"}
	type comparison struct {
		Query                    string
		BM25, Dense, Hybrid, ANN Results
		Warm                     map[string]Results
	}
	evidence := struct {
		Index, Repeat Index
		Policy        Policy
		Manifest      Manifest
		Queries       []comparison
	}{a, repeat, policy, manifest, nil}
	for _, query := range queries {
		var c comparison
		c.Query = query
		c.Warm = map[string]Results{}
		for _, mode := range []string{"bm25", "dense", "hybrid"} {
			q := request(a, mode, query)
			q.TopK = 5
			r, e := svc.Search(ctx, owner, q)
			if e != nil || len(r.Candidates) == 0 {
				t.Fatal(mode, e)
			}
			switch mode {
			case "bm25":
				c.BM25 = r
			case "dense":
				c.Dense = r
			case "hybrid":
				c.Hybrid = r
			}
		}
		q := request(a, "dense", query)
		q.ANN = true
		q.TopK = 5
		c.ANN, e = svc.Search(ctx, owner, q)
		if e != nil {
			t.Fatal(e)
		}
		for _, mode := range []string{"bm25", "dense", "hybrid"} {
			warm, e := svc.Search(ctx, owner, request(a, mode, query))
			if e != nil {
				t.Fatal("warm", mode, e)
			}
			c.Warm[mode] = warm
		}
		want := map[string]bool{}
		for _, v := range c.Dense.Candidates {
			want[v.Document.Chunk.ID] = true
		}
		hits := 0
		for _, v := range c.ANN.Candidates {
			if want[v.Document.Chunk.ID] {
				hits++
			}
		}
		t.Logf("Z query=%q first BM25=%s:%d dense=%s:%d hybrid=%s:%d ANN recall=%d/%d", query, c.BM25.Candidates[0].Document.Path, c.BM25.Candidates[0].Document.Chunk.Span.StartLine, c.Dense.Candidates[0].Document.Path, c.Dense.Candidates[0].Document.Chunk.Span.StartLine, c.Hybrid.Candidates[0].Document.Path, c.Hybrid.Candidates[0].Document.Chunk.Span.StartLine, hits, len(want))
		evidence.Queries = append(evidence.Queries, c)
	}
	if file := os.Getenv("S14_EVIDENCE_FILE"); file != "" {
		if e = os.WriteFile(file, canonical(evidence), 0600); e != nil {
			t.Fatal(e)
		}
	}
	// Exact identifier relevance is asserted independently of dense quality.
	found := false
	for _, v := range evidence.Queries[0].BM25.Candidates {
		if strings.Contains(v.Document.Symbol, "renewExecution") {
			found = true
		}
	}
	if !found {
		t.Fatal("known exact symbol missing")
	}
	if e = svc.Verify(ctx, owner, a.ID); e != nil {
		t.Fatal(e)
	}
}
