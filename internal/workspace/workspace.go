package workspace

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WorkspaceManager struct {
	DB     *pgxpool.Pool
	Source ingest.Reader
}

type Workspace struct {
	ID         string    `json:"id"`
	Run        string    `json:"research_run"`
	Repository string    `json:"repository"`
	Snapshot   string    `json:"snapshot"`
	Commit     string    `json:"commit"`
	Format     string    `json:"object_format"`
	Policy     string    `json:"policy"`
	Selected   string    `json:"selected"`
	Expires    time.Time `json:"expires_at"`
	Files      []File    `json:"files,omitempty"`
}

const wsFields = "id::text,run_id::text,repository::text,snapshot,commit_oid,object_format,policy,coalesce(selected,''),expires_at"

func scanWorkspace(row pgx.Row) (w Workspace, err error) {
	err = row.Scan(&w.ID, &w.Run, &w.Repository, &w.Snapshot, &w.Commit, &w.Format, &w.Policy, &w.Selected, &w.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return
}

func validUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func (m WorkspaceManager) scope(ctx context.Context, owner, id string) (Workspace, error) {
	if owner == "" || !validUUID(id) {
		return Workspace{}, ErrDenied
	}
	return scanWorkspace(m.DB.QueryRow(ctx, "SELECT "+wsFields+" FROM research_workspaces WHERE id=$1 AND owner=$2 AND selected IS NOT NULL AND expires_at>clock_timestamp()", id, owner))
}

func (m WorkspaceManager) Create(ctx context.Context, owner, run string) (Workspace, error) {
	if owner == "" || !validUUID(run) || m.Source == nil {
		return Workspace{}, ErrDenied
	}
	stage, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	ctx = stage
	var repo, snapshot, commit string
	err := m.DB.QueryRow(ctx, `SELECT repository::text,snapshot,commit_oid FROM research_runs WHERE id=$1 AND owner=$2 AND state='COMPLETED'`, run, owner).Scan(&repo, &snapshot, &commit)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, ErrDenied
	}
	if err != nil {
		return Workspace{}, err
	}
	existing, err := scanWorkspace(m.DB.QueryRow(ctx, "SELECT "+wsFields+" FROM research_workspaces WHERE run_id=$1 AND owner=$2 AND expires_at>clock_timestamp()", run, owner))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrDenied) {
		return Workspace{}, err
	}

	snap, manifest, err := m.Source.Ready(ctx, owner, snapshot)
	if err != nil {
		return Workspace{}, err
	}
	if snap.Repository != repo || snap.ID != snapshot || snap.Commit != commit || (snap.Format != "sha1" && snap.Format != "sha256") {
		return Workspace{}, ErrIntegrity
	}
	var files []File
	for _, entry := range manifest.Entries {
		// The verified manifest describes the whole snapshot topology, including excluded entries.
		if !portable(entry.Path) {
			return Workspace{}, ErrInvalid
		}
		file := File{Path: entry.Path, Mode: "RESERVED"}
		if entry.Type == "tree" && entry.Reason == "directory" {
			file.Mode = "DIRECTORY"
		}
		if !protected(entry.Path) && entry.Reason == "included" {
			if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
				return Workspace{}, ErrIntegrity
			}
			file = File{Path: entry.Path, Mode: entry.Mode, Hash: entry.SHA256, Size: entry.Size}
		}
		files = append(files, file)
	}
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	if err = validateInventory(files); err != nil {
		return Workspace{}, err
	}
	objects := map[string][]byte{}
	for _, f := range files {
		if f.Hash == "" {
			continue
		}
		if _, ok := objects[f.Hash]; ok {
			continue
		}
		v, e := m.Source.ReadFile(ctx, owner, snapshot, f.Path)
		if e != nil {
			return Workspace{}, e
		}
		if v.Snapshot.ID != snapshot || v.Snapshot.Commit != commit || v.Entry.Path != f.Path || v.Entry.Mode != f.Mode || int64(len(v.Bytes)) != f.Size || ingest.Digest(v.Bytes) != f.Hash {
			return Workspace{}, ErrIntegrity
		}
		objects[f.Hash] = v.Bytes
	}
	w := Workspace{ID: uuid.NewString(), Run: run, Repository: repo, Snapshot: snapshot, Commit: commit, Format: snap.Format, Policy: Policy}
	w.Selected = versionID(w, files)
	tx, err := m.DB.Begin(ctx)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('workspace:'||$1,0))`, owner); err != nil {
		return Workspace{}, err
	}
	existing, err = scanWorkspace(tx.QueryRow(ctx, "SELECT "+wsFields+" FROM research_workspaces WHERE run_id=$1 AND owner=$2 AND expires_at>clock_timestamp()", run, owner))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrDenied) {
		return Workspace{}, err
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT count(*)<3 FROM research_workspaces WHERE owner=$1 AND created_at>clock_timestamp()-interval '24 hours'`, owner).Scan(&allowed)
	if err != nil {
		return Workspace{}, err
	}
	if !allowed {
		return Workspace{}, ErrInvalid
	}
	if err = tx.QueryRow(ctx, `INSERT INTO research_workspaces(id,run_id,owner,repository,snapshot,commit_oid,object_format,policy)
 SELECT $1,id,owner,repository,snapshot,commit_oid,$4,$5 FROM research_runs WHERE id=$2 AND owner=$3 AND state='COMPLETED'
 RETURNING expires_at`, w.ID, run, owner, w.Format, Policy).Scan(&w.Expires); err != nil {
		return Workspace{}, err
	}
	batch := &pgx.Batch{}
	for hash, body := range objects {
		if body == nil {
			body = []byte{}
		}
		batch.Queue(`INSERT INTO research_workspace_objects(workspace,hash,body) VALUES($1,$2,$3)`, w.ID, hash, body)
	}
	if err = tx.SendBatch(ctx, batch).Close(); err != nil {
		return Workspace{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO research_workspace_versions(workspace,id,inventory) VALUES($1,$2,$3)`, w.ID, w.Selected, files); err != nil {
		return Workspace{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE research_workspaces SET selected=$2 WHERE id=$1`, w.ID, w.Selected); err != nil {
		return Workspace{}, err
	}
	if err = journal(ctx, tx, w.ID, "prepare_workspace", w.Selected, 0); err != nil {
		return Workspace{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Workspace{}, err
	}
	return w, nil
}

func versionID(w Workspace, files []File) string {
	return digest([]any{w.ID, w.Commit, w.Snapshot, w.Policy, files})
}

func (m WorkspaceManager) inventory(ctx context.Context, w Workspace, id string) ([]File, error) {
	var files []File
	err := m.DB.QueryRow(ctx, `SELECT inventory FROM research_workspace_versions WHERE workspace=$1 AND id=$2`, w.ID, id).Scan(&files)
	if err != nil {
		return nil, err
	}
	if validateInventory(files) != nil || versionID(w, files) != id {
		return nil, ErrIntegrity
	}
	return files, nil
}

func (m WorkspaceManager) InspectWorkspaceVersion(ctx context.Context, owner, id string) (Workspace, error) {
	w, err := m.scope(ctx, owner, id)
	if err != nil {
		return w, err
	}
	w.Files, err = m.inventory(ctx, w, w.Selected)
	if err == nil {
		err = m.inspectReceipt(ctx, owner, id, "inspect_workspace_version", w.Selected)
	}
	return w, err
}

func (m WorkspaceManager) ReadSelected(ctx context.Context, owner, id, path string) (File, []byte, error) {
	w, err := m.InspectWorkspaceVersion(ctx, owner, id)
	if err != nil {
		return File{}, nil, err
	}
	for _, f := range w.Files {
		if f.Path == path && f.Hash != "" {
			bytes, e := m.object(ctx, w.ID, f)
			return f, bytes, e
		}
	}
	return File{}, nil, ErrDenied
}

func (m WorkspaceManager) object(ctx context.Context, workspace string, f File) ([]byte, error) {
	var b []byte
	err := m.DB.QueryRow(ctx, `SELECT body FROM research_workspace_objects WHERE workspace=$1 AND hash=$2`, workspace, f.Hash).Scan(&b)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != f.Size || ingest.Digest(b) != f.Hash {
		return nil, ErrIntegrity
	}
	return b, nil
}

func proposalID(w Workspace, p Patch) string {
	return digest([]any{"apply_approved_patch", ApprovalPolicy, w.Run, w.ID, w.Repository, w.Snapshot, w.Commit, w.Policy, p})
}

// candidate validates declared preimages and constructs the complete new inventory.
func (m WorkspaceManager) candidate(ctx context.Context, w Workspace, p Patch) ([]File, string, error) {
	files, err := m.inventory(ctx, w, p.Base)
	if err != nil {
		return nil, "", err
	}
	index := map[string]int{}
	for i, f := range files {
		index[f.Path] = i
	}
	var diff strings.Builder
	for _, c := range p.Changes {
		i, present := index[c.Path]
		var before string
		switch c.Kind {
		case "ADD":
			if present {
				return nil, "", ErrConflict
			}
		case "MODIFY":
			if !present || files[i].Hash != c.Before || files[i].Size != int64(c.BeforeSize) || files[i].Mode != c.Mode {
				return nil, "", ErrConflict
			}
			b, e := m.object(ctx, w.ID, files[i])
			if e != nil {
				return nil, "", e
			}
			before = string(b)
			if !safeText(before) {
				return nil, "", ErrInvalid
			}
		}
		next := File{Path: c.Path, Mode: c.Mode, Hash: c.After, Size: int64(c.AfterSize)}
		if present {
			files[i] = next
		} else {
			files = append(files, next)
		}
		unified(&diff, c, before)
		if diff.Len() > maxDiff {
			return nil, "", ErrInvalid
		}
	}
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	if err = validateInventory(files); err != nil {
		return nil, "", err
	}
	return files, diff.String(), nil
}

func journal(ctx context.Context, tx pgx.Tx, id, action, digest string, generation int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO research_edit_journal(workspace,action,digest,generation) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, id, action, digest, generation)
	return err
}

