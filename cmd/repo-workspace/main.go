// repo-workspace exposes approved virtual edits only to a trusted operator.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "private workspace request failed")
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-development-editing", false, "enable the trusted workspace operator")
	action := flag.String("action", "", "prepare_workspace, inspect_workspace_version, read, propose_patch, inspect_patch, request_patch_approval, grant, deny, cancel, apply_approved_patch")
	runID := flag.String("run", "", "completed research run")
	id := flag.String("workspace", "", "authorized workspace ID")
	proposal := flag.String("digest", "", "exact proposal SHA-256")
	diffOnly := flag.Bool("diff", false, "print the unified preview for inspect_patch")
	path := flag.String("path", "", "virtual selected file for read")
	flag.Parse()
	cfg, e := config.Load()
	url := os.Getenv("REPO_DATABASE_URL")
	if e != nil || !*allow || os.Getenv("REPO_WORKSPACE_ENABLED") != "true" || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" || url == "" {
		return workspace.ErrDenied
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	db, e := pgxpool.New(ctx, url)
	if e != nil {
		return e
	}
	defer db.Close()
	if e = migrations.Check(ctx, db); e != nil {
		return e
	}
	m := workspace.WorkspaceManager{DB: db}
	owner := cfg.DevPrincipal
	emit := func(v any, err error) error {
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	}
	switch *action {
	case "prepare_workspace":
		var objects ingest.BlobStore
		switch os.Getenv("REPO_STORAGE_BACKEND") {
		case "local":
			root := os.Getenv("REPO_LOCAL_ROOT")
			if !filepath.IsAbs(root) {
				return workspace.ErrDenied
			}
			store, e := ingest.OpenLocal(root)
			if e != nil {
				return e
			}
			defer store.Close()
			objects = store
		case "s3":
			bucket := os.Getenv("REPO_S3_BUCKET")
			key := os.Getenv("REPO_S3_ACCESS_KEY")
			if bucket == os.Getenv("S3_BUNDLE_BUCKET") || bucket == os.Getenv("S3_CACHE_BUCKET") || (key != "" && key == os.Getenv("S3_ACCESS_KEY")) {
				return workspace.ErrDenied
			}
			store, e := ingest.OpenS3(os.Getenv("REPO_S3_ENDPOINT"), bucket, key, os.Getenv("REPO_S3_SECRET_KEY"), os.Getenv("REPO_S3_TLS") != "false")
			if e != nil {
				return e
			}
			objects = store
		default:
			return workspace.ErrDenied
		}
		m.Source = &ingest.Service{DB: ingest.Database{Pool: db}, Objects: objects}
		return emit(m.Create(ctx, owner, *runID))
	case "inspect_workspace_version":
		return emit(m.InspectWorkspaceVersion(ctx, owner, *id))
	case "read":
		f, b, e := m.ReadSelected(ctx, owner, *id, *path)
		return emit(struct {
			File  workspace.File `json:"file"`
			Bytes []byte         `json:"bytes"`
		}{f, b}, e)
	case "propose_patch":
		p, e := workspace.Decode(os.Stdin)
		if e != nil {
			return e
		}
		return emit(m.Propose(ctx, owner, *id, p))
	case "inspect_patch":
		p, e := m.InspectPatch(ctx, owner, *id, *proposal)
		if e == nil && *diffOnly {
			_, e = fmt.Fprint(os.Stdout, p.Diff)
			return e
		}
		return emit(p, e)
	case "request_patch_approval":
		return emit(m.RequestPatchApproval(ctx, owner, *id, *proposal))
	case "grant", "deny", "cancel":
		return emit(m.Decide(ctx, owner, *id, *proposal, *action))
	case "apply_approved_patch":
		return emit(m.ApplyApprovedPatch(ctx, owner, *id, *proposal))
	default:
		return workspace.ErrInvalid
	}
}
