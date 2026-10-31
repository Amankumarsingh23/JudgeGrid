//go:build integration

package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type lab struct {
	svc    Service
	store  *S3Store
	f      *fixture
	repo   Repository
	url    string
	schema string
}

func setupLab(t *testing.T) *lab {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" || os.Getenv("REPO_TEST_S3_ENDPOINT") == "" {
		t.Fatal("explicit PostgreSQL and separate repository S3 test environment required; no skips")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, base)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("judgegrid_ingest_%d", time.Now().UnixNano())
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
	store, e := OpenS3(os.Getenv("REPO_TEST_S3_ENDPOINT"), fmt.Sprintf("jg12-%d", time.Now().UnixNano()), os.Getenv("REPO_TEST_S3_ACCESS_KEY"), os.Getenv("REPO_TEST_S3_SECRET_KEY"), false)
	if e != nil {
		t.Fatal(e)
	}
	if e = store.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	f := newFixture(t, "sha1")
	f.write("main.go", "package main\n")
	f.write("one.py", "print(1)\n")
	f.write("two.js", "console.log(2)\n")
	f.write("three.cpp", "int main(){return 0;}\n")
	f.write("README.md", "fixture documentation\n")
	f.commit()
	trust := Trust{Roots: []string{filepath.Dir(f.dir)}}
	svc := Service{DB: Database{db}, Objects: store, Trust: trust}
	repo, e := svc.DB.Register(ctx, "owner", "fixture", f.dir, "", trust)
	if e != nil {
		t.Fatal(e)
	}
	// The DSN passed to owned crash helpers retains this isolated schema.
	u := cfg.ConnString()
	return &lab{svc, store, f, repo, u, schema}
}
func (l *lab) ingest(t *testing.T, ref string, p Policy, opts Options) Snapshot {
	t.Helper()
	s, e := l.svc.Ingest(context.Background(), "owner", l.repo.ID, ref, p, opts)
	if e != nil {
		t.Fatal(e)
	}
	if s.State != "READY" {
		t.Fatal(s.State)
	}
	return s
}
func mustConstraint(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	_, e := db.Exec(context.Background(), sql, args...)
	var pg *pgconn.PgError
	if !errors.As(e, &pg) || pg.Code != "P0001" {
		t.Fatal("immutability guard missing", e)
	}
}
func waitFor(t *testing.T, max time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("bounded fixture wait expired")
}

