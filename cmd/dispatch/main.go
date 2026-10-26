// dispatch publishes development harness references only; it never creates judge jobs automatically.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("development dispatch failed", "error", err)
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
	if cfg.AppEnv != "development" || !queue.IsDevelopmentStream(wcfg.Stream) {
		return fmt.Errorf("publisher requires development mode and an isolated dev: or test: stream")
	}
	if len(os.Args) < 2 || len(os.Args) > 3 {
		return fmt.Errorf("usage: dispatch SUBMISSION_UUID [ATTEMPT]")
	}
	attempt := int64(1)
	if len(os.Args) == 3 {
		attempt, err = strconv.ParseInt(os.Args[2], 10, 64)
		if err != nil {
			return err
		}
	}
	job := queue.Job{Kind: queue.DispatchTestJob, SubmissionID: os.Args[1], Attempt: attempt}
	if err = job.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := storage.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	var exists bool
	if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM submissions WHERE id=$1)`, job.SubmissionID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("submission does not exist")
	}
	client := queue.Open(cfg.RedisAddr, cfg.RedisPassword)
	defer client.Close()
	streams := client.Streams(wcfg.Stream, wcfg.Group)
	if err = streams.EnsureGroup(ctx); err != nil {
		return err
	}
	id, err := streams.Publish(ctx, job)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"message_id": id, "submission_id": job.SubmissionID, "attempt": attempt, "workload": "dispatch_test"})
}
