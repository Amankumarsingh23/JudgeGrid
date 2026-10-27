//go:build integration

package tests

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/internal/worker"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func relayOptions() outbox.Options {
	o := outbox.DefaultOptions()
	o.Batch = 2
	o.ClaimTTL = time.Second
	o.PublishTimeout = 100 * time.Millisecond
	o.Poll = 20 * time.Millisecond
	o.BackoffMin = 20 * time.Millisecond
	o.BackoffMax = 100 * time.Millisecond
	o.Drain = 200 * time.Millisecond
	return o
}
func TestOutboxAtomicityAdmissionAndBackfill(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo := storage.NewRepository(db)
	req := request(t, db)
	execute(t, db, `CREATE FUNCTION fail_intent() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected before commit'; END; $$; CREATE TRIGGER fail_intent BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_intent()`)
	if _, _, err := repo.Create(ctx, "alice", "rollback", req); err == nil {
		t.Fatal("A: injected transaction failure accepted")
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM submissions)+(SELECT count(*) FROM submission_idempotency)+(SELECT count(*) FROM outbox_events)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("A partial acceptance", count, err)
	}
	execute(t, db, `DROP TRIGGER fail_intent ON outbox_events; DROP FUNCTION fail_intent()`)
	sub := create(t, repo, "alice", "first", req)
	replay, yes, err := repo.Create(ctx, "alice", "first", req)
	if err != nil || !yes || replay.ID != sub.ID {
		t.Fatal(replay, yes, err)
	}
	changed := req
	changed.SourceCode = "different"
	if _, _, err = repo.Create(ctx, "alice", "first", changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	small := storage.NewRepositoryWithBacklog(db, 1)
	if _, _, err = small.Create(ctx, "alice", "second", req); !errors.Is(err, domain.ErrBacklog) {
		t.Fatal("admission missing", err)
	}
	if _, yes, err = small.Create(ctx, "alice", "first", req); err != nil || !yes {
		t.Fatal("replay blocked by backlog", err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = repo.BackfillOutbox(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	// Model a historical row explicitly using SQL, as it existed before migration 0005.
	var oldID string
	err = db.QueryRow(ctx, `INSERT INTO submissions(id,problem_id,problem_version_id,principal_id,language,source_code) VALUES(gen_random_uuid(),$1,$2,'historical','cpp17','int main(){}') RETURNING id::text`, sub.ProblemID, sub.ProblemVersionID).Scan(&oldID)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE submission_id=$1`, oldID).Scan(&count); err != nil || count != 0 {
		t.Fatal("automatic historical publication", count, err)
	}
	for range 2 {
		if err = repo.BackfillOutbox(ctx, oldID); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE submission_id=$1`, oldID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	constraint(t, db, "23505", `INSERT INTO outbox_events(submission_id) VALUES($1)`, sub.ID)
	constraint(t, db, "23514", `UPDATE outbox_events SET payload_version=99 WHERE submission_id=$1`, sub.ID)
	constraint(t, db, "23514", `DELETE FROM outbox_events WHERE submission_id=$1`, sub.ID)
	for _, index := range []string{"outbox_due", "outbox_expired_claims", "outbox_status_age"} {
		var exists bool
		if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND indexname=$1)`, index).Scan(&exists); err != nil || !exists {
			t.Fatal(index, err)
		}
	}
}

func TestOutboxCrashHelper(t *testing.T) {
	mode := os.Getenv("JUDGEGRID_OUTBOX_CRASH")
	if mode == "" {
		return
	}
	ctx := context.Background()
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
	repo := storage.NewRepository(db)
	if mode == "after_commit" {
		create(t, repo, "crash-api", "committed", request(t, db))
	} else {
		events, e := repo.Claim(ctx, 1, time.Second)
		if e != nil || len(events) != 1 {
			t.Fatal(events, e)
		}
		client := queue.Open(os.Getenv("TEST_REDIS_ADDR"), "")
		defer client.Close()
		j, e := events[0].Job()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = client.Streams(os.Getenv("JUDGEGRID_CRASH_STREAM"), "judges").Publish(ctx, j); e != nil {
			t.Fatal(e)
		}
	}
	fmt.Println("OUTBOX_CRASH_READY")
	time.Sleep(time.Minute)
	t.Fatal("parent did not kill relay")
}
func killOutboxBoundary(t *testing.T, db *pgxpool.Pool, stream, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOutboxCrashHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "JUDGEGRID_OUTBOX_CRASH="+mode, "JUDGEGRID_CRASH_SCHEMA="+db.Config().ConnConfig.RuntimeParams["search_path"], "JUDGEGRID_CRASH_STREAM="+stream)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() {
		scan := bufio.NewScanner(pipe)
		for scan.Scan() {
			if scan.Text() == "OUTBOX_CRASH_READY" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("boundary not reached")
		}
	case <-ctx.Done():
		_ = cmd.Wait()
		t.Fatal("boundary timeout")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("process did not crash")
	}
}

func TestOutboxCommittedAPICrashAndRelayCrash(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo := storage.NewRepository(db)
	s, raw := streams(t)
	killOutboxBoundary(t, db, s.Stream, "after_commit")
	stats, err := repo.Stats(ctx)
	if err != nil || stats.Pending != 1 {
		t.Fatal("B lost committed intent", stats, err)
	}
	killOutboxBoundary(t, db, s.Stream, "after_publish")
	if n := raw.XLen(ctx, s.Stream).Val(); n != 1 {
		t.Fatal("D publish missing", n)
	}
	relay := outbox.Relay{Store: repo, Publisher: s, Options: relayOptions()}
	eventually(t, 3*time.Second, func() bool {
		if err = relay.Step(ctx); err != nil {
			t.Fatal(err)
		}
		st, e := repo.Stats(ctx)
		return e == nil && st.Published == 1
	})
	messages, err := s.Read(ctx, "inspect-real", 2, 50*time.Millisecond)
	if err != nil || len(messages) != 2 {
		t.Fatal(messages, err)
	}
	first, _ := messages[0].Job()
	second, _ := messages[1].Job()
	if first.EventID == "" || first != second || messages[0].ID == messages[1].ID {
		t.Fatal("logical identity changed", first, second)
	}
	for _, m := range messages {
		j, e := m.Job()
		if e != nil {
			t.Fatal(e)
		}
		outcome, e := (worker.DevelopmentHandler{Store: repo}).Handle(ctx, j, "safe-consumer")
		if e != nil || outcome != worker.Unsupported {
			t.Fatal("real job was handled", outcome, e)
		}
	}
	if pendingCount(t, s) != 2 || receiptCount(t, db) != 0 {
		t.Fatal("real job ACKed or effect fabricated")
	}
	sub, err := repo.Get(ctx, "crash-api", first.SubmissionID)
	if err != nil || sub.Status != domain.Queued || sub.StateVersion != 0 || sub.Verdict != nil {
		t.Fatal(sub, err)
	}
	t.Log("B/D: killed API after commit, killed relay after XADD; two Stream IDs, one event ID, zero effects and both messages recoverable")
}

func TestOutboxExpiredClaimsAndQuarantine(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo := storage.NewRepository(db)
	sub := create(t, repo, "alice", "claims", request(t, db))
	s, _ := streams(t)
	a, err := repo.Claim(ctx, 1, time.Second)
	if err != nil || len(a) != 1 {
		t.Fatal(a, err)
	}
	// Advance database claim time deterministically, without altering authority generation.
	execute(t, db, `UPDATE outbox_events SET claim_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a[0].ID)
	if err = repo.Published(ctx, a[0], "1-0"); !errors.Is(err, outbox.ErrClaimLost) {
		t.Fatal("expired A accepted", err)
	}
	b, err := repo.Claim(ctx, 1, time.Second)
	if err != nil || len(b) != 1 || b[0].Generation != a[0].Generation+1 || b[0].Token == a[0].Token {
		t.Fatal(b, err)
	}
	if err = repo.Published(ctx, a[0], "1-0"); !errors.Is(err, outbox.ErrClaimLost) {
		t.Fatal("E stale A accepted", err)
	}
	if err = repo.Failed(ctx, a[0], "redis_unavailable_or_uncertain", false, time.Second); !errors.Is(err, outbox.ErrClaimLost) {
		t.Fatal("stale failure accepted", err)
	}
	if err = repo.Failed(ctx, b[0], "redis_configuration", true, time.Second); err != nil {
		t.Fatal(err)
	}
	if err = repo.RetryOutbox(ctx, b[0].ID); err != nil {
		t.Fatal(err)
	}
	retried, e := repo.Claim(ctx, 1, time.Second)
	if e != nil || len(retried) != 1 || retried[0].ID != b[0].ID || retried[0].Attempts != 1 {
		t.Fatal("operator retry did not reset budget", retried, e)
	}
	if err = repo.Failed(ctx, retried[0], "redis_unavailable_or_uncertain", false, 0); err != nil {
		t.Fatal(err)
	}
	if err = (outbox.Relay{Store: repo, Publisher: s, Options: relayOptions()}).Step(ctx); err != nil {
		t.Fatal(err)
	}
	if err = repo.RetryOutbox(ctx, b[0].ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("published event released", err)
	}
	var badID string
	if err = db.QueryRow(ctx, `INSERT INTO outbox_events(submission_id,intent_generation,payload_version) VALUES($1,2,99) RETURNING id::text`, sub.ID).Scan(&badID); err != nil {
		t.Fatal(err)
	}
	if err = (outbox.Relay{Store: repo, Publisher: s, Options: relayOptions()}).Step(ctx); err != nil {
		t.Fatal(err)
	}
	var status, code string
	if err = db.QueryRow(ctx, `SELECT status,last_error_code FROM outbox_events WHERE id=$1`, badID).Scan(&status, &code); err != nil || status != "QUARANTINED" || code != "malformed_event" {
		t.Fatal(status, code, err)
	}
	t.Log("E/H: expired/stale generations rejected; operator retry preserved identity; unsupported schema quarantined")
}

func TestDevelopmentReceiptChecksAuthoritativeState(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo := storage.NewRepository(db)
	sub := create(t, repo, "alice", "stale-receipt", request(t, db))
	if _, err := repo.Transition(ctx, sub.ID, domain.Transition{From: domain.Queued, To: domain.Running, ExpectedVersion: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordDispatch(ctx, sub.ID, 1, "stale-consumer"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale state accepted", err)
	}
	if receiptCount(t, db) != 0 {
		t.Fatal("stale dispatch created a receipt")
	}
}

func TestOutboxRedisOutageBacklogAndMultipleRelays(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo := storage.NewRepository(db)
	s, _ := streams(t)
	gate := newRedisGate(t)
	gate.setOnline(false)
	client := queue.Open(gate.listener.Addr().String(), "")
	defer client.Close()
	relay := outbox.Relay{Store: repo, Publisher: client.Streams(s.Stream, s.Group), Options: relayOptions()}
	for i := range 12 {
		create(t, repo, "backlog", fmt.Sprintf("backlog-%d", i), request(t, db))
	}
	if err := relay.Step(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := repo.Stats(ctx)
	if err != nil || st.Pending != 12 || st.Published != 0 {
		t.Fatal("C intents lost", st, err)
	}
	gate.setOnline(true)
	killOutboxBoundary(t, db, s.Stream, "after_publish")
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 2)
	go func() { done <- relay.Run(runCtx) }()
	go func() { done <- relay.Run(runCtx) }()
	eventually(t, 5*time.Second, func() bool { st, e := repo.Stats(ctx); return e == nil && st.Published >= 4 })
	cancel()
	for range 2 {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	restart, cancelRestart := context.WithCancel(ctx)
	go func() { done <- relay.Run(restart) }()
	eventually(t, 5*time.Second, func() bool {
		st, e := repo.Stats(ctx)
		return e == nil && st.Published == 12 && st.Pending == 0 && st.Claimed == 0
	})
	cancelRestart()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	var changed int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM submissions WHERE status<>'QUEUED' OR state_version<>0 OR verdict IS NOT NULL`).Scan(&changed); err != nil || changed != 0 {
		t.Fatal("unsafe state mutation", changed, err)
	}
	t.Log("C/F/G: real Redis TCP outage retained 12 intents; relay process killed during backlog; two relay loops drained and restarted without losing work")
}

type blockingPublisher struct {
	inner   outbox.Publisher
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingPublisher) Publish(ctx context.Context, j queue.Job) (string, error) {
	id, err := p.inner.Publish(ctx, j)
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.release:
		return id, err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func TestOutboxGracefulAndExpiredDrain(t *testing.T) {
	for _, drain := range []bool{true, false} {
		t.Run(fmt.Sprint(drain), func(t *testing.T) {
			ctx := context.Background()
			db := database(t, true)
			repo := storage.NewRepository(db)
			s, _ := streams(t)
			create(t, repo, "shutdown", "one", request(t, db))
			p := &blockingPublisher{inner: s, started: make(chan struct{}), release: make(chan struct{})}
			o := relayOptions()
			o.PublishTimeout = 300 * time.Millisecond
			o.Drain = 50 * time.Millisecond
			if drain {
				o.Drain = 500 * time.Millisecond
			}
			relay := outbox.Relay{Store: repo, Publisher: p, Options: o}
			run, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- relay.Run(run) }()
			select {
			case <-p.started:
			case <-time.After(3 * time.Second):
				cancel()
				t.Fatal("publish never started")
			}
			cancel()
			if drain {
				close(p.release)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("shutdown unbounded")
			}
			st, err := repo.Stats(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if drain {
				if st.Published != 1 {
					t.Fatal(st)
				}
			} else {
				if st.Published != 0 || st.Claimed != 1 {
					t.Fatal("K lost intent", st)
				}
				execute(t, db, `UPDATE outbox_events SET claim_expires_at=clock_timestamp()-interval '1 second' WHERE status='CLAIMED'`)
				if err = (outbox.Relay{Store: repo, Publisher: s, Options: o}).Step(ctx); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
