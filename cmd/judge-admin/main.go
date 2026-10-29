// judge-admin is a trusted development operator tool, excluded from ordinary images.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/buildcache"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/objects"
	"github.com/Amankumarsingh23/JudgeGrid/internal/scheduling"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "trusted administrative operation failed")
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-development-administration", false, "explicit local operator authorization")
	action := flag.String("action", "", "local-user, scheduler-config, init-storage, publish, rejudge, history, staged, object-audit, cache-evict")
	submission := flag.String("submission", "", "single owner-scoped submission UUID")
	version := flag.Int64("version", 0, "immutable target version")
	key := flag.String("key", "", "idempotency key or cache identity")
	file := flag.String("file", "", "trusted publication JSON, maximum 3 MiB")
	user := flag.String("user", "", "explicit local browser principal")
	passwordFile := flag.String("password-file", "", "private local password file, 12..128 bytes")
	flag.Parse()
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	if !*allow || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" {
		return fmt.Errorf("development operator scope required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, e := storage.Open(ctx, cfg.DatabaseURL)
	if e != nil {
		return e
	}
	defer db.Close()
	if e = migrations.Check(ctx, db); e != nil {
		return e
	}
	repo := storage.NewRepository(db)
	var result any
	switch *action {
	case "local-user":
		f, err := os.Open(*passwordFile)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, 131))
		if err != nil {
			return err
		}
		password := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
		e = repo.CreateLocalUser(ctx, *user, password)
		result = map[string]string{"status": "local_user_ready", "principal": *user}
	case "scheduler-config":
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer f.Close()
		d := json.NewDecoder(io.LimitReader(f, 32769))
		d.DisallowUnknownFields()
		p := scheduling.Default()
		if err = d.Decode(&p); err != nil {
			return err
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			return fmt.Errorf("one bounded policy required")
		}
		e = repo.ConfigureScheduler(ctx, p)
		result = map[string]string{"status": "scheduler_configured"}
	case "object-audit":
		if os.Getenv("BUNDLE_BACKEND") != "s3" {
			return fmt.Errorf("explicit S3 backend required")
		}
		items := []map[string]any{}
		for _, kind := range []string{"BUNDLE", "CACHE"} {
			s, err := objects.New(os.Getenv("S3_ENDPOINT"), os.Getenv("S3_"+kind+"_BUCKET"), os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), os.Getenv("S3_TLS") != "false")
			if err != nil {
				return err
			}
			keys, err := s.Inventory(ctx, 64)
			if err != nil {
				return err
			}
			for _, objectKey := range keys {
				known := false
				if kind == "BUNDLE" {
					err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM problem_test_suites WHERE 'bundles/'||bundle_sha||'.json'=$1) OR EXISTS(SELECT 1 FROM bundle_publications WHERE 'bundles/'||bundle_sha||'.json'=$1)`, objectKey).Scan(&known)
				} else {
					err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM compilation_cache WHERE 'artifacts/'||artifact_sha||'.bin'=$1)`, objectKey).Scan(&known)
				}
				if err != nil {
					return err
				}
				decision := "unknown-retain"
				if known {
					decision = "known-reference-retain"
				}
				items = append(items, map[string]any{"store": kind, "object_key": objectKey, "decision": decision})
			}
		}
		result = items
	case "init-storage":
		if os.Getenv("BUNDLE_BACKEND") != "s3" {
			return fmt.Errorf("explicit S3 backend required")
		}
		for _, bucket := range []string{os.Getenv("S3_BUNDLE_BUCKET"), os.Getenv("S3_CACHE_BUCKET")} {
			s, e := objects.New(os.Getenv("S3_ENDPOINT"), bucket, os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), os.Getenv("S3_TLS") != "false")
			if e != nil {
				return e
			}
			if e = s.Initialize(ctx); e != nil {
				return e
			}
		}
		result = map[string]string{"status": "private_versioned_buckets_ready"}
	case "publish":
		f, e := os.Open(*file)
		if e != nil {
			return e
		}
		defer f.Close()
		data, e := io.ReadAll(io.LimitReader(f, (3<<20)+1))
		if e != nil || len(data) > 3<<20 {
			return fmt.Errorf("publication size bound")
		}
		var p storage.Publication
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if e = decoder.Decode(&p); e != nil {
			return e
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return fmt.Errorf("one strict publication object required")
		}
		s, e := bundles.Configured(".judgegrid-test-bundles")
		if e != nil {
			return e
		}
		v, e := repo.PublishVersion(ctx, cfg.DevPrincipal, *key, p, s)
		if e != nil {
			return e
		}
		result = map[string]any{"problem_version_id": v}
	case "rejudge":
		result, e = repo.RequestRejudge(ctx, cfg.DevPrincipal, *key, *submission, *version)
	case "history":
		result, e = repo.History(ctx, cfg.DevPrincipal, *submission)
	case "staged":
		rows, err := db.Query(ctx, `SELECT id::text,state,version_id FROM bundle_publications WHERE operator=$1 ORDER BY created_at LIMIT 64`, cfg.DevPrincipal)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id, state string
			var v *int64
			if e = rows.Scan(&id, &state, &v); e != nil {
				return e
			}
			items = append(items, map[string]any{"publication_id": id, "state": state, "version_id": v})
		}
		e = rows.Err()
		result = items
	case "cache-evict":
		e = (&buildcache.Cache{DB: db}).Evict(ctx, *key)
		result = map[string]string{"status": "metadata_evicted_object_retained"}
	default:
		return fmt.Errorf("unknown bounded administrative action")
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
