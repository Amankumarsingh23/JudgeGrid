// repo-analyze is a trusted opt-in operator CLI, absent from ordinary images.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	debug.SetMemoryLimit(128 << 20)
	if e := run(); e != nil {
		code := "internal"
		for k, v := range map[string]error{"denied": analysis.ErrDenied, "budget": analysis.ErrBudget, "parser": analysis.ErrParser, "integrity": analysis.ErrIntegrity, "fenced": analysis.ErrFence, "busy": analysis.ErrBusy, "source-integrity": ingest.ErrIntegrity, "source-storage": ingest.ErrStorage, "canceled": context.Canceled} {
			if errors.Is(e, v) {
				code = k
				break
			}
		}
		fmt.Fprintln(os.Stderr, "repository analysis failed ("+code+")")
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-development-analysis", false, "explicit trusted local operator opt-in")
	action := flag.String("action", "", "migrate, policy, analyze, retry, status, files, symbols, symbol, imports, calls, edges, unresolved, chunks, chunk, compare")
	snapshot := flag.String("snapshot", "", "authorized READY source snapshot ID")
	index := flag.String("index", "", "analysis index ID")
	other := flag.String("other", "", "second authorized READY analysis index ID")
	id := flag.String("id", "", "symbol or chunk ID")
	file := flag.String("file", "", "filter exact snapshot-relative path")
	kind := flag.String("kind", "", "symbol kind")
	output := flag.String("output", "", "new private .txt file for source bytes (never overwritten)")
	policyFile := flag.String("policy", "", "optional bounded versioned policy JSON")
	flag.Parse()
	cfg, e := config.Load()
	if e != nil || !*allow || os.Getenv("REPO_ANALYSIS_ENABLED") != "true" || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" || os.Getenv("REPO_DATABASE_URL") == "" {
		return analysis.ErrDenied
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 630*time.Second)
	defer timeout()
	p := analysis.DefaultPolicy()
	if *policyFile != "" {
		f, x := os.Open(*policyFile)
		if x != nil {
			return analysis.ErrDenied
		}
		defer f.Close()
		d := json.NewDecoder(io.LimitReader(f, 16385))
		d.DisallowUnknownFields()
		if e = d.Decode(&p); e != nil {
			return analysis.ErrDenied
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return analysis.ErrDenied
		}
	}
	if e = p.Validate(); e != nil {
		return e
	}
	emit := func(v any) error { enc := json.NewEncoder(os.Stdout); enc.SetIndent("", "  "); return enc.Encode(v) }
	if *action == "policy" {
		return emit(p)
	}
	db, e := pgxpool.New(ctx, os.Getenv("REPO_DATABASE_URL"))
	if e != nil {
		return ingest.ErrStorage
	}
	defer db.Close()
	if *action == "migrate" {
		return migrations.Apply(ctx, db)
	}
	if e = migrations.Check(ctx, db); e != nil {
		return e
	}
	svc := analysis.Service{DB: analysis.Database{Pool: db}, Python: os.Getenv("REPO_ANALYSIS_PYTHON")}
	if *action == "status" {
		v, e := svc.DB.Status(ctx, cfg.DevPrincipal, *index)
		if e != nil {
			return e
		}
		return emit(v)
	}
	var store ingest.BlobStore
	switch os.Getenv("REPO_STORAGE_BACKEND") {
	case "local":
		root := os.Getenv("REPO_LOCAL_ROOT")
		if !filepath.IsAbs(root) {
			return ingest.ErrStorage
		}
		v, x := ingest.OpenLocal(root)
		if x != nil {
			return x
		}
		defer v.Close()
		store = v
	case "s3":
		bucket := os.Getenv("REPO_S3_BUCKET")
		if bucket == os.Getenv("S3_BUNDLE_BUCKET") || bucket == os.Getenv("S3_CACHE_BUCKET") || (os.Getenv("S3_ACCESS_KEY") != "" && os.Getenv("REPO_S3_ACCESS_KEY") == os.Getenv("S3_ACCESS_KEY")) {
			return analysis.ErrDenied
		}
		v, x := ingest.OpenS3(os.Getenv("REPO_S3_ENDPOINT"), bucket, os.Getenv("REPO_S3_ACCESS_KEY"), os.Getenv("REPO_S3_SECRET_KEY"), os.Getenv("REPO_S3_TLS") != "false")
		if x != nil {
			return x
		}
		store = v
	default:
		return ingest.ErrStorage
	}
	svc.Source = &ingest.Service{DB: ingest.Database{Pool: db}, Objects: store}
	tr, e := telemetry.Start("repository-analysis")
	if e != nil {
		return e
	}
	if tr != nil {
		defer tr.Close()
	}
	if *action == "analyze" || *action == "retry" {
		v, e := svc.Analyze(ctx, cfg.DevPrincipal, *snapshot, p, *action == "retry")
		if e != nil {
			return e
		}
		return emit(v)
	}
	if *action == "symbol" || *action == "chunk" {
		var b []byte
		var result any
		if *action == "symbol" {
			v, x := svc.ReadSymbol(ctx, cfg.DevPrincipal, *index, *id)
			if x != nil {
				return x
			}
			b = v.Bytes
			result = v.Symbol
		} else {
			v, x := svc.ReadChunk(ctx, cfg.DevPrincipal, *index, *id)
			if x != nil {
				return x
			}
			b = v.Bytes
			result = v.Chunk
		}
		if *output != "" {
			if filepath.Ext(*output) != ".txt" {
				return analysis.ErrDenied
			}
			f, x := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if x != nil {
				return analysis.ErrDenied
			}
			_, x = f.Write(b)
			closeErr := f.Close()
			if x != nil {
				return x
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return emit(result)
	}
	a, m, e := svc.Ready(ctx, cfg.DevPrincipal, *index)
	if e != nil {
		return e
	}
	files := map[string]string{}
	for _, v := range m.Files {
		files[v.ID] = v.Path
	}
	match := func(id string) bool { return *file == "" || files[id] == *file }
	result := []any{}
	switch *action {
	case "files":
		for _, v := range m.Files {
			if match(v.ID) {
				result = append(result, v)
			}
		}
	case "symbols":
		for _, v := range m.Symbols {
			if match(v.File) && (*kind == "" || v.Kind == *kind) {
				result = append(result, v)
			}
		}
	case "imports":
		for _, v := range m.Imports {
			if match(v.File) {
				result = append(result, v)
			}
		}
	case "calls", "unresolved":
		for _, v := range m.Calls {
			if match(v.File) && (*action != "unresolved" || v.Class == "unresolved" || v.Class == "candidate") {
				result = append(result, v)
			}
		}
	case "edges":
		for _, v := range m.Edges {
			if match(v.File) {
				result = append(result, v)
			}
		}
	case "chunks":
		for _, v := range m.Chunks {
			if match(v.File) {
				result = append(result, v)
			}
		}
	case "compare":
		b, n, x := svc.Ready(ctx, cfg.DevPrincipal, *other)
		if x != nil {
			return x
		}
		counts := map[string]int{"added_files": 0, "removed_files": 0, "changed_files": 0}
		old := map[string]string{}
		for _, f := range m.Files {
			old[f.Path] = f.SHA256 + ":" + f.State
		}
		for _, f := range n.Files {
			v, ok := old[f.Path]
			if !ok {
				counts["added_files"]++
			} else if v != f.SHA256+":"+f.State {
				counts["changed_files"]++
			}
			delete(old, f.Path)
		}
		counts["removed_files"] = len(old)
		return emit(struct {
			First, Second analysis.Index
			Changes       map[string]int
		}{a, b, counts})
	default:
		return analysis.ErrDenied
	}
	return emit(result)
}
