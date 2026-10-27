package outbox

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"testing"
	"time"
)

func TestBackoffBounds(t *testing.T) {
	for _, attempt := range []int{1, 2, 3, 20, 1000} {
		for range 100 {
			d := Backoff(attempt, time.Millisecond, time.Second)
			if d < 500*time.Microsecond || d > time.Second {
				t.Fatal(d)
			}
		}
	}
}
func TestRetryClassification(t *testing.T) {
	for _, tc := range []struct {
		err       error
		code      string
		permanent bool
	}{{queue.ErrMalformed, "malformed_event", true}, {errors.New("WRONGTYPE bad stream"), "redis_configuration", true}, {errors.New("NOAUTH Authentication required"), "redis_configuration", true}, {context.DeadlineExceeded, "redis_unavailable_or_uncertain", false}, {errors.New("connection refused"), "redis_unavailable_or_uncertain", false}} {
		c, p := Classify(tc.err)
		if c != tc.code || p != tc.permanent {
			t.Fatal(c, p)
		}
	}
}
func TestOptionsBounds(t *testing.T) {
	o := DefaultOptions()
	if o.Validate() != nil {
		t.Fatal(o)
	}
	for _, mutate := range []func(*Options){func(o *Options) { o.Batch = 17 }, func(o *Options) { o.PublishTimeout = o.ClaimTTL }, func(o *Options) { o.MaxAttempts = 0 }, func(o *Options) { o.Poll = 0 }, func(o *Options) { o.Drain = 0 }} {
		o = DefaultOptions()
		mutate(&o)
		if o.Validate() == nil {
			t.Fatal(o)
		}
	}
}
func TestEventIdentity(t *testing.T) {
	e := Event{ID: "550e8400-e29b-41d4-a716-446655440000", SubmissionID: "550e8400-e29b-41d4-a716-446655440001", IntentGeneration: 1, Version: 2}
	j, err := e.Job()
	if err != nil {
		t.Fatal(err)
	}
	round, err := (queue.Message{Values: j.Values()}).Job()
	if err != nil || round != j {
		t.Fatal(round, err)
	}
	e.Version = 99
	if _, err = e.Job(); err == nil {
		t.Fatal("unknown version accepted")
	}
}

type fakeStore struct {
	events            []Event
	published, failed int
	code              string
	quarantine        bool
}

func (s *fakeStore) Claim(context.Context, int, time.Duration) ([]Event, error) { return s.events, nil }
func (s *fakeStore) Published(context.Context, Event, string) error             { s.published++; return nil }
func (s *fakeStore) Failed(_ context.Context, _ Event, c string, q bool, _ time.Duration) error {
	s.failed++
	s.code = c
	s.quarantine = q
	return nil
}
func (s *fakeStore) Stats(context.Context) (Stats, error) { return Stats{}, nil }

type fakePublisher struct {
	err   error
	calls int
}

func (p *fakePublisher) Publish(context.Context, queue.Job) (string, error) {
	p.calls++
	return "1-0", p.err
}
func TestMalformedAndRetryBudget(t *testing.T) {
	s := &fakeStore{events: []Event{{Version: 99}}}
	p := &fakePublisher{}
	r := Relay{s, p, DefaultOptions()}
	if err := r.Step(context.Background()); err != nil || s.failed != 1 || s.code != "malformed_event" || !s.quarantine || p.calls != 0 {
		t.Fatal(s, err)
	}
	s.events = []Event{{ID: "550e8400-e29b-41d4-a716-446655440000", SubmissionID: "550e8400-e29b-41d4-a716-446655440001", IntentGeneration: 1, Version: 2, Attempts: 20}}
	p.err = context.DeadlineExceeded
	if err := r.Step(context.Background()); err != nil || s.code != "retry_exhausted" || !s.quarantine {
		t.Fatal(s, err)
	}
}
