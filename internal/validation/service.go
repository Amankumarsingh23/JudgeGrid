package validation

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrDenied = errors.New("validation authority unavailable")
	ErrBusy   = errors.New("validation has an active claim")
	ErrState  = errors.New("validation state unavailable")
)

const Policy = "go-validation-v1"

var idPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Service struct {
	DB        *pgxpool.Pool
	Workspace workspace.WorkspaceManager
	Docker    *execution.Docker
}

type Request struct {
	ID           string    `json:"id"`
	Workspace    string    `json:"workspace"`
	Version      string    `json:"version"`
	Profile      string    `json:"profile"`
	Policy       string    `json:"policy"`
	Image        string    `json:"image"`
	State        string    `json:"state"`
	Outcome      string    `json:"outcome,omitempty"`
	Generation   int64     `json:"generation"`
	Deadline     time.Time `json:"deadline"`
	Report       *Report   `json:"report,omitempty"`
	ReportDigest string    `json:"report_sha256,omitempty"`
	Attempts     []Attempt `json:"attempts,omitempty"`
}
type Attempt struct {
	Generation      int64  `json:"generation"`
	Sandbox         string `json:"sandbox"`
	CleanupRequired bool   `json:"cleanup_required"`
}
type Report struct {
	Validation      string                      `json:"validation"`
	Version         string                      `json:"version"`
	Profile         string                      `json:"profile"`
	Toolchain       string                      `json:"toolchain_reference"`
	Status          string                      `json:"outcome"`
	Failure         string                      `json:"failure_class,omitempty"`
	Runtime         execution.ValidationRuntime `json:"runtime"`
	Phases          execution.ValidationPhases  `json:"phases"`
	Materialization time.Duration               `json:"verify_ns"`
	Elapsed         time.Duration               `json:"total_ns"`
	Truncated       bool                        `json:"display_truncated"`
	LogDigest       string                      `json:"log_sha256"`
}

const fields = `id,workspace::text,version,profile,policy,image,state,generation,deadline,result,coalesce(result_sha256,'')`

func checksum(v any) string {
	b, _ := json.Marshal(v)
	return ingest.Digest(b)
}

func scan(row pgx.Row) (r Request, e error) {
	e = row.Scan(&r.ID, &r.Workspace, &r.Version, &r.Profile, &r.Policy, &r.Image, &r.State, &r.Generation, &r.Deadline, &r.Report, &r.ReportDigest)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrDenied
	}
	if e == nil && r.Report != nil && (checksum(r.Report) != r.ReportDigest || r.Report.LogDigest != checksum(reportLogs(r.Report.Phases))) {
		e = ErrState
	}
	if r.Report != nil {
		r.Outcome = r.Report.Status
	}
	if r.State == "CANCELLED" {
		r.Outcome = "CANCELLED"
	}
	return
}

func reportLogs(p execution.ValidationPhases) []string {
	var out []string
	for _, phase := range []*execution.PhaseResult{p.Stage, p.Run} {
		if phase != nil {
			out = append(out, phase.Stdout, phase.Stderr)
		}
	}
	return out
}

// RequestValidation is called only by the authenticated opt-in operator. It is
// a separate authority transaction from any edit or research approval.
func (s Service) RequestValidation(ctx context.Context, owner, id, version, profile string) (Request, error) {
	if owner == "" || !idPattern.MatchString(version) || !execution.ValidationProfile(profile) || s.Docker == nil {
		return Request{}, ErrDenied
	}
	w, e := s.Workspace.InspectWorkspaceVersion(ctx, owner, id)
	if e != nil {
		return Request{}, e
	}
	if w.Selected != version {
		return Request{}, workspace.ErrConflict
	}
	runtime, e := s.Docker.ValidationRuntime(ctx)
	if e != nil {
		return Request{}, e
	}
	policy := checksum([]string{Policy, execution.ValidationContract(), runtime.StageImage})
	identity := checksum([]string{owner, w.Repository, id, version, profile, policy, runtime.Image})
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Request{}, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('validation:'||$1,0))`, owner); e != nil {
		return Request{}, e
	}
	r, e := scan(tx.QueryRow(ctx, "SELECT "+fields+" FROM research_validations WHERE id=$1 AND owner=$2", identity, owner))
	if e == nil {
		return r, tx.Commit(ctx)
	}
	if !errors.Is(e, ErrDenied) {
		return r, e
	}
	var allowed bool
	e = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state IN ('PENDING','MATERIALIZING','RUNNING') AND deadline>clock_timestamp())<2 AND count(*)<12
 FROM research_validations WHERE owner=$1 AND created_at>clock_timestamp()-interval '1 hour'`, owner).Scan(&allowed)
	if e != nil {
		return r, e
	}
	if !allowed {
		return r, ErrBusy
	}
	r, e = scan(tx.QueryRow(ctx, `INSERT INTO research_validations(id,owner,workspace,version,profile,policy,image)
 SELECT $1,owner,id,$4,$5,$6,$7 FROM research_workspaces WHERE id=$2 AND owner=$3 AND selected=$4 AND expires_at>clock_timestamp()
 RETURNING `+fields, identity, id, owner, version, profile, policy, runtime.Image))
	if e != nil {
		return r, e
	}
	return r, tx.Commit(ctx)
}

