//go:build agentintegration && session9integration

package judge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/agent"
	"github.com/Amankumarsingh23/JudgeGrid/internal/buildcache"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/objects"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func s9Store(t *testing.T, prefix string) (*objects.S3, string) {
	t.Helper()
	if os.Getenv("TEST_S3_ENDPOINT") == "" {
		t.Fatal("explicit real MinIO test environment required; no skips")
	}
	bucket := fmt.Sprintf("jg9-%s-%d", prefix, time.Now().UnixNano())
	s, e := objects.New(os.Getenv("TEST_S3_ENDPOINT"), bucket, os.Getenv("TEST_S3_ACCESS_KEY"), os.Getenv("TEST_S3_SECRET_KEY"), false)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Initialize(context.Background()); e != nil {
		t.Fatal(e)
	}
	return s, bucket
}
func s9Raw(t *testing.T) *minio.Client {
	t.Helper()
	c, e := minio.New(os.Getenv("TEST_S3_ENDPOINT"), &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("TEST_S3_ACCESS_KEY"), os.Getenv("TEST_S3_SECRET_KEY"), ""), Secure: false})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func s9Write(t *testing.T, bucket, key string, b []byte) {
	t.Helper()
	if _, e := s9Raw(t).PutObject(context.Background(), bucket, key, bytes.NewReader(b), int64(len(b)), minio.PutObjectOptions{DisableMultipart: true}); e != nil {
		t.Fatal(e)
	}
}

type s9UncertainPublisher struct{ s *queue.Streams }

