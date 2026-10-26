package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/api"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/internal/worker"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	wcfg, err := config.LoadWorker(cfg.AppEnv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := storage.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	redis := queue.Open(cfg.RedisAddr, cfg.RedisPassword)
	defer redis.Close()
	streams := redis.Streams(wcfg.Stream, wcfg.Group)
	var pool *worker.Pool
	if wcfg.Enabled {
		pool, err = worker.NewPool(streams, worker.DevelopmentHandler{Store: storage.NewRepository(db)}, wcfg.Options)
		if err != nil {
			return err
		}
		slog.Warn("development dispatch receipts enabled; no judging or submission-state mutation", "stream", wcfg.Stream, "group", wcfg.Group)
	} else {
		slog.Info("production submission consumption disabled; no executor exists")
	}
	readiness := func(ctx context.Context) error {
		if err := db.Ping(ctx); err != nil {
			return err
		}
		if pool != nil && !pool.Ready() {
			return fmt.Errorf("dispatch pool not ready")
		}
		return nil
	}
	queueReady := func(ctx context.Context) error {
		if err := redis.Ping(ctx); err != nil {
			return err
		}
		if pool != nil {
			return streams.CheckGroup(ctx)
		}
		return nil
	}
	server := &http.Server{Addr: wcfg.HealthAddr, Handler: api.Handler(cfg.CheckTimeout, readiness, queueReady), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	serverResult := make(chan error, 1)
	go func() { serverResult <- server.ListenAndServe() }()
	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	workResult := make(chan error, 1)
	go func() {
		if pool != nil {
			workResult <- pool.Run(workCtx)
		} else {
			workResult <- worker.Run(workCtx, cfg.CheckTimeout, db.Ping, redis.Ping)
		}
	}()
	var result error
	select {
	case result = <-workResult:
	case err = <-serverResult:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
		cancelWork()
		if workErr := <-workResult; result == nil {
			result = workErr
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		if result == nil {
			result = err
		}
	}
	return result
}
