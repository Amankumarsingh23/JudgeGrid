// repo-validate is a trusted host command for fixed, offline validation profiles.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/internal/validation"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "private validation request failed")
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-development-validation", false, "explicit trusted host authorization")
	action := flag.String("action", "", "request_validation, run, inspect_validation, read_validation_log, cancel_validation")
	workspaceID := flag.String("workspace", "", "selected workspace identity")
	version := flag.String("version", "", "exact selected version hash")
	profile := flag.String("profile", "", "go-unit or go-format-check")
	id := flag.String("id", "", "validation digest")
	flag.Parse()
	cfg, e := config.Load()
	url := os.Getenv("REPO_DATABASE_URL")
	if e != nil || !*allow || os.Getenv("REPO_VALIDATION_ENABLED") != "true" || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" || url == "" || flag.NArg() != 0 {
		return validation.ErrDenied
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	if tr, e := telemetry.Start("repo-validate"); e == nil && tr != nil {
		defer tr.Close()
	}
	db, e := pgxpool.New(ctx, url)
	if e != nil {
		return e
	}
	defer db.Close()
	if e = migrations.Check(ctx, db); e != nil {
		return e
	}
	s := validation.Service{DB: db, Workspace: workspace.WorkspaceManager{DB: db}}
	owner := cfg.DevPrincipal
	emit := func(v any, err error) error {
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	}
	switch *action {
	case "inspect_validation":
		return emit(s.InspectValidation(ctx, owner, *id))
	case "read_validation_log":
		r, e := s.InspectValidation(ctx, owner, *id)
		if e != nil {
			return e
		}
		if r.Report == nil {
			return validation.ErrState
		}
		return emit(r.Report, nil)
	case "request_validation", "run", "cancel_validation":
	default:
		return validation.ErrDenied
	}
	images, e := execution.LoadImages(".sandbox-images.local.json")
	if e != nil {
		return e
	}
	s.Docker, e = execution.NewDocker(images, "infra/sandbox/seccomp.json", 1)
	if e != nil {
		return e
	}
	defer s.Docker.Close()
	switch *action {
	case "request_validation":
		return emit(s.RequestValidation(ctx, owner, *workspaceID, *version, *profile))
	case "run":
		return emit(s.Run(ctx, owner, *id))
	default:
		return s.CancelValidation(ctx, owner, *id)
	}
}
