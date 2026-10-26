//go:build integration

package tests

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/internal/worker"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func streams(t *testing.T) (*queue.Streams, *redis.Client) {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Fatal("TEST_REDIS_ADDR required; Redis integration tests do not skip")
	}
	client := queue.Open(addr, "")
	raw := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1, ContextTimeoutEnabled: true})
	name := fmt.Sprintf("test:judgegrid:%d", time.Now().UnixNano())
	s := client.Streams(name, "judges")
	t.Cleanup(func() {
		if err := raw.Del(context.Background(), name).Err(); err != nil {
			t.Error(err)
		}
		client.Close()
		raw.Close()
	})
	if err := s.EnsureGroup(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, raw
}
func eventually(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached before deadline")
}
func pendingCount(t *testing.T, s *queue.Streams) int64 {
	t.Helper()
	p, err := s.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return p.Count
}
func poolOptions() worker.Options {
	o := worker.DefaultOptions()
	o.Concurrency = 2
	o.Block = 50 * time.Millisecond
	o.CommandTimeout = 300 * time.Millisecond
	o.MinIdle = 2 * time.Second
	o.RecoveryInterval = 50 * time.Millisecond
	o.ShutdownGrace = 2 * time.Second
	o.BackoffMin = 20 * time.Millisecond
	o.BackoffMax = 100 * time.Millisecond
	return o
}
func startPool(t *testing.T, p *worker.Pool) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() { cancel() })
	eventually(t, 3*time.Second, p.Ready)
	return cancel, done
}
func stopPool(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("pool did not stop")
	}
}
func receiptCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM development_dispatch_receipts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestFiftyJobsFourSlotsTwoInstances(t *testing.T) {
	db := database(t, true)
	s, raw := streams(t)
	repo := storage.NewRepository(db)
	req := request(t, db)
	handler := worker.DevelopmentHandler{Store: repo}
	var active, peak atomic.Int32
	started := make(chan struct{}, 4)
	gate := make(chan struct{})
	wrapped := worker.HandlerFunc(func(ctx context.Context, job queue.Job, c string) (worker.Outcome, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return worker.Retry, ctx.Err()
		}
		return handler.Handle(ctx, job, c)
	})
	a, _ := worker.NewPool(s, wrapped, poolOptions())
	b, _ := worker.NewPool(s, wrapped, poolOptions())
	cancelA, doneA := startPool(t, a)
	cancelB, doneB := startPool(t, b)
	for i := range 50 {
		sub := create(t, repo, "dispatch-test", fmt.Sprintf("fifty-%d", i), req)
		if _, err := s.Publish(context.Background(), queue.Job{Kind: queue.DispatchTestJob, SubmissionID: sub.ID, Attempt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for range 4 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("four slots did not activate")
		}
	}
	close(gate)
	eventually(t, 8*time.Second, func() bool { return receiptCount(t, db) == 50 && pendingCount(t, s) == 0 })
	stopPool(t, cancelA, doneA)
	stopPool(t, cancelB, doneB)
	if peak.Load() != 4 {
		t.Fatalf("peak concurrency=%d", peak.Load())
	}
	rows, err := db.Query(context.Background(), `SELECT DISTINCT consumer_id FROM development_dispatch_receipts`)
	if err != nil {
		t.Fatal(err)
	}
	instances := map[string]bool{}
	consumers := 0
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		consumers++
		if strings.HasPrefix(c, a.InstanceID()) {
			instances[a.InstanceID()] = true
		}
		if strings.HasPrefix(c, b.InstanceID()) {
			instances[b.InstanceID()] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 || consumers != 4 {
		t.Fatalf("instances=%d consumers=%d", len(instances), consumers)
	}
	var unchanged int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM submissions WHERE status='QUEUED' AND verdict IS NULL AND state_version=0`).Scan(&unchanged); err != nil || unchanged != 50 {
		t.Fatalf("real submissions changed: %d %v", unchanged, err)
	}
	length, err := raw.XLen(context.Background(), s.Stream).Result()
	if err != nil || length != 50 {
		t.Fatalf("ACK deleted entries: length=%d %v", length, err)
	}
	t.Logf("50 unique receipts; PEL=0; peak=4; 4 consumers across 2 independent pool instances; all 50 submissions remain QUEUED; XLEN=50")
}

// This helper is executed in a separate OS process only by the crash test below.
func TestDispatchCrashHelper(t *testing.T) {
	mode := os.Getenv("JUDGEGRID_CRASH_HELPER")
	if mode == "" {
		return
	}
	ctx := context.Background()
	client := queue.Open(os.Getenv("TEST_REDIS_ADDR"), "")
	defer client.Close()
	s := client.Streams(os.Getenv("JUDGEGRID_CRASH_STREAM"), "judges")
	messages, err := s.Read(ctx, "consumer-to-kill", 1, time.Second)
	if err != nil || len(messages) != 1 {
		t.Fatalf("helper delivery: %v %v", messages, err)
	}
	if mode == "commit" {
		cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = os.Getenv("JUDGEGRID_CRASH_SCHEMA")
		db, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		job, err := messages[0].Job()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = (worker.DevelopmentHandler{Store: storage.NewRepository(db)}).Handle(ctx, job, "consumer-to-kill"); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("CRASH_READY")
	<-time.After(time.Minute)
	t.Fatal("parent did not kill consumer")
}

func TestConsumerProcessCrashAndCommitBeforeACK(t *testing.T) {
	for _, mode := range []string{"delivery", "commit"} {
		t.Run(mode, func(t *testing.T) {
			db := database(t, true)
			s, _ := streams(t)
			repo := storage.NewRepository(db)
			sub := create(t, repo, "crash-test", mode, request(t, db))
			id, err := s.Publish(context.Background(), queue.Job{Kind: queue.DispatchTestJob, SubmissionID: sub.ID, Attempt: 1})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDispatchCrashHelper$", "-test.v")
			child.Env = append(os.Environ(), "JUDGEGRID_CRASH_HELPER="+mode, "JUDGEGRID_CRASH_STREAM="+s.Stream, "JUDGEGRID_CRASH_SCHEMA="+db.Config().ConnConfig.RuntimeParams["search_path"])
			output, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			child.Stderr = os.Stderr
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			ready := make(chan bool, 1)
			go func() {
				scan := bufio.NewScanner(output)
				for scan.Scan() {
					if scan.Text() == "CRASH_READY" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					_ = child.Process.Kill()
					_ = child.Wait()
					t.Fatal("crash helper did not reach boundary")
				}
			case <-ctx.Done():
				_ = child.Wait()
				t.Fatal("crash helper timed out")
			}
			if err = child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = child.Wait(); err == nil {
				t.Fatal("consumer was not terminated")
			}
			if pendingCount(t, s) != 1 {
				t.Fatal("delivery vanished before ACK")
			}
			expectedBefore := 0
			if mode == "commit" {
				expectedBefore = 1
			}
			if receiptCount(t, db) != expectedBefore {
				t.Fatal("incorrect pre-crash effect")
			}
			var claimed []queue.Message
			eventually(t, time.Second, func() bool {
				var err error
				claimed, _, err = s.AutoClaim(context.Background(), "survivor", "0-0", time.Millisecond, 1)
				if err != nil {
					t.Fatal(err)
				}
				return len(claimed) == 1
			})
			if claimed[0].ID != id {
				t.Fatal("wrong delivery reclaimed")
			}
			job, err := claimed[0].Job()
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := (worker.DevelopmentHandler{Store: repo}).Handle(context.Background(), job, "survivor")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "commit" && outcome != worker.AlreadyHandled {
				t.Fatal("committed effect was not recognized")
			}
			if err = s.Ack(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if receiptCount(t, db) != 1 || pendingCount(t, s) != 0 {
				t.Fatal("redelivery duplicated or lost effect")
			}
			t.Logf("killed separate consumer at %s boundary; redelivery safe; exactly 1 receipt; PEL=0", mode)
		})
	}
}

func TestClaimCursorAndLiveZombieDuplicate(t *testing.T) {
	db := database(t, true)
	s, _ := streams(t)
	repo := storage.NewRepository(db)
	sub := create(t, repo, "cursor-test", "one", request(t, db))
	ctx := context.Background()
	for i := range 12 {
		if _, err := s.Publish(ctx, queue.Job{Kind: queue.DispatchTestJob, SubmissionID: sub.ID, Attempt: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	delivered, err := s.Read(ctx, "still-alive", 12, 50*time.Millisecond)
	if err != nil || len(delivered) != 12 {
		t.Fatalf("%d %v", len(delivered), err)
	}
	// No entries are idle enough: Redis still advances its scan cursor despite returning no claims.
	empty, cursor, err := s.AutoClaim(ctx, "scanner", "0-0", time.Hour, 1)
	if err != nil || len(empty) != 0 || cursor == "0-0" {
		t.Fatalf("empty scan cursor=%q err=%v", cursor, err)
	}
	seen := map[string]bool{}
	cursor = "0-0"
	pages := 0
	eventually(t, 2*time.Second, func() bool {
		messages, next, err := s.AutoClaim(ctx, "new-owner", cursor, time.Millisecond, 1)
		if err != nil {
			t.Fatal(err)
		}
		cursor = next
		pages++
		for _, m := range messages {
			seen[m.ID] = true
		}
		return len(seen) == 12
	})
	if pages < 12 {
		t.Fatal("cursor was not paginated")
	}
	// The original consumer is still alive. Idle claiming did not fence it: both handlers may run.
	job, _ := delivered[0].Job()
	handler := worker.DevelopmentHandler{Store: repo}
	var wg sync.WaitGroup
	var effects, duplicates atomic.Int32
	for _, consumer := range []string{"still-alive", "new-owner"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcome, err := handler.Handle(ctx, job, consumer)
			if err != nil {
				t.Error(err)
			}
			if outcome == worker.Handled {
				effects.Add(1)
			}
			if outcome == worker.AlreadyHandled {
				duplicates.Add(1)
			}
		}()
	}
	wg.Wait()
	if effects.Load() != 1 || duplicates.Load() != 1 || receiptCount(t, db) != 1 {
		t.Fatal("concurrent duplicate effects")
	}
	entries, err := s.PendingEntries(ctx, "-", 100)
	if err != nil || len(entries) != 12 {
		t.Fatalf("pending inspection: %d %v", len(entries), err)
	}
	for _, entry := range entries {
		if entry.Consumer != "new-owner" || entry.RetryCount < 2 {
			t.Fatalf("bad recovery metadata %+v", entry)
		}
	}
	t.Logf("12 pending entries reclaimed with cursor pagination; live old and new handlers produced 1 effect + 1 duplicate")
}

func TestFailuresMalformedAndUnsupportedRemainPending(t *testing.T) {
	db := database(t, true)
	s, raw := streams(t)
	repo := storage.NewRepository(db)
	sub := create(t, repo, "pending-test", "one", request(t, db))
	ctx := context.Background()
	raw.XAdd(ctx, &redis.XAddArgs{Stream: s.Stream, Values: map[string]any{"bad": "payload"}})
	if _, err := s.Publish(ctx, queue.Job{Kind: queue.SubmissionJob, SubmissionID: sub.ID, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, queue.Job{Kind: queue.DispatchTestJob, SubmissionID: sub.ID, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	handler := worker.HandlerFunc(func(ctx context.Context, j queue.Job, c string) (worker.Outcome, error) {
		calls.Add(1)
		if j.Kind == queue.SubmissionJob {
			return worker.Unsupported, nil
		}
		return worker.Retry, errors.New("transient database failure")
	})
	options := poolOptions()
	options.MinIdle = time.Hour
	pool, _ := worker.NewPool(s, handler, options)
	cancel, done := startPool(t, pool)
	eventually(t, 3*time.Second, func() bool { return pendingCount(t, s) == 3 && calls.Load() == 2 })
	stopPool(t, cancel, done)
	if receiptCount(t, db) != 0 {
		t.Fatal("unhandled jobs created effects")
	}
	got, err := repo.Get(ctx, "pending-test", sub.ID)
	if err != nil || got.Status != domain.Queued || got.Verdict != nil {
		t.Fatal("real submission was changed")
	}
	t.Log("malformed + unsupported real job + transient failure remain observable in PEL; no ACK or judge mutation")
}

func TestPoolRecoversAbandonedPending(t *testing.T) {
	db := database(t, true)
	s, _ := streams(t)
	repo := storage.NewRepository(db)
	sub := create(t, repo, "recovery-test", "one", request(t, db))
	ctx := context.Background()
	if _, err := s.Publish(ctx, queue.Job{Kind: queue.DispatchTestJob, SubmissionID: sub.ID, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, "abandoned", 1, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	options := poolOptions()
	options.MinIdle = time.Millisecond
	pool, _ := worker.NewPool(s, worker.DevelopmentHandler{Store: repo}, options)
	cancel, done := startPool(t, pool)
	eventually(t, 3*time.Second, func() bool { return receiptCount(t, db) == 1 && pendingCount(t, s) == 0 })
	stopPool(t, cancel, done)
}

func TestRealRedisDrainAndGraceExpiry(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprintf("expire=%v", expire), func(t *testing.T) {
			db := database(t, true)
			s, _ := streams(t)
			repo := storage.NewRepository(db)
			sub := create(t, repo, "drain-test", "one", request(t, db))
			ctx := context.Background()
			started := make(chan struct{})
			finish := make(chan struct{})
			handler := worker.HandlerFunc(func(ctx context.Context, j queue.Job, c string) (worker.Outcome, error) {
				close(started)
				select {
				case <-finish:
					return (worker.DevelopmentHandler{Store: repo}).Handle(ctx, j, c)
				case <-ctx.Done():
					return worker.Retry, ctx.Err()
				}
			})
			options := poolOptions()
			options.Concurrency = 1
			if expire {
				options.ShutdownGrace = 30 * time.Millisecond
			}
			pool, _ := worker.NewPool(s, handler, options)
			cancel, done := startPool(t, pool)
			if _, err := s.Publish(ctx, queue.Job{Kind: queue.DispatchTestJob, SubmissionID: sub.ID, Attempt: 1}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("handler not started")
			}
			cancel()
			if !expire {
				close(finish)
			}
			select {
			case err := <-done:
				if expire && !errors.Is(err, worker.ErrDrainTimeout) {
					t.Fatalf("expected grace expiry, got %v", err)
				}
				if !expire && err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("goroutines did not exit")
			}
			if expire {
				if receiptCount(t, db) != 0 || pendingCount(t, s) != 1 {
					t.Fatal("unfinished work lost")
				}
			} else {
				if receiptCount(t, db) != 1 || pendingCount(t, s) != 0 {
					t.Fatal("successful drain lost ACK")
				}
			}
		})
	}
}
