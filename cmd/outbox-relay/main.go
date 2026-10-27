package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/api"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/internal/submission"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("outbox relay failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	retry := flag.String("retry", "", "operator: release quarantined or expired event UUID")
	backfill := flag.String("backfill", "", "operator: create initial intent for one historical QUEUED submission UUID")
	stats := flag.Bool("stats", false, "operator: show bounded backlog statistics")
	flag.Parse()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	rcfg, err := config.LoadRelay()
	if err != nil {
		return err
	}
	if !rcfg.Enabled {
		return fmt.Errorf("explicit OUTBOX_RELAY_ENABLED=true is required")
	}
	if (*retry != "" && *backfill != "") || (*stats && (*retry != "" || *backfill != "")) {
		return fmt.Errorf("choose one operator operation")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := storage.Open(startup, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("outbox database unavailable")
	}
	defer db.Close()
	if err = migrations.Check(startup, db); err != nil {
		return fmt.Errorf("outbox migrations missing or inconsistent")
	}
	repo := storage.NewRepository(db)
	if *retry != "" || *backfill != "" {
		id := *retry
		if *backfill != "" {
			id = *backfill
		}
		if !submission.ValidID(id) {
			return fmt.Errorf("operator operation requires UUID")
		}
		if *retry != "" {
			err = repo.RetryOutbox(startup, id)
		} else {
			err = repo.BackfillOutbox(startup, id)
		}
		if err != nil {
			return fmt.Errorf("operator operation rejected")
		}
		slog.Info("outbox operator operation complete", "id", id)
		return nil
	}
	if *stats {
		snapshot, e := repo.Stats(startup)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(snapshot)
	}
	redis := queue.Open(cfg.RedisAddr, cfg.RedisPassword)
	defer redis.Close()
	// Redis outage must not stop recovery. Readiness reports it; the bounded loop retains durable intents.
	mux := http.NewServeMux()
	mux.Handle("/", api.Handler(cfg.CheckTimeout, db.Ping, redis.Ping))
	mux.HandleFunc("/outboxz", func(w http.ResponseWriter, r *http.Request) {
		check, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		snapshot, e := repo.Stats(check)
		if e != nil {
			http.Error(w, "statistics unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	server := &http.Server{Addr: rcfg.HealthAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	work, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	done := make(chan error, 1)
	relay := outbox.Relay{Store: repo, Publisher: redis.Streams(rcfg.Stream, "judges"), Options: rcfg.Options}
	go func() { done <- relay.Run(work) }()
	slog.Info("outbox relay started; real judging remains disabled", "stream", rcfg.Stream, "batch", rcfg.Options.Batch, "claim_ttl", rcfg.Options.ClaimTTL)
	select {
	case err = <-done:
	case e := <-result:
		if !errors.Is(e, http.ErrServerClosed) {
			err = e
		}
		cancelWork()
		<-done
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if e := server.Shutdown(shutdown); e != nil {
		_ = server.Close()
		if err == nil {
			err = e
		}
	}
	return err
}
