// repo-context is a trusted, opt-in evidence tool. It cannot execute pack content.
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
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"
)

func main() {
	debug.SetMemoryLimit(256 << 20)
	if run() != nil {
		fmt.Fprintln(os.Stderr, "private context operation failed")
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-development-context", false, "trusted local operator opt-in")
	action := flag.String("action", "", "policy, pack, rerank, graph, tokens, citations, compare, verify")
	index := flag.String("index", "", "authorized READY retrieval index")
	query := flag.String("query", "", "private query")
	intent := flag.String("intent", "", "optional exact/implementation/conceptual/tests/documentation/mixed override")
	policy := flag.String("policy", "", "bounded complete context policy JSON")
	output := flag.String("output", "", "exclusive new private .json pack output")
	input := flag.String("input", "", "private pack .json for verification")
	budget := flag.Int("tokens", 0, "optional bounded token budget")
	flag.Parse()
	cfg, e := config.Load()
	if e != nil || !*allow || os.Getenv("REPO_CONTEXT_ENABLED") != "true" || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" || os.Getenv("REPO_DATABASE_URL") == "" {
		return contextpack.ErrDenied
	}
	p := contextpack.DefaultPolicy()
	if *policy != "" {
		if e = readJSON(*policy, 16384, &p); e != nil {
			return e
		}
	}
	if *budget != 0 {
		p.Tokens = *budget
	}
	if p.Validate() != nil {
		return contextpack.ErrBudget
	}
	emit := func(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
	if *action == "policy" {
		return emit(p)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, 310*time.Second)
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
			return contextpack.ErrDenied
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
			return contextpack.ErrDenied
		}
		s, x := ingest.OpenS3(os.Getenv("REPO_S3_ENDPOINT"), bucket, os.Getenv("REPO_S3_ACCESS_KEY"), os.Getenv("REPO_S3_SECRET_KEY"), os.Getenv("REPO_S3_TLS") != "false")
		if x != nil {
			return x
		}
		objects = s
	default:
		return contextpack.ErrDenied
	}
	source := &analysis.Service{DB: analysis.Database{Pool: db}, Source: &ingest.Service{DB: ingest.Database{Pool: db}, Objects: objects}}
	retrievalModel, e := retrieval.OpenModel(os.Getenv("REPO_EMBEDDING_PYTHON"), os.Getenv("REPO_EMBEDDING_MODEL"))
	if e != nil {
		return e
	}
	defer retrievalModel.Close()
	engine, e := contextpack.OpenEngine(os.Getenv("REPO_CONTEXT_PYTHON"), os.Getenv("REPO_CONTEXT_MODEL"))
	if e != nil {
		return e
	}
	defer engine.Close()
	reader := &retrieval.Service{DB: retrieval.Database{Pool: db}, Source: source, Model: retrievalModel}
	builder := contextpack.Builder{Retrieval: reader, Analysis: source, Engine: engine}
	tr, e := telemetry.Start("repository-context")
	if e != nil {
		return e
	}
	if tr != nil {
		defer tr.Close()
	}
	if *action == "verify" {
		var pack contextpack.Pack
		if e = readJSON(*input, 2<<20, &pack); e != nil {
			return e
		}
		if e = builder.Verify(ctx, cfg.DevPrincipal, pack); e != nil {
			return e
		}
		return emit(map[string]any{"verified": true, "pack_id": pack.ID})
	}
	a, _, _, e := reader.Ready(ctx, cfg.DevPrincipal, *index)
	if e != nil {
		return e
	}
	request := contextpack.Request{Repository: a.Repository, Snapshot: a.Snapshot, Index: a.ID, Query: *query, Intent: *intent, Policy: p}
	pack, e := builder.Build(ctx, cfg.DevPrincipal, request)
	if e != nil {
		return e
	}
	if *output != "" {
		if filepath.Ext(*output) != ".json" {
			return contextpack.ErrDenied
		}
		f, x := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if x != nil {
			return x
		}
		x = json.NewEncoder(f).Encode(pack)
		ce := f.Close()
		if x != nil {
			return x
		}
		if ce != nil {
			return ce
		}
	}
	switch *action {
	case "pack":
		return emit(pack)
	case "rerank":
		return emit(map[string]any{"status": pack.Status, "scope": pack.Scope, "candidates": pack.Candidates})
	case "graph":
		out := map[string][]contextpack.Reason{}
		for _, c := range pack.Candidates {
			if len(c.Graph) > 0 {
				out[c.Chunk.ID] = c.Graph
			}
		}
		return emit(map[string]any{"status": pack.Status, "scope": pack.Scope, "graph": out})
	case "tokens":
		return emit(map[string]any{"status": pack.Status, "budget": pack.Budget, "consumed": pack.Consumed, "remaining": pack.Remaining, "omitted": pack.Omitted})
	case "citations":
		return emit(map[string]any{"status": pack.Status, "scope": pack.Scope, "evidence": pack.Evidence})
	case "compare":
		request.Baseline = true
		base, e := builder.Build(ctx, cfg.DevPrincipal, request)
		if e != nil {
			return e
		}
		return emit(struct{ Hybrid, Context contextpack.Pack }{base, pack})
	}
	return contextpack.ErrDenied
}
func readJSON(path string, max int64, value any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil || stat.Size() > max {
		return contextpack.ErrBudget
	}
	d := json.NewDecoder(io.LimitReader(f, max+1))
	d.DisallowUnknownFields()
	if e = d.Decode(value); e != nil {
		return e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return contextpack.ErrBudget
	}
	return nil
}