func TestS12DeterministicIncrementalOwnerAndPolicy(t *testing.T) {
	l := setupLab(t)
	p := DefaultPolicy()
	a := l.f.git(nil, "rev-parse", "HEAD")
	first := l.ingest(t, a, p, Options{})
	second := l.ingest(t, a, p, Options{})
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}
	anonymous, e := client.Get("http://" + os.Getenv("REPO_TEST_S3_ENDPOINT") + "/" + l.store.bucket + "/" + blobKey(l.repo.ID, Digest([]byte("package main\n"))))
	if e != nil {
		t.Fatal(e)
	}
	anonymous.Body.Close()
	client.CloseIdleConnections()
	if anonymous.StatusCode != http.StatusForbidden {
		t.Fatal("private source object allowed anonymous access", anonymous.StatusCode)
	}
	if first.ID != second.ID || first.Manifest != second.Manifest || !second.Replayed || first.Stats.Fetched != 5 {
		t.Fatal("A deterministic identity", first, second)
	}
	if second.Attempt == nil || second.Attempt.Fetched != 0 || second.Attempt.Reused != 5 {
		t.Fatal("A replay attempt confused with original publication stats")
	}
	l.f.write("one.py", "print(10)\n")
	l.f.write("two.js", "console.log(20)\n")
	l.f.write("three.cpp", "int main(){return 3;}\n")
	b := l.f.commit()
	next := l.ingest(t, b, p, Options{})
	if next.Stats.Fetched != 3 || next.Stats.Reused != 2 || next.Stats.Included != 5 {
		t.Fatal("C complete incremental inventory", next.Stats)
	}
	l.f.git(nil, "mv", "main.go", "renamed.go")
	l.f.git(nil, "update-index", "--chmod=+x", "renamed.go")
	mode := l.f.git(nil, "write-tree")
	c := l.f.git([]byte("mode/rename"), "commit-tree", mode, "-p", b)
	renamed := l.ingest(t, c, p, Options{})
	if renamed.Stats.Fetched != 0 || renamed.Stats.Reused != 5 {
		t.Fatal("D mode/rename did not reuse verified content", renamed.Stats)
	}
	file, e := l.svc.ReadFile(context.Background(), "owner", renamed.ID, "renamed.go")
	if e != nil || file.Entry.Mode != "100755" || string(file.Bytes) != "package main\n" {
		t.Fatal("D provenance", e)
	}
	changes, e := l.svc.Compare(context.Background(), "owner", next.ID, renamed.ID)
	if e != nil || len(changes) != 2 {
		t.Fatal("D compare", changes, e)
	}
	p.Docs = false
	other := l.ingest(t, a, p, Options{})
	if other.ID == first.ID || other.Policy == first.Policy || other.Stats.Included != 4 {
		t.Fatal("P policy collision")
	}
	same, e := l.svc.DB.Register(context.Background(), "owner", "fixture", l.f.dir, "", l.svc.Trust)
	if e != nil || same.ID != l.repo.ID {
		t.Fatal("P duplicate registration", e)
	}
	if _, e = l.svc.DB.Register(context.Background(), "owner", "fixture", filepath.Dir(l.f.dir), "", l.svc.Trust); !errors.Is(e, ErrDenied) {
		t.Fatal("P rebound identity", e)
	}
	spy := &spyStore{BlobStore: l.store}
	foreign := l.svc
	foreign.Objects = spy
	if _, _, e = foreign.Ready(context.Background(), "foreign", first.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("O foreign metadata", e)
	}
	if _, e = foreign.ReadFile(context.Background(), "foreign", first.ID, "main.go"); !errors.Is(e, ErrDenied) {
		t.Fatal("O foreign bytes", e)
	}
	if _, e = foreign.Audit(context.Background(), "foreign", l.repo.ID, 100); !errors.Is(e, ErrDenied) {
		t.Fatal("O foreign inventory", e)
	}
	if spy.reads.Load() != 0 {
		t.Fatal("O touched storage before authorization")
	}
	// Same raw contents under another owner/repository never reuse the first scope's catalog.
	peer, e := l.svc.DB.Register(context.Background(), "peer", "fixture", l.f.dir, "", l.svc.Trust)
	if e != nil {
		t.Fatal(e)
	}
	peerSnap, e := l.svc.Ingest(context.Background(), "peer", peer.ID, a, DefaultPolicy(), Options{})
	if e != nil || peerSnap.Stats.Fetched != 5 || peerSnap.Stats.Reused != 0 {
		t.Fatal("O cross-tenant reuse shortcut", peerSnap, e)
	}
	mustConstraint(t, l.svc.DB.Pool, `UPDATE source_snapshots SET state='FAILED',manifest_sha256=NULL WHERE id=$1`, first.ID)
	mustConstraint(t, l.svc.DB.Pool, `UPDATE source_files SET language='python' WHERE snapshot_id=$1`, first.ID)
	mustConstraint(t, l.svc.DB.Pool, `UPDATE ingestion_policies SET body='{}' WHERE id=$1`, first.Policy)
	t.Logf("A/C/D/O/P: stable snapshot=%s manifest=%s; B fetched=%d reused=%d fetchedBytes=%d reusedBytes=%d; mode/rename fetched=%d reused=%d; policy identity separated", first.ID, first.Manifest, next.Stats.Fetched, next.Stats.Reused, next.Stats.FetchedBytes, next.Stats.ReusedBytes, renamed.Stats.Fetched, renamed.Stats.Reused)
}

type spyStore struct {
	BlobStore
	reads atomic.Int64
}

func (s *spyStore) Get(ctx context.Context, k string, n int64) ([]byte, error) {
	s.reads.Add(1)
	return s.BlobStore.Get(ctx, k, n)
}

