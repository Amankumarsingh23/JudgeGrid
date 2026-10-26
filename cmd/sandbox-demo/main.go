// sandbox-demo is a trusted local fixture controller, deliberately absent from API/worker runtime images.
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

	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/worker"
)

func main() {
	if err := run(); err != nil {
		slog.Error("local sandbox demo failed", "error", err)
		os.Exit(1)
	}
}
func run() error {
	allow := flag.Bool("allow-local-fixtures", false, "explicitly enable approved local fixture execution")
	fixture := flag.String("fixture", "python-hello", "approved fixture name")
	imagesPath := flag.String("images", ".sandbox-images.local.json", "trusted local pinned image identities")
	profile := flag.String("seccomp", "infra/sandbox/seccomp.json", "verified vendored seccomp policy")
	flag.Parse()
	if !*allow {
		return fmt.Errorf("requires --allow-local-fixtures; this controller is local development/test only")
	}
	images, err := execution.LoadImages(*imagesPath)
	if err != nil {
		return err
	}
	backend, err := execution.NewDocker(images, *profile, 2)
	if err != nil {
		return err
	}
	defer backend.Close()
	request, err := execution.Fixture(*fixture)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	environment, err := backend.Environment(ctx)
	if err != nil {
		return err
	}
	result, err := (worker.ProgramProcessor{Executor: backend}).Process(ctx, request)
	output := map[string]any{"environment": environment, "result": result}
	if err != nil {
		output["infrastructure_error"] = err.Error()
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(output); encodeErr != nil {
		return encodeErr
	}
	return err
}
