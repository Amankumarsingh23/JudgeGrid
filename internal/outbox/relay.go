// Package outbox implements bounded at-least-once publication, never program execution.
package outbox

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

var ErrClaimLost = errors.New("outbox claim expired or replaced")

type Event struct {
	ID, SubmissionID, Token      string
	Generation, IntentGeneration int64
	Version, Attempts            int
}

func (e Event) Job() (queue.Job, error) {
	j := queue.Job{Kind: queue.SubmissionJob, SubmissionID: e.SubmissionID, Attempt: e.IntentGeneration, EventID: e.ID}
	if e.Version != 2 {
		return j, queue.ErrMalformed
	}
	return j, j.Validate()
}

type Stats struct {
	Pending, Claimed, Published, Quarantined, RetryCount int64
	OldestAgeSeconds                                     float64
	Capped                                               bool
}
type Store interface {
	Claim(context.Context, int, time.Duration) ([]Event, error)
	Published(context.Context, Event, string) error
	Failed(context.Context, Event, string, bool, time.Duration) error
	Stats(context.Context) (Stats, error)
}
type Publisher interface {
	Publish(context.Context, queue.Job) (string, error)
}
type Options struct {
	Batch, MaxAttempts                                            int
	ClaimTTL, PublishTimeout, Poll, BackoffMin, BackoffMax, Drain time.Duration
}

func DefaultOptions() Options {
	return Options{Batch: 4, MaxAttempts: 20, ClaimTTL: 30 * time.Second, PublishTimeout: 3 * time.Second, Poll: 500 * time.Millisecond, BackoffMin: time.Second, BackoffMax: time.Minute, Drain: 10 * time.Second}
}
func (o Options) Validate() error {
	if o.Batch < 1 || o.Batch > 16 || o.MaxAttempts < 1 || o.MaxAttempts > 1000 || o.ClaimTTL < time.Second || o.ClaimTTL > 5*time.Minute || o.PublishTimeout < time.Millisecond || o.PublishTimeout > o.ClaimTTL/3 || o.Poll < 10*time.Millisecond || o.Poll > time.Minute || o.BackoffMin < time.Millisecond || o.BackoffMax < o.BackoffMin || o.BackoffMax > time.Hour || o.Drain < time.Millisecond || o.Drain > time.Minute {
		return errors.New("invalid outbox bounds")
	}
	return nil
}
func Backoff(attempt int, min, max time.Duration) time.Duration {
	cap := min
	for i := 1; i < attempt && cap < max; i++ {
		if cap > max/2 {
			cap = max
			break
		}
		cap *= 2
	}
	if cap > max {
		cap = max
	}
	return cap/2 + time.Duration(rand.Int64N(int64(cap-cap/2)+1))
}
func Classify(err error) (string, bool) {
	if errors.Is(err, queue.ErrMalformed) {
		return "malformed_event", true
	}
	s := strings.ToUpper(err.Error())
	for _, prefix := range []string{"WRONGTYPE", "NOAUTH", "WRONGPASS", "NOPERM"} {
		if strings.HasPrefix(s, prefix) {
			return "redis_configuration", true
		}
	}
	return "redis_unavailable_or_uncertain", false
}

type Relay struct {
	Store     Store
	Publisher Publisher
	Options   Options
}

func (r Relay) Step(ctx context.Context) error {
	if err := r.Options.Validate(); err != nil {
		return err
	}
	claimCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	events, err := r.Store.Claim(claimCtx, r.Options.Batch, r.Options.ClaimTTL)
	cancel()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	failures := make(chan error, len(events))
	for _, event := range events {
		wg.Add(1)
		go func(e Event) { defer wg.Done(); failures <- r.deliver(ctx, e) }(event)
	}
	wg.Wait()
	close(failures)
	var joined error
	for e := range failures {
		joined = errors.Join(joined, e)
	}
	return joined
}
func (r Relay) deliver(ctx context.Context, e Event) error {
	j, err := e.Job()
	var streamID string
	if err == nil {
		publish, cancel := context.WithTimeout(ctx, r.Options.PublishTimeout)
		streamID, err = r.Publisher.Publish(publish, j)
		cancel()
	}
	record, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err == nil {
		err = r.Store.Published(record, e, streamID)
		if err != nil {
			slog.Warn("publication record uncertain; claim will expire", "event_id", e.ID, "code", "publication_record_uncertain")
		}
		return err
	}
	code, permanent := Classify(err)
	if !permanent && e.Attempts >= r.Options.MaxAttempts {
		code = "retry_exhausted"
		permanent = true
	}
	slog.Warn("outbox publication deferred", "event_id", e.ID, "attempt", e.Attempts, "code", code, "quarantined", permanent)
	return r.Store.Failed(record, e, code, permanent, Backoff(e.Attempts, r.Options.BackoffMin, r.Options.BackoffMax))
}
func (r Relay) Run(ctx context.Context) error {
	if err := r.Options.Validate(); err != nil {
		return err
	}
	failures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		work, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.Step(work) }()
		pause := r.Options.Poll
		select {
		case err := <-done:
			cancel()
			if err != nil {
				failures++
				pause = Backoff(failures, r.Options.BackoffMin, r.Options.BackoffMax)
				if pause < r.Options.Poll {
					pause = r.Options.Poll
				}
				slog.Warn("outbox iteration deferred", "code", "database_or_claim_unavailable")
			} else {
				failures = 0
			}
		case <-ctx.Done():
			timer := time.NewTimer(r.Options.Drain)
			select {
			case <-done:
				timer.Stop()
				cancel()
				return nil
			case <-timer.C:
				cancel()
				<-done
				return nil
			}
		}
		timer := time.NewTimer(pause)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil
		}
	}
}
