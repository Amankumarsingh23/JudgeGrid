package agent

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/scheduling"
	"github.com/Amankumarsingh23/JudgeGrid/internal/worker"
	"sync"
	"time"
)

func (h *Handler) runFair(ctx context.Context, s *queue.Streams) error {
	poll, stop := context.WithCancel(ctx)
	defer stop()
	work, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()
	options := worker.DefaultOptions()
	options.Concurrency = h.Options.Concurrency
	options.Block = 100 * time.Millisecond
	options.MinIdle = h.Options.Lease
	options.RecoveryInterval = h.Options.Recovery
	options.ShutdownGrace = h.Options.Drain
	pool, e := worker.NewPool(s, h, options)
	if e != nil {
		return e
	}
	results := make(chan error, 1)
	go func() { results <- pool.Run(poll) }()
	var workers sync.WaitGroup
	for range h.Options.Concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for poll.Err() == nil {
				check, cancel := context.WithTimeout(poll, 3*time.Second)
				next, e := h.Store.(scheduling.Store).NextScheduled(check, h.Scope)
				cancel()
				if e == nil && next != nil {
					out, e := h.execute(work, next.Job, next.Principal)
					if e == nil && (out == worker.Handled || out == worker.AlreadyHandled) {
						continue
					}
				}
				timer := time.NewTimer(h.Options.Recovery)
				select {
				case <-poll.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}
	finished := make(chan struct{})
	go func() { workers.Wait(); close(finished) }()
	var result error
	poolDone := false
	select {
	case <-ctx.Done():
	case result = <-results:
		poolDone = true
	}
	stop()
	timer := time.NewTimer(h.Options.Drain)
	defer timer.Stop()
	select {
	case <-finished:
	case <-timer.C:
		cancelWork()
		<-finished
	}
	if !poolDone {
		result = <-results
	}
	if errors.Is(result, context.Canceled) {
		return nil
	}
	return result
}
