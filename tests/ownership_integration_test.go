//go:build integration

package tests

import (
	"context"
	"errors"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/judge"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"sync"
	"testing"
	"time"
)

func executionJob(t *testing.T, db *pgxpool.Pool, lang string) (*storage.Repository, queue.Job) {
	t.Helper()
	ctx := context.Background()
	repo := storage.NewRepository(db)
	b, _ := bundles.Open(t.TempDir())
	if err := storage.SeedSuites(ctx, db, b); err != nil {
		t.Fatal(err)
	}
	req := request(t, db)
	req.Language = lang
	sub := create(t, repo, "lease-user", fmt.Sprintf("lease-%d", time.Now().UnixNano()), req)
	s, _ := streams(t)
	if err := (outbox.Relay{Store: repo, Publisher: s, Options: relayOptions()}).Step(ctx); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(ctx, `SELECT id::text FROM outbox_events WHERE submission_id=$1`, sub.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return repo, queue.Job{Kind: queue.SubmissionJob, SubmissionID: sub.ID, EventID: id, Attempt: 1}
}
func acEvidence(w judge.Work) judge.Report {
	yes := true
	e := judge.Evidence{Outcome: execution.Exited, StdoutHash: bundles.Hash(nil), StderrHash: bundles.Hash(nil)}
	r := judge.Report{Verdict: "AC", Image: "sha256:" + strings.Repeat("a", 64), Policy: execution.PolicyVersion, Cleaned: true, Compile: &e}
	for _, c := range w.Reference.Cases {
		r.Tests = append(r.Tests, judge.TestResult{CaseID: c.ID, Order: c.Order, Verdict: "AC", Match: &yes, Evidence: e})
	}
	return r
}
func TestExecutionConcurrentClaimsAndHeartbeats(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo, j := executionJob(t, db, "cpp17")
	var wg sync.WaitGroup
	wins := make(chan judge.Work, 20)
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w, e := repo.ClaimExecution(ctx, "lease-user", fmt.Sprint("owner-", i), j, 5*time.Second, 3)
			if e == nil {
				wins <- w
			}
		}(i)
	}
	wg.Wait()
	close(wins)
	var w judge.Work
	count := 0
	for winner := range wins {
		count++
		w = winner
	}
	if count != 1 || w.Generation != 1 {
		t.Fatal("A concurrent owners", count, w)
	}
	var before, after time.Time
	if e := db.QueryRow(ctx, `SELECT lease_expires_at FROM execution_ownership WHERE submission_id=$1`, j.SubmissionID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e := repo.RenewExecution(ctx, w, 6*time.Second); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(ctx, `SELECT lease_expires_at FROM execution_ownership WHERE submission_id=$1`, j.SubmissionID).Scan(&after); e != nil || !after.After(before) {
		t.Fatal("B renewal", e)
	}
	wrong := w
	wrong.Owner = "wrong"
	if e := repo.RenewExecution(ctx, wrong, time.Second); !errors.Is(e, storage.ErrOwned) {
		t.Fatal("C owner mismatch", e)
	}
	wrong = w
	wrong.Generation++
	if e := repo.RenewExecution(ctx, wrong, time.Second); !errors.Is(e, storage.ErrOwned) {
		t.Fatal("C generation mismatch", e)
	}
	if _, e := repo.Transition(ctx, j.SubmissionID, domain.Transition{From: domain.Running, To: domain.Evaluating, ExpectedVersion: w.StateVersion}); e == nil {
		t.Fatal("unfenced generic state write")
	}
	if e := repo.FailExecution(ctx, w, "sandbox_infrastructure", false); e != nil {
		t.Fatal(e)
	}
}
func TestExecutionExpiryTakeoverAndStaleWrites(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo, j := executionJob(t, db, "cpp17")
	a, e := repo.ClaimExecution(ctx, "lease-user", "A", j, time.Second, 3)
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(1100 * time.Millisecond)
	if e = repo.RenewExecution(ctx, a, time.Second); !errors.Is(e, storage.ErrOwned) {
		t.Fatal("D expired revived", e)
	}
	b, e := repo.ClaimExecution(ctx, "lease-user", "B", j, 5*time.Second, 3)
	if e != nil || b.Generation != 2 || b.RunID == a.RunID {
		t.Fatal("E takeover", b, e)
	}
	if _, e = repo.CommitEvaluation(ctx, a, acEvidence(a)); !errors.Is(e, storage.ErrOwned) {
		t.Fatal("F old result", e)
	}
	if e = repo.FailExecution(ctx, a, "sandbox_infrastructure", false); !errors.Is(e, storage.ErrOwned) {
		t.Fatal("G old failure", e)
	}
	var oldStatus string
	if e = db.QueryRow(ctx, `SELECT status FROM evaluation_runs WHERE id=$1`, a.RunID).Scan(&oldStatus); e != nil || oldStatus != "FAILED" {
		t.Fatal(oldStatus, e)
	}
	r := acEvidence(b)
	v, e := repo.CommitEvaluation(ctx, b, r)
	if e != nil || v.Verdict == nil || *v.Verdict != "AC" {
		t.Fatal(v, e)
	}
	v, e = repo.CommitEvaluation(ctx, b, r)
	if e != nil || !v.Replayed {
		t.Fatal("L exact replay", v, e)
	}
	r.Tests[0].Evidence.WallTimeNS++
	if _, e = repo.CommitEvaluation(ctx, b, r); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("conflicting replay", e)
	}
	var rows int
	if e = db.QueryRow(ctx, `SELECT count(*) FROM evaluation_test_results`).Scan(&rows); e != nil || rows != 6 {
		t.Fatal("F stale evidence committed", rows, e)
	}
	t.Log("A-G fencing: expired heartbeat rejected, generation advanced, stale result/failure rejected, exact completed replay only")
}
func TestExecutionScannerRetryCapUnsupportedAndScope(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo, j := executionJob(t, db, "cpp17")
	a, e := repo.ClaimExecution(ctx, "lease-user", "crashed", j, time.Second, 1)
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(1100 * time.Millisecond)
	jobs, e := repo.RecoveryJobs(ctx, "lease-user", 4)
	if e != nil || len(jobs) != 1 || jobs[0].EventID != j.EventID {
		t.Fatal("I scanner", jobs, e)
	}
	if jobs, e = repo.RecoveryJobs(ctx, "another-principal", 4); e != nil || len(jobs) != 0 {
		t.Fatal("scanner principal leak", jobs, e)
	}
	w, e := repo.ClaimExecution(ctx, "lease-user", "recovery", j, time.Second, 3)
	if e != nil || w.Existing == nil {
		t.Fatal("O retry cap", w, e)
	}
	var status, reason string
	if e = db.QueryRow(ctx, `SELECT status,failure_reason FROM submissions WHERE id=$1`, a.Submission.ID).Scan(&status, &reason); e != nil || status != "FAILED" || reason != "attempt_limit" {
		t.Fatal(status, reason, e)
	}
	_, goJob := executionJob(t, db, "go")
	w, e = repo.ClaimExecution(ctx, "lease-user", "supported-check", goJob, time.Second, 3)
	if e != nil || w.Existing == nil || w.Existing.FailureCode == nil || *w.Existing.FailureCode != "unsupported_language" {
		t.Fatal("R unsupported", w, e)
	}
	var evaluations int
	if e = db.QueryRow(ctx, `SELECT count(*) FROM evaluation_runs WHERE submission_id=$1`, goJob.SubmissionID).Scan(&evaluations); e != nil || evaluations != 0 {
		t.Fatal("unsupported fabricated run", evaluations, e)
	}
}