func (p s9UncertainPublisher) Publish(ctx context.Context, j queue.Job) (string, error) {
	_, e := p.s.Publish(ctx, j)
	if e != nil {
		return "", e
	}
	return "", errors.New("injected lost XADD reply")
}
func s9Restart(t *testing.T) {
	t.Helper()
	name := os.Getenv("TEST_MINIO_CONTAINER")
	if name == "" {
		t.Fatal("requires explicit owned MinIO container identity")
	}
	if out, e := exec.Command("docker", "restart", name).CombinedOutput(); e != nil {
		t.Fatal(string(out), e)
	}
	waitAuto(t, 30*time.Second, func() bool {
		r, e := http.Get("http://" + os.Getenv("TEST_S3_ENDPOINT") + "/minio/health/live")
		if e != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	})
}
func TestS9PrivateImmutableObjectStorage(t *testing.T) {
	s, bucket := s9Store(t, "bundles")
	remote := &bundles.Remote{Objects: s}
	b, _ := bundles.Demo("two-sum")
	ref, e := remote.Put(b)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = remote.Put(b); e != nil {
		t.Fatal("conditional duplicate", e)
	}
	got, e := remote.Load(ref.Hash)
	if e != nil || got.Verify(ref) != nil {
		t.Fatal(e)
	}
	response, e := http.Get("http://" + os.Getenv("TEST_S3_ENDPOINT") + "/" + bucket + "/bundles/" + ref.Hash + ".json")
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("private object anonymously readable", response.StatusCode)
	}
	if _, e = remote.Load(strings.Repeat("f", 64)); e == nil {
		t.Fatal("missing object accepted")
	}
	s9Write(t, bucket, "bundles/"+ref.Hash+".json", []byte("corrupt"))
	if _, e = remote.Load(ref.Hash); e == nil {
		t.Fatal("wrong digest accepted")
	}
	canonical, _ := json.Marshal(b)
	s9Write(t, bucket, "bundles/"+ref.Hash+".json", canonical)
	s9Restart(t)
	if got, e = remote.Load(ref.Hash); e != nil || got.Verify(ref) != nil {
		t.Fatal("restart lost immutable data", e)
	}
	t.Log("A/B/C/P: conditional publish, digest verification, missing/corrupt rejection, anonymous 403 and persistent restart observed")
}
func TestS9RealCacheConcurrencyAndPoisoning(t *testing.T) {
	f := auto(t)
	s, bucket := s9Store(t, "cache")
	cache := &buildcache.Cache{DB: f.db, Objects: s}
	f.engine.SetCache(cache)
	r, _ := execution.Fixture("cpp-hello")
	r.Source = append([]byte("// session9 concurrent cache\n"), r.Source...)
	r.Source = []byte(strings.Replace(string(r.Source), "int main(){", "int main(){std::ifstream source(\"/work/main.cpp\");std::string first;std::getline(source,first);if(first!=\"// session9 concurrent cache\")return 19;", 1))
	r.Source = append([]byte("// session9 concurrent cache\n#include <fstream>\n"), r.Source[len("// session9 concurrent cache\n"):]...)
	before := f.engine.Counts()
	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(f.ctx, 90*time.Second)
			defer cancel()
			v, e := f.engine.Execute(ctx, r)
			if e == nil && (v.Run == nil || v.Run.Outcome != execution.Exited || !v.Cleaned) {
				e = fmt.Errorf("cache runtime/cleanup failed")
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	counts := cache.Counts()
	compiled := f.engine.Counts().Compilations - before.Compilations
	if counts.Builds != 1 || counts.Hits != 9 || compiled != 1 {
		t.Fatal("10 identical builds not deduplicated", counts, compiled)
	}
	t.Logf("E/H: 10 actual C++ requests; compiler invocations=%d, cache builds=%d misses=%d hits=%d elapsed=%s", compiled, counts.Builds, counts.Misses, counts.Hits, time.Since(start))
	var key, sha string
	var keyJSON []byte
	if e := f.db.QueryRow(f.ctx, `SELECT cache_key,artifact_sha,key_json FROM compilation_cache WHERE state='READY'`).Scan(&key, &sha, &keyJSON); e != nil {
		t.Fatal(e)
	}
	var identity buildcache.Key
	if e := json.Unmarshal(keyJSON, &identity); e != nil {
		t.Fatal(e)
	}
	changed := identity
	changed.Flags += " changed"
	cl, e := cache.Acquire(f.ctx, changed, "changed-flags")
	if e != nil || cl.Hit || cl.Key == key {
		t.Fatal("F flags failed to miss", e)
	}
	cache.Release(f.ctx, cl)
	changed = identity
	changed.Toolchain = "sha256:" + strings.Repeat("b", 64)
	cl, e = cache.Acquire(f.ctx, changed, "changed-image")
	if e != nil || cl.Hit {
		t.Fatal("F image failed to miss", e)
	}
	cache.Release(f.ctx, cl)
	artifact, e := s.Get(f.ctx, "artifacts/"+sha+".bin", objects.MaxBytes)
	if e != nil {
		t.Fatal(e)
	}
	s9Write(t, bucket, "artifacts/"+sha+".bin", []byte("poison"))
	runtimeBefore := f.engine.Counts().RuntimeStarts
	if _, e = f.engine.Execute(f.ctx, r); e == nil {
		t.Fatal("G corrupted artifact accepted")
	}
	if f.engine.Counts().RuntimeStarts != runtimeBefore {
		t.Fatal("corrupted artifact executed")
	}
	s9Write(t, bucket, "artifacts/"+sha+".bin", artifact)
	if e = cache.Evict(f.ctx, key); e != nil {
		t.Fatal(e)
	}
	v, e := f.engine.Execute(f.ctx, r)
	if e != nil || v.CacheHit {
		t.Fatal("safe metadata eviction did not rebuild", e)
	}
	t.Log("F/G: changed flag/image claim identities miss; corrupt object rejected before runtime; explicit metadata eviction safely rebuilt")
}
func TestS9ObjectBackedAutomaticRejudge(t *testing.T) {
	f := auto(t)
	s, _ := s9Store(t, "tests")
	remote := &bundles.Remote{Objects: s}
	b, _ := bundles.Demo("two-sum")
	if _, e := remote.Put(b); e != nil {
		t.Fatal(e)
	}
	cacheObjects, _ := s9Store(t, "builds")
	cache := &buildcache.Cache{DB: f.db, Objects: cacheObjects}
	f.engine.SetCache(cache)
	o := agent.DefaultOptions()
	o.Concurrency = 1
	h, e := agent.New(f.repo, remote, f.engine, "agent-user", "s9-agent", o)
	if e != nil {
		t.Fatal(e)
	}
	source := "#include <iostream>\nint main(){long long a,b;std::cin>>a>>b;if(a==42&&b==0)std::cout<<0;else std::cout<<a+b;}"
	j := f.submit(t, "cpp17", source)
	if _, e = h.Handle(f.ctx, j, "actual-delivery"); e != nil {
		t.Fatal(e)
	}
	old, e := f.repo.Get(f.ctx, "agent-user", j.SubmissionID)
	if e != nil || old.Verdict == nil || *old.Verdict != "AC" {
		t.Fatal("initial real AC", old, e)
	}
	py := f.submit(t, "python3", "a,b=map(int,input().split());print(a+b)")
	if _, e = h.Handle(f.ctx, py, "python-delivery"); e != nil {
		t.Fatal(e)
	}
	pyResult, e := f.repo.Get(f.ctx, "agent-user", py.SubmissionID)
	if e != nil || pyResult.Verdict == nil || *pyResult.Verdict != "AC" {
		history, _ := f.repo.History(f.ctx, "agent-user", py.SubmissionID)
		safe, _ := json.Marshal(history)
		t.Fatal("object-backed Python AC", string(safe), e)
	}
	b.Tests = append(b.Tests, bundles.Case{ID: "edge-42", Order: 7, Input: []byte("42 0\n"), Expected: []byte("42\n")})
	v, e := f.repo.PublishVersion(f.ctx, "agent-user", "corrected-tests", storage.Publication{ProblemID: f.problem, Statement: "additional edge case", TimeLimitMS: 2000, MemoryLimitKB: 262144, Bundle: b}, remote)
	if e != nil {
		t.Fatal(e)
	}
	request, e := f.repo.RequestRejudge(f.ctx, "agent-user", "rejudge-once", j.SubmissionID, v)
	if e != nil {
		t.Fatal(e)
	}
	repeat, e := f.repo.RequestRejudge(f.ctx, "agent-user", "rejudge-once", j.SubmissionID, v)
	if e != nil || repeat.JobID != request.JobID || !repeat.Replayed {
		t.Fatal("M duplicate request", e)
	}
	publicationOptions := outbox.DefaultOptions()
	publicationOptions.BackoffMin = time.Millisecond
	publicationOptions.BackoffMax = time.Millisecond
	if e = (outbox.Relay{Store: f.repo, Publisher: s9UncertainPublisher{f.s}, Options: publicationOptions}).Step(f.ctx); e != nil {
		t.Fatal(e)
	}
	time.Sleep(5 * time.Millisecond)
	if e = (outbox.Relay{Store: f.repo, Publisher: f.s, Options: publicationOptions}).Step(f.ctx); e != nil {
		t.Fatal(e)
	}
	var event string
	if e = f.db.QueryRow(f.ctx, `SELECT id::text FROM outbox_events WHERE job_id=$1`, request.JobID).Scan(&event); e != nil {
		t.Fatal(e)
	}
	eventJob := queue.Job{Kind: queue.RejudgeJob, JobID: request.JobID, SubmissionID: j.SubmissionID, EventID: event, Attempt: 1}
	compiled := f.engine.Counts().Compilations
	if _, e = h.Handle(f.ctx, eventJob, "rejudge-delivery"); e != nil {
		t.Fatal(e)
	}
	if f.engine.Counts().Compilations != compiled {
		t.Fatal("rejudge unexpectedly recompiled equivalent source")
	}
	if _, e = h.Handle(f.ctx, eventJob, "duplicate-after-commit"); e != nil {
		t.Fatal(e)
	}
	current, e := f.repo.Get(f.ctx, "agent-user", j.SubmissionID)
	if e != nil || current.Verdict == nil || *current.Verdict != "WA" || current.OriginalVerdict == nil || *current.OriginalVerdict != "AC" || current.ProblemVersionID != old.ProblemVersionID {
		t.Fatal("J/L selected result", current, e)
	}
	history, e := f.repo.History(f.ctx, "agent-user", j.SubmissionID)
	if e != nil || len(history) != 2 || *history[0].Verdict != "AC" || *history[1].Verdict != "WA" {
		t.Fatal("K history", history, e)
	}
	if !history[1].CacheHit || history[0].CacheHit {
		t.Fatal("durable cache-hit observation lost")
	}
	data, _ := json.Marshal(current)
	for _, secret := range []string{"edge-42", "42 0", "bundle_sha", "S3_SECRET", os.Getenv("TEST_S3_SECRET_KEY")} {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			t.Fatal("P public result leaked private data")
		}
	}
	var open int
	if e = f.db.QueryRow(f.ctx, `SELECT count(*) FROM sandbox_executions WHERE state='OPEN'`).Scan(&open); e != nil || open != 0 {
		t.Fatal("Q registry not closed", open, e)
	}
	t.Logf("D/J/K/L/M/P/Q: actual C++/Python AC; original %s AC -> job %s version %d WA with cache hit, zero new compiler calls, immutable old history, duplicate delivery harmless", j.SubmissionID, request.JobID, v)
}