func (s Service) InspectValidation(ctx context.Context, owner, id string) (Request, error) {
	if owner == "" || !idPattern.MatchString(id) {
		return Request{}, ErrDenied
	}
	r, e := scan(s.DB.QueryRow(ctx, "SELECT "+fields+" FROM research_validations WHERE owner=$1 AND id=$2", owner, id))
	if e != nil {
		return r, e
	}
	rows, e := s.DB.Query(ctx, `SELECT generation,sandbox,cleanup_required FROM research_validation_attempts WHERE validation=$1 ORDER BY generation`, id)
	if e != nil {
		return r, e
	}
	defer rows.Close()
	for rows.Next() {
		var a Attempt
		if e = rows.Scan(&a.Generation, &a.Sandbox, &a.CleanupRequired); e != nil {
			return r, e
		}
		r.Attempts = append(r.Attempts, a)
	}
	return r, rows.Err()
}

type claim struct {
	owner, id, token string
	generation       int64
	sandbox          string
}

func (s Service) acquire(ctx context.Context, owner, id string) (claim, error) {
	c := claim{owner: owner, id: id, token: uuid.NewString(), sandbox: "jg-v19-" + strings.ReplaceAll(uuid.NewString(), "-", "")}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return c, e
	}
	defer tx.Rollback(ctx)
	e = tx.QueryRow(ctx, `UPDATE research_validations SET state='MATERIALIZING',claim=$3,generation=generation+1,lease_until=least(deadline,clock_timestamp()+interval '4 minutes')
 WHERE id=$1 AND owner=$2 AND generation<3 AND deadline>clock_timestamp()
 AND (state='PENDING' OR (state IN ('MATERIALIZING','RUNNING') AND lease_until<=clock_timestamp()))
 RETURNING generation`, id, owner, c.token).Scan(&c.generation)
	if errors.Is(e, pgx.ErrNoRows) {
		if _, e = tx.Exec(ctx, `UPDATE research_validations SET state=CASE WHEN deadline<=clock_timestamp() THEN 'EXPIRED' ELSE 'FAILED' END,claim=NULL,lease_until=NULL
  WHERE id=$1 AND owner=$2 AND state IN ('PENDING','MATERIALIZING','RUNNING') AND (lease_until IS NULL OR lease_until<=clock_timestamp()) AND (deadline<=clock_timestamp() OR generation>=3)`, id, owner); e != nil {
			return c, e
		}
		if e = tx.Commit(ctx); e != nil {
			return c, e
		}
		return c, ErrBusy
	}
	if e != nil {
		return c, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO research_validation_attempts(validation,generation,sandbox) VALUES($1,$2,$3)`, id, c.generation, c.sandbox)
	if e != nil {
		return c, e
	}
	return c, tx.Commit(ctx)
}

func (s Service) fenced(ctx context.Context, c claim, fn func(pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var permitted bool
	e = tx.QueryRow(ctx, `SELECT true FROM research_validations WHERE id=$1 AND owner=$2 AND claim=$3 AND generation=$4 AND state IN ('MATERIALIZING','RUNNING')
 AND lease_until>clock_timestamp() AND deadline>clock_timestamp() FOR UPDATE`, c.id, c.owner, c.token, c.generation).Scan(&permitted)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrDenied
	}
	if e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s Service) markClean(ctx context.Context, id, name string) error {
	_, e := s.DB.Exec(ctx, `UPDATE research_validation_attempts SET cleanup_required=false WHERE validation=$1 AND sandbox=$2`, id, name)
	return e
}

func (s Service) reconcile(ctx context.Context, r Request, current int64) error {
	for _, attempt := range r.Attempts {
		if attempt.CleanupRequired && attempt.Generation != current {
			if e := s.Docker.CleanupValidation(ctx, attempt.Sandbox); e != nil {
				return e
			}
			if e := s.markClean(ctx, r.ID, attempt.Sandbox); e != nil {
				return e
			}
		}
	}
	return nil
}

func (s Service) CancelValidation(ctx context.Context, owner, id string) error {
	r, e := s.InspectValidation(ctx, owner, id)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec(ctx, `UPDATE research_validations SET state='CANCELLED',claim=NULL,lease_until=NULL WHERE id=$1 AND owner=$2 AND state IN ('PENDING','MATERIALIZING','RUNNING')`, id, owner)
	if e != nil {
		return e
	}
	r, e = s.InspectValidation(ctx, owner, id)
	if e != nil {
		return e
	}
	if r.State == "CANCELLED" || r.State == "FAILED" || r.State == "EXPIRED" || r.State == "COMPLETED" {
		return s.reconcile(ctx, r, 0)
	}
	return nil
}

func (s Service) Run(ctx context.Context, owner, id string) (r Request, err error) {
	start := time.Now()
	defer func() {
		telemetry.Observe("validation.run", telemetry.Class(err), "", time.Since(start).Seconds())
	}()
	return s.run(ctx, owner, id, nil)
}
func (s Service) run(ctx context.Context, owner, id string, beforePublish func() error) (Request, error) {
	start := time.Now()
	r, e := s.InspectValidation(ctx, owner, id)
	if e != nil || r.State == "COMPLETED" {
		return r, e
	}
	c, e := s.acquire(ctx, owner, id)
	if e != nil {
		return r, e
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	r, e = s.InspectValidation(ctx, owner, id)
	if e != nil {
		return r, e
	}
	runtime, e := s.Docker.ValidationRuntime(ctx)
	if e != nil {
		return r, e
	}
	actualPolicy := checksum([]string{Policy, execution.ValidationContract(), runtime.StageImage})
	if runtime.Image != r.Image || actualPolicy != r.Policy {
		return r, ErrDenied
	}
	// Previous generations are fenced out before their exact resources are removed.
	if e = s.reconcile(ctx, r, c.generation); e != nil {
		return r, e
	}
	report := Report{Validation: id, Version: r.Version, Profile: r.Profile, Toolchain: execution.ValidationGoRef, Runtime: runtime}
	material := time.Now()
	stage, stop := context.WithTimeout(ctx, 25*time.Second)
	_, files, sourceErr := s.Workspace.ExportSelected(stage, owner, r.Workspace, r.Version)
	stop()
	report.Materialization = time.Since(material)
	if sourceErr != nil {
		if errors.Is(sourceErr, workspace.ErrConflict) || errors.Is(sourceErr, workspace.ErrDenied) {
			report.Status = "POLICY_DENIED"
		} else {
			report.Status = "INFRA_FAILURE"
		}
		report.Failure = "source"
		if e = s.markClean(ctx, id, c.sandbox); e != nil {
			return r, e
		}
	} else {
		workload, allowed := prepare(r.Profile, files)
		if !allowed {
			report.Status, report.Failure = "POLICY_DENIED", "profile-input"
			if e = s.markClean(ctx, id, c.sandbox); e != nil {
				return r, e
			}
		} else {
			guard := func(work context.Context, fn func() error) error {
				return s.fenced(work, c, func(tx pgx.Tx) error {
					var selected bool
					e := tx.QueryRow(work, `SELECT EXISTS(SELECT 1 FROM research_workspaces WHERE id=$1 AND owner=$2 AND selected=$3 AND expires_at>clock_timestamp())`, r.Workspace, owner, r.Version).Scan(&selected)
					if e != nil {
						return e
					}
					if !selected {
						return ErrDenied
					}
					return fn()
				})
			}
			if e = s.fenced(ctx, c, func(tx pgx.Tx) error {
				_, e := tx.Exec(ctx, `UPDATE research_validations SET state='RUNNING' WHERE id=$1`, id)
				return e
			}); e != nil {
				return r, e
			}
			report.Phases, e = s.Docker.ExecuteValidation(ctx, c.sandbox, r.Profile, runtime, workload, guard)
			report.Status = classify(r.Profile, report.Phases, e)
			if e != nil {
				report.Failure = "sandbox"
			}
			if report.Phases.Cleaned {
				if e = s.markClean(ctx, id, c.sandbox); e != nil {
					return r, e
				}
			}
		}
	}
	if beforePublish != nil {
		if e = beforePublish(); e != nil {
			return r, e
		}
	}
	report.Elapsed = time.Since(start)
	for _, phase := range []*execution.PhaseResult{report.Phases.Stage, report.Phases.Run} {
		if phase == nil {
			continue
		}
		before, stderr := phase.Stdout, phase.Stderr
		phase.Stdout = sanitize(before)
		phase.Stderr = sanitize(stderr)
		report.Truncated = report.Truncated || len(before) != len(phase.Stdout) || len(stderr) != len(phase.Stderr) || phase.Outcome == execution.OutputExceeded
	}
	report.LogDigest = checksum(reportLogs(report.Phases))
	e = s.fenced(ctx, c, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE research_validations SET state='COMPLETED',result=$4,result_sha256=$5,claim=NULL,lease_until=NULL
  WHERE id=$1 AND generation=$2 AND claim=$3 AND lease_until>clock_timestamp() AND deadline>clock_timestamp()`, id, c.generation, c.token, report, checksum(report))
		if e == nil && tag.RowsAffected() != 1 {
			return ErrDenied
		}
		return e
	})
	if e != nil {
		return r, e
	}
	return s.InspectValidation(ctx, owner, id)
}

