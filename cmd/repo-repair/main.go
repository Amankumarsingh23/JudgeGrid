// repo-repair is a trusted operator command for narrowly scoped, reviewed repairs.
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
	"github.com/Amankumarsingh23/JudgeGrid/internal/repair"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/internal/validation"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "private repair request failed")
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-development-repair", false, "explicit trusted repair operator authority")
	approve := flag.Bool("allow-development-approval", false, "authenticated human decision")
	fixture := flag.Bool("fixture-planner", false, "opt in to deterministic fixture proposals")
	action := flag.String("action", "", "start, inspect, diagnose, propose, submit, inspect_patch, grant, deny, resume, cancel")
	baseline := flag.String("baseline", "", "authoritative failing validation digest")
	file := flag.String("target", "", "one bounded editable Go source target")
	goal := flag.String("requirement", "", "bounded task requirement")
	id := flag.String("id", "", "repair run")
	digest := flag.String("digest", "", "exact patch digest for grant or deny")
	flag.Parse()
	cfg, e := config.Load()
	url := os.Getenv("REPO_DATABASE_URL")
	if e != nil || !*allow || os.Getenv("REPO_REPAIR_ENABLED") != "true" || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" || url == "" || flag.NArg() != 0 {
		return repair.ErrDenied
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if tr, e := telemetry.Start("repo-repair"); e == nil && tr != nil {
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
	w := workspace.WorkspaceManager{DB: db}
	s := repair.Service{DB: db, Workspace: w, Validation: validation.Service{DB: db, Workspace: w}}
	if *fixture {
		s.Proposer = repair.Fixture{}
	}
	owner := cfg.DevPrincipal
	emit := func(v any, err error) error {
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	}
	switch *action {
	case "start":
		return emit(s.Start(ctx, owner, *baseline, *file, *goal))
	case "inspect":
		return emit(s.Inspect(ctx, owner, *id))
	case "diagnose":
		return emit(s.Diagnose(ctx, owner, *id))
	case "propose":
		return emit(s.Propose(ctx, owner, *id, nil))
	case "submit":
		p, e := workspace.Decode(os.Stdin)
		if e != nil {
			return e
		}
		return emit(s.Propose(ctx, owner, *id, &p))
	case "inspect_patch":
		return emit(s.Review(ctx, owner, *id))
	case "grant", "deny":
		if !*approve {
			return repair.ErrDenied
		}
		return emit(s.Decide(ctx, owner, *id, *digest, *action))
	case "resume", "cancel":
	default:
		return repair.ErrDenied
	}
	images, e := execution.LoadImages(".sandbox-images.local.json")
	if e != nil {
		return e
	}
	s.Validation.Docker, e = execution.NewDocker(images, "infra/sandbox/seccomp.json", 1)
	if e != nil {
		return e
	}
	defer s.Validation.Docker.Close()
	if *action == "cancel" {
		return s.Cancel(ctx, owner, *id)
	}
	return emit(s.Resume(ctx, owner, *id))
}
