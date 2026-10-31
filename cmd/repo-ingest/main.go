// repo-ingest is an opted-in local operator tool, absent from ordinary images.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
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
	debug.SetMemoryLimit(64 << 20)
	if e := run(); e != nil {
		code := "internal"
		for k, v := range map[string]error{"denied": ingest.ErrDenied, "source": ingest.ErrSource, "budget": ingest.ErrBudget, "integrity": ingest.ErrIntegrity, "storage": ingest.ErrStorage, "fenced": ingest.ErrFence, "busy": ingest.ErrBusy, "canceled": context.Canceled} {
			if errors.Is(e, v) {
				code = k
				break
			}
		}
		fmt.Fprintln(os.Stderr, "repository ingestion failed ("+code+")")
		os.Exit(1)
	}
}
func readJSON(file string, v any) error {
	f, e := os.Open(file)
	if e != nil {
		return ingest.ErrDenied
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || info.Size() > 16384 {
		return ingest.ErrBudget
	}
	decoder := json.NewDecoder(io.LimitReader(f, 16385))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(v); e != nil {
		return ingest.ErrDenied
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ingest.ErrDenied
	}
	return nil
}
func run() error {
	allow := flag.Bool("allow-development-ingestion", false, "explicit trusted local operator opt-in")
	action := flag.String("action", "", "migrate, init-storage, register, ingest, fetch-ingest, retry, status, inventory, file, compare, audit")
	trustFile := flag.String("trust", "", "private operator JSON with local_roots and exact remote_origins")
	repo := flag.String("repository", "", "authorized repository UUID")
	name := flag.String("name", "", "repository registration name")
	location := flag.String("location", "", "approved local repository or bare mirror directory")
	origin := flag.String("origin", "", "exact allowlisted public HTTPS origin for remote workflow")
	format := flag.String("object-format", "sha1", "new empty remote mirror format: sha1 or sha256")
	ref := flag.String("ref", "", "full commit OID, HEAD, or fully qualified heads/tags ref")
	snap := flag.String("snapshot", "", "snapshot SHA-256 identity")
	other := flag.String("other", "", "second authorized snapshot for comparison")
	filePath := flag.String("path", "", "exact Git file path, never a source host path")
	out := flag.String("output", "", "new private output file for authorized source bytes; never overwritten")
	policyFile := flag.String("policy", "", "optional bounded versioned ingestion policy JSON")
	remoteRetry := flag.Bool("remote", false, "explicit approved remote fetch when retrying")
	flag.Parse()
	cfg, e := config.Load()
	if e != nil {
		return ingest.ErrDenied
	}
	if !*allow || os.Getenv("REPO_INGEST_ENABLED") != "true" || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" || os.Getenv("REPO_DATABASE_URL") == "" {
		return ingest.ErrDenied
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, 330*time.Second)
	defer deadline()
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
	var trust ingest.Trust
	if e = readJSON(*trustFile, &trust); e != nil {
		return e
	}
	if len(trust.Roots) > 16 || len(trust.Origins) > 16 {
		return ingest.ErrBudget
	}
	d := ingest.Database{Pool: db}
	var result any
	if *action == "register" {
		if *origin != "" {
			if !trust.Remote(*origin) {
				return ingest.ErrDenied
			}
			if e = ingest.InitializeMirror(*location, *format, trust); e != nil {
				return e
			}
		}
		result, e = d.Register(ctx, cfg.DevPrincipal, *name, *location, *origin, trust)
	} else if *action == "status" {
		result, e = d.Status(ctx, cfg.DevPrincipal, *snap)
	} else {
		var store ingest.BlobStore
		switch os.Getenv("REPO_STORAGE_BACKEND") {
		case "local":
			root := os.Getenv("REPO_LOCAL_ROOT")
			if !filepath.IsAbs(root) {
				return ingest.ErrStorage
			}
			local, x := ingest.OpenLocal(root)
			if x != nil {
				return x
			}
			defer local.Close()
			store = local
			if *action == "init-storage" {
				return nil
			}
		case "s3":
			bucket := os.Getenv("REPO_S3_BUCKET")
			if bucket == os.Getenv("S3_BUNDLE_BUCKET") || bucket == os.Getenv("S3_CACHE_BUCKET") {
				return ingest.ErrDenied
			}
			if os.Getenv("REPO_S3_ACCESS_KEY") == os.Getenv("S3_ACCESS_KEY") && os.Getenv("S3_ACCESS_KEY") != "" {
				return ingest.ErrDenied
			}
			remote, x := ingest.OpenS3(os.Getenv("REPO_S3_ENDPOINT"), bucket, os.Getenv("REPO_S3_ACCESS_KEY"), os.Getenv("REPO_S3_SECRET_KEY"), os.Getenv("REPO_S3_TLS") != "false")
			if x != nil {
				return x
			}
			store = remote
			if *action == "init-storage" {
				return remote.Initialize(ctx)
			}
		default:
			return ingest.ErrStorage
		}
		runtime, x := telemetry.Start("repository-ingest")
		if x != nil {
			return x
		}
		if runtime != nil {
			defer runtime.Close()
		}
		svc := ingest.Service{DB: d, Objects: store, Trust: trust}
		p := ingest.DefaultPolicy()
		if *policyFile != "" {
			if e = readJSON(*policyFile, &p); e != nil {
				return e
			}
		}
		switch *action {
		case "ingest", "fetch-ingest", "retry":
			if *ref == "" {
				return ingest.ErrSource
			}
			result, e = svc.Ingest(ctx, cfg.DevPrincipal, *repo, *ref, p, ingest.Options{Retry: *action == "retry", AllowRemote: *action == "fetch-ingest" || (*action == "retry" && *remoteRetry)})
		case "inventory":
			var m ingest.Manifest
			_, m, e = svc.Ready(ctx, cfg.DevPrincipal, *snap)
			result = m
		case "file":
			var f ingest.File
			f, e = svc.ReadFile(ctx, cfg.DevPrincipal, *snap, *filePath)
			if e == nil {
				if *out == "" {
					return ingest.ErrDenied
				}
				var target *os.File
				target, e = os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if e == nil {
					_, e = target.Write(f.Bytes)
					closeErr := target.Close()
					if e == nil {
						e = closeErr
					}
				}
			}
			result = f.Entry
		case "compare":
			result, e = svc.Compare(ctx, cfg.DevPrincipal, *snap, *other)
		case "audit":
			result, e = svc.Audit(ctx, cfg.DevPrincipal, *repo, 1000)
		default:
			return ingest.ErrDenied
		}
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
