// sandbox-reconcile is a trusted host tool, deliberately absent from ordinary service images.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/reconcile"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"log/slog"
	"os"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("sandbox reconciliation refused or interrupted", "error", e)
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-local-reconciliation", false, "explicitly permit trusted development host inventory")
	apply := flag.Bool("apply", false, "explicitly remove only database-authorized orphan resources; default dry-run")
	max := flag.Int("max-resources", 64, "bounded inventory, 1..128")
	flag.Parse()
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	if !*allow || cfg.AppEnv != "development" || *max < 1 || *max > 128 {
		return fmt.Errorf("requires development mode, explicit local opt-in and bounded inventory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, e := storage.Open(ctx, cfg.DatabaseURL)
	if e != nil {
		return fmt.Errorf("database authority unavailable")
	}
	defer db.Close()
	if e = migrations.Check(ctx, db); e != nil {
		return fmt.Errorf("database authority schema unavailable")
	}
	report, e := reconcile.Run(ctx, storage.NewRepository(db), reconcile.Docker{}, *apply, *max)
	if encode := json.NewEncoder(os.Stdout).Encode(report); encode != nil {
		return encode
	}
	return e
}
