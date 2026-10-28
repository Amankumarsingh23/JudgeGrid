// judge-local is a trusted, opt-in controller. It is deliberately absent from API/worker runtime images.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/judge"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("local judging command failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-local-judging", false, "explicitly opt in to trusted local judging")
	seed := flag.Bool("seed-tests", false, "seed trusted version-one demo test bundles")
	id := flag.String("submission", "", "existing submission UUID owned by the configured local principal")
	root := flag.String("bundles", ".judgegrid-test-bundles", "private local content-addressed test storage")
	imagesPath := flag.String("images", ".sandbox-images.local.json", "trusted pinned local runtime identities")
	profile := flag.String("seccomp", "infra/sandbox/seccomp.json", "verified seccomp profile")
	flag.Parse()
	if !*allow {
		return fmt.Errorf("requires --allow-local-judging")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AppEnv != "development" {
		return fmt.Errorf("local judging requires APP_ENV=development")
	}
	if !*seed && (!cfg.DevAuthEnabled || cfg.DevPrincipal == "" || *id == "") {
		return fmt.Errorf("judging requires configured development identity and --submission")
	}
	if *seed && *id != "" {
		return fmt.Errorf("choose --seed-tests or --submission")
	}
	store, err := bundles.Configured(*root)
	if err != nil {
		return fmt.Errorf("private bundle store unavailable")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	db, err := storage.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("database unavailable")
	}
	defer db.Close()
	if err = migrations.Check(ctx, db); err != nil {
		return fmt.Errorf("database migrations unavailable or pending")
	}
	if *seed {
		if err = storage.Seed(ctx, db); err == nil {
			err = storage.SeedSuites(ctx, db, store)
		}
		if err != nil {
			return fmt.Errorf("test-suite setup failed")
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "test_suites_seeded"})
	}
	images, err := execution.LoadImages(*imagesPath)
	if err != nil {
		return fmt.Errorf("pinned runtime manifest unavailable")
	}
	backend, err := execution.NewDocker(images, *profile, 2)
	if err != nil {
		return fmt.Errorf("sandbox policy unavailable")
	}
	defer backend.Close()
	if err = backend.ConfigureCache(db); err != nil {
		return fmt.Errorf("cache configuration unavailable")
	}
	summary, err := (judge.Service{Repository: storage.NewRepository(db), Bundles: store, Engine: backend}).Run(ctx, cfg.DevPrincipal, *id)
	output := map[string]any{"evaluation": summary, "controller_operations": backend.Counts()}
	if err != nil {
		output["error"] = "judging_not_successful"
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(output); encodeErr != nil {
		return encodeErr
	}
	if err != nil {
		return fmt.Errorf("judging did not complete successfully; inspect safe evaluation status")
	}
	return nil
}
