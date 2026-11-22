// repo-eval is trusted, opt-in, private offline evaluation tooling.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/contextpack"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/repoeval"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
)

func main() {
	debug.SetMemoryLimit(512 << 20)
	if run() != nil {
		fmt.Fprintln(os.Stderr, "private repository evaluation failed")
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-development-evaluation", false, "explicit trusted operator opt-in")
	action := flag.String("action", "", "policy, catalog, freeze, validate, run, report")
	index := flag.String("index", "", "authorized READY retrieval index")
	manifest := flag.String("benchmark", "", "private draft/frozen benchmark JSON")
	output := flag.String("output", "", "exclusive private JSON for catalog/freeze")
	dir := flag.String("directory", "", "absolute private checkpoint/report directory")
	fresh := flag.String("fresh", "", "comma-separated <=3 query IDs for first/repeated production-reader latency")
	flag.Parse()
	cfg, e := config.Load()
	if e != nil || !*allow || os.Getenv("REPO_EVALUATION_ENABLED") != "true" || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" || os.Getenv("REPO_DATABASE_URL") == "" {
		return repoeval.ErrDenied
	}
	emit := func(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
	if *action == "policy" {
		return emit(repoeval.DefaultPolicy())
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, 60*time.Minute)
	defer deadline()
	db, e := pgxpool.New(ctx, os.Getenv("REPO_DATABASE_URL"))
	if e != nil {
		return e
	}
	defer db.Close()
	if e = migrations.Check(ctx, db); e != nil {
		return e
	}
	var objects ingest.BlobStore
	switch os.Getenv("REPO_STORAGE_BACKEND") {
	case "local":
		root := os.Getenv("REPO_LOCAL_ROOT")
		if !filepath.IsAbs(root) {
			return repoeval.ErrDenied
		}
		s, x := ingest.OpenLocal(root)
		if x != nil {
			return x
		}
		defer s.Close()
		objects = s
	case "s3":
		bucket := os.Getenv("REPO_S3_BUCKET")
		if bucket == os.Getenv("S3_BUNDLE_BUCKET") || bucket == os.Getenv("S3_CACHE_BUCKET") || (os.Getenv("S3_ACCESS_KEY") != "" && os.Getenv("REPO_S3_ACCESS_KEY") == os.Getenv("S3_ACCESS_KEY")) {
			return repoeval.ErrDenied
		}
		s, x := ingest.OpenS3(os.Getenv("REPO_S3_ENDPOINT"), bucket, os.Getenv("REPO_S3_ACCESS_KEY"), os.Getenv("REPO_S3_SECRET_KEY"), os.Getenv("REPO_S3_TLS") != "false")
		if x != nil {
			return x
		}
		objects = s
	default:
		return repoeval.ErrDenied
	}
	source := &analysis.Service{DB: analysis.Database{Pool: db}, Source: &ingest.Service{DB: ingest.Database{Pool: db}, Objects: objects}}
	model, e := retrieval.OpenModel(os.Getenv("REPO_EMBEDDING_PYTHON"), os.Getenv("REPO_EMBEDDING_MODEL"))
	if e != nil {
		return e
	}
	defer model.Close()
	engine, e := contextpack.OpenEngine(os.Getenv("REPO_CONTEXT_PYTHON"), os.Getenv("REPO_CONTEXT_MODEL"))
	if e != nil {
		return e
	}
	defer engine.Close()
	reader := &retrieval.Service{DB: retrieval.Database{Pool: db}, Source: source, Model: model}
	s, e := repoeval.Capture(ctx, cfg.DevPrincipal, *index, reader, source, engine)
	if e != nil {
		return e
	}
	if *action == "catalog" {
		c, e := s.Export(ctx, cfg.DevPrincipal)
		if e != nil {
			return e
		}
		if filepath.Ext(*output) != ".json" {
			return repoeval.ErrInvalid
		}
		if e = repoeval.WriteExclusive(*output, c); e != nil {
			return e
		}
		return emit(map[string]any{"captured": true, "source_files": len(c.Files), "documents": len(c.Documents), "seconds": s.SourceCaptureSeconds})
	}
	var b repoeval.Benchmark
	if e = repoeval.ReadJSON(*manifest, 8<<20, &b); e != nil {
		return e
	}
	if *action == "freeze" {
		if b.Frozen {
			return repoeval.ErrInvalid
		}
		b.Frozen = true
		b.Policy = repoeval.DefaultPolicy()
		b.SplitDigest = b.SplitsID()
		if e = s.Validate(ctx, cfg.DevPrincipal, b); e != nil {
			return e
		}
		if filepath.Ext(*output) != ".json" {
			return repoeval.ErrInvalid
		}
		if e = repoeval.WriteExclusive(*output, b); e != nil {
			return e
		}
		return emit(map[string]any{"frozen": true, "benchmark_sha256": b.ID(), "queries": len(b.Queries), "policy_sha256": b.Policy.ID()})
	}
	if e = s.Validate(ctx, cfg.DevPrincipal, b); e != nil {
		return e
	}
	if *action == "validate" {
		if e = s.Reverify(ctx, cfg.DevPrincipal); e != nil {
			return e
		}
		return emit(map[string]any{"valid": true, "benchmark_sha256": b.ID(), "queries": len(b.Queries)})
	}
	if !filepath.IsAbs(*dir) {
		return repoeval.ErrInvalid
	}
	if *action == "report" {
		cp, e := repoeval.LoadCheckpoint(*dir, b)
		if e != nil {
			return e
		}
		if len(cp.Rows) != len(b.Queries)*len(repoeval.Variants) {
			return repoeval.ErrIncomplete
		}
		if len(cp.Fresh) != len(cp.FreshPlan)*12 {
			return repoeval.ErrIncomplete
		}
		if e = s.RecheckCheckpoint(ctx, cfg.DevPrincipal, b, &cp); e != nil {
			return e
		}
		r := repoeval.BuildReport(b, cp, s)
		if e = repoeval.WriteReport(*dir, r); e != nil {
			return e
		}
		return emit(map[string]any{"state": r.State, "metrics_sha256": r.MetricsDigest})
	}
	if *action != "run" {
		return repoeval.ErrInvalid
	}
	ids := []string{}
	if *fresh != "" {
		ids = strings.Split(*fresh, ",")
	}
	r, e := s.Run(ctx, cfg.DevPrincipal, b, *dir, ids)
	if e != nil {
		return e
	}
	return emit(map[string]any{"state": r.State, "benchmark_sha256": r.Benchmark, "metrics_sha256": r.MetricsDigest, "queries": r.Queries, "observations": r.Observations, "errors": r.Counts})
}