func (m WorkspaceManager) Propose(ctx context.Context, owner, id string, p Patch) (Proposal, error) {
	w, err := m.scope(ctx, owner, id)
	if err != nil {
		return Proposal{}, err
	}
	p, err = normalize(p)
	if err != nil {
		return Proposal{}, err
	}
	if p.Base != w.Selected {
		return Proposal{}, ErrConflict
	}
	_, preview, err := m.candidate(ctx, w, p)
	if err != nil {
		return Proposal{}, err
	}
	result := Proposal{Workspace: id, Digest: proposalID(w, p), Patch: p, Diff: preview}
	tx, locked, err := m.lock(ctx, owner, id)
	if err != nil {
		return Proposal{}, err
	}
	defer tx.Rollback(ctx)
	if locked.Selected != p.Base {
		return Proposal{}, ErrConflict
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM research_edit_proposals WHERE workspace=$1 AND digest<>$2`, id, result.Digest).Scan(&count); err != nil {
		return Proposal{}, err
	}
	if count >= 12 {
		return Proposal{}, ErrInvalid
	}
	_, err = tx.Exec(ctx, `INSERT INTO research_edit_proposals(workspace,digest,base,patch,preview) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, id, result.Digest, p.Base, p, preview)
	if err != nil {
		return Proposal{}, err
	}
	if err = journal(ctx, tx, id, "propose_patch", result.Digest, 0); err != nil {
		return Proposal{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Proposal{}, err
	}
	return result, nil
}

func (m WorkspaceManager) InspectPatch(ctx context.Context, owner, id, identity string) (Proposal, error) {
	w, err := m.scope(ctx, owner, id)
	if err != nil {
		return Proposal{}, err
	}
	p, err := m.loadPatch(ctx, w, identity)
	if err == nil {
		err = m.inspectReceipt(ctx, owner, id, "inspect_patch", identity)
	}
	return p, err
}

func (m WorkspaceManager) inspectReceipt(ctx context.Context, owner, id, action, identity string) error {
	tx, w, err := m.lock(ctx, owner, id)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = journal(ctx, tx, id, action, identity, 0); err != nil {
		return err
	}
	if action == "inspect_workspace_version" && w.Selected != identity {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func (m WorkspaceManager) loadPatch(ctx context.Context, w Workspace, identity string) (Proposal, error) {
	if !validHash(identity) {
		return Proposal{}, ErrInvalid
	}
	p := Proposal{Workspace: w.ID, Digest: identity}
	var base string
	err := m.DB.QueryRow(ctx, `SELECT base,patch,preview FROM research_edit_proposals WHERE workspace=$1 AND digest=$2`, w.ID, identity).Scan(&base, &p.Patch, &p.Diff)
	if errors.Is(err, pgx.ErrNoRows) {
		return Proposal{}, ErrDenied
	}
	if err != nil {
		return Proposal{}, err
	}
	p.Patch, err = normalize(p.Patch)
	if err != nil || base != p.Patch.Base || proposalID(w, p.Patch) != identity {
		return Proposal{}, ErrIntegrity
	}
	return p, nil
}

func (m WorkspaceManager) lock(ctx context.Context, owner, id string) (pgx.Tx, Workspace, error) {
	if !validUUID(id) || owner == "" {
		return nil, Workspace{}, ErrDenied
	}
	tx, err := m.DB.Begin(ctx)
	if err != nil {
		return nil, Workspace{}, err
	}
	w, err := scanWorkspace(tx.QueryRow(ctx, "SELECT "+wsFields+" FROM research_workspaces WHERE id=$1 AND owner=$2 AND selected IS NOT NULL AND expires_at>clock_timestamp() FOR UPDATE", id, owner))
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, Workspace{}, err
	}
	return tx, w, nil
}
