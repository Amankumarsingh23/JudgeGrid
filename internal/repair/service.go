package repair

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/internal/validation"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	DB         *pgxpool.Pool
	Workspace  workspace.WorkspaceManager
	Validation validation.Service
	Proposer   RepairProposer
	Parent     workspace.Authority
}
type Run struct {
	ID               string      `json:"id"`
	Workspace        string      `json:"workspace"`
	ResearchRun      string      `json:"research_run"`
	Repository       string      `json:"repository"`
	Commit           string      `json:"commit"`
	Snapshot         string      `json:"snapshot"`
	Base             string      `json:"base_version"`
	Baseline         string      `json:"baseline_validation"`
	BaselineDigest   string      `json:"baseline_sha256"`
	Profile          string      `json:"profile"`
	ValidationPolicy string      `json:"validation_policy"`
	Image            string      `json:"image"`
	Planner          string      `json:"planner"`
	Policy           string      `json:"policy"`
	Target           string      `json:"target"`
	Requirement      string      `json:"requirement"`
	Version          string      `json:"current_version"`
	Validation       string      `json:"current_validation"`
	State            string      `json:"state"`
	Iterations       int         `json:"iterations"`
	ModelCalls       int         `json:"proposer_calls"`
	Bytes            int         `json:"attempted_bytes"`
	Generation       int64       `json:"generation"`
	Created          time.Time   `json:"created_at"`
	Deadline         time.Time   `json:"deadline"`
	History          []Iteration `json:"history,omitempty"`
	Events           []Event     `json:"events,omitempty"`
	EvidenceDigest   string      `json:"evidence_sha256,omitempty"`
}

type Event struct {
	Generation int64     `json:"generation"`
	Ordinal    int       `json:"ordinal"`
	Action     string    `json:"action"`
	Evidence   string    `json:"evidence,omitempty"`
	At         time.Time `json:"at"`
}

type Iteration struct {
	Ordinal        int       `json:"ordinal"`
	Patch          string    `json:"patch_sha256"`
	Base           string    `json:"base_version"`
	Baseline       string    `json:"baseline_validation"`
	BaselineDigest string    `json:"baseline_report_sha256"`
	SourceDigest   string    `json:"source_sha256"`
	Created        time.Time `json:"proposed_at"`
	ProposalNS     int64     `json:"proposal_ns"`
	Version        string    `json:"new_version,omitempty"`
	Validation     string    `json:"validation,omitempty"`
	ReportDigest   string    `json:"report_sha256,omitempty"`
	Outcome        string    `json:"outcome,omitempty"`
}

const columns = `id::text,workspace::text,research_run::text,repository::text,base_version,baseline,baseline_digest,profile,validation_policy,image,planner,policy,target,requirement,current_version,current_validation,state,iterations,model_calls,attempted_bytes,generation,created_at,deadline`
const active = `('BASELINE_VERIFIED','DIAGNOSING','WAITING_APPROVAL','APPLYING','VALIDATING','ITERATION_FAILED')`

func scan(row pgx.Row) (r Run, e error) {
	e = row.Scan(&r.ID, &r.Workspace, &r.ResearchRun, &r.Repository, &r.Base, &r.Baseline, &r.BaselineDigest, &r.Profile, &r.ValidationPolicy, &r.Image, &r.Planner, &r.Policy, &r.Target, &r.Requirement, &r.Version, &r.Validation, &r.State, &r.Iterations, &r.ModelCalls, &r.Bytes, &r.Generation, &r.Created, &r.Deadline)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrDenied
	}
	r.Created, r.Deadline = r.Created.UTC(), r.Deadline.UTC()
	return
}

func validID(id string) bool {
	parsed, e := uuid.Parse(id)
	return e == nil && parsed.String() == id
}

