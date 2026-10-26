package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Run checks infrastructure and waits for shutdown. Job consumption starts in a later session.
func Run(ctx context.Context, timeout time.Duration, database, redis func(context.Context) error) error {
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := database(checkCtx); err != nil {
		return fmt.Errorf("postgres connectivity: %w", err)
	}
	if err := redis(checkCtx); err != nil {
		return fmt.Errorf("redis connectivity: %w", err)
	}
	slog.Info("worker infrastructure connected; job processing is not implemented")
	<-ctx.Done()
	slog.Info("worker stopped")
	return nil
}
