package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
)

type fakeTransport struct {
	messages    chan queue.Message
	acked       atomic.Int32
	reads       atomic.Int32
	ensureCalls atomic.Int32
	failure     bool
}

func (f *fakeTransport) EnsureGroup(context.Context) error {
	f.ensureCalls.Add(1)
	if f.failure {
		return errors.New("offline")
	}
	return nil
}
func (f *fakeTransport) Read(ctx context.Context, _ string, count int64, _ time.Duration) ([]queue.Message, error) {
	f.reads.Add(1)
	if count != 1 {
		return nil, errors.New("unbounded read")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case m := <-f.messages:
		return []queue.Message{m}, nil
	}
}
func (f *fakeTransport) AutoClaim(context.Context, string, string, time.Duration, int64) ([]queue.Message, string, error) {
	return nil, "0-0", nil
}
func (f *fakeTransport) Ack(context.Context, string) error { f.acked.Add(1); return nil }
func testMessage() queue.Message {
	return queue.Message{ID: "1-0", Values: queue.Job{Kind: queue.DispatchTestJob, SubmissionID: "550e8400-e29b-41d4-a716-446655440000", Attempt: 1}.Values()}
}
func quickOptions() Options {
	o := DefaultOptions()
	o.Block = 10 * time.Millisecond
	o.CommandTimeout = 100 * time.Millisecond
	o.ShutdownGrace = time.Second
	return o
}

func TestPoolACKPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		outcome   Outcome
		err       error
		malformed bool
		want      int32
	}{
		{"durable", Handled, nil, false, 1}, {"duplicate", AlreadyHandled, nil, false, 1},
		{"retry", Retry, nil, false, 0}, {"failure", Handled, errors.New("commit uncertain"), false, 0},
		{"unsupported", Unsupported, nil, false, 0}, {"malformed", Handled, nil, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &fakeTransport{}
			calls := 0
			pool, err := NewPool(transport, HandlerFunc(func(context.Context, queue.Job, string) (Outcome, error) { calls++; return tc.outcome, tc.err }), quickOptions())
			if err != nil {
				t.Fatal(err)
			}
			message := testMessage()
			if tc.malformed {
				message.Values = map[string]any{"bad": "message"}
			}
			pool.process(context.Background(), "consumer", message)
			if transport.acked.Load() != tc.want || (tc.malformed && calls != 0) {
				t.Fatal("unsafe ACK policy")
			}
		})
	}
}
func TestPoolDrainsActiveWork(t *testing.T) {
	transport := &fakeTransport{messages: make(chan queue.Message, 1)}
	transport.messages <- testMessage()
	started := make(chan struct{})
	finish := make(chan struct{})
	pool, _ := NewPool(transport, HandlerFunc(func(ctx context.Context, _ queue.Job, _ string) (Outcome, error) {
		close(started)
		select {
		case <-finish:
			return Handled, nil
		case <-ctx.Done():
			return Retry, ctx.Err()
		}
	}), quickOptions())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	cancel()
	close(finish)
	select {
	case err := <-done:
		if err != nil || transport.acked.Load() != 1 || pool.Ready() {
			t.Fatalf("drain err=%v ACK=%d", err, transport.acked.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leaked workers")
	}
}
func TestPoolShutdownGraceExpires(t *testing.T) {
	transport := &fakeTransport{messages: make(chan queue.Message, 1)}
	transport.messages <- testMessage()
	started := make(chan struct{})
	options := quickOptions()
	options.ShutdownGrace = 20 * time.Millisecond
	pool, _ := NewPool(transport, HandlerFunc(func(ctx context.Context, _ queue.Job, _ string) (Outcome, error) {
		close(started)
		<-ctx.Done()
		return Retry, ctx.Err()
	}), options)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrDrainTimeout) || transport.acked.Load() != 0 {
			t.Fatalf("%v ACK=%d", err, transport.acked.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown leaked")
	}
}
func TestStartupBackoffAndCancellation(t *testing.T) {
	transport := &fakeTransport{failure: true}
	options := quickOptions()
	options.BackoffMin = 20 * time.Millisecond
	options.BackoffMax = 80 * time.Millisecond
	pool, _ := NewPool(transport, HandlerFunc(func(context.Context, queue.Job, string) (Outcome, error) { return Handled, nil }), options)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
	defer cancel()
	if err := pool.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := transport.ensureCalls.Load(); n < 2 || n > 5 {
		t.Fatalf("retry storm or no retry: %d", n)
	}
	if pool.Ready() {
		t.Fatal("offline pool marked ready")
	}
}
func TestPoolOptionsAndIdentity(t *testing.T) {
	a, _ := NewPool(&fakeTransport{}, HandlerFunc(func(context.Context, queue.Job, string) (Outcome, error) { return Retry, nil }), DefaultOptions())
	b, _ := NewPool(&fakeTransport{}, HandlerFunc(func(context.Context, queue.Job, string) (Outcome, error) { return Retry, nil }), DefaultOptions())
	if a.InstanceID() == b.InstanceID() {
		t.Fatal("consumer identity collision")
	}
	o := DefaultOptions()
	o.Concurrency = 0
	if _, err := NewPool(&fakeTransport{}, a.handler, o); err == nil {
		t.Fatal("invalid concurrency accepted")
	}
	if nextDelay(100*time.Millisecond, 150*time.Millisecond) != 150*time.Millisecond {
		t.Fatal("backoff cap")
	}
}
