//go:build validationintegration

package validation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/repotest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestS19ApprovedValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if os.Getenv("JUDGEGRID_VALIDATION_TESTS") != "1" {
		t.Fatal("explicit validation integration environment required")
	}
	const original = "package fixture\n\nfunc Value() int { return 1 }\n"
	const fixed = "package fixture\n\nfunc Value() int { return 2 }\n"
	fixture := repotest.Open(t, ctx, original)
	db, wmgr, w, d := fixture.DB, fixture.Workspace, fixture.Selected, fixture.Docker
	s := Service{DB: db, Workspace: wmgr, Docker: d}
	var e error
	h := strings.Repeat("a", 64)
	if _, e = s.RequestValidation(ctx, "foreign", w.ID, w.Selected, "go-unit"); e == nil {
		t.Fatal("foreign scope accepted")
	}
	if _, e = s.RequestValidation(ctx, "owner", w.ID, w.Selected, "execute_command"); e == nil {
		t.Fatal("arbitrary command accepted")
	}
	if _, e = s.RequestValidation(ctx, "owner", w.ID, h, "go-unit"); e == nil {
		t.Fatal("wrong version accepted")
	}

	request := func(version, profile string) Request {
		t.Helper()
		r, e := s.RequestValidation(ctx, "owner", w.ID, version, profile)
		if e != nil {
			t.Fatal(e)
		}
		replay, e := s.RequestValidation(ctx, "owner", w.ID, version, profile)
		if e != nil || replay.ID != r.ID {
			t.Fatal("request replay", e)
		}
		return r
	}
	verify := func(r Request, want string) Request {
		t.Helper()
		got, e := s.Run(ctx, "owner", r.ID)
		if e != nil || got.Report == nil || got.Report.Status != want || len(got.Attempts) == 0 || got.Attempts[len(got.Attempts)-1].CleanupRequired {
			t.Fatalf("wanted %s: %+v %v", want, got.Report, e)
		}
		t.Logf("%s version %.12s exit=%d verify=%s stage=%s run=%s total=%s log=%s", want, r.Version, got.Report.Phases.Run.ExitCode, got.Report.Materialization, got.Report.Phases.Stage.WallTime, got.Report.Phases.Run.WallTime, got.Report.Elapsed, got.Report.LogDigest)
		return got
	}
	v1 := request(w.Selected, "go-unit")
	failed := verify(v1, "TEST_FAILURE")
	if !strings.Contains(failed.Report.Phases.Run.Stdout, "got 1, want 2") {
		t.Fatal("actual failure evidence missing")
	}
	if _, e = s.InspectValidation(ctx, "foreign", v1.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("foreign log read", e)
	}
	patch := func(base, before, after string) string {
		t.Helper()
		p, e := wmgr.Propose(ctx, "owner", w.ID, workspace.Patch{Base: base, Changes: []workspace.Change{{Kind: "MODIFY", Mode: "100644", Path: "value.go", Before: ingest.Digest([]byte(before)), BeforeSize: len(before), After: ingest.Digest([]byte(after)), AfterSize: len(after), Text: after}}})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = wmgr.ApplyApprovedPatch(ctx, "owner", w.ID, p.Digest); e == nil {
			t.Fatal("unapproved edit applied")
		}
		if _, e = wmgr.RequestPatchApproval(ctx, "owner", w.ID, p.Digest); e != nil {
			t.Fatal(e)
		}
		if _, e = wmgr.Decide(ctx, "owner", w.ID, p.Digest, "grant"); e != nil {
			t.Fatal(e)
		}
		edit, e := wmgr.ApplyApprovedPatch(ctx, "owner", w.ID, p.Digest)
		if e != nil {
			t.Fatal(e)
		}
		return edit.Version
	}
	v2 := patch(w.Selected, original, fixed)
	req := request(v2, "go-unit")
	crash := errors.New("simulated loss after execution before report commit")
	if _, e = s.run(ctx, "owner", req.ID, func() error { return crash }); !errors.Is(e, crash) {
		t.Fatal(e)
	}
	before, e := s.InspectValidation(ctx, "owner", req.ID)
	if e != nil || before.Report != nil || len(before.Attempts) != 1 {
		t.Fatal("incomplete report became authoritative", e)
	}
	var token string
	if e = db.QueryRow(ctx, `SELECT claim::text FROM research_validations WHERE id=$1`, req.ID).Scan(&token); e != nil {
		t.Fatal(e)
	}
	old := before.Attempts[0]
	orphan := old.Sandbox + "-run"
	docker := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", args...)
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("trusted Docker fixture: %v %s", e, out)
		}
	}
	docker("run", "--detach", "--name", orphan, "--network", "none", "--read-only", "--cap-drop", "ALL", "--user", "10001:10001", "--label", "judgegrid.validation="+old.Sandbox, execution.ValidationGoRef, "/bin/sleep", "120")
	defer d.CleanupValidation(context.Background(), old.Sandbox)
	if _, e = db.Exec(ctx, `UPDATE research_validation_attempts SET cleanup_required=true WHERE validation=$1 AND generation=1`, req.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, `UPDATE research_validations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, req.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.fenced(ctx, claim{owner: "owner", id: req.ID, token: token, generation: 1}, func(pgx.Tx) error { return nil }); !errors.Is(e, ErrDenied) {
		t.Fatal("expired worker retained write authority", e)
	}
	passed := verify(req, "PASS")
	if len(passed.Attempts) != 2 || passed.Attempts[0].CleanupRequired || passed.Generation != 2 {
		t.Fatal("recovery authority invalid")
	}
	if _, e = execution.InspectResource(ctx, "container", orphan); e == nil {
		t.Fatal("old resource still exists")
	}
	again, e := s.Run(ctx, "owner", req.ID)
	if e != nil || again.ReportDigest != passed.ReportDigest || len(again.Attempts) != 2 {
		t.Fatal("published result not idempotent", e)
	}
	t.Logf("recovery generations=1/2 old resource removed one result=%s", passed.ReportDigest)
	if passed.ID == failed.ID {
		t.Fatal("distinct versions reused same result")
	}
	format := request(v2, "go-format-check")
	if format.ID == passed.ID {
		t.Fatal("distinct profiles shared identity")
	}
	if e = s.CancelValidation(ctx, "owner", format.ID); e != nil {
		t.Fatal(e)
	}
	cancelled, e := s.InspectValidation(ctx, "owner", format.ID)
	if e != nil || cancelled.State != "CANCELLED" || len(cancelled.Attempts) != 0 {
		t.Fatal("pending cancellation failed", e)
	}
	v3 := patch(v2, fixed, "package fixture\nfunc Value( {\n")
	verify(request(v3, "go-unit"), "BUILD_FAILURE")
	fixture.AssertUnchanged()
}

func TestS19RetainedFormat(t *testing.T) {
	url := os.Getenv("S19_RETAINED_DATABASE_URL")
	if url == "" {
		t.Skip("retained operator database test only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
	const body = "This private note records the bounded format-validation demo.\n"
	p, e := m.Propose(ctx, owner, id, workspace.Patch{Base: base, Changes: []workspace.Change{{Kind: "ADD", Path: "docs/session-19-validation-demo.txt", Mode: "100644", After: ingest.Digest([]byte(body)), AfterSize: len(body), Text: body}}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.RequestPatchApproval(ctx, owner, id, p.Digest); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Decide(ctx, owner, id, p.Digest, "grant"); e != nil {
		t.Fatal(e)
	}
	edit, e := m.ApplyApprovedPatch(ctx, owner, id, p.Digest)
	if e != nil {
		t.Fatal(e)
	}
	s := Service{DB: db, Workspace: m, Docker: repotest.Docker(t)}
	r, e := s.RequestValidation(ctx, owner, id, edit.Version, "go-format-check")
	if e != nil {
		t.Fatal(e)
	}
	result, e := s.Run(ctx, owner, r.ID)
	if e != nil || result.Report == nil || result.Report.Status != "PASS" {
		t.Fatalf("retained profile: %+v %v", result.Report, e)
	}
	t.Logf("retained commit=%s patch=%s version=%s profile=%s result=%s bytes=%d runtime=%v log=%s", commit, p.Digest, edit.Version, r.Profile, result.Report.Status, result.Report.Phases.Bytes, result.Report.Elapsed, result.Report.LogDigest)
}
