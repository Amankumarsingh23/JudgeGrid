// repo-task composes only the trusted coding-task controllers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/repair"
	"github.com/Amankumarsingh23/JudgeGrid/internal/research"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"github.com/Amankumarsingh23/JudgeGrid/internal/task"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/internal/validation"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if e := run(); e != nil {
		message := "private coding task request failed"
		if errors.Is(e, task.ErrModelUnavailable) {
			message = e.Error()
		}
		fmt.Fprintln(os.Stderr, message)
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-development-task", false, "trusted development task authority")
	approve := flag.Bool("allow-development-approval", false, "trusted human approval authority")
	action := flag.String("action", "", "start, resume, inspect, research, inspect_patch, workspace, validation, repair, grant, deny, cancel, report")
	provider := flag.String("provider", "", "fixture or fixture-two")
	id := flag.String("id", "", "task UUID; optional on start")
	index := flag.String("index", "", "exact READY retrieval index")
	goal := flag.String("goal", "", "bounded user task")
	target := flag.String("target", "", "one ordinary Go target; omit for research only")
	patch := flag.String("digest", "", "exact proposal digest")
	flag.Parse()
	cfg, e := config.Load()
	url := os.Getenv("REPO_DATABASE_URL")
	if e != nil || !*allow || os.Getenv("REPO_TASK_ENABLED") != "true" || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" || url == "" || flag.NArg() != 0 {
		return task.ErrDenied
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	if tr, e := telemetry.Start("repo-task"); e == nil && tr != nil {
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
	s := task.Service{DB: db, Workspace: w, Research: research.Service{DB: db, Planner: task.ResearchFixture{}}, Validation: validation.Service{DB: db, Workspace: w}}
	owner := cfg.DevPrincipal
	emit := func(v any, err error) error {
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	}
	switch *action {
	case "inspect":
		return emit(s.Inspect(ctx, owner, *id))
	case "research":
		return emit(s.ResearchEvidence(ctx, owner, *id))
	case "inspect_patch":
		return emit(s.Review(ctx, owner, *id))
	case "grant", "deny":
		if !*approve {
			return task.ErrDenied
		}
		return emit(s.Decide(ctx, owner, *id, *patch, *action))
	case "workspace", "validation", "repair", "report":
		item, e := s.Inspect(ctx, owner, *id)
		if e != nil {
			return e
		}
		switch *action {
		case "workspace":
			return emit(w.InspectWorkspaceVersion(ctx, owner, item.Workspace))
		case "validation":
			v := item.Baseline
			if item.Report != nil && item.Report.Validation != "" {
				v = item.Report.Validation
			}
			return emit(s.Validation.InspectValidation(ctx, owner, v))
		case "repair":
			return emit((repair.Service{DB: db}).Inspect(ctx, owner, item.Repair))
		default:
			if item.Report == nil {
				return task.ErrPolicy
			}
			return emit(item.Report, nil)
		}
	case "cancel", "start", "resume":
	default:
		return task.ErrPolicy
	}
	if *action == "cancel" || *action == "resume" {
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
	}
	switch *provider {
	case "fixture":
		s.Model = task.Fixture{}
	case "fixture-two":
		s.Model = task.Fixture{FirstWrong: true}
	default:
		return task.ErrModelUnavailable
	}
	var blobs ingest.BlobStore
	switch os.Getenv("REPO_STORAGE_BACKEND") {
	case "local":
		root := os.Getenv("REPO_LOCAL_ROOT")
		if !filepath.IsAbs(root) {
			return task.ErrDenied
		}
		f, e := ingest.OpenLocal(root)
		if e != nil {
			return e
		}
		defer f.Close()
		blobs = f
	case "s3":
		bucket, key := os.Getenv("REPO_S3_BUCKET"), os.Getenv("REPO_S3_ACCESS_KEY")
		if bucket == os.Getenv("S3_BUNDLE_BUCKET") || bucket == os.Getenv("S3_CACHE_BUCKET") || (key != "" && key == os.Getenv("S3_ACCESS_KEY")) {
			return task.ErrDenied
		}
		f, e := ingest.OpenS3(os.Getenv("REPO_S3_ENDPOINT"), bucket, key, os.Getenv("REPO_S3_SECRET_KEY"), os.Getenv("REPO_S3_TLS") != "false")
		if e != nil {
			return e
		}
		blobs = f
	default:
		return task.ErrDenied
	}
	source := &ingest.Service{DB: ingest.Database{Pool: db}, Objects: blobs}
	model, e := retrieval.OpenModel(os.Getenv("REPO_EMBEDDING_PYTHON"), os.Getenv("REPO_EMBEDDING_MODEL"))
	if e != nil {
		return e
	}
	defer model.Close()
	a := &analysis.Service{DB: analysis.Database{Pool: db}, Source: source}
	s.Research.Readers = research.Readers{Analysis: a, Retrieval: &retrieval.Service{DB: retrieval.Database{Pool: db}, Source: a, Model: model}}
	s.Workspace.Source = source
	if *action == "start" {
		if *id == "" {
			*id = uuid.NewString()
		}
		return emit(s.Start(ctx, owner, *id, *index, *goal, *target))
	}
	return emit(s.Resume(ctx, owner, *id))
}