func prepare(profile string, files []workspace.VerifiedFile) ([]execution.ValidationFile, bool) {
	out := make([]execution.ValidationFile, 0, len(files))
	hasMod, hasGo, hasTest := false, false, false
	for _, f := range files {
		if f.Mode != "100644" && f.Mode != "100755" {
			return nil, false
		}
		out = append(out, execution.ValidationFile{Path: f.Path, SHA256: f.Hash, Bytes: f.Bytes})
		if f.Path == "go.mod" {
			hasMod = true
		}
		if path.Ext(f.Path) == ".go" {
			hasGo = true
		}
		if !strings.Contains(f.Path, "/") && strings.HasSuffix(f.Path, "_test.go") {
			hasTest = true
		}
	}
	return out, hasGo && (profile == "go-format-check" || profile == "go-unit" && hasMod && hasTest)
}

func classify(profile string, p execution.ValidationPhases, err error) string {
	if err != nil || p.Run == nil || !p.Cleaned {
		return "INFRA_FAILURE"
	}
	r := p.Run
	switch r.Outcome {
	case execution.WallTimeout:
		return "TIMEOUT"
	case execution.MemoryExceeded:
		return "MEMORY_LIMIT"
	case execution.OutputExceeded:
		return "OUTPUT_LIMIT"
	case execution.Canceled:
		return "CANCELLED"
	}
	if profile == "go-format-check" {
		if r.ExitCode != 0 {
			return "BUILD_FAILURE"
		}
		if r.Stdout != "" {
			return "TEST_FAILURE"
		}
		return "PASS"
	}
	tests := false
	for _, line := range strings.Split(r.Stdout, "\n") {
		var v struct{ Action, Test, FailedBuild string }
		if json.Unmarshal([]byte(line), &v) != nil {
			continue
		}
		if v.Action == "build-fail" || v.FailedBuild != "" {
			return "BUILD_FAILURE"
		}
		if v.Test != "" && (v.Action == "pass" || v.Action == "fail") {
			tests = true
		}
	}
	if r.ExitCode == 0 && tests {
		return "PASS"
	}
	if tests {
		return "TEST_FAILURE"
	}
	if r.ExitCode != 0 {
		return "BUILD_FAILURE"
	}
	return "POLICY_DENIED"
}

func sanitize(in string) string {
	var out strings.Builder
	column := 0
	for _, r := range in {
		if r == '\n' {
			out.WriteRune(r)
			column = 0
			continue
		}
		if column >= 2048 || unicode.IsControl(r) && r != '\t' || unicode.Is(unicode.Cf, r) {
			continue
		}
		column++
		out.WriteRune(r)
	}
	return out.String()
}
