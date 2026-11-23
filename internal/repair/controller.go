package repair

import (
	"context"
	"errors"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
	"github.com/jackc/pgx/v5"
)

type interruptions struct {
	afterApply      func() error
	afterValidation func() error
}

func (s Service) Decide(ctx context.Context, owner, id, digest, action string) (workspace.Approval, error) {
	if action != "grant" && action != "deny" {
		return workspace.Approval{}, ErrPolicy
	}
	c, e := s.acquire(ctx, owner, id)
	if e != nil {
		return workspace.Approval{}, e
	}
	defer s.release(ctx, c)
	r, e := s.Inspect(ctx, owner, id)
	if e != nil {
		return workspace.Approval{}, e
	}
	if r.State != "WAITING_APPROVAL" || len(r.History) == 0 {
		return workspace.Approval{}, ErrPolicy
	}
	i := r.History[len(r.History)-1]
	if i.Patch != digest {
		return workspace.Approval{}, ErrDenied
	}
	var a workspace.Approval
	e = s.fenced(ctx, c, func(tx pgx.Tx) error {
		var err error
		a, err = s.Workspace.Decide(ctx, owner, r.Workspace, digest, action)
		if err != nil {
			return err
		}
		if action == "deny" {
			if _, err = tx.Exec(ctx, `UPDATE research_repairs SET state='APPROVAL_DENIED' WHERE id=$1`, id); err != nil {
				return err
			}
		}
		return event(ctx, tx, id, c.generation, i.Ordinal, action, digest)
	})
	return a, e
}

func (s Service) Patch(ctx context.Context, owner, id string) (workspace.Proposal, error) {
	r, e := s.Inspect(ctx, owner, id)
	if e != nil {
		return workspace.Proposal{}, e
	}
	if len(r.History) == 0 {
		return workspace.Proposal{}, ErrPolicy
	}
	return s.Workspace.InspectPatch(ctx, owner, r.Workspace, r.History[len(r.History)-1].Patch)
}

type Review struct {
	Patch    workspace.Proposal `json:"proposal"`
	Approval workspace.Approval `json:"approval"`
	Profile  string             `json:"required_profile"`
	Image    string             `json:"required_image"`
}

func (s Service) Review(ctx context.Context, owner, id string) (Review, error) {
	r, e := s.Inspect(ctx, owner, id)
	if e != nil {
		return Review{}, e
	}
	if len(r.History) == 0 {
		return Review{}, ErrPolicy
	}
	digest := r.History[len(r.History)-1].Patch
	p, e := s.Workspace.InspectPatch(ctx, owner, r.Workspace, digest)
	if e != nil {
		return Review{}, e
	}
	a, e := s.Workspace.InspectApproval(ctx, owner, r.Workspace, digest)
	return Review{p, a, r.Profile, r.Image}, e
}

func (s Service) Resume(ctx context.Context, owner, id string) (r Run, err error) {
	start := time.Now()
	defer func() { telemetry.Observe("repair.resume", telemetry.Class(err), "", time.Since(start).Seconds()) }()
	return s.resume(ctx, owner, id, interruptions{})
}