func (s Service) Start(ctx context.Context, owner, validationID, target, requirement string) (Run, error) {
	if owner == "" || !targetAllowed(target) || len(requirement) < 1 || len(requirement) > 256 || !utf8.ValidString(requirement) || strings.ContainsAny(requirement, "\x00\r\n") {
		return Run{}, ErrPolicy
	}
	baseline, e := s.Validation.InspectValidation(ctx, owner, validationID)
	if e != nil {
		return Run{}, e
	}
	if !verifiedGoReport(baseline) || !actionable(baseline.Outcome) {
		return Run{}, ErrPolicy
	}
	w, e := s.Workspace.InspectWorkspaceVersion(ctx, owner, baseline.Workspace)
	if e != nil {
		return Run{}, e
	}
	if w.Selected != baseline.Version {
		return Run{}, workspace.ErrConflict
	}
	planner := ManualPolicy
	if s.Proposer != nil {
		planner = s.Proposer.Identity()
		if planner == "" || len(planner) > 128 {
			return Run{}, ErrPolicy
		}
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Run{}, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('repair:'||$1,0))`, owner); e != nil {
		return Run{}, e
	}
	if _, e = tx.Exec(ctx, `UPDATE research_repairs SET state='BUDGET_EXHAUSTED',claim=NULL,lease_until=NULL WHERE owner=$1 AND deadline<=clock_timestamp() AND state IN `+active, owner); e != nil {
		return Run{}, e
	}
	existing, e := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM research_repairs WHERE workspace=$1 AND owner=$2 AND state IN "+active, w.ID, owner))
	if e == nil {
		if existing.Baseline != baseline.ID || existing.Target != target || existing.Requirement != requirement || existing.Planner != planner {
			return Run{}, ErrBusy
		}
		existing.Commit, existing.Snapshot = w.Commit, w.Snapshot
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(e, ErrDenied) {
		return Run{}, e
	}
	var quota bool
	e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state IN `+active+`)<2 AND count(*)<6 FROM research_repairs WHERE owner=$1 AND created_at>clock_timestamp()-interval '1 hour'`, owner).Scan(&quota)
	if e != nil {
		return Run{}, e
	}
	if !quota {
		return Run{}, ErrBusy
	}
	r, e := scan(tx.QueryRow(ctx, `INSERT INTO research_repairs(owner,workspace,research_run,repository,base_version,baseline,baseline_digest,profile,validation_policy,image,planner,policy,target,requirement,current_version,current_validation)
 SELECT owner,id,run_id,repository,$4,$3,$5,$6,$7,$8,$9,$10,$11,$12,$4,$3 FROM research_workspaces
 WHERE id=$1 AND owner=$2 AND selected=$4 AND expires_at>clock_timestamp() RETURNING `+columns,
		w.ID, owner, baseline.ID, baseline.Version, baseline.ReportDigest, baseline.Profile, baseline.Policy, baseline.Image, planner, Policy, target, requirement))
	if e != nil {
		return r, e
	}
	if e = event(ctx, tx, r.ID, 0, 0, "baseline_verified", baseline.ReportDigest); e != nil {
		return r, e
	}
	r.Commit, r.Snapshot = w.Commit, w.Snapshot
	return r, tx.Commit(ctx)
}

func (s Service) Inspect(ctx context.Context, owner, id string) (Run, error) {
	if owner == "" || !validID(id) {
		return Run{}, ErrDenied
	}
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return Run{}, e
	}
	defer tx.Rollback(ctx)
	r, e := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM research_repairs WHERE id=$1 AND owner=$2", id, owner))
	if e != nil {
		return r, e
	}
	if e = tx.QueryRow(ctx, `SELECT commit_oid,snapshot FROM research_workspaces WHERE id=$1 AND owner=$2`, r.Workspace, owner).Scan(&r.Commit, &r.Snapshot); e != nil {
		return r, e
	}
	rows, e := tx.Query(ctx, `SELECT ordinal,patch,base_version,baseline,baseline_digest,source_digest,created_at,proposal_ns,
 coalesce(new_version,''),coalesce(validation,''),coalesce(report_sha256,''),coalesce(outcome,'')
 FROM research_repair_iterations WHERE repair=$1 ORDER BY ordinal`, id)
	if e != nil {
		return r, e
	}
	defer rows.Close()
	for rows.Next() {
		var i Iteration
		if e = rows.Scan(&i.Ordinal, &i.Patch, &i.Base, &i.Baseline, &i.BaselineDigest, &i.SourceDigest, &i.Created, &i.ProposalNS, &i.Version, &i.Validation, &i.ReportDigest, &i.Outcome); e != nil {
			return r, e
		}
		i.Created = i.Created.UTC()
		r.History = append(r.History, i)
	}
	if e = rows.Err(); e != nil {
		return r, e
	}
	events, e := tx.Query(ctx, `SELECT generation,ordinal,action,evidence,created_at FROM research_repair_events WHERE repair=$1 ORDER BY sequence`, id)
	if e != nil {
		return r, e
	}
	defer events.Close()
	for events.Next() {
		var item Event
		if e = events.Scan(&item.Generation, &item.Ordinal, &item.Action, &item.Evidence, &item.At); e != nil {
			return r, e
		}
		item.At = item.At.UTC()
		r.Events = append(r.Events, item)
	}
	if e = events.Err(); e != nil {
		return r, e
	}
	if e = tx.Commit(ctx); e != nil {
		return r, e
	}
	body, _ := json.Marshal(r)
	r.EvidenceDigest = ingest.Digest(body)
	return r, nil
}