func TestS12MovingBranchAndBoundedSelection(t *testing.T) {
	l := setupLab(t)
	a := l.f.git(nil, "rev-parse", "HEAD")
	l.svc.fault = func(stage string, n int) {
		if stage == "resolved" {
			l.f.write("new.go", "package new\n")
			l.f.commit()
		}
	}
	pinned := l.ingest(t, "refs/heads/main", DefaultPolicy(), Options{})
	if pinned.Commit != a || pinned.Stats.Included != 5 {
		t.Fatal("B branch movement affected pinned tree")
	}
	l.svc.fault = nil
	symlink := l.f.git([]byte("../../private-host-file"), "hash-object", "-w", "--stdin")
	l.f.git(nil, "update-index", "--add", "--cacheinfo", "120000,"+symlink+",escape.go")
	l.f.git(nil, "update-index", "--add", "--cacheinfo", "160000,"+a+",external")
	l.f.write("pointer.go", "version https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("a", 64)+"\nsize 9999999\n")
	l.f.write(".env", "only harmless synthetic fixture material\n")
	l.f.write("binary.go", "binary\x00bytes")
	l.f.write("invalid.py", string([]byte{255}))
	l.f.write("generated.go", "// Code generated by fixture. DO NOT EDIT.\npackage generated\n")
	l.f.write("x.min.js", "unreviewed minified fixture")
	l.f.write("vendor/dep.go", "package dependency\n")
	l.f.write("private.go", "-----BEGIN PRIVATE KEY-----\nsynthetic fixture\n")
	l.f.write("large.go", strings.Repeat("x", 2048))
	l.f.git(nil, "add", ".")
	l.f.git(nil, "update-index", "--add", "--cacheinfo", "120000,"+symlink+",escape.go")
	l.f.git(nil, "update-index", "--add", "--cacheinfo", "160000,"+a+",external")
	l.f.git(nil, "commit", "-m", "selection")
	p := DefaultPolicy()
	p.MaxFileBytes = 1024
	s := l.ingest(t, "HEAD", p, Options{})
	_, manifest, e := l.svc.Ready(context.Background(), "owner", s.ID)
	if e != nil {
		t.Fatal(e)
	}
	want := map[string]string{"escape.go": "symlink", "external": "gitlink", "pointer.go": "lfs_pointer", ".env": "sensitive_path", "binary.go": "binary", "invalid.py": "encoding", "generated.go": "generated_minified", "x.min.js": "generated_minified", "vendor/dep.go": "dependency_build", "private.go": "secret_evidence", "large.go": "oversize"}
	for _, f := range manifest.Entries {
		if reason, ok := want[f.Path]; ok {
			if f.Reason != reason {
				t.Fatal(f.Path, f.Reason, reason)
			}
			delete(want, f.Path)
		}
	}
	if len(want) != 0 {
		t.Fatal("J/K/L/M missing inventory", want)
	}
	if _, e = l.svc.ReadFile(context.Background(), "owner", s.ID, "escape.go"); !errors.Is(e, ErrDenied) {
		t.Fatal("J symlink retrieval", e)
	}
	p = DefaultPolicy()
	p.MaxEntries = 2
	if _, e = l.svc.Ingest(context.Background(), "owner", l.repo.ID, "HEAD", p, Options{}); !errors.Is(e, ErrBudget) {
		t.Fatal("Q entry budget", e)
	}
	p = DefaultPolicy()
	p.MaxFiles = 1
	if _, e = l.svc.Ingest(context.Background(), "owner", l.repo.ID, "HEAD", p, Options{}); !errors.Is(e, ErrBudget) {
		t.Fatal("Q candidate-file cap", e)
	}
	p = DefaultPolicy()
	p.MaxTotalBytes = 5
	if _, e = l.svc.Ingest(context.Background(), "owner", l.repo.ID, "HEAD", p, Options{}); !errors.Is(e, ErrBudget) {
		t.Fatal("Q byte budget", e)
	}
	var ready int
	l.svc.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM source_snapshots WHERE policy_id=$1 AND state='READY'`, p.ID()).Scan(&ready)
	if ready != 0 {
		t.Fatal("Q partial READY")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = l.svc.Ingest(ctx, "owner", l.repo.ID, "HEAD", DefaultPolicy(), Options{}); e == nil {
		t.Fatal("Q canceled ingestion accepted")
	}
	t.Logf("B/J/K/L/M/Q: pinned original=%s; selection reasons=%s; budget failures had no READY snapshot", a, canonical(s.Stats.Reasons))
}

func TestS12ConcurrentAndStalePublication(t *testing.T) {
	l := setupLab(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	l.svc.fault = func(stage string, n int) {
		if stage == "claimed" {
			once.Do(func() { close(entered); <-release })
		}
	}
	done := make(chan error, 1)
	go func() {
		_, e := l.svc.Ingest(context.Background(), "owner", l.repo.ID, "HEAD", DefaultPolicy(), Options{})
		done <- e
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("F first claim not acquired")
	}
	_, e := l.svc.Ingest(context.Background(), "owner", l.repo.ID, "HEAD", DefaultPolicy(), Options{})
	close(release)
	if !errors.Is(e, ErrBusy) {
		t.Fatal("F concurrent claim admitted", e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	l.svc.fault = nil
	var count int
	l.svc.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM source_snapshots WHERE state='READY'`).Scan(&count)
	if count != 1 {
		t.Fatal("F multiple publications")
	}
	g := l.f.open(t)
	commit, tree, e := g.Resolve("HEAD")
	if e != nil {
		t.Fatal(e)
	}
	p := DefaultPolicy()
	p.Docs = false
	s, _, e := l.svc.DB.Request(context.Background(), l.repo, g.Format, commit, tree, p)
	if e != nil {
		t.Fatal(e)
	}
	old, e := l.svc.DB.Claim(context.Background(), "owner", s.ID, 200*time.Millisecond, false)
	if e != nil {
		t.Fatal(e)
	}
	forged := old
	forged.Snapshot.Repository = "11111111-1111-4111-8111-111111111111"
	if e = l.svc.DB.RecordBlob(context.Background(), forged, commit, Digest([]byte("forged")), 6); !errors.Is(e, ErrFence) {
		t.Fatal("G claim crossed repository provenance", e)
	}
	if e = l.svc.DB.Verifying(context.Background(), old); e != nil {
		t.Fatal(e)
	}
	time.Sleep(250 * time.Millisecond)
	newClaim, e := l.svc.DB.Claim(context.Background(), "owner", s.ID, time.Second, true)
	if e != nil {
		t.Fatal(e)
	}
	m := Manifest{1, l.repo.ID, s.ID, g.Format, commit, tree, p.ID(), []Entry{}}
	if e = l.svc.DB.Publish(context.Background(), old, m, Stats{}); !errors.Is(e, ErrFence) {
		t.Fatal("G stale generation published", e)
	}
	if e = l.svc.DB.Renew(context.Background(), old); !errors.Is(e, ErrFence) {
		t.Fatal("G stale heartbeat revived", e)
	}
	if e = l.svc.DB.Fail(context.Background(), newClaim, "canceled"); e != nil {
		t.Fatal(e)
	}
	t.Log("F/G: exactly one READY publication, active second claim refused, expired generation could neither renew nor publish")
}

