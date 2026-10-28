// Package agent connects trusted development execution to authoritative database leases.
package agent

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/judge"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/worker"
	"log/slog"
	"sync/atomic"
	"time"
)

type Store interface {
	ClaimExecution(context.Context, string, string, queue.Job, time.Duration, int) (judge.Work, error)
	RenewExecution(context.Context, judge.Work, time.Duration) error
	CommitEvaluation(context.Context, judge.Work, judge.Report) (judge.Summary, error)
	FailExecution(context.Context, judge.Work, string, bool) error
	RecoveryJobs(context.Context, string, int) ([]queue.Job, error)
}
type Options struct {
	Lease, Heartbeat, Recovery, Drain time.Duration
	Attempts, Concurrency             int
}

func DefaultOptions() Options {
	return Options{30 * time.Second, 10 * time.Second, 2 * time.Second, 20 * time.Second, 3, 2}
}
func (o Options) Validate() error {
	if o.Lease < time.Second || o.Lease > 5*time.Minute || o.Heartbeat < 50*time.Millisecond || o.Heartbeat > o.Lease/3 || o.Recovery < 50*time.Millisecond || o.Recovery > time.Minute || o.Drain < time.Second || o.Drain > 3*time.Minute || o.Attempts < 1 || o.Attempts > 5 || o.Concurrency < 1 || o.Concurrency > 4 {
		return errors.New("invalid agent bounds")
	}
	return nil
}

type Handler struct {
	Store            Store
	Bundles          bundles.Store
	Engine           execution.Preparer
	Principal, Owner string
	Options          Options
	slots            chan struct{}
}

func New(store Store, b bundles.Store, e execution.Preparer, principal, owner string, o Options) (*Handler, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if principal == "" || owner == "" || len(owner) > 200 {
		return nil, errors.New("agent identity required")
	}
	return &Handler{store, b, e, principal, owner, o, make(chan struct{}, o.Concurrency)}, nil
}
func (h *Handler) Handle(ctx context.Context, j queue.Job, _ string) (worker.Outcome, error) {
	if (j.Kind != queue.SubmissionJob && j.Kind != queue.RejudgeJob) || j.EventID == "" {
		return worker.Unsupported, nil
	}
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		return worker.Retry, ctx.Err()
	}
	defer func() { <-h.slots }()
	setup, cancelSetup := context.WithTimeout(ctx, 3*time.Second)
	w, err := h.Store.ClaimExecution(setup, h.Principal, h.Owner, j, h.Options.Lease, h.Options.Attempts)
	cancelSetup()
	if err != nil {
		return worker.Retry, errors.New("execution claim unavailable")
	}
	if w.Existing != nil {
		return worker.AlreadyHandled, nil
	}
	evaluation, cancel := context.WithTimeout(ctx, 120*time.Second)
	if registry, ok := h.Store.(execution.Registry); ok {
		evaluation = execution.WithRegistry(evaluation, registry)
	}
	defer cancel()
	heartbeat, stopHeartbeat := context.WithCancel(context.Background())
	defer stopHeartbeat()
	done := make(chan struct{})
	var uncertain atomic.Bool
	go func() {
		defer close(done)
		ticker := time.NewTicker(h.Options.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeat.Done():
				return
			case <-ticker.C:
				check, stop := context.WithTimeout(heartbeat, min(h.Options.Heartbeat/2, 3*time.Second))
				e := h.Store.RenewExecution(check, w, h.Options.Lease)
				stop()
				if e != nil {
					uncertain.Store(true)
					cancel()
					slog.Warn("execution authority renewal stopped; no further result writes permitted", "submission_id", w.Submission.ID, "generation", w.Generation)
					return
				}
			}
		}
	}()
	defer func() { stopHeartbeat(); <-done }()
	suite, err := h.Bundles.Load(w.Reference.Hash)
	var report judge.Report
	if err == nil {
		report, err = judge.Evaluate(evaluation, h.Engine, w, suite)
	}
	if uncertain.Load() {
		return worker.Retry, errors.New("execution ownership uncertain")
	}
	persist, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err != nil || evaluation.Err() != nil {
		code := "sandbox_infrastructure"
		retry := true
		if errors.Is(err, bundles.ErrIntegrity) {
			code = "bundle_verification_failed"
			retry = false
		} else if errors.Is(err, judge.ErrConfiguration) {
			code = "invalid_configuration"
			retry = false
		} else if evaluation.Err() != nil {
			code = "evaluation_canceled"
		}
		if e := h.Store.FailExecution(persist, w, code, retry); e != nil {
			return worker.Retry, errors.New("execution failure not durably recorded")
		}
		return worker.Retry, errors.New("execution infrastructure deferred; scanner retains work")
	}
	// Independent heartbeat continues through persistence. Every write rechecks the database fence.
	for attempt := 0; attempt < 3; attempt++ {
		if uncertain.Load() {
			return worker.Retry, errors.New("execution ownership uncertain")
		}
		_, err = h.Store.CommitEvaluation(persist, w, report)
		if err == nil {
			return worker.Handled, nil
		}
		select {
		case <-persist.Done():
			return worker.Retry, errors.New("execution completion uncertain")
		case <-time.After(100 * time.Millisecond):
		}
	}
	return worker.Retry, errors.New("execution completion not confirmed")
}
func (h *Handler) Run(ctx context.Context, s *queue.Streams) error {
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	o := worker.DefaultOptions()
	o.Concurrency = h.Options.Concurrency
	o.HandlerTimeout = 150 * time.Second
	o.ShutdownGrace = h.Options.Drain
	o.MinIdle = h.Options.Lease
	o.Block = 100 * time.Millisecond
	o.RecoveryInterval = h.Options.Recovery
	pool, err := worker.NewPool(s, h, o)
	if err != nil {
		return err
	}
	process, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- pool.Run(ctx) }()
	go func() {
		for {
			if ctx.Err() != nil {
				results <- nil
				return
			}
			check, stop := context.WithTimeout(ctx, 3*time.Second)
			jobs, e := h.Store.RecoveryJobs(check, h.Principal, h.Options.Concurrency)
			stop()
			if e == nil {
				for _, j := range jobs {
					if ctx.Err() != nil {
						break
					}
					_, _ = h.Handle(process, j, "database-recovery")
				}
			}
			timer := time.NewTimer(h.Options.Recovery)
			select {
			case <-ctx.Done():
				timer.Stop()
				results <- nil
				return
			case <-timer.C:
			}
		}
	}()
	received := 0
	select {
	case <-ctx.Done():
	case err = <-results:
		received = 1
		cancelRun()
	}
	timer := time.NewTimer(h.Options.Drain)
	defer timer.Stop()
	for remaining := 2 - received; remaining > 0; {
		select {
		case e := <-results:
			remaining--
			if e != nil {
				err = e
			}
		case <-timer.C:
			cancel()
		}
	}
	return err
}