func event(ctx context.Context, tx pgx.Tx, id string, generation int64, ordinal int, action, evidence string) error {
	_, e := tx.Exec(ctx, `INSERT INTO research_repair_events(repair,generation,ordinal,action,evidence) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, id, generation, ordinal, action, evidence)
	return e
}

type claim struct {
	owner, id, token string
	generation       int64
}

func (s Service) acquire(ctx context.Context, owner, id string) (claim, error) {
	c := claim{owner: owner, id: id, token: uuid.NewString()}
	if !validID(id) || owner == "" {
		return c, ErrDenied
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return c, e
	}
	defer tx.Rollback(ctx)
	e = tx.QueryRow(ctx, `UPDATE research_repairs SET generation=generation+1,claim=$3,lease_until=least(deadline,clock_timestamp()+interval '5 minutes')
 WHERE id=$1 AND owner=$2 AND deadline>clock_timestamp() AND generation<40 AND state IN `+active+`
 AND (claim IS NULL OR lease_until<=clock_timestamp()) RETURNING generation`, id, owner, c.token).Scan(&c.generation)
	if errors.Is(e, pgx.ErrNoRows) {
		if _, e = tx.Exec(ctx, `UPDATE research_repairs SET state='BUDGET_EXHAUSTED',claim=NULL,lease_until=NULL WHERE id=$1 AND owner=$2 AND (deadline<=clock_timestamp() OR (generation>=40 AND (claim IS NULL OR lease_until<=clock_timestamp()))) AND state IN `+active, id, owner); e != nil {
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
	if e = event(ctx, tx, id, c.generation, 0, "attempt_claimed", ""); e != nil {
		return c, e
	}
	return c, tx.Commit(ctx)
}

func (s Service) fenced(ctx context.Context, c claim, fn func(pgx.Tx) error) error {
	return s.Parent.Guard(ctx, func() error { return s.fencedChild(ctx, c, fn) })
}

func (s Service) fencedChild(ctx context.Context, c claim, fn func(pgx.Tx) error) error {
	bounded, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	tx, e := s.DB.Begin(bounded)
	if e != nil {
		return e
	}
	defer tx.Rollback(bounded)
	var ok bool
	e = tx.QueryRow(bounded, `SELECT true FROM research_repairs WHERE id=$1 AND owner=$2 AND claim=$3 AND generation=$4 AND deadline>clock_timestamp() AND lease_until>clock_timestamp() AND state IN `+active+` FOR UPDATE`, c.id, c.owner, c.token, c.generation).Scan(&ok)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrDenied
	}
	if e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(bounded)
}

func (s Service) authority(c claim) workspace.Authority {
	return func(ctx context.Context, fn func() error) error {
		return s.fenced(ctx, c, func(tx pgx.Tx) error {
			var remaining bool
			if e := tx.QueryRow(ctx, `SELECT lease_until>clock_timestamp()+interval '25 seconds' FROM research_repairs WHERE id=$1`, c.id).Scan(&remaining); e != nil {
				return e
			}
			if !remaining {
				return ErrDenied
			}
			return fn()
		})
	}
}

func (s Service) release(ctx context.Context, c claim) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = s.DB.Exec(ctx, `UPDATE research_repairs SET claim=NULL,lease_until=NULL WHERE id=$1 AND owner=$2 AND claim=$3 AND generation=$4`, c.id, c.owner, c.token, c.generation)
}

func (s Service) state(ctx context.Context, c claim, state, evidence string, ordinal int) error {
	return s.fenced(ctx, c, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE research_repairs SET state=$2 WHERE id=$1`, c.id, state); e != nil {
			return e
		}
		return event(ctx, tx, c.id, c.generation, ordinal, state, evidence)
	})
}

