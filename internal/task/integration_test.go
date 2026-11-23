//go:build taskintegration

package task

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/repair"
	"github.com/Amankumarsingh23/JudgeGrid/internal/repotest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/research"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"github.com/Amankumarsingh23/JudgeGrid/internal/validation"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const first = "package fixture\n\nfunc Value() int { return 1 }\n"
const goal = "Fix Value to satisfy the test that expects two"

func TestS21ActualTask(t *testing.T) {
	if os.Getenv("JUDGEGRID_TASK_TESTS") != "1" {
		t.Fatal("explicit task integration authority required")
	}
	for _, tc := range []struct {
		name        string
		wrong, deny bool
	}{{"one", false, false}, {"two", true, false}, {"deny", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()
			f := repotest.Open(t, ctx, first)
			w := f.Workspace
			a := &analysis.Service{DB: analysis.Database{Pool: f.DB}, Source: w.Source, Python: os.Getenv("REPO_ANALYSIS_PYTHON")}
			ai, e := a.Analyze(ctx, "owner", f.Selected.Snapshot, analysis.DefaultPolicy(), false)
			if e != nil {
				t.Fatal(e)
			}
			embed, e := retrieval.OpenModel(os.Getenv("REPO_EMBEDDING_PYTHON"), os.Getenv("REPO_EMBEDDING_MODEL"))
			if e != nil {
				t.Fatal(e)
			}
			defer embed.Close()
			rr := &retrieval.Service{DB: retrieval.Database{Pool: f.DB}, Source: a, Model: embed}
			ri, e := rr.Build(ctx, "owner", ai.ID, retrieval.DefaultPolicy(), false)
			if e != nil {
				t.Fatal(e)
			}
			s := Service{DB: f.DB, Workspace: w, Research: research.Service{DB: f.DB, Planner: ResearchFixture{}, Readers: research.Readers{Analysis: a, Retrieval: rr}}, Validation: validation.Service{DB: f.DB, Workspace: w, Docker: f.Docker}, Model: Fixture{FirstWrong: tc.wrong}}
			started := time.Now()
			r, e := s.Start(ctx, "owner", uuid.NewString(), ri.ID, goal, "value.go")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.Start(ctx, "foreign", uuid.NewString(), ri.ID, goal, "value.go"); e == nil {
				t.Fatal("foreign index accepted")
			}
			if _, e = s.Start(ctx, "owner", uuid.NewString(), ri.ID, goal, "value_test.go"); !errors.Is(e, ErrPolicy) {
				t.Fatal("test weakening accepted", e)
			}
			unavailable := s
			unavailable.Model = nil
			if _, e = unavailable.Resume(ctx, "owner", r.ID); !errors.Is(e, ErrModelUnavailable) {
				t.Fatal("silent provider fallback", e)
			}
			lost := errors.New("controlled lost controller")
			expire := func() {
				if _, e = f.DB.Exec(ctx, `UPDATE research_tasks SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, r.ID); e != nil {
					t.Fatal(e)
				}
			}
			if tc.name == "one" {
				if _, e = s.resume(ctx, "owner", r.ID, func(stage string) error {
					if stage == "research" {
						return lost
					}
					return nil
				}); !errors.Is(e, lost) {
					t.Fatal("research failure injection", e)
				}
				expire()
				var old claim
				old.owner, old.id = "owner", r.ID
				if e = f.DB.QueryRow(ctx, `SELECT generation,claim::text FROM research_tasks WHERE id=$1`, r.ID).Scan(&old.generation, &old.token); e != nil {
					t.Fatal(e)
				}
				if e = s.parent(old).Guard(ctx, func() error { t.Error("stale task dispatched"); return nil }); !errors.Is(e, ErrDenied) {
					t.Fatal(e)
				}
			}
			r, e = s.Resume(ctx, "owner", r.ID)
			if e != nil || r.State != "WAITING_APPROVAL" {
				t.Fatal("no review", r.State, e)
			}
			evidence, e := s.ResearchEvidence(ctx, "owner", r.ID)
			if e != nil || evidence.State != "COMPLETED" || len(evidence.Steps) < 3 || len(evidence.Steps[1].Observation.Citations) == 0 {
				t.Fatal("real citation missing", e)
			}
			original, e := w.InspectWorkspaceVersion(ctx, "owner", r.Workspace)
			if e != nil {
				t.Fatal(e)
			}
			review, e := s.Review(ctx, "owner", r.ID)
			if e != nil || review.Patch.Patch.Base != original.Selected || review.Patch.Diff == "" || review.Approval.State != "PENDING" {
				t.Fatal("bad review", e)
			}
			if _, e = s.Decide(ctx, "owner", r.ID, "wrong", "grant"); e == nil {
				t.Fatal("wrong grant accepted")
			}
			if _, e = s.Decide(ctx, "foreign", r.ID, review.Patch.Digest, "grant"); e == nil {
				t.Fatal("forged principal")
			}
			paused, e := s.Resume(ctx, "owner", r.ID)
			if e != nil || paused.State != "WAITING_APPROVAL" {
				t.Fatal("ungranted work advanced", e)
			}
			before, e := w.InspectWorkspaceVersion(ctx, "owner", r.Workspace)
			if e != nil || before.Selected != original.Selected {
				t.Fatal("unreviewed mutation", e)
			}
			action := "grant"
			if tc.deny {
				action = "deny"
			}
			if _, e = s.Decide(ctx, "owner", r.ID, review.Patch.Digest, action); e != nil {
				t.Fatal(e)
			}
			if tc.name == "one" {
				if _, e = s.resume(ctx, "owner", r.ID, func(stage string) error {
					if stage == "repair" {
						return lost
					}
					return nil
				}); !errors.Is(e, lost) {
					t.Fatal("repair receipt interruption missing", e)
				}
				expire()
			}
			r, e = s.Resume(ctx, "owner", r.ID)
			if e != nil {
				t.Fatal(e)
			}
			if tc.wrong {
				if r.State != "WAITING_APPROVAL" {
					t.Fatal("failed first repair did not pause")
				}
				denied, e := s.Review(ctx, "owner", r.ID)
				if e != nil || denied.Patch.Digest == review.Patch.Digest {
					t.Fatal("no distinct second proposal", e)
				}
				if _, e = s.Decide(ctx, "owner", r.ID, review.Patch.Digest, "grant"); e == nil {
					t.Fatal("old grant accepted")
				}
				if _, e = s.Decide(ctx, "owner", r.ID, denied.Patch.Digest, "grant"); e != nil {
					t.Fatal(e)
				}
				r, e = s.Resume(ctx, "owner", r.ID)
				if e != nil {
					t.Fatal(e)
				}
			}
			want := "COMPLETED_VALIDATED"
			if tc.deny {
				want = "DENIED"
			}
			if r.State != want || r.Report == nil || r.Digest == "" || len(r.Report.Citations) == 0 || r.InputTokens != 0 || r.OutputTokens != 0 {
				t.Fatal("bad final", r.State)
			}
			baseline, e := s.Validation.InspectValidation(ctx, "owner", r.Baseline)
			if e != nil || baseline.Outcome != "TEST_FAILURE" {
				t.Fatal("baseline unverified", e)
			}
			last, e := (repair.Service{DB: f.DB}).Inspect(ctx, "owner", r.Repair)
			if e != nil {
				t.Fatal(e)
			}
			if !tc.deny {
				if r.Report.Outcome != "PASS" || r.Report.Version == baseline.Version || r.Report.Validation == baseline.ID || r.Report.Profile != "go-unit" {
					t.Fatal("failed exact validation")
				}
				if tc.wrong && (len(last.History) != 2 || last.History[0].Outcome != "TEST_FAILURE") {
					t.Fatal("missing real first failure")
				}
				for _, step := range last.History {
					v, e := s.Validation.InspectValidation(ctx, "owner", step.Validation)
					if e != nil || len(v.Attempts) != 1 {
						t.Fatal("validation duplicated", e)
					}
				}
			}
			replay, e := s.Resume(ctx, "owner", r.ID)
			if e != nil || replay.Digest != r.Digest {
				t.Fatal("authoritative replay changed", e)
			}
			f.AssertUnchanged()
			t.Logf("scope=%s baseline=%s result=%s calls=%d iterations=%d generation=%d reports=%s elapsed=%v", r.Scope.Commit, baseline.Outcome, r.State, r.Calls, len(last.History), r.Generation, r.Digest, time.Since(started))
			if tc.deny {
				budget, e := s.Start(ctx, "owner", uuid.NewString(), ri.ID, goal, "value.go")
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.DB.Exec(ctx, `UPDATE research_tasks SET active_us=690000000 WHERE id=$1`, budget.ID); e != nil {
					t.Fatal(e)
				}
				budget, e = s.Resume(ctx, "owner", budget.ID)
				if e != nil || budget.State != "BUDGET_EXHAUSTED" || budget.Research != "" || budget.ActiveUS != 690000000 {
					t.Fatal("parent budget was reset or dispatched", e, budget.State)
				}
			}
		})
	}
}

func TestS21RetainedReview(t *testing.T) {
	url := os.Getenv("S21_RETAINED_DATABASE_URL")
	if url == "" {
		t.Skip("retained private operator database only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db, e := pgxpool.New(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = migrations.Check(ctx, db); e != nil {
		t.Fatal(e)
	}
	bits, e := ingest.OpenS3(os.Getenv("REPO_S3_ENDPOINT"), os.Getenv("REPO_S3_BUCKET"), os.Getenv("REPO_S3_ACCESS_KEY"), os.Getenv("REPO_S3_SECRET_KEY"), false)
	if e != nil {
		t.Fatal(e)
	}
	src := &ingest.Service{DB: ingest.Database{Pool: db}, Objects: bits}
	a := &analysis.Service{DB: analysis.Database{Pool: db}, Source: src}
	embed, e := retrieval.OpenModel(os.Getenv("REPO_EMBEDDING_PYTHON"), os.Getenv("REPO_EMBEDDING_MODEL"))
	if e != nil {
		t.Fatal(e)
	}
	defer embed.Close()
	w := workspace.WorkspaceManager{DB: db, Source: src}
	s := Service{DB: db, Workspace: w, Research: research.Service{DB: db, Planner: ResearchFixture{}, Readers: research.Readers{Analysis: a, Retrieval: &retrieval.Service{DB: retrieval.Database{Pool: db}, Source: a, Model: embed}}}, Validation: validation.Service{DB: db, Workspace: w, Docker: repotest.Docker(t)}, Model: Fixture{}}
	const owner = "aman-s12"
	const index = "754720b7e2731b6b4321754b75a1f99b5cfd694b459ebca858781068af1dd338"
	item, e := s.Start(ctx, owner, uuid.NewString(), index, "Where is renewExecution and the lease generation guard?", "value.go")
	if e != nil {
		t.Fatal(e)
	}
	lost := errors.New("pause before controlled setup")
	_, e = s.resume(ctx, owner, item.ID, func(stage string) error {
		if stage == "workspace" {
			return lost
		}
		return nil
	})
	if !errors.Is(e, lost) {
		t.Fatal("preparation did not reach setup pause", e)
	}
	item, e = s.Inspect(ctx, owner, item.ID)
	if e != nil || item.Workspace == "" {
		t.Fatal("missing retained workspace", e)
	}
	base, e := w.InspectWorkspaceVersion(ctx, owner, item.Workspace)
	if e != nil {
		t.Fatal(e)
	}
	tests := "package fixture\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value()!=2 { t.Fatalf(\"got %d, want 2\",Value()) } }\n"
	p, e := w.Propose(ctx, owner, item.Workspace, workspace.Patch{Base: base.Selected, Changes: []workspace.Change{
		{Kind: "ADD", Path: "value.go", Mode: "100644", Text: first, After: ingest.Digest([]byte(first)), AfterSize: len(first)},
		{Kind: "ADD", Path: "value_test.go", Mode: "100644", Text: tests, After: ingest.Digest([]byte(tests)), AfterSize: len(tests)},
	}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.RequestPatchApproval(ctx, owner, item.Workspace, p.Digest); e != nil {
		t.Fatal(e)
	}
	if _, e = w.Decide(ctx, owner, item.Workspace, p.Digest, "grant"); e != nil {
		t.Fatal(e)
	}
	setup, e := w.ApplyApprovedPatch(ctx, owner, item.Workspace, p.Digest)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, `UPDATE research_tasks SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, item.ID); e != nil {
		t.Fatal(e)
	}
	item, e = s.Resume(ctx, owner, item.ID)
	if e != nil || item.State != "WAITING_APPROVAL" {
		t.Fatal("retained review", item.State, e)
	}
	review, e := s.Review(ctx, owner, item.ID)
	if e != nil || review.Patch.Patch.Base != setup.Version || review.Approval.State != "PENDING" {
		t.Fatal("retained review escaped", e)
	}
	citations, e := s.citations(ctx, owner, item)
	if e != nil || len(citations) == 0 {
		t.Fatal("retained citation missing", e)
	}
	t.Logf("retained commit=%s path=%s version=%s patch=%s state=%s", item.Scope.Commit, citations[0].Path, setup.Version, review.Patch.Digest, item.State)
}
