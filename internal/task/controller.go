package task

import (
	"context"

	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/repair"
	"github.com/Amankumarsingh23/JudgeGrid/internal/research"
	"github.com/Amankumarsingh23/JudgeGrid/internal/validation"
	"github.com/jackc/pgx/v5"
)

func (s Service) resume(ctx context.Context, owner, id string, interrupted func(string) error) (Task, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	r, e := s.Inspect(ctx, owner, id)
	if e != nil || terminal(r.State) {
		return r, e
	}
	if s.Model == nil || s.Model.Identity() != r.Provider {
		return r, ErrModelUnavailable
	}
	c, e := s.acquire(ctx, owner, id)
	if e != nil {
		tag, updateErr := s.DB.Exec(ctx, `UPDATE research_tasks SET state='BUDGET_EXHAUSTED',active_us=active_us+`+elapsedClaim+`,claim=NULL,claim_since=NULL,lease_until=NULL
   WHERE id=$1 AND owner=$2 AND state IN `+active+` AND (deadline<=clock_timestamp()+interval '30 seconds' OR ((generation>=32 OR active_us+`+elapsedClaim+`>=690000000) AND (claim IS NULL OR lease_until<=clock_timestamp())))`, id, owner)
		if updateErr == nil && tag.RowsAffected() == 1 {
			return s.Inspect(ctx, owner, id)
		}
		return r, e
	}
	lost := false
	defer func() {
		if !lost {
			s.release(ctx, c)
		}
	}()
	parent := s.parent(c)
	rs := s.Research
	rs.Parent = parent
	for n := 0; n < 10; n++ {
		r, e = s.Inspect(ctx, owner, id)
		if e != nil || terminal(r.State) {
			return r, e
		}
		repairs := repair.Service{DB: s.DB, Workspace: s.Workspace, Validation: s.Validation, Parent: parent, Proposer: patchClient{s, r, c}}
		switch r.State {
		case "CREATED":
			if e = s.reserve(ctx, c, "research", 12); e != nil {
				return r, e
			}
			var child research.Run
			e = parent.Guard(ctx, func() error {
				var err error
				child, err = rs.CreateWithID(ctx, owner, r.Scope.Retrieval, r.Goal, id)
				return err
			})
			if e == nil && (child.Scope.Repository != r.Scope.Repository || child.Scope.Snapshot != r.Scope.Snapshot || child.Scope.Commit != r.Scope.Commit) {
				return r, ErrPolicy
			}
			if e == nil {
				e = s.link(ctx, c, "research", child.ID, "CREATED", "RESEARCHING")
			}
		case "RESEARCHING":
			child, err := rs.View(ctx, owner, r.Research)
			if err != nil {
				return r, err
			}
			if child.State != "COMPLETED" {
				if err = rs.Resume(ctx, owner, r.Research); err != nil {
					latest, readErr := rs.View(ctx, owner, r.Research)
					if readErr == nil && latest.State == "FAILED" {
						return s.finish(ctx, c, r, "INFRA_BLOCKED", nil, nil)
					}
					return r, err
				}
			}
			child, err = rs.View(ctx, owner, r.Research)
			if err != nil {
				return r, err
			}
			if child.State != "COMPLETED" {
				return r, ErrPolicy
			}
			if interrupted != nil {
				if err = interrupted("research"); err != nil {
					lost = true
					return r, err
				}
			}
			e = s.move(ctx, c, "RESEARCHING", "PLANNING", digest(child))
		case "PLANNING":
			citations, err := s.citations(ctx, owner, r)
			if err != nil {
				return r, err
			}
			allowed := []string{"PREPARE_WORKSPACE", "ABSTAIN"}
			if r.Target == "" {
				allowed = []string{"FINISH", "ABSTAIN"}
			}
			if r.Calls >= 5 {
				return s.finish(ctx, c, r, "BUDGET_EXHAUSTED", nil, nil)
			}
			choice, err := s.model(ctx, c, r, Input{UserGoal: r.Goal, State: r.State, Citations: citations, Allowed: allowed})
			if err != nil {
				return r, err
			}
			if choice.Action == "FINISH" {
				return s.finish(ctx, c, r, "COMPLETED_RESEARCH", nil, nil)
			}
			if choice.Action == "ABSTAIN" || len(citations) == 0 {
				return s.finish(ctx, c, r, "STOPPED_UNRESOLVED", nil, nil)
			}
			e = s.move(ctx, c, "PLANNING", "PREPARING_WORKSPACE", "")
		case "PREPARING_WORKSPACE":
			var workspaceID string
			e = parent.Guard(ctx, func() error {
				stage, cancel := context.WithTimeout(ctx, 27*time.Second)
				defer cancel()
				w, err := s.Workspace.Create(stage, owner, r.Research)
				if err == nil && (w.Repository != r.Scope.Repository || w.Snapshot != r.Scope.Snapshot || w.Commit != r.Scope.Commit) {
					return ErrPolicy
				}
				workspaceID = w.ID
				return err
			})
			if e == nil {
				e = s.link(ctx, c, "workspace", workspaceID, "PREPARING_WORKSPACE", "VALIDATING_BASELINE")
			}
			if e == nil && interrupted != nil {
				if e = interrupted("workspace"); e != nil {
					lost = true
					return r, e
				}
			}
		case "VALIDATING_BASELINE":
			if e = s.reserve(ctx, c, "validation", 3); e != nil {
				return r, e
			}
			if r.Baseline == "" {
				var baseline validation.Request
				e = parent.Guard(ctx, func() error {
					w, err := s.Workspace.InspectWorkspaceVersion(ctx, owner, r.Workspace)
					if err != nil {
						return err
					}
					baseline, err = s.Validation.RequestValidation(ctx, owner, r.Workspace, w.Selected, "go-unit")
					return err
				})
				if e != nil {
					return r, e
				}
				if e = s.link(ctx, c, "baseline", baseline.ID, "VALIDATING_BASELINE", "VALIDATING_BASELINE"); e != nil {
					return r, e
				}
				r.Baseline = baseline.ID
			}
			observed, err := s.Validation.RunWithAuthority(ctx, owner, r.Baseline, parent)
			if err != nil {
				return r, err
			}
			if interrupted != nil {
				if err = interrupted("validation"); err != nil {
					lost = true
					return r, err
				}
			}
			if observed.Report == nil || observed.Workspace != r.Workspace || observed.Profile != "go-unit" {
				return s.finish(ctx, c, r, "FAILED_RECOVERY", &observed, nil)
			}
			switch observed.Outcome {
			case "PASS":
				return s.finish(ctx, c, r, "COMPLETED_VALIDATED", &observed, nil)
			case "TEST_FAILURE", "BUILD_FAILURE":
			default:
				return s.finish(ctx, c, r, "INFRA_BLOCKED", &observed, nil)
			}
			if e = s.reserve(ctx, c, "repair", 2); e != nil {
				return r, e
			}
			var flow repair.Run
			e = parent.Guard(ctx, func() error {
				var err error
				flow, err = repairs.Start(ctx, owner, r.Baseline, r.Target, r.Goal)
				return err
			})
			if e == nil {
				e = s.link(ctx, c, "repair", flow.ID, "VALIDATING_BASELINE", "REPAIRING")
			}
		case "REPAIRING", "WAITING_APPROVAL":
			flow, err := repairs.Inspect(ctx, owner, r.Repair)
			if err != nil {
				return r, err
			}
			switch flow.State {
			case "BASELINE_VERIFIED", "ITERATION_FAILED", "DIAGNOSING":
				if r.Calls >= 5 {
					return s.finish(ctx, c, r, "BUDGET_EXHAUSTED", nil, &flow)
				}
				flow, err = repairs.Propose(ctx, owner, r.Repair, nil)
				if err != nil {
					return r, err
				}
				if interrupted != nil {
					if err = interrupted("proposal"); err != nil {
						lost = true
						return r, err
					}
				}
				if flow.State == "WAITING_APPROVAL" {
					if r.State != "WAITING_APPROVAL" {
						if err = s.move(ctx, c, r.State, "WAITING_APPROVAL", flow.History[len(flow.History)-1].Patch); err != nil {
							return r, err
						}
					}
					return s.Inspect(ctx, owner, id)
				}
			case "WAITING_APPROVAL":
				if r.State != "WAITING_APPROVAL" {
					if e = s.move(ctx, c, r.State, "WAITING_APPROVAL", flow.History[len(flow.History)-1].Patch); e != nil {
						return r, e
					}
					return s.Inspect(ctx, owner, id)
				}
				review, err := repairs.Review(ctx, owner, r.Repair)
				if err != nil {
					return r, err
				}
				if review.Approval.State == "PENDING" {
					return r, nil
				}
				flow, err = repairs.Resume(ctx, owner, r.Repair)
				if err != nil {
					return r, err
				}
			case "APPLYING", "VALIDATING":
				flow, err = repairs.Resume(ctx, owner, r.Repair)
				if err != nil {
					return r, err
				}
			}
			if interrupted != nil && flow.State == "COMPLETED_VALIDATED" {
				if err = interrupted("repair"); err != nil {
					lost = true
					return r, err
				}
			}
			switch flow.State {
			case "ITERATION_FAILED":
				if r.State == "WAITING_APPROVAL" {
					e = s.move(ctx, c, r.State, "REPAIRING", flow.EvidenceDigest)
				}
			case "COMPLETED_VALIDATED", "BUDGET_EXHAUSTED", "STOPPED_UNRESOLVED", "INFRA_BLOCKED", "FAILED_RECOVERY":
				return s.finish(ctx, c, r, flow.State, nil, &flow)
			case "APPROVAL_DENIED", "POLICY_DENIED":
				return s.finish(ctx, c, r, "DENIED", nil, &flow)
			case "CANCELLED":
				return s.finish(ctx, c, r, "CANCELLED", nil, &flow)
			default:
				return r, ErrPolicy
			}
		}
		if e != nil {
			return r, e
		}
	}
	return s.Inspect(ctx, owner, id)
}
func (s Service) link(ctx context.Context, c claim, field, id, previous, next string) error {
	switch field {
	case "research", "workspace", "baseline", "repair":
	default:
		return ErrPolicy
	}
	return s.fence(ctx, c, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE research_tasks SET `+field+`=$2,state=$3 WHERE id=$1 AND state=$4 AND (`+field+` IS NULL OR `+field+`=$2)`, c.id, id, next, previous)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrPolicy
		}
		return addEvent(ctx, tx, c, field+"_linked", id)
	})
}
func (s Service) citations(ctx context.Context, owner string, r Task) ([]research.Citation, error) {
	child, e := s.Research.View(ctx, owner, r.Research)
	if e != nil {
		return nil, e
	}
	if child.State != "COMPLETED" || child.Scope.Repository != r.Scope.Repository || child.Scope.Snapshot != r.Scope.Snapshot || child.Scope.Commit != r.Scope.Commit {
		return nil, ErrPolicy
	}
	var out []research.Citation
	for _, step := range child.Steps {
		if step.Observation == nil {
			continue
		}
		for _, v := range step.Observation.Citations {
			if len(out) == 8 {
				return out, nil
			}
			out = append(out, v)
		}
	}
	return out, nil
}
func (s Service) finish(ctx context.Context, c claim, r Task, state string, v *validation.Request, p *repair.Run) (Task, error) {
	report := Report{Task: r.ID, Scope: r.Scope, State: state, Provider: r.Provider, Research: r.Research, Workspace: r.Workspace, Baseline: r.Baseline, Repair: r.Repair,
		Uncertainty: "citation integrity and selected test results do not establish general task correctness"}
	if r.Research != "" {
		citations, e := s.citations(ctx, c.owner, r)
		if e != nil && state != "INFRA_BLOCKED" {
			return r, e
		}
		report.Citations = citations
	}
	if p != nil {
		report.RepairDigest = p.EvidenceDigest
		if p.Validation != "" {
			value, e := s.Validation.InspectValidation(ctx, c.owner, p.Validation)
			if e != nil && state == "COMPLETED_VALIDATED" {
				return r, e
			}
			if e == nil {
				v = &value
			}
		}
	}
	if v != nil {
		report.Validation, report.ValidationDigest, report.Version, report.Profile, report.Outcome = v.ID, v.ReportDigest, v.Version, v.Profile, v.Outcome
		report.Image = v.Image
	}
	if r.Baseline != "" {
		base, e := s.Validation.InspectValidation(ctx, c.owner, r.Baseline)
		if e != nil {
			return r, e
		}
		report.BaselineDigest = base.ReportDigest
	}
	if state == "COMPLETED_VALIDATED" && (v == nil || v.Outcome != "PASS" || v.Report == nil || v.ReportDigest == "") {
		return r, ErrPolicy
	}
	e := s.fence(ctx, c, func(tx pgx.Tx) error {
		e := tx.QueryRow(ctx, `SELECT active_us+`+elapsedClaim+`,(extract(epoch from (clock_timestamp()-created_at))*1000000)::bigint,calls,input_tokens,output_tokens
		 FROM research_tasks WHERE id=$1`, c.id).Scan(&report.ActiveUS, &report.WallUS, &report.PlannerCalls, &report.InputTokens, &report.OutputTokens)
		if e != nil {
			return e
		}
		e = tx.QueryRow(ctx, `SELECT coalesce(sum((extract(epoch from (g.at-w.at))*1000000)::bigint),0) FROM research_task_events g
		JOIN research_task_events w ON w.task=g.task AND w.action='WAITING_APPROVAL' AND w.receipt=g.receipt
		WHERE g.task=$1 AND g.action='grant'`, c.id).Scan(&report.ApprovalWaitUS)
		if e != nil {
			return e
		}
		sha := digest(report)
		_, e = tx.Exec(ctx, `UPDATE research_tasks SET state=$2,report=$3,report_sha256=$4,active_us=$5,claim=NULL,claim_since=NULL,lease_until=NULL WHERE id=$1`, c.id, state, report, sha, report.ActiveUS)
		if e != nil {
			return e
		}
		return addEvent(ctx, tx, c, state, sha)
	})
	if e != nil {
		return r, e
	}
	return s.Inspect(ctx, c.owner, c.id)
}
func (s Service) Review(ctx context.Context, owner, id string) (repair.Review, error) {
	r, e := s.Inspect(ctx, owner, id)
	if e != nil {
		return repair.Review{}, e
	}
	if r.Repair == "" {
		return repair.Review{}, ErrPolicy
	}
	child := repair.Service{DB: s.DB, Workspace: s.Workspace, Validation: s.Validation}
	return child.Review(ctx, owner, r.Repair)
}
func (s Service) Decide(ctx context.Context, owner, id, patch, action string) (Task, error) {
	r, e := s.Inspect(ctx, owner, id)
	if e != nil {
		return r, e
	}
	if r.State != "WAITING_APPROVAL" || r.Repair == "" || (action != "grant" && action != "deny") {
		return r, ErrPolicy
	}
	c, e := s.acquire(ctx, owner, id)
	if e != nil {
		return r, e
	}
	defer s.release(ctx, c)
	child := repair.Service{DB: s.DB, Workspace: s.Workspace, Validation: s.Validation, Parent: s.parent(c)}
	if _, e = child.Decide(ctx, owner, r.Repair, patch, action); e != nil {
		return r, e
	}
	if action == "deny" {
		p, e := child.Inspect(ctx, owner, r.Repair)
		if e != nil {
			return r, e
		}
		return s.finish(ctx, c, r, "DENIED", nil, &p)
	}
	if e = s.fence(ctx, c, func(tx pgx.Tx) error { return addEvent(ctx, tx, c, "grant", patch) }); e != nil {
		return r, e
	}
	return s.Inspect(ctx, owner, id)
}
func (s Service) Cancel(ctx context.Context, owner, id string) error {
	r, e := s.Inspect(ctx, owner, id)
	if e != nil {
		return e
	}
	if !terminal(r.State) {
		if _, e = s.DB.Exec(ctx, `UPDATE research_tasks SET state='CANCELLED',active_us=active_us+`+elapsedClaim+`,claim=NULL,claim_since=NULL,lease_until=NULL WHERE id=$1 AND owner=$2 AND state IN `+active, id, owner); e != nil {
			return e
		}
	}
	r, e = s.Inspect(ctx, owner, id)
	if e != nil || r.State != "CANCELLED" {
		return e
	}
	switch {
	case r.Repair != "":
		child := repair.Service{DB: s.DB, Workspace: s.Workspace, Validation: s.Validation}
		return child.Cancel(ctx, owner, r.Repair)
	case r.Baseline != "":
		return s.Validation.CancelValidation(ctx, owner, r.Baseline)
	case r.Research != "":
		return s.Research.Cancel(ctx, owner, r.Research)
	}
	return nil
}
func (s Service) ResearchEvidence(ctx context.Context, owner, id string) (research.Run, error) {
	r, e := s.Inspect(ctx, owner, id)
	if e != nil {
		return research.Run{}, e
	}
	if r.Research == "" {
		return research.Run{}, ErrPolicy
	}
	return s.Research.View(ctx, owner, r.Research)
}