func (s Service) Diagnose(ctx context.Context, owner, id string) (Diagnosis, error) {
	r, e := s.Inspect(ctx, owner, id)
	if e != nil {
		return Diagnosis{}, e
	}
	return s.diagnose(ctx, owner, r)
}

func (s Service) diagnose(ctx context.Context, owner string, r Run) (Diagnosis, error) {
	report, e := s.Validation.InspectValidation(ctx, owner, r.Validation)
	if e != nil {
		return Diagnosis{}, e
	}
	if !verifiedGoReport(report) || !actionable(report.Outcome) || report.Version != r.Version || report.Workspace != r.Workspace || report.Image != r.Image || report.Profile != r.Profile || report.Policy != r.ValidationPolicy {
		return Diagnosis{}, ErrPolicy
	}
	expected := r.BaselineDigest
	if r.Validation != r.Baseline && len(r.History) > 0 {
		expected = r.History[len(r.History)-1].ReportDigest
	}
	if report.ReportDigest != expected {
		return Diagnosis{}, ErrPolicy
	}
	w, e := s.Workspace.InspectWorkspaceVersion(ctx, owner, r.Workspace)
	if e != nil {
		return Diagnosis{}, e
	}
	if w.Selected != r.Version {
		return Diagnosis{}, workspace.ErrConflict
	}
	// Find the immutable target and cap it before fetching any source bytes.
	for _, entry := range w.Files {
		if entry.Path != r.Target || entry.Hash == "" {
			continue
		}
		if entry.Size > maxEvidence || entry.Mode != "100644" {
			return Diagnosis{}, ErrEvidence
		}
		f, b, e := s.Workspace.ReadSelected(ctx, owner, r.Workspace, r.Target)
		if e != nil {
			return Diagnosis{}, e
		}
		if f != entry || ingest.Digest(b) != entry.Hash || !utf8.Valid(b) {
			return Diagnosis{}, ErrPolicy
		}
		current, e := s.Workspace.InspectWorkspaceVersion(ctx, owner, r.Workspace)
		if e != nil {
			return Diagnosis{}, e
		}
		if current.Selected != r.Version {
			return Diagnosis{}, workspace.ErrConflict
		}
		phase := report.Report.Phases.Run
		lines := strings.Count(string(b), "\n")
		if len(b) > 0 && b[len(b)-1] != '\n' {
			lines++
		}
		return Diagnosis{Workspace: r.Workspace, Version: r.Version, Validation: r.Validation, ReportDigest: report.ReportDigest, Outcome: report.Outcome, Profile: r.Profile, Requirement: r.Requirement, File: entry, FirstLine: 1, LastLine: max(1, lines), Source: string(b), UntrustedLog: bound(phase.Stdout + "\n" + phase.Stderr), Uncertainty: "verified lines and runtime evidence do not identify a general fix"}, nil
	}
	return Diagnosis{}, ErrEvidence
}