func TestExecutionRepeatedInfrastructureBudget(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo, j := executionJob(t, db, "cpp17")
	for gen := 1; gen <= 3; gen++ {
		w, e := repo.ClaimExecution(ctx, "lease-user", fmt.Sprint("retry-", gen), j, 5*time.Second, 3)
		if e != nil || w.Generation != gen {
			t.Fatal(w, e)
		}
		if e = repo.FailExecution(ctx, w, "sandbox_infrastructure", true); e != nil {
			t.Fatal(e)
		}
		if gen < 3 {
			time.Sleep(1100 * time.Millisecond)
		}
	}
	var state string
	var generation, rows int
	if e := db.QueryRow(ctx, `SELECT state,generation,(SELECT count(*) FROM evaluation_test_results) FROM execution_ownership WHERE submission_id=$1`, j.SubmissionID).Scan(&state, &generation, &rows); e != nil || state != "EXHAUSTED" || generation != 3 || rows != 0 {
		t.Fatal(state, generation, rows, e)
	}
	w, e := repo.ClaimExecution(ctx, "lease-user", "late", j, time.Second, 5)
	if e != nil || w.Existing == nil {
		t.Fatal("budget changed by caller", w, e)
	}
}

func TestExecutionRecoveryAfterQueueReferenceRemoved(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo := storage.NewRepository(db)
	b, _ := bundles.Open(t.TempDir())
	if e := storage.SeedSuites(ctx, db, b); e != nil {
		t.Fatal(e)
	}
	sub := create(t, repo, "lease-user", "removed-reference", request(t, db))
	s, raw := streams(t)
	if e := (outbox.Relay{Store: repo, Publisher: s, Options: relayOptions()}).Step(ctx); e != nil {
		t.Fatal(e)
	}
	messages, e := s.Read(ctx, "lost-reference", 1, 10*time.Millisecond)
	if e != nil || len(messages) != 1 {
		t.Fatal(messages, e)
	}
	j, e := messages[0].Job()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.ClaimExecution(ctx, "lease-user", "owner-before-loss", j, time.Second, 3); e != nil {
		t.Fatal(e)
	}
	if e = s.Ack(ctx, messages[0].ID); e != nil {
		t.Fatal(e)
	}
	if e = raw.XDel(ctx, s.Stream, messages[0].ID).Err(); e != nil {
		t.Fatal(e)
	}
	time.Sleep(1100 * time.Millisecond)
	jobs, e := repo.RecoveryJobs(ctx, "lease-user", 4)
	if e != nil || len(jobs) != 1 || jobs[0].SubmissionID != sub.ID {
		t.Fatal("scanner depended on Redis reference", jobs, e)
	}
	w, e := repo.ClaimExecution(ctx, "lease-user", "database-recovery", jobs[0], 5*time.Second, 3)
	if e != nil || w.Generation != 2 {
		t.Fatal(w, e)
	}
	if _, e = repo.CommitEvaluation(ctx, w, acEvidence(w)); e != nil {
		t.Fatal(e)
	}
	t.Log("database scanner recovered after real delivery ACK and explicit test-stream entry deletion")
}
