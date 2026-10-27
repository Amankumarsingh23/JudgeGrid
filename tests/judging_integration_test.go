//go:build integration

package tests

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/judge"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEvaluationAtomicFinalization(t *testing.T) {
	ctx := context.Background()
	pool := database(t, true)
	repo := storage.NewRepository(pool)
	store, err := bundles.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.SeedSuites(ctx, pool, store); err != nil {
		t.Fatal(err)
	}
	if err = storage.SeedSuites(ctx, pool, store); err != nil {
		t.Fatal("seed replay", err)
	}
	constraint(t, pool, "23514", `INSERT INTO problem_test_suites(problem_version_id,bundle_sha,comparator,test_count) SELECT id,repeat('a',64),'exact',1 FROM problem_versions WHERE id NOT IN(SELECT problem_version_id FROM problem_test_suites) UNION ALL SELECT 999999,repeat('a',64),'exact',1`)
	sub := create(t, repo, "alice", "judge-atomic", request(t, pool))
	session, err := repo.Acquire(ctx, "alice", sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err = repo.Acquire(ctx, "bob", sub.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("owner leak", err)
	}
	if _, err = repo.Acquire(ctx, "alice", sub.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("duplicate evaluator", err)
	}
	w, err := session.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic sanitized evidence tests SQL semantics only, not program correctness.
	yes := true
	r := judge.Report{Verdict: "AC", Image: "sha256:" + strings.Repeat("a", 64), Policy: execution.PolicyVersion, Cleaned: true}
	r.Compile = &judge.Evidence{Outcome: execution.Exited, StdoutHash: bundles.Hash(nil), StderrHash: bundles.Hash(nil)}
	for _, c := range w.Reference.Cases {
		r.Tests = append(r.Tests, judge.TestResult{CaseID: c.ID, Order: c.Order, Verdict: "AC", Match: &yes, Evidence: judge.Evidence{Outcome: execution.Exited, StdoutHash: bundles.Hash(nil), StderrHash: bundles.Hash(nil)}})
	}
	execute(t, pool, `CREATE FUNCTION inject_result_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected write failure'; END; $$; CREATE TRIGGER inject_result_failure BEFORE INSERT ON evaluation_test_results FOR EACH ROW EXECUTE FUNCTION inject_result_failure()`)
	if _, err = session.Complete(ctx, w, r); err == nil {
		t.Fatal("injected failure accepted")
	}
	var status string
	var count int
	if err = pool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM evaluation_test_results) FROM submissions WHERE id=$1`, sub.ID).Scan(&status, &count); err != nil || status != "RUNNING" || count != 0 {
		t.Fatalf("partial transaction: %s %d %v", status, count, err)
	}
	execute(t, pool, `DROP TRIGGER inject_result_failure ON evaluation_test_results; DROP FUNCTION inject_result_failure()`)
	stale := w
	stale.StateVersion--
	if _, err = repo.CommitEvaluation(ctx, stale, r); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale accepted", err)
	}
	foreign := w
	foreign.Submission.PrincipalID = "bob"
	if _, err = repo.CommitEvaluation(ctx, foreign, r); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign finalization", err)
	}
	var wg sync.WaitGroup
	results := make(chan judge.Summary, 20)
	failures := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); v, e := repo.CommitEvaluation(ctx, w, r); results <- v; failures <- e }()
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	fresh := 0
	for v := range results {
		if !v.Replayed {
			fresh++
		}
		if v.TestsPassed != 6 {
			t.Fatal(v)
		}
	}
	if fresh != 1 {
		t.Fatal("fresh finalizations", fresh)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM evaluation_test_results WHERE evaluation_run_id=$1`, w.RunID).Scan(&count); err != nil || count != 6 {
		t.Fatal(count, err)
	}
	r.Tests[0].Evidence.WallTimeNS++
	if _, err = repo.CommitEvaluation(ctx, w, r); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("conflicting replay accepted", err)
	}
	constraint(t, pool, "23514", `UPDATE evaluation_runs SET tests_passed=0 WHERE id=$1`, w.RunID)
	constraint(t, pool, "23514", `DELETE FROM evaluation_test_results WHERE evaluation_run_id=$1`, w.RunID)
	constraint(t, pool, "23514", `UPDATE problem_test_suites SET comparator='exact' WHERE problem_version_id=$1`, sub.ProblemVersionID)
	constraint(t, pool, "23514", `UPDATE suite_cases SET expected_sha=repeat('a',64) WHERE problem_version_id=$1`, sub.ProblemVersionID)
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := (judge.Service{Repository: repo}).Run(ctx, "alice", sub.ID)
	if err != nil || !replay.Replayed || replay.RunID != w.RunID {
		t.Fatal(replay, err)
	}
}

func TestCorruptBundleNeverUserVerdict(t *testing.T) {
	ctx := context.Background()
	pool := database(t, true)
	repo := storage.NewRepository(pool)
	root := t.TempDir()
	store, _ := bundles.Open(root)
	if err := storage.SeedSuites(ctx, pool, store); err != nil {
		t.Fatal(err)
	}
	sub := create(t, repo, "alice", "corrupt", request(t, pool))
	var hash string
	if err := pool.QueryRow(ctx, `SELECT bundle_sha FROM problem_test_suites WHERE problem_version_id=$1`, sub.ProblemVersionID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, hash+".json")
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupted private bundle"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := (judge.Service{Repository: repo, Bundles: store}).Run(ctx, "alice", sub.ID)
	if err == nil || v.Status != "FAILED" || v.Verdict != nil || v.FailureCode == nil || *v.FailureCode != "bundle_verification_failed" {
		t.Fatal(v, err)
	}
	var verdict *string
	var status string
	if err = pool.QueryRow(ctx, `SELECT status,verdict FROM submissions WHERE id=$1`, sub.ID).Scan(&status, &verdict); err != nil || status != "FAILED" || verdict != nil {
		t.Fatal(status, verdict, err)
	}
	pool.Close()
	if _, err = (judge.Service{Repository: repo, Bundles: store}).Run(ctx, "alice", sub.ID); err == nil {
		t.Fatal("database outage claimed success")
	}
}
