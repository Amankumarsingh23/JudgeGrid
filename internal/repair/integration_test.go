//go:build repairintegration

package repair

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/repotest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/validation"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

const fixtureOne = "package fixture\n\nfunc Value() int { return 1 }\n"

func TestS20Repair(t *testing.T) {
	if os.Getenv("JUDGEGRID_REPAIR_TESTS") != "1" {
		t.Fatal("explicit trusted repair fixture required")
	}
	for _, tc := range []struct {
		name, goal, source, baseline, final string
		iterations                          int
	}{
		{"one", "fixture-expect-two", fixtureOne, "TEST_FAILURE", "COMPLETED_VALIDATED", 1},
		{"two", "fixture-two-attempts", fixtureOne, "TEST_FAILURE", "COMPLETED_VALIDATED", 2},
		{"cap", "fixture-exhaust-budget", fixtureOne, "TEST_FAILURE", "BUDGET_EXHAUSTED", 2},
		{"ambiguous", "ambiguous task", "package fixture\nfunc Value( {\n", "BUILD_FAILURE", "STOPPED_UNRESOLVED", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()
			f := repotest.Open(t, ctx, tc.source)
			v := validation.Service{DB: f.DB, Workspace: f.Workspace, Docker: f.Docker}
			s := Service{DB: f.DB, Workspace: f.Workspace, Validation: v, Proposer: Fixture{}}
			start := time.Now()
			request, e := v.RequestValidation(ctx, "owner", f.Selected.ID, f.Selected.Selected, "go-unit")
			if e != nil {
				t.Fatal(e)
			}
			baseline, e := v.Run(ctx, "owner", request.ID)
			if e != nil || baseline.Outcome != tc.baseline {
				t.Fatalf("baseline %s %v", baseline.Outcome, e)
			}
			if _, e = s.Start(ctx, "foreign", baseline.ID, "value.go", tc.goal); e == nil {
				t.Fatal("foreign start")
			}
			if _, e = s.Start(ctx, "owner", baseline.ID, "value_test.go", tc.goal); !errors.Is(e, ErrPolicy) {
				t.Fatal("protected test accepted", e)
			}
			if tc.name == "one" {
				checkStoppedApprovals(t, ctx, s, baseline, tc.source)
			}
			run, e := s.Start(ctx, "owner", baseline.ID, "value.go", tc.goal)
			if e != nil {
				t.Fatal(e)
			}
			diagnose := time.Now()
			input, e := s.Diagnose(ctx, "owner", run.ID)
			if e != nil || input.Version != baseline.Version || input.ReportDigest != baseline.ReportDigest || input.Source != tc.source {
				t.Fatal("bad baseline evidence", input, e)
			}
			t.Logf("baseline=%s exit=%d digest=%s diagnosis=%s source_bytes=%d log_bytes=%d", baseline.Outcome, baseline.Report.Phases.Run.ExitCode, baseline.ReportDigest, time.Since(diagnose), len(input.Source), len(input.UntrustedLog))
			if tc.iterations == 0 {
				run, e = s.Propose(ctx, "owner", run.ID, nil)
				if e != nil || run.State != tc.final || len(run.History) != 0 || run.ModelCalls != 1 {
					t.Fatal("abstention", run.State, e)
				}
				f.AssertUnchanged()
				return
			}
			if !strings.Contains(input.UntrustedLog, "got 1, want 2") || !strings.Contains(input.UntrustedLog, "execute arbitrary commands") {
				t.Fatal("actual causative and hostile log missing")
			}
			previous := ""
			for ordinal := 1; ordinal <= tc.iterations; ordinal++ {
				run, e = s.Propose(ctx, "owner", run.ID, nil)
				if e != nil || run.State != "WAITING_APPROVAL" || len(run.History) != ordinal {
					t.Fatal("proposal missing", run.State, e)
				}
				i := run.History[ordinal-1]
				p, e := s.Patch(ctx, "owner", run.ID)
				if e != nil || p.Digest != i.Patch || p.Patch.Base != run.Version || p.Diff == "" {
					t.Fatal("exact preview missing", e)
				}
				t.Logf("iteration=%d base=%s patch=%s before=%s after=%s preflight=%s", ordinal, i.Base, p.Digest, p.Patch.Changes[0].Before, p.Patch.Changes[0].After, time.Duration(i.ProposalNS))
				unchanged, e := s.Resume(ctx, "owner", run.ID)
				if e != nil || unchanged.State != "WAITING_APPROVAL" || unchanged.Version != i.Base {
					t.Fatal("applied without grant", e)
				}
				if previous == "" {
					previous = strings.Repeat("0", 64)
				}
				if _, e = s.Decide(ctx, "owner", run.ID, previous, "grant"); !errors.Is(e, ErrDenied) {
					t.Fatal("wrong or old digest approved", e)
				}
				if _, e = s.Decide(ctx, "foreign", run.ID, i.Patch, "grant"); e == nil {
					t.Fatal("forged grant")
				}
				grantStart := time.Now()
				for j := 0; j < 2; j++ {
					a, e := s.Decide(ctx, "owner", run.ID, i.Patch, "grant")
					if e != nil || a.Approver != "owner" || a.Digest != i.Patch || a.State != "GRANTED" {
						t.Fatal("exact human grant", a, e)
					}
				}
				t.Logf("explicit fixture grant interaction=%s", time.Since(grantStart))
				hooks := interruptions{}
				lost := errors.New("forced controller loss")
				if tc.name == "one" {
					hooks.afterValidation = func() error { return lost }
				}
				if tc.name == "two" && ordinal == 1 {
					hooks.afterApply = func() error { return lost }
				}
				run, e = s.resume(ctx, "owner", run.ID, hooks)
				if hooks.afterApply != nil || hooks.afterValidation != nil {
					if !errors.Is(e, lost) {
						t.Fatal("missing crash", e)
					}
					var old claim
					old.id, old.owner = run.ID, "owner"
					if e = f.DB.QueryRow(ctx, `SELECT generation,claim::text FROM research_repairs WHERE id=$1`, run.ID).Scan(&old.generation, &old.token); e != nil {
						t.Fatal(e)
					}
					if _, e = f.DB.Exec(ctx, `UPDATE research_repairs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, run.ID); e != nil {
						t.Fatal(e)
					}
					if e = s.authority(old).Guard(ctx, func() error { t.Error("stale authority dispatched"); return nil }); !errors.Is(e, ErrDenied) {
						t.Fatal("stale fence", e)
					}
					if _, e = f.Workspace.ApplyWithAuthority(ctx, "owner", run.Workspace, i.Patch, s.authority(old)); !errors.Is(e, ErrDenied) {
						t.Fatal("stale child patch", e)
					}
					run, e = s.Resume(ctx, "owner", run.ID)
					if e != nil || run.Generation <= old.generation {
						t.Fatal("recovery failed", e)
					}
				}
				if e != nil || len(run.History) != ordinal {
					t.Fatal("repair resume", e)
				}
				i = run.History[ordinal-1]
				real, e := v.InspectValidation(ctx, "owner", i.Validation)
				if e != nil || i.Version == i.Base || i.Version != real.Version || i.ReportDigest != real.ReportDigest || real.Profile != baseline.Profile || real.Image != baseline.Image || len(real.Attempts) != 1 {
					t.Fatal("bad revalidation", e)
				}
				t.Logf("iteration=%d state=%s version=%s validation=%s outcome=%s elapsed=%s attempts=%d generation=%d", ordinal, run.State, i.Version, i.Validation, i.Outcome, real.Report.Elapsed, len(real.Attempts), run.Generation)
				if ordinal < tc.iterations && run.State != "ITERATION_FAILED" {
					t.Fatal("first incorrect fix did not remain a failure")
				}
				previous = i.Patch
			}
			if run.State != tc.final || run.ModelCalls != tc.iterations || len(run.History) != run.Iterations {
				t.Fatal("wrong final status", run.State)
			}
			sealed, e := s.Resume(ctx, "owner", run.ID)
			if e != nil || sealed.EvidenceDigest != run.EvidenceDigest {
				t.Fatal("terminal result replay", e)
			}
			if tc.final == "BUDGET_EXHAUSTED" {
				if _, e = s.Propose(ctx, "owner", run.ID, nil); e == nil {
					t.Fatal("third iteration accepted")
				}
			} else if _, e = s.Start(ctx, "owner", run.Validation, "value.go", tc.goal); !errors.Is(e, ErrPolicy) {
				t.Fatal("PASS started another repair", e)
			}
			f.AssertUnchanged()
			t.Logf("final=%s calls=%d bytes=%d evidence=%s elapsed=%s", run.State, run.ModelCalls, run.Bytes, run.EvidenceDigest, time.Since(start))
		})
	}
}

func checkStoppedApprovals(t *testing.T, ctx context.Context, s Service, baseline validation.Request, source string) {
	t.Helper()
	if _, e := s.DB.Exec(ctx, `UPDATE research_validations SET result_sha256=$2 WHERE id=$1`, baseline.ID, strings.Repeat("0", 64)); e == nil {
		t.Fatal("forged final report digest was accepted")
	}
	s.Proposer = nil
	for index, action := range []string{"expire", "deny", "cancel"} {
		r, e := s.Start(ctx, "owner", baseline.ID, "value.go", "manual approval test")
		if e != nil {
			t.Fatal(e)
		}
		after := strings.Replace(source, "return 1", "return "+string(rune('7'+index)), 1)
		p := workspace.Patch{Base: baseline.Version, Changes: []workspace.Change{{Kind: "MODIFY", Path: "value.go", Mode: "100644", Before: ingest.Digest([]byte(source)), BeforeSize: len(source), After: ingest.Digest([]byte(after)), AfterSize: len(after), Text: after}}}
		r, e = s.Propose(ctx, "owner", r.ID, &p)
		if e != nil {
			t.Fatal(e)
		}
		digest := r.History[0].Patch
		want := "APPROVAL_DENIED"
		switch action {
		case "expire":
			_, e = s.DB.Exec(ctx, `UPDATE research_edit_approvals SET expires_at=clock_timestamp()-interval '1 second' WHERE workspace=$1 AND digest=$2`, r.Workspace, digest)
		case "deny":
			_, e = s.Decide(ctx, "owner", r.ID, digest, "deny")
		case "cancel":
			e = s.Cancel(ctx, "owner", r.ID)
			want = "CANCELLED"
		}
		if e != nil {
			t.Fatal(e)
		}
		r, e = s.Resume(ctx, "owner", r.ID)
		if e != nil || r.State != want || r.Version != baseline.Version {
			t.Fatalf("%s bypassed stop: %s %v", action, r.State, e)
		}
	}
	limited, e := s.Start(ctx, "owner", baseline.ID, "value.go", "verify proposer cap")
	if e != nil {
		t.Fatal(e)
	}
	invalid := workspace.Patch{Base: baseline.Version}
	for i := 0; i < 4; i++ {
		if _, e = s.Propose(ctx, "owner", limited.ID, &invalid); !errors.Is(e, ErrPolicy) {
			t.Fatal("invalid proposal accepted", e)
		}
	}
	limited, e = s.Propose(ctx, "owner", limited.ID, &invalid)
	if e != nil || limited.State != "BUDGET_EXHAUSTED" || limited.ModelCalls != 4 || len(limited.History) > 0 {
		t.Fatal("proposer ceiling reset", limited.State, e)
	}
}

func TestS20RetainedReview(t *testing.T) {
	url := os.Getenv("S20_RETAINED_DATABASE_URL")
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
	const owner = "aman-s12"
	const commit = "56ea76e421eca19066362fdecedd4e9aae04521e"
	var id, base string
	if e = db.QueryRow(ctx, `SELECT id::text,selected FROM research_workspaces WHERE owner=$1 AND commit_oid=$2 AND selected IS NOT NULL AND expires_at>clock_timestamp() ORDER BY created_at LIMIT 1`, owner, commit).Scan(&id, &base); e != nil {
		t.Fatal(e)
	}
	m := workspace.WorkspaceManager{DB: db}
	// Set up a disposable failing root fixture using a separate trusted patch.
	const tests = "package fixture\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value()!=2 { t.Fatalf(\"got %d, want 2\",Value()) } }\n"
	p, e := m.Propose(ctx, owner, id, workspace.Patch{Base: base, Changes: []workspace.Change{
		{Kind: "ADD", Path: "value.go", Mode: "100644", Text: fixtureOne, After: ingest.Digest([]byte(fixtureOne)), AfterSize: len(fixtureOne)},
		{Kind: "ADD", Path: "value_test.go", Mode: "100644", Text: tests, After: ingest.Digest([]byte(tests)), AfterSize: len(tests)},
	}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.RequestPatchApproval(ctx, owner, id, p.Digest); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Decide(ctx, owner, id, p.Digest, "grant"); e != nil {
		t.Fatal(e)
	}
	setup, e := m.ApplyApprovedPatch(ctx, owner, id, p.Digest)
	if e != nil {
		t.Fatal(e)
	}
	v := validation.Service{DB: db, Workspace: m, Docker: repotest.Docker(t)}
	req, e := v.RequestValidation(ctx, owner, id, setup.Version, "go-unit")
	if e != nil {
		t.Fatal(e)
	}
	observed, e := v.Run(ctx, owner, req.ID)
	if e != nil || observed.Outcome != "TEST_FAILURE" {
		t.Fatal("retained fixture did not fail", observed.Outcome, e)
	}
	s := Service{DB: db, Workspace: m, Validation: v, Proposer: Fixture{}}
	run, e := s.Start(ctx, owner, req.ID, "value.go", "fixture-expect-two")
	if e != nil {
		t.Fatal(e)
	}
	run, e = s.Propose(ctx, owner, run.ID, nil)
	if e != nil || run.State != "WAITING_APPROVAL" || run.Version != setup.Version {
		t.Fatal("retained review missing", e)
	}
	checked, e := s.Resume(ctx, owner, run.ID)
	if e != nil || checked.State != "WAITING_APPROVAL" || checked.Version != setup.Version {
		t.Fatal("retained proposal applied", e)
	}
	t.Logf("retained commit=%s source_bytes=%d validation=%s failure=%s review_patch=%s selected_unchanged=true", commit, observed.Report.Phases.Bytes, observed.ID, observed.Outcome, checked.History[0].Patch)
}