type crashConfig struct{ URL, Endpoint, Bucket, Access, Secret, Path, Repository, Schema string }

func TestS12CrashHelper(t *testing.T) {
	raw := os.Getenv("JUDGEGRID_S12_CRASH_HELPER")
	if raw == "" {
		return
	}
	var c crashConfig
	if json.Unmarshal([]byte(raw), &c) != nil {
		os.Exit(92)
	}
	cfg, e := pgxpool.ParseConfig(c.URL)
	if e != nil || !strings.HasPrefix(c.Schema, "judgegrid_ingest_") {
		os.Exit(92)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = c.Schema
	db, e := pgxpool.NewWithConfig(context.Background(), cfg)
	if e != nil {
		os.Exit(92)
	}
	store, e := OpenS3(c.Endpoint, c.Bucket, c.Access, c.Secret, false)
	if e != nil {
		os.Exit(92)
	}
	svc := Service{DB: Database{db}, Objects: store, Trust: Trust{Roots: []string{c.Path}}, Lease: 300 * time.Millisecond, fault: func(stage string, n int) {
		if stage == "uploaded" {
			os.Exit(91)
		}
	}}
	if _, e = svc.Ingest(context.Background(), "owner", c.Repository, "HEAD", DefaultPolicy(), Options{}); e != nil {
		os.Exit(93)
	}
	os.Exit(94)
}
func TestS12ProcessCrashResumeAndUnknownObjects(t *testing.T) {
	l := setupLab(t)
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	c := crashConfig{l.url, os.Getenv("REPO_TEST_S3_ENDPOINT"), l.store.bucket, os.Getenv("REPO_TEST_S3_ACCESS_KEY"), os.Getenv("REPO_TEST_S3_SECRET_KEY"), l.f.dir, l.repo.ID, l.schema}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, exe, "-test.run=^TestS12CrashHelper$")
	child.Env = append(os.Environ(), "JUDGEGRID_S12_CRASH_HELPER="+string(canonical(c)))
	child.Stdout = io.Discard
	child.Stderr = io.Discard
	e = child.Run()
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.ExitCode() != 91 {
		t.Fatal("E child did not reach owned upload boundary", e)
	}
	var id, state string
	var available bool
	e = l.svc.DB.Pool.QueryRow(context.Background(), `SELECT id,state,lease_expires_at<=clock_timestamp() FROM source_snapshots WHERE repository_id=$1`, l.repo.ID).Scan(&id, &state, &available)
	if e != nil || state == "READY" {
		t.Fatal("E partial publication", e)
	}
	waitFor(t, 3*time.Second, func() bool {
		l.svc.DB.Pool.QueryRow(context.Background(), `SELECT lease_expires_at<=clock_timestamp() FROM source_snapshots WHERE id=$1`, id).Scan(&available)
		return available
	})
	resumed := l.ingest(t, "HEAD", DefaultPolicy(), Options{Retry: true})
	if resumed.Generation != 2 || resumed.Stats.Reused < 1 || resumed.Stats.Fetched != 4 {
		t.Fatal("E resume did not reuse durable verified content", resumed)
	}
	unknown := blobKey(l.repo.ID, Digest([]byte("unreferenced fixture")))
	if e = l.store.Put(context.Background(), unknown, []byte("unreferenced fixture")); e != nil {
		t.Fatal(e)
	}
	audit, e := l.svc.Audit(context.Background(), "owner", l.repo.ID, 100)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, a := range audit {
		if a.Key == unknown {
			found = a.Decision == "unknown-retain"
		}
	}
	if !found {
		t.Fatal("R unknown object not preserved")
	}
	if _, e = l.store.Get(context.Background(), unknown, 100); e != nil {
		t.Fatal(e)
	}
	t.Logf("E/R: actual child exited after upload; generation=%d resumed with fetched=%d reused=%d; unknown object retained", resumed.Generation, resumed.Stats.Fetched, resumed.Stats.Reused)
}

