//go:build agentintegration && session9integration

package judge_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/agent"
	"github.com/Amankumarsingh23/JudgeGrid/internal/buildcache"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/objects"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/reconcile"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type stoppedUpload struct {
	objects.Store
	engine *execution.Docker
}

func (s stoppedUpload) Put(ctx context.Context, key string, b []byte) error {
	fmt.Printf("S9_CACHE_BOUNDARY %d\n", s.engine.Counts().Compilations)
	<-ctx.Done()
	return ctx.Err()
}
func TestS9CacheCrashHelper(t *testing.T) {
	if os.Getenv("JUDGEGRID_S9_CACHE_CRASH") != "1" {
		return
	}
	ctx := context.Background()
	cfg, e := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = os.Getenv("S9_SCHEMA")
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	root := os.Getenv("S9_ROOT")
	images, e := execution.LoadImages(filepath.Join(root, ".sandbox-images.local.json"))
	if e != nil {
		t.Fatal(e)
	}
	engine, e := execution.NewDocker(images, filepath.Join(root, "infra/sandbox/seccomp.json"), 1)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	s, e := objects.New(os.Getenv("TEST_S3_ENDPOINT"), os.Getenv("S9_CACHE_BUCKET"), os.Getenv("TEST_S3_ACCESS_KEY"), os.Getenv("TEST_S3_SECRET_KEY"), false)
	if e != nil {
		t.Fatal(e)
	}
	engine.SetCache(&buildcache.Cache{DB: db, Objects: stoppedUpload{s, engine}, Lease: 5 * time.Second})
	b, e := bundles.Open(os.Getenv("S9_BUNDLES"))
	if e != nil {
		t.Fatal(e)
	}
	var j queue.Job
	if e = json.Unmarshal([]byte(os.Getenv("S9_JOB")), &j); e != nil {
		t.Fatal(e)
	}
	h, e := agent.New(storage.NewRepository(db), b, engine, "agent-user", "s9-crashed-builder", autoOptions())
	if e != nil {
		t.Fatal(e)
	}
	h.Handle(ctx, j, "real-redis-reference")
}
func TestS9CrashBeforeCachePublication(t *testing.T) {
	f := auto(t)
	s, bucket := s9Store(t, "crash")
	j := f.submit(t, "cpp17", "#include <iostream>\nint main(){long long a,b;std::cin>>a>>b;std::cout<<a+b;}")
	jobJSON, _ := json.Marshal(j)
	ctx, cancel := context.WithTimeout(f.ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestS9CacheCrashHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "JUDGEGRID_S9_CACHE_CRASH=1", "S9_SCHEMA="+f.db.Config().ConnConfig.RuntimeParams["search_path"], "S9_ROOT="+f.root, "S9_BUNDLES="+f.dir, "S9_CACHE_BUCKET="+bucket, "S9_JOB="+string(jobJSON))
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	ready := make(chan string, 1)
	go func() {
		scan := bufio.NewScanner(pipe)
		for scan.Scan() {
			if strings.HasPrefix(scan.Text(), "S9_CACHE_BOUNDARY ") {
				ready <- scan.Text()
				return
			}
		}
		ready <- "missing"
	}()
	select {
	case line := <-ready:
		if line != "S9_CACHE_BOUNDARY 1" {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatal("compiler boundary", line)
		}
	case <-ctx.Done():
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("compile publication boundary timeout")
	}
	cmd.Process.Kill()
	cmd.Wait()
	var old buildcache.Claim
	var keyJSON []byte
	var state string
	if e = f.db.QueryRow(f.ctx, `SELECT cache_key,owner,generation,key_json,state FROM compilation_cache`).Scan(&old.Key, &old.Owner, &old.Generation, &keyJSON, &state); e != nil || state != "BUILDING" {
		t.Fatal("partial READY after process death", state, e)
	}
	waitAuto(t, 7*time.Second, func() bool {
		var expired bool
		// Cache construction and execution ownership have independent leases.
		// A live execution must not be stolen merely because its cache claim expired.
		f.db.QueryRow(f.ctx, `SELECT c.lease_expires_at<=clock_timestamp() AND NOT EXISTS(SELECT 1 FROM sandbox_authority a WHERE a.submission_id=$2 AND a.state='ACTIVE' AND a.lease_expires_at>clock_timestamp()) FROM compilation_cache c WHERE c.cache_key=$1`, old.Key, j.SubmissionID).Scan(&expired)
		return expired
	})
	if _, e = reconcile.Run(f.ctx, f.repo, reconcile.Docker{}, true, 128); e != nil {
		t.Fatal(e)
	}
	cache := &buildcache.Cache{DB: f.db, Objects: s}
	f.engine.SetCache(cache)
	h, e := agent.New(f.repo, f.b, f.engine, "agent-user", "replacement-builder", agent.DefaultOptions())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.Handle(f.ctx, j, "recovery"); e != nil {
		t.Fatal(e)
	}
	var sha string
	var generation int
	if e = f.db.QueryRow(f.ctx, `SELECT artifact_sha,generation,state FROM compilation_cache WHERE cache_key=$1`, old.Key).Scan(&sha, &generation, &state); e != nil || generation != 2 || state != "READY" {
		t.Fatal("cache takeover", generation, state, e)
	}
	data, e := s.Get(f.ctx, "artifacts/"+sha+".bin", objects.MaxBytes)
	if e != nil {
		t.Fatal(e)
	}
	if e = cache.Publish(f.ctx, old, data); e == nil {
		t.Fatal("stale builder published READY")
	}
	t.Log("I/N/Q: real compiler finished once before child kill; no partial READY; expired generation 2 rebuilt; stale publisher fenced; orphan resources reconciled")
}
func TestS9ObjectStoreOutageRecovery(t *testing.T) {
	f := auto(t)
	s, _ := s9Store(t, "outage")
	remote := &bundles.Remote{Objects: s}
	b, _ := bundles.Demo("two-sum")
	if _, e := remote.Put(b); e != nil {
		t.Fatal(e)
	}
	j := f.submit(t, "python3", "a,b=map(int,input().split());print(a+b)")
	name := os.Getenv("TEST_MINIO_CONTAINER")
	if name == "" {
		t.Fatal("owned MinIO identity required")
	}
	defer func() { exec.Command("docker", "start", name).Run() }()
	if out, e := exec.Command("docker", "stop", name).CombinedOutput(); e != nil {
		t.Fatal(string(out), e)
	}
	h, e := agent.New(f.repo, remote, f.engine, "agent-user", "outage-owner", agent.DefaultOptions())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.Handle(f.ctx, j, "during-outage"); e == nil {
		t.Fatal("outage manufactured completion")
	}
	var state string
	var verdict *string
	if e = f.db.QueryRow(f.ctx, `SELECT status,verdict FROM submissions WHERE id=$1`, j.SubmissionID).Scan(&state, &verdict); e != nil || state == "COMPLETED" || verdict != nil {
		t.Fatal("false verdict during storage outage", state, e)
	}
	s9Restart(t)
	time.Sleep(1100 * time.Millisecond)
	if _, e = h.Handle(f.ctx, j, "after-storage-recovery"); e != nil {
		t.Fatal(e)
	}
	result, e := f.repo.Get(f.ctx, "agent-user", j.SubmissionID)
	if e != nil || result.Verdict == nil || *result.Verdict != "AC" {
		t.Fatal("storage recovery failed", e)
	}
	t.Log("O: actual MinIO stop caused retryable infrastructure failure with null verdict; restart recovered real Python AC")
}

