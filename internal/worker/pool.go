package worker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
)

var ErrDrainTimeout = errors.New("worker shutdown grace expired; unfinished messages remain pending")

type Outcome int

const (
	Retry Outcome = iota
	Handled
	AlreadyHandled
	Unsupported
)

// Handler must cooperate with context cancellation. A durable effect must be idempotent on redelivery.
type Handler interface {
	Handle(context.Context, queue.Job, string) (Outcome, error)
}
type HandlerFunc func(context.Context, queue.Job, string) (Outcome, error)

func (h HandlerFunc) Handle(ctx context.Context, j queue.Job, c string) (Outcome, error) {
	return h(ctx, j, c)
}

type Transport interface {
	EnsureGroup(context.Context) error
	Read(context.Context, string, int64, time.Duration) ([]queue.Message, error)
	AutoClaim(context.Context, string, string, time.Duration, int64) ([]queue.Message, string, error)
	Ack(context.Context, string) error
}
type Options struct {
	Concurrency      int
	Block            time.Duration
	MinIdle          time.Duration
	RecoveryInterval time.Duration
	HandlerTimeout   time.Duration
	CommandTimeout   time.Duration
	ShutdownGrace    time.Duration
	BackoffMin       time.Duration
	BackoffMax       time.Duration
}

func DefaultOptions() Options {
	return Options{Concurrency: 4, Block: time.Second, MinIdle: time.Minute, RecoveryInterval: 2 * time.Second, HandlerTimeout: 5 * time.Second, CommandTimeout: 3 * time.Second, ShutdownGrace: 10 * time.Second, BackoffMin: 100 * time.Millisecond, BackoffMax: 5 * time.Second}
}
func (o Options) Validate() error {
	if o.Concurrency < 1 || o.Concurrency > 64 || o.Block < time.Millisecond || o.Block > 5*time.Second || o.MinIdle < time.Millisecond || o.RecoveryInterval < time.Millisecond || o.HandlerTimeout <= 0 || o.CommandTimeout <= 0 || o.ShutdownGrace <= 0 || o.BackoffMin < time.Millisecond || o.BackoffMax < o.BackoffMin {
		return fmt.Errorf("invalid worker pool options")
	}
	return nil
}
func NewInstanceID() string { return fmt.Sprintf("worker-%x", rand.Text()) }

type Pool struct {
	transport Transport
	handler   Handler
	options   Options
	instance  string
	ready     atomic.Bool
}

func NewPool(t Transport, h Handler, o Options) (*Pool, error) {
	if t == nil || h == nil {
		return nil, fmt.Errorf("worker transport and handler required")
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return &Pool{transport: t, handler: h, options: o, instance: NewInstanceID()}, nil
}
func (p *Pool) InstanceID() string { return p.instance }
func (p *Pool) Ready() bool        { return p.ready.Load() }

func (p *Pool) Run(ctx context.Context) error {
	// Startup must complete before announcing readiness. Retry with bounded, cancelable backoff.
	delay := p.options.BackoffMin
	for {
		command, cancel := context.WithTimeout(ctx, p.options.CommandTimeout)
		err := p.transport.EnsureGroup(command)
		cancel()
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return nil
		}
		slog.Warn("consumer group unavailable; retrying", "error", err, "delay", delay)
		if !wait(ctx, delay) {
			return nil
		}
		delay = nextDelay(delay, p.options.BackoffMax)
	}
	p.ready.Store(true)
	defer p.ready.Store(false)
	processing, stopProcessing := context.WithCancel(context.WithoutCancel(ctx))
	defer stopProcessing()
	var wg sync.WaitGroup
	for slot := range p.options.Concurrency {
		wg.Add(1)
		go func() { defer wg.Done(); p.slot(ctx, processing, fmt.Sprintf("%s-slot-%d", p.instance, slot)) }()
	}
	slog.Info("dispatch pool ready", "instance", p.instance, "slots", p.options.Concurrency)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}
	slog.Info("dispatch pool draining", "instance", p.instance)
	p.ready.Store(false)
	timer := time.NewTimer(p.options.ShutdownGrace)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		stopProcessing()
		// Internal handlers and transports must honor deadlines; do not orphan their goroutines.
		<-done
		return ErrDrainTimeout
	}
}