func (s Service) resume(ctx context.Context, owner, id string, h interruptions) (Run, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	r, e := s.Inspect(ctx, owner, id)
	if e != nil {
		return r, e
	}
	switch r.State {
	case "WAITING_APPROVAL", "APPLYING", "VALIDATING":
	default:
		return r, nil
	}
	c, e := s.acquire(ctx, owner, id)
	if e != nil {
		state, inspectErr := s.Inspect(ctx, owner, id)
		if inspectErr == nil && state.State == "BUDGET_EXHAUSTED" {
			return state, nil
		}
		return r, e
	}
	interrupted := false
	defer func() {
		if !interrupted {
			s.release(ctx, c)
		}
	}()
	r, e = s.Inspect(ctx, owner, id)
	if e != nil {
		return r, e
	}
	if len(r.History) == 0 {
		return r, ErrPolicy
	}
	i := r.History[len(r.History)-1]
	switch r.State {
	case "WAITING_APPROVAL":
		a, e := s.Workspace.InspectApproval(ctx, owner, r.Workspace, i.Patch)
		if e != nil {
			return r, e
		}
		switch a.State {
		case "PENDING":
			return r, nil
		case "DENIED", "CANCELLED", "EXPIRED":
			if e = s.state(ctx, c, "APPROVAL_DENIED", i.Patch, i.Ordinal); e != nil {
				return r, e
			}
			return s.Inspect(ctx, owner, id)
		case "GRANTED":
		default:
			return r, ErrPolicy
		}
		if e = s.state(ctx, c, "APPLYING", i.Patch, i.Ordinal); e != nil {
			return r, e
		}
	case "APPLYING", "VALIDATING":
	default:
		return r, nil
	}
	if r.State != "VALIDATING" {
		edit, e := s.Workspace.ApplyWithAuthority(ctx, owner, r.Workspace, i.Patch, s.authority(c))
		if e != nil {
			if errors.Is(e, workspace.ErrConflict) {
				if e = s.state(ctx, c, "POLICY_DENIED", "workspace_changed", i.Ordinal); e != nil {
					return r, e
				}
				return s.Inspect(ctx, owner, id)
			}
			if errors.Is(e, workspace.ErrDenied) {
				a, checkErr := s.Workspace.InspectApproval(ctx, owner, r.Workspace, i.Patch)
				if checkErr == nil && a.State != "GRANTED" {
					if e = s.state(ctx, c, "APPROVAL_DENIED", i.Patch, i.Ordinal); e != nil {
						return r, e
					}
					return s.Inspect(ctx, owner, id)
				}
			}
			return r, e
		}
		if h.afterApply != nil {
			if e = h.afterApply(); e != nil {
				interrupted = true
				return r, e
			}
		}
		if edit.Version == i.Base || edit.Version == "" {
			return r, ErrPolicy
		}
		if e = s.fenced(ctx, c, func(tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, `UPDATE research_repair_iterations SET new_version=$3 WHERE repair=$1 AND ordinal=$2 AND new_version IS NULL`, id, i.Ordinal, edit.Version)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrPolicy
			}
			if _, e = tx.Exec(ctx, `UPDATE research_repairs SET state='VALIDATING',current_version=$2 WHERE id=$1`, id, edit.Version); e != nil {
				return e
			}
			return event(ctx, tx, id, c.generation, i.Ordinal, "workspace_published", edit.Version)
		}); e != nil {
			return r, e
		}
		i.Version = edit.Version
	}
	if i.Version == "" {
		return r, ErrPolicy
	}
	if i.Validation == "" {
		e = s.fenced(ctx, c, func(tx pgx.Tx) error {
			next, e := s.Validation.RequestValidation(ctx, owner, r.Workspace, i.Version, r.Profile)
			if e != nil {
				return e
			}
			if next.Profile != r.Profile || next.Policy != r.ValidationPolicy || next.Image != r.Image {
				// The changed runtime never receives dispatch authority from this repair.
				_ = s.Validation.CancelValidation(ctx, owner, next.ID)
				if _, e = tx.Exec(ctx, `UPDATE research_repairs SET state='POLICY_DENIED' WHERE id=$1`, id); e != nil {
					return e
				}
				return event(ctx, tx, id, c.generation, i.Ordinal, "POLICY_DENIED", "validation_identity_changed")
			}
			i.Validation = next.ID
			if _, e = tx.Exec(ctx, `UPDATE research_repair_iterations SET validation=$3 WHERE repair=$1 AND ordinal=$2 AND validation IS NULL`, id, i.Ordinal, next.ID); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE research_repairs SET current_validation=$2 WHERE id=$1`, id, next.ID); e != nil {
				return e
			}
			return event(ctx, tx, id, c.generation, i.Ordinal, "validation_requested", next.ID)
		})
		if e != nil {
			if errors.Is(e, workspace.ErrConflict) {
				if e = s.state(ctx, c, "POLICY_DENIED", "workspace_changed", i.Ordinal); e != nil {
					return r, e
				}
				return s.Inspect(ctx, owner, id)
			}
			return r, e
		}
		if i.Validation == "" {
			return s.Inspect(ctx, owner, id)
		}
	}
	observed, e := s.Validation.RunWithAuthority(ctx, owner, i.Validation, s.authority(c))
	if e != nil {
		current, inspectErr := s.Validation.InspectValidation(ctx, owner, i.Validation)
		if inspectErr == nil && (current.State == "FAILED" || current.State == "EXPIRED" || current.State == "CANCELLED") {
			if e = s.state(ctx, c, "INFRA_BLOCKED", i.Validation, i.Ordinal); e != nil {
				return r, e
			}
			return s.Inspect(ctx, owner, id)
		}
		return r, e
	}
	if h.afterValidation != nil {
		if e = h.afterValidation(); e != nil {
			interrupted = true
			return r, e
		}
	}
	if !reportMatches(observed) || observed.ID != i.Validation || observed.Version != i.Version || observed.Workspace != r.Workspace || observed.Image != r.Image || observed.Policy != r.ValidationPolicy || observed.Profile != r.Profile {
		if e = s.state(ctx, c, "FAILED_RECOVERY", "validation_identity", i.Ordinal); e != nil {
			return r, e
		}
		return s.Inspect(ctx, owner, id)
	}
	next := "STOPPED_UNRESOLVED"
	switch {
	case verifiedGoReport(observed) && observed.Outcome == "PASS":
		next = "COMPLETED_VALIDATED"
	case verifiedGoReport(observed) && actionable(observed.Outcome):
		next = "ITERATION_FAILED"
		if i.Ordinal == 2 {
			next = "BUDGET_EXHAUSTED"
		}
	case observed.Outcome == "INFRA_FAILURE" || observed.Outcome == "CANCELLED":
		next = "INFRA_BLOCKED"
	case observed.Outcome == "POLICY_DENIED":
		next = "POLICY_DENIED"
	}
	if e = s.fenced(ctx, c, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE research_repair_iterations SET report_sha256=$3,outcome=$4 WHERE repair=$1 AND ordinal=$2 AND report_sha256 IS NULL`, id, i.Ordinal, observed.ReportDigest, observed.Outcome)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrPolicy
		}
		if _, e = tx.Exec(ctx, `UPDATE research_repairs SET state=$2 WHERE id=$1`, id, next); e != nil {
			return e
		}
		return event(ctx, tx, id, c.generation, i.Ordinal, next, observed.ReportDigest)
	}); e != nil {
		return r, e
	}
	return s.Inspect(ctx, owner, id)
}