func TestS9CacheStoreOutageRecovery(t *testing.T) {
	f := auto(t)
	s, _ := s9Store(t, "cache-outage")
	cache := &buildcache.Cache{DB: f.db, Objects: s}
	f.engine.SetCache(cache)
	j := f.submit(t, "cpp17", "#include <iostream>\nint main(){long long a,b;std::cin>>a>>b;std::cout<<a+b;}")
	name := os.Getenv("TEST_MINIO_CONTAINER")
	if name == "" {
		t.Fatal("owned MinIO identity required")
	}
	defer func() { exec.Command("docker", "start", name).Run() }()
	if out, e := exec.Command("docker", "stop", name).CombinedOutput(); e != nil {
		t.Fatal(string(out), e)
	}
	h, e := agent.New(f.repo, f.b, f.engine, "agent-user", "cache-outage", agent.DefaultOptions())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.Handle(f.ctx, j, "cache-outage"); e == nil {
		t.Fatal("cache outage manufactured completion")
	}
	var verdict *string
	var ready int
	if e = f.db.QueryRow(f.ctx, `SELECT verdict FROM submissions WHERE id=$1`, j.SubmissionID).Scan(&verdict); e != nil || verdict != nil {
		t.Fatal("false verdict", e)
	}
	if e = f.db.QueryRow(f.ctx, `SELECT count(*) FROM compilation_cache WHERE state='READY'`).Scan(&ready); e != nil || ready != 0 || f.engine.Counts().RuntimeStarts != 0 {
		t.Fatal("partial publication/runtime after outage", e)
	}
	s9Restart(t)
	time.Sleep(1100 * time.Millisecond)
	if _, e = h.Handle(f.ctx, j, "cache-storage-restored"); e != nil {
		t.Fatal(e)
	}
	result, e := f.repo.Get(f.ctx, "agent-user", j.SubmissionID)
	if e != nil || result.Verdict == nil || *result.Verdict != "AC" {
		t.Fatal(e)
	}
	if f.engine.Counts().Compilations != 2 {
		t.Fatal("actual rebuild count", f.engine.Counts())
	}
	t.Log("O cache: unavailable MinIO prevented READY and runtime, null verdict retained; restart safely rebuilt, two actual compiler invocations")
}
