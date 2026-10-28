//go:build integration

package tests

import (
	"context"
	"errors"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"sync"
	"testing"
	"time"
)

type unavailablePublisher struct{ bundles.Publisher }

func (unavailablePublisher) Put(bundles.Bundle) (bundles.Reference, error) {
	return bundles.Reference{}, fmt.Errorf("injected unavailable object store")
}
func TestVersionPublicationStagingAndResume(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo := storage.NewRepository(db)
	req := request(t, db)
	store, _ := bundles.Open(t.TempDir())
	b, _ := bundles.Demo("two-sum")
	p := storage.Publication{ProblemID: req.ProblemID, Statement: "published revision", TimeLimitMS: 2000, MemoryLimitKB: 262144, Bundle: b}
	if _, e := repo.PublishVersion(ctx, "operator", "publication", p, unavailablePublisher{}); e == nil {
		t.Fatal("outage activated version")
	}
	var state string
	if e := db.QueryRow(ctx, `SELECT state FROM bundle_publications WHERE operator='operator'`).Scan(&state); e != nil || state != "STAGED" {
		t.Fatal(state, e)
	}
	version, e := repo.PublishVersion(ctx, "operator", "publication", p, store)
	if e != nil {
		t.Fatal(e)
	}
	again, e := repo.PublishVersion(ctx, "operator", "publication", p, store)
	if e != nil || again != version {
		t.Fatal(again, e)
	}
	p.TimeLimitMS++
	if _, e = repo.PublishVersion(ctx, "operator", "publication", p, store); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("conflicting publication", e)
	}
	if _, e = db.Exec(ctx, `UPDATE problem_versions SET statement='changed' WHERE id=$1`, version); e == nil {
		t.Fatal("published version mutable")
	}
}
func TestRejudgeLogicalIdentityAndFencing(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo, initial := executionJob(t, db, "cpp17")
	w, e := repo.ClaimExecution(ctx, "lease-user", "initial", initial, 30*time.Second, 3)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.CommitEvaluation(ctx, w, acEvidence(w)); e != nil {
		t.Fatal(e)
	}
	store, _ := bundles.Open(t.TempDir())
	b, _ := bundles.Demo("two-sum")
	version, e := repo.PublishVersion(ctx, "operator", "new-tests", storage.Publication{ProblemID: w.Submission.ProblemID, Statement: "revision", TimeLimitMS: 2000, MemoryLimitKB: 262144, Bundle: b}, store)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, e := repo.RequestRejudge(ctx, "lease-user", "same", initial.SubmissionID, version)
			ids <- j.JobID
			errs <- e
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate logical jobs")
		}
	}
	if _, e = repo.RequestRejudge(ctx, "different-owner", "bad", initial.SubmissionID, version); e == nil {
		t.Fatal("cross-owner rejudge")
	}
	s, _ := streams(t)
	if e = (outbox.Relay{Store: repo, Publisher: s, Options: relayOptions()}).Step(ctx); e != nil {
		t.Fatal(e)
	}
	var event string
	if e = db.QueryRow(ctx, `SELECT id::text FROM outbox_events WHERE job_id=$1`, first).Scan(&event); e != nil {
		t.Fatal(e)
	}
	j := queue.Job{Kind: queue.RejudgeJob, JobID: first, SubmissionID: initial.SubmissionID, EventID: event, Attempt: 1}
	old, e := repo.ClaimExecution(ctx, "lease-user", "old", j, time.Second, 3)
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(1100 * time.Millisecond)
	newer, e := repo.ClaimExecution(ctx, "lease-user", "new", j, 30*time.Second, 3)
	if e != nil || newer.Generation != 2 {
		t.Fatal(newer, e)
	}
	if _, e = repo.CommitEvaluation(ctx, old, acEvidence(old)); e == nil {
		t.Fatal("stale rejudge attempt finalized")
	}
	if _, e = repo.CommitEvaluation(ctx, newer, acEvidence(newer)); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.CommitEvaluation(ctx, newer, acEvidence(newer)); e != nil {
		t.Fatal("idempotent completion", e)
	}
	got, e := repo.Get(ctx, "lease-user", initial.SubmissionID)
	if e != nil || got.ProblemVersionID != w.Submission.ProblemVersionID || got.CurrentVersionID == nil || *got.CurrentVersionID != version {
		t.Fatal(got, e)
	}
	history, e := repo.History(ctx, "lease-user", initial.SubmissionID)
	if e != nil || len(history) != 3 {
		t.Fatal(history, e)
	}
	if history[0].RunID != w.RunID || history[0].Verdict == nil || *history[0].Verdict != "AC" {
		t.Fatal("original history changed")
	}
	replay, e := repo.ClaimExecution(ctx, "lease-user", "duplicate", initial, 30*time.Second, 3)
	if e != nil || replay.Existing == nil || replay.Existing.RunID != w.RunID {
		t.Fatal("initial replay selected a rejudge", replay, e)
	}
	t.Log("20 rejudge requests -> one job/outbox; generation takeover fenced stale result; original history retained")
}
func TestRejudgeSelectionOrdering(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo, initial := executionJob(t, db, "cpp17")
	w, e := repo.ClaimExecution(ctx, "lease-user", "initial", initial, 30*time.Second, 3)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.CommitEvaluation(ctx, w, acEvidence(w)); e != nil {
		t.Fatal(e)
	}
	store, _ := bundles.Open(t.TempDir())
	b, _ := bundles.Demo("two-sum")
	v, e := repo.PublishVersion(ctx, "operator", "revision", storage.Publication{ProblemID: w.Submission.ProblemID, Statement: "revision", TimeLimitMS: 2000, MemoryLimitKB: 262144, Bundle: b}, store)
	if e != nil {
		t.Fatal(e)
	}
	jobs := []storage.RejudgeRequest{}
	for _, key := range []string{"first", "second"} {
		j, e := repo.RequestRejudge(ctx, "lease-user", key, initial.SubmissionID, v)
		if e != nil {
			t.Fatal(e)
		}
		jobs = append(jobs, j)
	}
	s, _ := streams(t)
	if e = (outbox.Relay{Store: repo, Publisher: s, Options: relayOptions()}).Step(ctx); e != nil {
		t.Fatal(e)
	}
	for _, index := range []int{1, 0} {
		j := jobs[index]
		var event string
		db.QueryRow(ctx, `SELECT id::text FROM outbox_events WHERE job_id=$1`, j.JobID).Scan(&event)
		work, e := repo.ClaimExecution(ctx, "lease-user", fmt.Sprint("owner", index), queue.Job{Kind: queue.RejudgeJob, JobID: j.JobID, SubmissionID: initial.SubmissionID, EventID: event, Attempt: 1}, 30*time.Second, 3)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = repo.CommitEvaluation(ctx, work, acEvidence(work)); e != nil {
			t.Fatal(e)
		}
	}
	var selected string
	if e = db.QueryRow(ctx, `SELECT job_id::text FROM selected_evaluations WHERE submission_id=$1`, initial.SubmissionID).Scan(&selected); e != nil || selected != jobs[1].JobID {
		t.Fatal("older job replaced newest selection", selected, e)
	}
}