func (p *Pool) slot(poll, processing context.Context, consumer string) {
	cursor := "0-0"
	nextRecovery := time.Time{}
	delay := p.options.BackoffMin
	for poll.Err() == nil {
		var messages []queue.Message
		var err error
		if !time.Now().Before(nextRecovery) {
			command, cancel := context.WithTimeout(poll, p.options.CommandTimeout)
			var nextCursor string
			messages, nextCursor, err = p.transport.AutoClaim(command, consumer, cursor, p.options.MinIdle, 1)
			if err == nil {
				cursor = nextCursor
			}
			cancel()
			nextRecovery = time.Now().Add(p.options.RecoveryInterval)
		}
		if err == nil && len(messages) == 0 && poll.Err() == nil {
			command, cancel := context.WithTimeout(poll, p.options.Block+p.options.CommandTimeout)
			messages, err = p.transport.Read(command, consumer, 1, p.options.Block)
			cancel()
		}
		if err != nil {
			if poll.Err() != nil {
				return
			}
			slog.Warn("dispatch transport unavailable", "consumer", consumer, "error", err, "retry_in", delay)
			if !wait(poll, delay) {
				return
			}
			delay = nextDelay(delay, p.options.BackoffMax)
			// Recreate a missing group after Redis data loss without dropping retained stream entries.
			command, cancel := context.WithTimeout(poll, p.options.CommandTimeout)
			if groupErr := p.transport.EnsureGroup(command); groupErr != nil && poll.Err() == nil {
				slog.Warn("consumer group recovery failed", "error", groupErr)
			}
			cancel()
			continue
		}
		delay = p.options.BackoffMin
		for _, message := range messages {
			// Deliveries received as shutdown begins stay in the PEL for another consumer.
			if poll.Err() != nil {
				return
			}
			p.process(processing, consumer, message)
		}
	}
}

func (p *Pool) process(ctx context.Context, consumer string, message queue.Message) {
	job, err := message.Job()
	if err != nil {
		slog.Error("malformed message retained pending; operator repair required", "message_id", message.ID, "consumer", consumer)
		return
	}
	ctx, end := telemetry.Link(ctx, "redis.process", "", job.TraceParent)
	defer func() { end(err) }()
	handlerCtx, cancel := context.WithTimeout(ctx, p.options.HandlerTimeout)
	outcome, err := p.handler.Handle(handlerCtx, job, consumer)
	cancel()
	if err != nil || (outcome != Handled && outcome != AlreadyHandled) {
		if err == nil {
			err = fmt.Errorf("delivery deferred")
		}
		slog.Warn("dispatch not acknowledged; retained pending", "message_id", message.ID, "consumer", consumer, "outcome", outcome, "error", err)
		return
	}
	if outcome == AlreadyHandled {
		telemetry.Count("redis.duplicate", "ok", "")
	}
	// ACK gets a separate command budget, including during successful drain.
	command, cancel := context.WithTimeout(ctx, p.options.CommandTimeout)
	defer cancel()
	if err = p.transport.Ack(command, message.ID); err != nil {
		slog.Warn("durable handling completed but ACK failed; replay is safe", "message_id", message.ID, "consumer", consumer, "error", err)
		return
	}
	slog.Info("dispatch acknowledged", "message_id", message.ID, "submission_id", job.SubmissionID, "attempt", job.Attempt, "consumer", consumer, "duplicate", outcome == AlreadyHandled)
}
func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func nextDelay(current, max time.Duration) time.Duration {
	if current >= max/2 {
		return max
	}
	return current * 2
}