func (s Service) Propose(ctx context.Context, owner, id string, manual *workspace.Patch) (out Run, err error) {
	started := time.Now()
	defer func() { telemetry.Observe("repair.propose", telemetry.Class(err), "", time.Since(started).Seconds()) }()
	c, e := s.acquire(ctx, owner, id)
	if e != nil {
		return out, e
	}
	defer s.release(ctx, c)
	r, e := s.Inspect(ctx, owner, id)
	if e != nil {
		return r, e
	}
	if r.State != "BASELINE_VERIFIED" && r.State != "ITERATION_FAILED" && r.State != "DIAGNOSING" {
		return r, ErrPolicy
	}
	if r.Iterations >= 2 || r.ModelCalls >= 4 {
		if e = s.state(ctx, c, "BUDGET_EXHAUSTED", "", r.Iterations); e != nil {
			return r, e
		}
		return s.Inspect(ctx, owner, id)
	}
	if manual == nil && (s.Proposer == nil || r.Planner != s.Proposer.Identity()) || manual != nil && r.Planner != ManualPolicy {
		return r, ErrPolicy
	}
	if e = s.fenced(ctx, c, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE research_repairs SET model_calls=model_calls+1,state='DIAGNOSING' WHERE id=$1`, id)
		return e
	}); e != nil {
		return r, e
	}
	details, e := s.diagnose(ctx, owner, r)
	if e != nil {
		if errors.Is(e, ErrEvidence) {
			if e = s.state(ctx, c, "STOPPED_UNRESOLVED", "insufficient_evidence", r.Iterations); e != nil {
				return r, e
			}
			return s.Inspect(ctx, owner, id)
		}
		return r, e
	}
	patch := manual
	if patch == nil {
		stage, cancel := context.WithTimeout(ctx, 3*time.Second)
		decision, nextErr := s.Proposer.Propose(stage, details)
		if stage.Err() != nil {
			nextErr = stage.Err()
		}
		cancel()
		if nextErr != nil {
			return r, nextErr
		}
		if decision.Patch != nil && decision.Stop != "" {
			return r, ErrPolicy
		}
		patch = decision.Patch
		if patch == nil {
			if decision.Stop != "insufficient_evidence" {
				return r, ErrPolicy
			}
			if e = s.state(ctx, c, "STOPPED_UNRESOLVED", "insufficient_evidence", r.Iterations); e != nil {
				return r, e
			}
			return s.Inspect(ctx, owner, id)
		}
	}
	if patch.Base != r.Version || len(patch.Changes) != 1 {
		return r, ErrPolicy
	}
	change := patch.Changes[0]
	if change.Kind != "MODIFY" || change.Path != r.Target || change.Before != details.File.Hash || change.BeforeSize != int(details.File.Size) || change.AfterSize != len(change.Text) || len(change.Text) > maxEvidence {
		return r, ErrPolicy
	}
	cost := change.BeforeSize + change.AfterSize
	if r.Bytes+cost > 32768 {
		if e = s.state(ctx, c, "BUDGET_EXHAUSTED", "bytes", r.Iterations); e != nil {
			return r, e
		}
		return s.Inspect(ctx, owner, id)
	}
	fp := fingerprint(*patch)
	e = s.fenced(ctx, c, func(tx pgx.Tx) error {
		var old bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM research_repair_iterations WHERE repair=$1 AND (fingerprint=$2 OR source_digest=$3))`, id, fp, change.After).Scan(&old); e != nil {
			return e
		}
		if old {
			return ErrPolicy
		}
		p, e := s.Workspace.Propose(ctx, owner, r.Workspace, *patch)
		if e != nil {
			return e
		}
		approval, e := s.Workspace.RequestPatchApproval(ctx, owner, r.Workspace, p.Digest)
		if e != nil {
			return e
		}
		if approval.State != "PENDING" {
			return ErrPolicy
		}
		ordinal := r.Iterations + 1
		if _, e = tx.Exec(ctx, `INSERT INTO research_repair_iterations(repair,ordinal,workspace,patch,fingerprint,base_version,baseline,baseline_digest,source_digest,proposal_ns)
    VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, ordinal, r.Workspace, p.Digest, fp, r.Version, details.Validation, details.ReportDigest, details.File.Hash, int64(time.Since(started))); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE research_repairs SET state='WAITING_APPROVAL',iterations=$2,attempted_bytes=attempted_bytes+$3 WHERE id=$1`, id, ordinal, cost); e != nil {
			return e
		}
		return event(ctx, tx, id, c.generation, ordinal, "approval_requested", p.Digest)
	})
	if e != nil {
		return r, e
	}
	return s.Inspect(ctx, owner, id)
}
