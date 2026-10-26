package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/worker"
)

type Worker struct {
	Enabled    bool
	Stream     string
	Group      string
	HealthAddr string
	Options    worker.Options
}

func LoadWorker(appEnv string) (Worker, error) {
	w := Worker{Stream: env("QUEUE_STREAM", "submissions:jobs"), Group: env("QUEUE_GROUP", "judges"), HealthAddr: env("WORKER_HEALTH_ADDR", ":8081"), Options: worker.DefaultOptions()}
	var err error
	w.Enabled, err = strconv.ParseBool(env("DEV_DISPATCH_ENABLED", "false"))
	if err != nil {
		return w, fmt.Errorf("DEV_DISPATCH_ENABLED must be boolean")
	}
	if strings.TrimSpace(w.Stream) == "" || strings.TrimSpace(w.Group) == "" || len(w.Stream) > 200 || len(w.Group) > 200 {
		return w, fmt.Errorf("invalid queue stream/group")
	}
	if w.Enabled && (appEnv != "development" || !queue.IsDevelopmentStream(w.Stream)) {
		return w, fmt.Errorf("test dispatch requires APP_ENV=development and isolated dev: or test: QUEUE_STREAM")
	}
	if value := os.Getenv("WORKER_CONCURRENCY"); value != "" {
		w.Options.Concurrency, err = strconv.Atoi(value)
		if err != nil {
			return w, fmt.Errorf("invalid WORKER_CONCURRENCY")
		}
	}
	for name, target := range map[string]*time.Duration{
		"WORKER_BLOCK": &w.Options.Block, "WORKER_MIN_IDLE": &w.Options.MinIdle, "WORKER_RECOVERY_INTERVAL": &w.Options.RecoveryInterval,
		"WORKER_HANDLER_TIMEOUT": &w.Options.HandlerTimeout, "WORKER_COMMAND_TIMEOUT": &w.Options.CommandTimeout, "WORKER_SHUTDOWN_GRACE": &w.Options.ShutdownGrace,
		"WORKER_BACKOFF_MIN": &w.Options.BackoffMin, "WORKER_BACKOFF_MAX": &w.Options.BackoffMax,
	} {
		if value := os.Getenv(name); value != "" {
			*target, err = time.ParseDuration(value)
			if err != nil {
				return w, fmt.Errorf("invalid %s", name)
			}
		}
	}
	return w, w.Options.Validate()
}
