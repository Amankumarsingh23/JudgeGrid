// judge-agent is a host-side trusted development controller, absent from service images.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/agent"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/config"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/internal/worker"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func main() {
	if err := run(); err != nil {
		slog.Error("trusted development agent stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-development-execution", false, "explicit permission to execute owned development submissions on this trusted host")
	root := flag.String("bundles", ".judgegrid-test-bundles", "trusted private suite store")
	imagesPath := flag.String("images", ".sandbox-images.local.json", "pinned runtime images")
	flag.Parse()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !*allow || os.Getenv("JUDGE_AGENT_ENABLED") != "true" || cfg.AppEnv != "development" || !cfg.DevAuthEnabled || cfg.DevPrincipal == "" {
		return fmt.Errorf("agent requires explicit flag, JUDGE_AGENT_ENABLED=true and trusted development identity")
	}
	stream := env("AGENT_STREAM", "test:judgegrid:agent")
	group := env("AGENT_GROUP", "judge-agents")
	if !queue.IsDevelopmentStream(stream) || len(group) < 1 || len(group) > 200 {
		return fmt.Errorf("agent requires isolated development stream/group")
	}
	o := agent.DefaultOptions()
	for name, target := range map[string]*time.Duration{"AGENT_LEASE": &o.Lease, "AGENT_HEARTBEAT": &o.Heartbeat, "AGENT_RECOVERY": &o.Recovery, "AGENT_DRAIN": &o.Drain} {
		*target, err = time.ParseDuration(env(name, target.String()))
		if err != nil {
			return fmt.Errorf("invalid agent duration")
		}
	}
	for name, target := range map[string]*int{"AGENT_ATTEMPTS": &o.Attempts, "AGENT_CONCURRENCY": &o.Concurrency} {
		*target, err = strconv.Atoi(env(name, strconv.Itoa(*target)))
		if err != nil {
			return fmt.Errorf("invalid agent bound")
		}
	}
	if err = o.Validate(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	setup, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	db, err := storage.Open(setup, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("agent database unavailable")
	}
	defer db.Close()
	if err = migrations.Check(setup, db); err != nil {
		return fmt.Errorf("agent migrations unavailable")
	}
	images, err := execution.LoadImages(*imagesPath)
	if err != nil {
		return fmt.Errorf("pinned runtimes unavailable")
	}
	engine, err := execution.NewDocker(images, "infra/sandbox/seccomp.json", o.Concurrency)
	if err != nil {
		return fmt.Errorf("sandbox policy unavailable")
	}
	defer engine.Close()
	if err = engine.ConfigureCache(db); err != nil {
		return fmt.Errorf("cache configuration unavailable")
	}
	if _, err = engine.Environment(setup); err != nil {
		return fmt.Errorf("sandbox capabilities unavailable")
	}
	if err = engine.CheckImages(setup); err != nil {
		return fmt.Errorf("pinned runtime image capability unavailable")
	}
	b, err := bundles.Configured(*root)
	if err != nil {
		return fmt.Errorf("suite store unavailable")
	}
	redis := queue.Open(cfg.RedisAddr, cfg.RedisPassword)
	defer redis.Close()
	h, err := agent.New(storage.NewRepository(db), b, engine, cfg.DevPrincipal, "agent-"+worker.NewInstanceID(), o)
	if err != nil {
		return err
	}
	slog.Warn("trusted host development auto-judging enabled", "principal", cfg.DevPrincipal, "stream", stream, "lease", o.Lease, "heartbeat", o.Heartbeat)
	return h.Run(ctx, redis.Streams(stream, group))
}
