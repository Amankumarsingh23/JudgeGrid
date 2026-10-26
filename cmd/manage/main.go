package main

import (
	"context"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"log/slog"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("management command failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: manage migrate|check|seed")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := storage.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	switch os.Args[1] {
	case "migrate":
		err = migrations.Apply(ctx, pool)
	case "check":
		err = migrations.Check(ctx, pool)
	case "seed":
		if cfg.AppEnv != "development" {
			return fmt.Errorf("seed requires APP_ENV=development")
		}
		if err = migrations.Check(ctx, pool); err == nil {
			err = storage.Seed(ctx, pool)
		}
	default:
		return fmt.Errorf("unknown management command")
	}
	if err == nil {
		slog.Info("management command complete", "command", os.Args[1])
	}
	return err
}
