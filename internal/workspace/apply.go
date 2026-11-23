package workspace

import (
	"context"
	"errors"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/research"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Approval struct {
	Digest    string    `json:"digest"`
	State     string    `json:"state"`
	Requester string    `json:"requested_by"`
	Approver  string    `json:"decided_by,omitempty"`
	Expires   time.Time `json:"expires_at"`
}
type Result struct {
	Version        string  `json:"version"`
	Generation     int64   `json:"generation"`
	Replay         bool    `json:"replay"`
	ReusedObjects  int     `json:"reused_objects"`
	StageSeconds   float64 `json:"stage_seconds"`
	PublishSeconds float64 `json:"publish_seconds"`
}

func scanApproval(row pgx.Row) (a Approval, err error) {
	err = row.Scan(&a.Digest, &a.State, &a.Requester, &a.Approver, &a.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return
}

const approvalFields = "digest,state,requested_by,coalesce(decided_by,''),expires_at"

func expireApproval(ctx context.Context, tx pgx.Tx, id, identity string) error {
	_, err := tx.Exec(ctx, `UPDATE research_edit_approvals SET state='EXPIRED',decided_at=clock_timestamp(),decided_by=NULL WHERE workspace=$1 AND digest=$2 AND state IN ('PENDING','GRANTED') AND expires_at<=clock_timestamp() AND NOT EXISTS(SELECT 1 FROM research_edit_publications WHERE workspace=$1 AND digest=$2)`, id, identity)
	return err
}

func (m WorkspaceManager) RequestPatchApproval(ctx context.Context, owner, id, identity string) (Approval, error) {
	scope, err := m.scope(ctx, owner, id)
	if err != nil {
		return Approval{}, err
	}
	p, err := m.loadPatch(ctx, scope, identity)
	if err != nil {
		return Approval{}, err
	}
	tx, w, err := m.lock(ctx, owner, id)
	if err != nil {
		return Approval{}, err
	}
	defer tx.Rollback(ctx)
	if w.Selected != p.Patch.Base {
		return Approval{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO research_edit_approvals(workspace,digest,requested_by,expires_at,state)
 VALUES($1,$2,$3,least($4,clock_timestamp()+interval '5 minutes'),'PENDING') ON CONFLICT DO NOTHING`, id, identity, owner, w.Expires); err != nil {
		return Approval{}, err
	}
	if err = expireApproval(ctx, tx, id, identity); err != nil {
		return Approval{}, err
	}
	a, err := scanApproval(tx.QueryRow(ctx, "SELECT "+approvalFields+" FROM research_edit_approvals WHERE workspace=$1 AND digest=$2", id, identity))
	if err != nil {
		return a, err
	}
	if err = journal(ctx, tx, id, "request_patch_approval", identity, 0); err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}

// Decide requires an authenticated operator principal supplied outside the planner.
func (m WorkspaceManager) Decide(ctx context.Context, owner, id, identity, action string) (Approval, error) {
	tx, w, err := m.lock(ctx, owner, id)
	if err != nil {
		return Approval{}, err
	}
	defer tx.Rollback(ctx)
	if !validHash(identity) {
		return Approval{}, ErrInvalid
	}
	var next string
	switch action {
	case "grant":
		next = "GRANTED"
	case "deny":
		next = "DENIED"
	case "cancel":
		next = "CANCELLED"
	default:
		return Approval{}, ErrInvalid
	}
	if err = expireApproval(ctx, tx, id, identity); err != nil {
		return Approval{}, err
	}
	current, err := scanApproval(tx.QueryRow(ctx, "SELECT "+approvalFields+" FROM research_edit_approvals WHERE workspace=$1 AND digest=$2 AND requested_by=$3", id, identity, owner))
	if err != nil {
		return current, err
	}
	allowed, replay := research.ApprovalTransition(current.State, current.Approver, owner, next)
	if replay {
		return current, tx.Commit(ctx)
	}
	if current.State == "EXPIRED" {
		if err = tx.Commit(ctx); err != nil {
			return current, err
		}
		return current, ErrDenied
	}
	if !allowed {
		return current, ErrDenied
	}
	var decider any = owner
	if next == "CANCELLED" {
		decider = nil
	}
	a, err := scanApproval(tx.QueryRow(ctx, `UPDATE research_edit_approvals AS a SET state=$3,decided_at=clock_timestamp(),decided_by=$4
 FROM research_edit_proposals AS p WHERE a.workspace=$1 AND a.digest=$2 AND a.expires_at>clock_timestamp()
 AND p.workspace=a.workspace AND p.digest=a.digest AND ($3<>'GRANTED' OR p.base=$5)
 AND NOT EXISTS(SELECT 1 FROM research_edit_publications WHERE workspace=$1 AND digest=$2) RETURNING a.digest,a.state,a.requested_by,coalesce(a.decided_by,''),a.expires_at`, id, identity, next, decider, w.Selected))
	if err != nil {
		return a, err
	}
	if err = journal(ctx, tx, id, action, identity, 0); err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}

type lease struct {
	token      string
	generation int64
}

type faultHooks struct {
	staged    func(int) error
	published func() error
	authority Authority
}

// Authority is an optional trusted parent fence for child side effects.
type Authority func(context.Context, func() error) error

func (a Authority) Guard(ctx context.Context, action func() error) error {
	if a == nil {
		return action()
	}
	return a(ctx, action)
}

func (m WorkspaceManager) ApplyApprovedPatch(ctx context.Context, owner, id, identity string) (Result, error) {
	return m.apply(ctx, owner, id, identity, faultHooks{})
}

func (m WorkspaceManager) ApplyWithAuthority(ctx context.Context, owner, id, identity string, authority Authority) (Result, error) {
	if authority == nil {
		return Result{}, ErrDenied
	}
	return m.apply(ctx, owner, id, identity, faultHooks{authority: authority})
}

func (m WorkspaceManager) InspectApproval(ctx context.Context, owner, id, identity string) (Approval, error) {
	if !validHash(identity) {
		return Approval{}, ErrInvalid
	}
	tx, w, err := m.lock(ctx, owner, id)
	if err != nil {
		return Approval{}, err
	}
	defer tx.Rollback(ctx)
	if err = expireApproval(ctx, tx, w.ID, identity); err != nil {
		return Approval{}, err
	}
	a, err := scanApproval(tx.QueryRow(ctx, "SELECT "+approvalFields+" FROM research_edit_approvals WHERE workspace=$1 AND digest=$2 AND requested_by=$3", id, identity, owner))
	if err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}

func (m WorkspaceManager) apply(ctx context.Context, owner, id, identity string, hooks faultHooks) (Result, error) {
	work, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	ctx = work
	w, err := m.scope(ctx, owner, id)
	if err != nil {
		return Result{}, err
	}
	p, err := m.loadPatch(ctx, w, identity)
	if err != nil {
		return Result{}, err
	}
	var claim lease
	var replay Result
	err = hooks.authority.Guard(ctx, func() error {
		var e error
		claim, replay, e = m.claim(ctx, owner, p)
		return e
	})
	if err != nil || replay.Replay {
		return replay, err
	}
	stage := time.Now()
	files, preview, err := m.candidate(ctx, w, p.Patch)
	if err != nil {
		return Result{}, err
	}
	if preview != p.Diff {
		return Result{}, ErrIntegrity
	}
	out := Result{Version: versionID(w, files), Generation: claim.generation}
	for i, c := range p.Patch.Changes {
		tag, e := m.DB.Exec(ctx, `INSERT INTO research_workspace_objects(workspace,hash,body) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, c.After, []byte(c.Text))
		if e != nil {
			return Result{}, e
		}
		if tag.RowsAffected() == 0 {
			out.ReusedObjects++
		}
		if hooks.staged != nil {
			if e = hooks.staged(i + 1); e != nil {
				return Result{}, e
			}
		}
	}
	if err = m.verifyTree(ctx, w.ID, files); err != nil {
		return Result{}, err
	}
	out.StageSeconds = time.Since(stage).Seconds()
	publication := time.Now()
	if err = hooks.authority.Guard(ctx, func() error { return m.publish(ctx, owner, p, claim, files, out.Version) }); err != nil {
		return Result{}, err
	}
	out.PublishSeconds = time.Since(publication).Seconds()
	if hooks.published != nil {
		if err = hooks.published(); err != nil {
			return Result{}, err
		}
	}
	return out, nil
}

func (m WorkspaceManager) verifyTree(ctx context.Context, id string, files []File) error {
	expected := map[string]int64{}
	hashes := make([]string, 0, len(files))
	for _, f := range files {
		if f.Hash == "" {
			continue
		}
		if old, ok := expected[f.Hash]; ok {
			if old != f.Size {
				return ErrIntegrity
			}
			continue
		}
		expected[f.Hash] = f.Size
		hashes = append(hashes, f.Hash)
	}
	rows, err := m.DB.Query(ctx, `SELECT hash,body FROM research_workspace_objects WHERE workspace=$1 AND hash=ANY($2::text[])`, id, hashes)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		var body []byte
		if err = rows.Scan(&h, &body); err != nil {
			return err
		}
		if int64(len(body)) != expected[h] || ingest.Digest(body) != h {
			return ErrIntegrity
		}
		delete(expected, h)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(expected) > 0 {
		return ErrIntegrity
	}
	return nil
}

func (m WorkspaceManager) claim(ctx context.Context, owner string, p Proposal) (lease, Result, error) {
	tx, w, err := m.lock(ctx, owner, p.Workspace)
	if err != nil {
		return lease{}, Result{}, err
	}
	defer tx.Rollback(ctx)
	var previous Result
	err = tx.QueryRow(ctx, `SELECT version,generation FROM research_edit_publications WHERE workspace=$1 AND digest=$2`, w.ID, p.Digest).Scan(&previous.Version, &previous.Generation)
	if err == nil {
		previous.Replay = true
		return lease{}, previous, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return lease{}, Result{}, err
	}
	if w.Selected != p.Patch.Base {
		return lease{}, Result{}, ErrConflict
	}
	if err = expireApproval(ctx, tx, w.ID, p.Digest); err != nil {
		return lease{}, Result{}, err
	}
	allowed, err := approved(ctx, tx, owner, p)
	if err != nil {
		return lease{}, Result{}, err
	}
	if !allowed {
		if err = tx.Commit(ctx); err != nil {
			return lease{}, Result{}, err
		}
		return lease{}, Result{}, ErrDenied
	}
	var attempts int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM research_edit_attempts WHERE workspace=$1 AND digest=$2`, w.ID, p.Digest).Scan(&attempts); err != nil {
		return lease{}, Result{}, err
	}
	if attempts >= 3 {
		return lease{}, Result{}, ErrDenied
	}
	c := lease{token: uuid.NewString()}
	err = tx.QueryRow(ctx, `UPDATE research_workspaces SET generation=generation+1,claim=$2,lease_until=least(expires_at,clock_timestamp()+interval '45 seconds')
 WHERE id=$1 AND (claim IS NULL OR lease_until<=clock_timestamp()) RETURNING generation`, w.ID, c.token).Scan(&c.generation)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrBusy
	}
	if err != nil {
		return lease{}, Result{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO research_edit_attempts(workspace,digest,generation) VALUES($1,$2,$3)`, w.ID, p.Digest, c.generation); err != nil {
		return lease{}, Result{}, err
	}
	return c, Result{}, tx.Commit(ctx)
}