func (s Service) Cancel(ctx context.Context, owner, id string) error {
	if !validID(id) || owner == "" {
		return ErrDenied
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	r, e := scan(tx.QueryRow(ctx, "SELECT "+columns+" FROM research_repairs WHERE id=$1 AND owner=$2 FOR UPDATE", id, owner))
	if e != nil {
		return e
	}
	changed, e := tx.Exec(ctx, `UPDATE research_repairs SET state='CANCELLED',claim=NULL,lease_until=NULL WHERE id=$1 AND state IN `+active, id)
	if e != nil {
		return e
	}
	if changed.RowsAffected() == 1 {
		if e = event(ctx, tx, id, r.Generation, r.Iterations, "CANCELLED", ""); e != nil {
			return e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	if changed.RowsAffected() == 0 && r.State != "CANCELLED" {
		return nil
	}
	w, e := s.Inspect(ctx, owner, id)
	if e != nil || len(w.History) == 0 {
		return e
	}
	i := w.History[len(w.History)-1]
	var revoke, stop error
	a, lookup := s.Workspace.InspectApproval(ctx, owner, w.Workspace, i.Patch)
	if lookup == nil && (a.State == "PENDING" || a.State == "GRANTED") && i.Version == "" {
		var published bool
		revoke = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM research_edit_publications WHERE workspace=$1 AND digest=$2)`, w.Workspace, i.Patch).Scan(&published)
		if revoke == nil && !published {
			_, revoke = s.Workspace.Decide(ctx, owner, w.Workspace, i.Patch, "cancel")
		}
	}
	if i.Validation != "" {
		stop = s.Validation.CancelValidation(ctx, owner, i.Validation)
	}
	return errors.Join(revoke, stop)
}
