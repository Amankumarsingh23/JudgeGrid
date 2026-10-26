//go:build integration

package tests

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/internal/worker"
)

// A TCP gate interrupts actual connections to real Redis without disturbing other services.
type redisGate struct {
	listener    net.Listener
	upstream    string
	mu          sync.Mutex
	online      bool
	connections map[net.Conn]bool
	accepted    atomic.Int32
	wg          sync.WaitGroup
}

func TestGroupInitializationAndReadDeadline(t *testing.T) {
	s, raw := streams(t)
	ctx := context.Background()
	if err := raw.XGroupDestroy(ctx, s.Stream, s.Group).Err(); err != nil {
		t.Fatal(err)
	}
	job := queue.Job{Kind: queue.DispatchTestJob, SubmissionID: "550e8400-e29b-41d4-a716-446655440000", Attempt: 1}
	id, err := s.Publish(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = s.EnsureGroup(ctx); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := s.Read(ctx, "reader", 1, 50*time.Millisecond)
	if err != nil || len(messages) != 1 || messages[0].ID != id {
		t.Fatalf("preexisting entry not delivered: %v %v", messages, err)
	}
	if err = s.Ack(ctx, id); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err = s.Read(deadline, "reader", 1, 5*time.Second); err == nil {
		t.Fatal("blocking read ignored deadline")
	}
	if time.Since(start) > time.Second {
		t.Fatal("read cancellation exceeded bound")
	}
}

func newRedisGate(t *testing.T) *redisGate {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate := &redisGate{listener: listener, upstream: os.Getenv("TEST_REDIS_ADDR"), online: true, connections: map[net.Conn]bool{}}
	gate.wg.Add(1)
	go func() {
		defer gate.wg.Done()
		for {
			incoming, err := listener.Accept()
			if err != nil {
				return
			}
			gate.accepted.Add(1)
			gate.mu.Lock()
			if !gate.online {
				gate.mu.Unlock()
				incoming.Close()
				continue
			}
			gate.connections[incoming] = true
			gate.mu.Unlock()
			gate.wg.Add(1)
			go func() {
				defer gate.wg.Done()
				defer incoming.Close()
				defer func() { gate.mu.Lock(); delete(gate.connections, incoming); gate.mu.Unlock() }()
				upstream, err := net.DialTimeout("tcp", gate.upstream, time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				copied := make(chan struct{})
				go func() { _, _ = io.Copy(upstream, incoming); upstream.Close(); close(copied) }()
				_, _ = io.Copy(incoming, upstream)
				incoming.Close()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); gate.setOnline(false); gate.wg.Wait() })
	return gate
}
func (g *redisGate) setOnline(online bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.online = online
	if !online {
		for connection := range g.connections {
			connection.Close()
		}
	}
}

func TestRedisConnectionOutageBackoffAndRecovery(t *testing.T) {
	db := database(t, true)
	s, _ := streams(t)
	repo := storage.NewRepository(db)
	sub := create(t, repo, "outage-test", "one", request(t, db))
	gate := newRedisGate(t)
	client := queue.Open(gate.listener.Addr().String(), "")
	defer client.Close()
	throughGate := client.Streams(s.Stream, s.Group)
	options := poolOptions()
	options.Concurrency = 1
	options.CommandTimeout = 100 * time.Millisecond
	options.BackoffMin = 50 * time.Millisecond
	options.BackoffMax = 200 * time.Millisecond
	pool, _ := worker.NewPool(throughGate, worker.DevelopmentHandler{Store: repo}, options)
	cancel, done := startPool(t, pool)
	gate.setOnline(false)
	before := gate.accepted.Load()
	if _, err := s.Publish(context.Background(), queue.Job{Kind: queue.DispatchTestJob, SubmissionID: sub.ID, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-time.After(500 * time.Millisecond):
	case err := <-done:
		t.Fatalf("pool exited during outage: %v", err)
	}
	attempts := gate.accepted.Load() - before
	if attempts > 25 {
		t.Fatalf("unbounded reconnect attempts: %d", attempts)
	}
	if receiptCount(t, db) != 0 {
		t.Fatal("worker processed through disconnected transport")
	}
	gate.setOnline(true)
	eventually(t, 5*time.Second, func() bool { return receiptCount(t, db) == 1 && pendingCount(t, s) == 0 })
	stopPool(t, cancel, done)
	t.Logf("real Redis TCP outage: %d connection attempts in 500ms; recovered and ACKed; pool goroutines exited", attempts)
}

type failFirstACK struct {
	*queue.Streams
	calls atomic.Int32
}

func (t *failFirstACK) Ack(ctx context.Context, id string) error {
	if t.calls.Add(1) == 1 {
		return errors.New("injected ACK transport failure")
	}
	return t.Streams.Ack(ctx, id)
}
func TestDurableEffectSurvivesACKFailure(t *testing.T) {
	db := database(t, true)
	s, _ := streams(t)
	repo := storage.NewRepository(db)
	sub := create(t, repo, "ack-test", "one", request(t, db))
	transport := &failFirstACK{Streams: s}
	options := poolOptions()
	options.Concurrency = 1
	options.MinIdle = 50 * time.Millisecond
	pool, _ := worker.NewPool(transport, worker.DevelopmentHandler{Store: repo}, options)
	cancel, done := startPool(t, pool)
	if _, err := s.Publish(context.Background(), queue.Job{Kind: queue.DispatchTestJob, SubmissionID: sub.ID, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 4*time.Second, func() bool { return transport.calls.Load() >= 2 && receiptCount(t, db) == 1 && pendingCount(t, s) == 0 })
	stopPool(t, cancel, done)
	t.Log("receipt committed; first ACK failed; reclaim recognized duplicate; one effect and eventual ACK")
}
