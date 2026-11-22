// repo-research is an opt-in trusted development operator command.
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

	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/contextpack"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/research"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "research request failed; check the opt-in, scope and private run status")
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-development-research", false, "trusted local operator opt-in")
	action := flag.String("action", "", "create, show, resume, cancel, grant, deny")
	index := flag.String("index", "", "exact READY retrieval index for create")
	goal := flag.String("goal", "", "lease, outbox, cache, or a bounded search question")
	runID := flag.String("run", "", "private run ID")
	ordinal := flag.Int("step", 0, "approval step number")
	digest := flag.String("digest", "", "exact approval action digest")
	flag.Parse()
	cfg, err := config.Load()
	url := os.Getenv("REPO_DATABASE_URL")
	if err != nil || !*allow || os.Getenv("REPO_RESEARCH_ENABLED") != "true" || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" || url == "" {
		return research.ErrDenied
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if tr, e := telemetry.Start("repo-research"); e == nil && tr != nil {
		defer tr.Close()
	}
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = migrations.Check(ctx, db); err != nil {
		return err
	}
	s := research.Service{DB: db, Planner: research.Fixture{}}
	owner := cfg.DevPrincipal
	emit := json.NewEncoder(os.Stdout)
	switch *action {
	case "show":
		result, e := s.View(ctx, owner, *runID)
		if e != nil {
			return e
		}
		return emit.Encode(result)
	case "cancel":
		return s.Cancel(ctx, owner, *runID)
	case "grant", "deny":
		return s.Decide(ctx, owner, *runID, *ordinal, *digest, *action == "grant")
	case "create", "resume":
	default:
		return research.ErrPolicy
	}
	var objects ingest.BlobStore
	switch os.Getenv("REPO_STORAGE_BACKEND") {
	case "local":
		root := os.Getenv("REPO_LOCAL_ROOT")
		if !filepath.IsAbs(root) {
			return research.ErrDenied
		}
		storage, e := ingest.OpenLocal(root)
		if e != nil {
			return e
		}
		defer storage.Close()
		objects = storage
	case "s3":
		bucket := os.Getenv("REPO_S3_BUCKET")
		access := os.Getenv("REPO_S3_ACCESS_KEY")
		if bucket == os.Getenv("S3_BUNDLE_BUCKET") || bucket == os.Getenv("S3_CACHE_BUCKET") || (access != "" && access == os.Getenv("S3_ACCESS_KEY")) {
			return research.ErrDenied
		}
		storage, e := ingest.OpenS3(os.Getenv("REPO_S3_ENDPOINT"), bucket, access, os.Getenv("REPO_S3_SECRET_KEY"), os.Getenv("REPO_S3_TLS") != "false")
		if e != nil {
			return e
		}
		objects = storage
	default:
		return research.ErrDenied
	}
	model, err := retrieval.OpenModel(os.Getenv("REPO_EMBEDDING_PYTHON"), os.Getenv("REPO_EMBEDDING_MODEL"))
	if err != nil {
		return err
	}
	defer model.Close()
	engine, err := contextpack.OpenEngine(os.Getenv("REPO_CONTEXT_PYTHON"), os.Getenv("REPO_CONTEXT_MODEL"))
	if err != nil {
		return err
	}
	defer engine.Close()
	analysisReader := &analysis.Service{DB: analysis.Database{Pool: db}, Source: &ingest.Service{DB: ingest.Database{Pool: db}, Objects: objects}}
	retrievalReader := &retrieval.Service{DB: retrieval.Database{Pool: db}, Source: analysisReader, Model: model}
	s.Readers = research.Readers{Analysis: analysisReader, Retrieval: retrievalReader, Packs: &contextpack.Builder{Analysis: analysisReader, Retrieval: retrievalReader, Engine: engine}}
	if *action == "create" {
		result, e := s.Create(ctx, owner, *index, *goal)
		if e != nil {
			return e
		}
		return emit.Encode(result)
	}
	if err = s.Resume(ctx, owner, *runID); err != nil {
		return err
	}
	result, err := s.View(ctx, owner, *runID)
	if err != nil {
		return err
	}
	return emit.Encode(result)
}