func approved(ctx context.Context, tx pgx.Tx, owner string, p Proposal) (ok bool, err error) {
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM research_edit_approvals WHERE workspace=$1 AND digest=$2 AND
 state='GRANTED' AND requested_by=$3 AND decided_by=$3 AND expires_at>clock_timestamp())`, p.Workspace, p.Digest, owner).Scan(&ok)
	return
}

func (m WorkspaceManager) publish(ctx context.Context, owner string, p Proposal, c lease, files []File, next string) error {
	tx, w, err := m.lock(ctx, owner, p.Workspace)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if w.Selected != p.Patch.Base {
		return ErrConflict
	}
	if next != versionID(w, files) {
		return ErrIntegrity
	}
	if _, err = tx.Exec(ctx, `INSERT INTO research_workspace_versions(workspace,id,inventory) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, w.ID, next, files); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE research_workspaces SET selected=$5,claim=NULL,lease_until=NULL
 WHERE id=$1 AND owner=$2 AND claim=$3 AND generation=$4 AND selected=$6 AND lease_until>clock_timestamp() AND expires_at>clock_timestamp()
 AND EXISTS(SELECT 1 FROM research_edit_approvals WHERE workspace=$1 AND digest=$7 AND state='GRANTED' AND requested_by=$2 AND decided_by=$2 AND expires_at>clock_timestamp())`,
		w.ID, owner, c.token, c.generation, next, p.Patch.Base, p.Digest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrDenied
	}
	if _, err = tx.Exec(ctx, `INSERT INTO research_edit_publications(workspace,digest,version,generation) VALUES($1,$2,$3,$4)`, w.ID, p.Digest, next, c.generation); err != nil {
		return err
	}
	if err = journal(ctx, tx, w.ID, "apply_approved_patch", p.Digest, c.generation); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