type gate struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	online bool
	conns  map[net.Conn]bool
	wg     sync.WaitGroup
}

func newGate(t *testing.T, target string) *gate {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	g := &gate{ln: ln, target: target, online: true, conns: map[net.Conn]bool{}}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			g.mu.Lock()
			if !g.online {
				g.mu.Unlock()
				c.Close()
				continue
			}
			g.conns[c] = true
			g.mu.Unlock()
			g.wg.Add(1)
			go func() {
				defer g.wg.Done()
				defer c.Close()
				defer func() { g.mu.Lock(); delete(g.conns, c); g.mu.Unlock() }()
				up, e := net.DialTimeout("tcp", target, time.Second)
				if e != nil {
					return
				}
				defer up.Close()
				done := make(chan struct{})
				go func() { io.Copy(up, c); up.Close(); close(done) }()
				io.Copy(c, up)
				c.Close()
				<-done
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); g.set(false); g.wg.Wait() })
	return g
}
func (g *gate) set(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.online = on
	if !on {
		for c := range g.conns {
			c.Close()
		}
	}
}

func TestS12StorageOutageCorruptionAndTelemetry(t *testing.T) {
	l := setupLab(t)
	g := newGate(t, os.Getenv("REPO_TEST_S3_ENDPOINT"))
	gated, e := OpenS3(g.ln.Addr().String(), l.store.bucket, os.Getenv("REPO_TEST_S3_ACCESS_KEY"), os.Getenv("REPO_TEST_S3_SECRET_KEY"), false)
	if e != nil {
		t.Fatal(e)
	}
	l.svc.Objects = gated
	g.set(false)
	t.Setenv("APP_ENV", "development")
	t.Setenv("TELEMETRY_ENABLED", "true")
	t.Setenv("TELEMETRY_ADDR", "127.0.0.1:0")
	t.Setenv("TELEMETRY_OTLP_URL", "http://127.0.0.1:1")
	restore := telemetry.Install(nil)
	defer restore()
	runtime, e := telemetry.Start("repository-ingest")
	if e != nil {
		t.Fatal(e)
	}
	defer runtime.Close()
	if _, e = l.svc.Ingest(context.Background(), "owner", l.repo.ID, "HEAD", DefaultPolicy(), Options{}); !errors.Is(e, ErrStorage) {
		t.Fatal("H unavailable store accepted", e)
	}
	var ready int
	l.svc.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM source_snapshots WHERE state='READY'`).Scan(&ready)
	if ready != 0 {
		t.Fatal("H false READY")
	}
	g.set(true)
	time.Sleep(1100 * time.Millisecond)
	recovered := l.ingest(t, "HEAD", DefaultPolicy(), Options{Retry: true})
	if recovered.Generation != 2 {
		t.Fatal("H retry generation", recovered.Generation)
	}
	// Use a new policy identity and corrupt one uploaded fixture exactly before verification.
	p := DefaultPolicy()
	p.Docs = false
	original := []byte("package main\n")
	key := blobKey(l.repo.ID, Digest(original))
	l.svc.fault = func(stage string, n int) {
		if stage == "verifying" {
			_, e := l.store.client.PutObject(context.Background(), l.store.bucket, key, strings.NewReader("poison"), 6, minio.PutObjectOptions{DisableMultipart: true})
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e = l.svc.Ingest(context.Background(), "owner", l.repo.ID, "HEAD", p, Options{}); !errors.Is(e, ErrIntegrity) {
		t.Fatal("I corrupt readback accepted", e)
	}
	l.svc.fault = nil
	l.svc.DB.Pool.QueryRow(context.Background(), `SELECT count(*) FROM source_snapshots WHERE policy_id=$1 AND state='READY'`, p.ID()).Scan(&ready)
	if ready != 0 {
		t.Fatal("I false READY")
	}
	_, e = l.store.client.PutObject(context.Background(), l.store.bucket, key, bytes.NewReader(original), int64(len(original)), minio.PutObjectOptions{DisableMultipart: true})
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(1100 * time.Millisecond)
	l.ingest(t, "HEAD", p, Options{Retry: true})
	w := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	text := w.Body.String()
	for _, want := range []string{`operation="ingest.completed"`, `operation="ingest.failed"`, `judgegrid_ingestion_bytes_total`, `operation="exporter.export",outcome="error"`} {
		if !strings.Contains(text, want) {
			t.Fatal("missing bounded telemetry", want)
		}
	}
	if strings.Contains(text, l.repo.ID) || strings.Contains(text, l.f.dir) || strings.Contains(text, "main.go") {
		t.Fatal("private telemetry label leaked")
	}
	t.Log("H/I/telemetry: isolated S3 gate outage and poisoned current object refused READY; restored bytes recovered; absent exporter did not block publication")
}

func TestS12RealJudgeGridBaseline(t *testing.T) {
	l := setupLab(t)
	root := os.Getenv("REPO_REAL_PATH")
	if root == "" {
		t.Fatal("T requires explicit authorized JudgeGrid root; no skips")
	}
	l.svc.Trust = Trust{Roots: []string{root}}
	r, e := l.svc.DB.Register(context.Background(), "owner", "JudgeGrid-baseline", root, "", l.svc.Trust)
	if e != nil {
		t.Fatal(e)
	}
	p := DefaultPolicy()
	p.MaxSeconds = 300
	baseline := "56ea76e421eca19066362fdecedd4e9aae04521e"
	start := time.Now()
	first, e := l.svc.Ingest(context.Background(), "owner", r.ID, baseline, p, Options{})
	initial := time.Since(start)
	if e != nil {
		t.Fatal(e)
	}
	start = time.Now()
	repeat, e := l.svc.Ingest(context.Background(), "owner", r.ID, baseline, p, Options{})
	repeated := time.Since(start)
	if e != nil || repeat.ID != first.ID || repeat.Manifest != first.Manifest || !repeat.Replayed {
		t.Fatal("T reproducibility", e)
	}
	_, m, e := l.svc.Ready(context.Background(), "owner", first.ID)
	if e != nil {
		t.Fatal(e)
	}
	if first.Commit != baseline || first.Stats.Included < 100 || len(m.Entries) != first.Stats.Discovered {
		t.Fatal("T actual inventory incomplete", first)
	}
	t.Logf("T REAL: snapshot=%s manifest=%s commit=%s tree=%s entries=%d included=%d excluded=%d bytes=%d fetched=%d fetchedBytes=%d reasons=%s initial=%s repeatVerified=%s sourceSeconds=%.6f storageSeconds=%.6f dbSeconds=%.6f", first.ID, first.Manifest, first.Commit, first.Tree, first.Stats.Discovered, first.Stats.Included, first.Stats.Excluded, first.Stats.Bytes, first.Stats.Fetched, first.Stats.FetchedBytes, canonical(first.Stats.Reasons), initial, repeated, first.Stats.SourceSeconds, first.Stats.StorageSeconds, first.Stats.DBSeconds)
}
