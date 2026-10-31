// repo-search is explicitly enabled trusted local tooling, absent from services.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
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
		fmt.Fprintln(os.Stderr, "repository retrieval failed; inspect private operator state")
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-development-retrieval", false, "explicit trusted operator opt-in")
	action := flag.String("action", "", "policy, migrate, build, retry, status, manifest, verify, bm25, dense, hybrid, compare, chunk")
	index := flag.String("index", "", "exact retrieval index ID")
	source := flag.String("analysis", "", "exact authorized READY analysis ID")
	query := flag.String("query", "", "private search query")
	top := flag.Int("top-k", 10, "bounded top K")
	depth := flag.Int("depth", 100, "bounded per-channel depth")
	ann := flag.Bool("ann", false, "per-index HNSW with exact underfill fallback")
	path := flag.String("file", "", "exact path filter")
	language := flag.String("language", "", "language filter")
	state := flag.String("parse-state", "", "PARSED/PARTIAL/UNSUPPORTED filter")
	id := flag.String("chunk", "", "ranked chunk ID")
	output := flag.String("output", "", "exclusive new private .txt source export")
	policy := flag.String("policy", "", "bounded complete policy JSON")
	flag.Parse()
	cfg, e := config.Load()
	if e != nil || !*allow || os.Getenv("REPO_RETRIEVAL_ENABLED") != "true" || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" || os.Getenv("REPO_DATABASE_URL") == "" {
		return retrieval.ErrDenied
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, 1900*time.Second)
	defer deadline()
	emit := func(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
	p := retrieval.DefaultPolicy()
	if *policy != "" {
		f, x := os.Open(*policy)
		if x != nil {
			return x
		}
		defer f.Close()
		d := json.NewDecoder(io.LimitReader(f, 16385))
		d.DisallowUnknownFields()
		if x = d.Decode(&p); x != nil {
			return x
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return retrieval.ErrBudget
		}
	}
	if p.Validate() != nil {
		return retrieval.ErrBudget
	}
	if *action == "policy" {
		return emit(p)
	}
	db, e := pgxpool.New(ctx, os.Getenv("REPO_DATABASE_URL"))
	if e != nil {
		return e
	}
	defer db.Close()
	if *action == "migrate" {
		return migrations.Apply(ctx, db)
	}
	if e = migrations.Check(ctx, db); e != nil {
		return e
	}
	svc := retrieval.Service{DB: retrieval.Database{Pool: db}}
	if *action == "status" {
		a, x := svc.DB.Status(ctx, cfg.DevPrincipal, *index)
		if x != nil {
			return x
		}
		return emit(a)
	}
	var objects ingest.BlobStore
	switch os.Getenv("REPO_STORAGE_BACKEND") {
	case "local":
		root := os.Getenv("REPO_LOCAL_ROOT")
		if !filepath.IsAbs(root) {
			return retrieval.ErrDenied
		}
		store, x := ingest.OpenLocal(root)
		if x != nil {
			return x
		}
		defer store.Close()
		objects = store
	case "s3":
		bucket := os.Getenv("REPO_S3_BUCKET")
		if bucket == os.Getenv("S3_BUNDLE_BUCKET") || bucket == os.Getenv("S3_CACHE_BUCKET") || (os.Getenv("S3_ACCESS_KEY") != "" && os.Getenv("REPO_S3_ACCESS_KEY") == os.Getenv("S3_ACCESS_KEY")) {
			return retrieval.ErrDenied
		}
		store, x := ingest.OpenS3(os.Getenv("REPO_S3_ENDPOINT"), bucket, os.Getenv("REPO_S3_ACCESS_KEY"), os.Getenv("REPO_S3_SECRET_KEY"), os.Getenv("REPO_S3_TLS") != "false")
		if x != nil {
			return x
		}
		objects = store
	default:
		return retrieval.ErrDenied
	}
	svc.Source = &analysis.Service{DB: analysis.Database{Pool: db}, Source: &ingest.Service{DB: ingest.Database{Pool: db}, Objects: objects}}
	if *action == "build" || *action == "retry" || *action == "dense" || *action == "hybrid" || *action == "compare" {
		model, x := retrieval.OpenModel(os.Getenv("REPO_EMBEDDING_PYTHON"), os.Getenv("REPO_EMBEDDING_MODEL"))
		if x != nil {
			return x
		}
		defer model.Close()
		svc.Model = model
	}
	tr, e := telemetry.Start("repository-retrieval")
	if e != nil {
		return e
	}
	if tr != nil {
		defer tr.Close()
	}
	if *action == "build" || *action == "retry" {
		a, x := svc.Build(ctx, cfg.DevPrincipal, *source, p, *action == "retry")
		if x != nil {
			return x
		}
		return emit(a)
	}
	if *action == "chunk" {
		v, x := svc.ReadChunk(ctx, cfg.DevPrincipal, *index, *id)
		if x != nil {
			return x
		}
		if *output != "" {
			if filepath.Ext(*output) != ".txt" {
				return retrieval.ErrDenied
			}
			f, x := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if x != nil {
				return x
			}
			_, x = f.Write(v.Bytes)
			ce := f.Close()
			if x != nil {
				return x
			}
			if ce != nil {
				return ce
			}
		}
		return emit(v.Chunk)
	}
	a, m, _, e := svc.Ready(ctx, cfg.DevPrincipal, *index)
	if e != nil {
		return e
	}
	switch *action {
	case "manifest":
		return emit(m)
	case "verify":
		if e = svc.Verify(ctx, cfg.DevPrincipal, *index); e != nil {
			return e
		}
		return emit(map[string]any{"verified": true, "documents": len(m.Documents), "index": a.ID})
	case "bm25", "dense", "hybrid", "compare":
		q := retrieval.Request{Index: a.ID, Snapshot: a.Snapshot, Repository: a.Repository, Query: *query, Mode: *action, TopK: *top, Depth: *depth, ANN: *ann, Filter: retrieval.Filter{Path: *path, Language: *language, State: *state}}
		if *action == "compare" {
			out := map[string]retrieval.Results{}
			for _, mode := range []string{"bm25", "dense", "hybrid"} {
				q.Mode = mode
				v, x := svc.Search(ctx, cfg.DevPrincipal, q)
				if x != nil {
					return x
				}
				out[mode] = v
			}
			return emit(out)
		}
		v, x := svc.Search(ctx, cfg.DevPrincipal, q)
		if x != nil {
			return x
		}
		return emit(v)
	}
	return retrieval.ErrDenied
}
