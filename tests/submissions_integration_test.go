//go:build integration

package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/api"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/internal/submission"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func database(t *testing.T, apply bool) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("integration tests require TEST_DATABASE_URL; no tests skipped")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("judgegrid_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 25
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	if apply {
		if err = migrations.Apply(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if err = storage.Seed(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}
func request(t *testing.T, pool *pgxpool.Pool) domain.CreateRequest {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `SELECT id FROM problems WHERE slug='two-sum'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return domain.CreateRequest{ProblemID: id, Language: "cpp17", SourceCode: "int main() {return 0;}"}
}
func create(t *testing.T, repo *storage.Repository, principal, key string, req domain.CreateRequest) domain.Submission {
	t.Helper()
	s, replay, err := repo.Create(context.Background(), principal, key, req)
	if err != nil || replay {
		t.Fatalf("create: %v replay=%v", err, replay)
	}
	return s
}
func execute(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func constraint(t *testing.T, pool *pgxpool.Pool, code, sql string, args ...any) {
	t.Helper()
	_, err := pool.Exec(context.Background(), sql, args...)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != code {
		t.Fatalf("wanted SQLSTATE %s, got %v", code, err)
	}
}

func TestMigrationsAndSeeds(t *testing.T) {
	pool := database(t, false)
	ctx := context.Background()
	if err := migrations.Check(ctx, pool); err == nil {
		t.Fatal("pending migration accepted")
	}
	for range 2 {
		if err := migrations.Apply(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if err := storage.Seed(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrations.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var problems, versions int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM problems),(SELECT count(*) FROM problem_versions)`).Scan(&problems, &versions); err != nil || problems != 2 || versions != 2 {
		t.Fatalf("problems=%d versions=%d err=%v", problems, versions, err)
	}
	execute(t, pool, `UPDATE schema_migrations SET checksum='tampered'`)
	if err := migrations.Check(ctx, pool); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	if err := migrations.Apply(ctx, pool); err == nil {
		t.Fatal("apply accepted checksum mismatch")
	}
}

func TestTwentyConcurrentHTTPRequests(t *testing.T) {
	pool := database(t, true)
	repo := storage.NewRepository(pool)
	req := request(t, pool)
	payload, _ := json.Marshal(req)
	server := httptest.NewServer(api.Routes(time.Second, pool.Ping, func(context.Context) error { return nil }, submission.New(repo, 8*time.Second), api.DevelopmentPrincipal(true, "alice")))
	defer server.Close()
	type result struct {
		s      domain.Submission
		status int
		err    error
	}
	results := make(chan result, 20)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			request, err := http.NewRequest("POST", server.URL+"/api/v1/submissions", bytes.NewReader(payload))
			if err != nil {
				results <- result{err: err}
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "concurrent-key")
			response, err := server.Client().Do(request)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer response.Body.Close()
			var s domain.Submission
			err = json.NewDecoder(response.Body).Decode(&s)
			results <- result{s: s, status: response.StatusCode, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	id := ""
	created, replayed := 0, 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		switch result.status {
		case 201:
			created++
		case 200:
			replayed++
		default:
			t.Fatalf("status=%d", result.status)
		}
		if id == "" {
			id = result.s.ID
		}
		if id == "" || result.s.ID != id {
			t.Fatal("duplicate identities")
		}
	}
	var submissions, keys int
	if err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM submissions),(SELECT count(*) FROM submission_idempotency)`).Scan(&submissions, &keys); err != nil {
		t.Fatal(err)
	}
	if created != 1 || replayed != 19 || submissions != 1 || keys != 1 {
		t.Fatalf("created=%d replayed=%d rows=%d mappings=%d", created, replayed, submissions, keys)
	}
	var intents int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE submission_id=$1`, id).Scan(&intents); err != nil || intents != 1 {
		t.Fatal("I: initial outbox uniqueness", intents, err)
	}
	t.Logf("20 HTTP requests: %d created, %d replays, %d submission, %d mapping, same ID %s", created, replayed, submissions, keys, id)
}

func TestIdempotencyOwnershipAndVersionPinning(t *testing.T) {
	pool := database(t, true)
	repo := storage.NewRepository(pool)
	ctx := context.Background()
	req := request(t, pool)
	original := create(t, repo, "alice", "same-key", req)
	changed := req
	changed.SourceCode += "\n"
	if _, _, err := repo.Create(ctx, "alice", "same-key", changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("mismatch: %v", err)
	}
	bob := create(t, repo, "bob", "same-key", changed)
	if bob.ID == original.ID {
		t.Fatal("principals share namespace")
	}
	if _, err := repo.Get(ctx, "bob", original.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("cross-principal read")
	}
	list, err := repo.List(ctx, "bob", 100, nil)
	if err != nil || len(list) != 1 || list[0].ID != bob.ID {
		t.Fatalf("cross-principal list %v %v", list, err)
	}
	var newer int64
	if err = pool.QueryRow(ctx, `INSERT INTO problem_versions(problem_id,version_number,statement,time_limit_ms,memory_limit_kb) VALUES($1,2,'new statement',1000,128000) RETURNING id`, req.ProblemID).Scan(&newer); err != nil {
		t.Fatal(err)
	}
	execute(t, pool, `UPDATE problems SET current_version_id=$2 WHERE id=$1`, req.ProblemID, newer)
	if err = storage.Seed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	replay, yes, err := repo.Create(ctx, "alice", "same-key", req)
	if err != nil || !yes || replay.ID != original.ID || replay.ProblemVersion != 1 {
		t.Fatalf("replay changed historical version: %+v %v", replay, err)
	}
	next := create(t, repo, "alice", "new-key", req)
	if next.ProblemVersion != 2 || next.ProblemVersionID != newer {
		t.Fatal("current version not resolved")
	}
	constraint(t, pool, "23514", `UPDATE problem_versions SET statement='tampered' WHERE id=$1`, original.ProblemVersionID)
	constraint(t, pool, "23514", `DELETE FROM problem_versions WHERE id=$1`, original.ProblemVersionID)
	constraint(t, pool, "23514", `UPDATE submissions SET problem_version_id=$2 WHERE id=$1`, original.ID, newer)
	// HTTP ownership uses the same owner-scoped service; arbitrary user headers cannot override it.
	h := api.Routes(time.Second, nil, nil, submission.New(repo, time.Second), api.DevelopmentPrincipal(true, "bob"))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/submissions/"+original.ID, nil)
	r.Header.Set("X-User-ID", "alice")
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("owner isolation HTTP=%d", w.Code)
	}
}

func TestCASRaceAndTerminalStates(t *testing.T) {
	pool := database(t, true)
	repo := storage.NewRepository(pool)
	req := request(t, pool)
	s := create(t, repo, "alice", "cas", req)
	ctx := context.Background()
	results := make(chan error, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Transition(ctx, s.ID, domain.Transition{From: domain.Queued, To: domain.Running, ExpectedVersion: 0})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners, conflicts := 0, 0
	for err := range results {
		if err == nil {
			winners++
		} else if errors.Is(err, domain.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 1 || conflicts != 19 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
	if _, err := repo.Transition(ctx, s.ID, domain.Transition{From: domain.Running, To: domain.Evaluating, ExpectedVersion: 0}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale version accepted")
	}
	if _, err := repo.Transition(ctx, s.ID, domain.Transition{From: domain.Running, To: domain.Evaluating, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	done, err := repo.Transition(ctx, s.ID, domain.Transition{From: domain.Evaluating, To: domain.Completed, ExpectedVersion: 2, Verdict: "WA"})
	if err != nil || done.StateVersion != 3 || done.Verdict == nil || *done.Verdict != "WA" {
		t.Fatalf("%+v %v", done, err)
	}
	if _, err := repo.Transition(ctx, s.ID, domain.Transition{From: domain.Queued, To: domain.Running, ExpectedVersion: 0}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("terminal state overwritten")
	}
	if _, err := repo.Transition(ctx, "00000000-0000-4000-8000-000000000000", domain.Transition{From: domain.Queued, To: domain.Running}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("missing not distinguished")
	}
	failed := create(t, repo, "alice", "failure", req)
	failed, err = repo.Transition(ctx, failed.ID, domain.Transition{From: domain.Queued, To: domain.Failed, FailureReason: "infrastructure unavailable"})
	if err != nil || failed.Status != domain.Failed || failed.Verdict != nil {
		t.Fatal("failure semantics")
	}
	t.Logf("CAS race: %d winner, %d conflicts; terminal WA and infrastructure FAILED verified", winners, conflicts)
}

func TestConstraintsAndRollback(t *testing.T) {
	pool := database(t, true)
	repo := storage.NewRepository(pool)
	req := request(t, pool)
	s := create(t, repo, "alice", "baseline", req)
	// Each statement runs independently so a constraint failure does not mask subsequent checks.
	for _, tc := range []struct {
		name, code, sql string
		args            []any
	}{
		{"unique problem slug", "23505", `INSERT INTO problems(slug,title) VALUES('two-sum','duplicate')`, nil},
		{"version FK", "23503", `INSERT INTO problem_versions(problem_id,version_number,statement,time_limit_ms,memory_limit_kb) VALUES(999999,1,'x',1,1)`, nil},
		{"positive limits", "23514", `INSERT INTO problem_versions(problem_id,version_number,statement,time_limit_ms,memory_limit_kb) VALUES($1,3,'x',0,1)`, []any{req.ProblemID}},
		{"version unique", "23505", `INSERT INTO problem_versions(problem_id,version_number,statement,time_limit_ms,memory_limit_kb) VALUES($1,1,'x',1,1)`, []any{req.ProblemID}},
		{"composite FK", "23503", `INSERT INTO submissions(id,problem_id,problem_version_id,principal_id,language,source_code) SELECT '00000000-0000-4000-8000-000000000001',p.id,$1,'alice','go','x' FROM problems p WHERE slug='maximum'`, []any{s.ProblemVersionID}},
		{"completed needs verdict", "23514", `INSERT INTO submissions(id,problem_id,problem_version_id,principal_id,language,source_code,status) VALUES('00000000-0000-4000-8000-000000000002',$1,$2,'alice','go','x','COMPLETED')`, []any{s.ProblemID, s.ProblemVersionID}},
		{"queued forbids verdict", "23514", `INSERT INTO submissions(id,problem_id,problem_version_id,principal_id,language,source_code,verdict) VALUES('00000000-0000-4000-8000-000000000003',$1,$2,'alice','go','x','AC')`, []any{s.ProblemID, s.ProblemVersionID}},
		{"idempotency unique", "23505", `INSERT INTO submission_idempotency SELECT principal_id,idempotency_key,request_fingerprint,submission_id,created_at FROM submission_idempotency`, nil},
		{"mapping owner FK", "23503", `INSERT INTO submission_idempotency(principal_id,idempotency_key,request_fingerprint,submission_id) VALUES('bob','bad',$1,$2)`, []any{req.Fingerprint(), s.ID}},
		{"current composite FK", "23503", `UPDATE problems SET current_version_id=$1 WHERE slug='maximum'`, []any{s.ProblemVersionID}},
	} {
		t.Run(tc.name, func(t *testing.T) { constraint(t, pool, tc.code, tc.sql, tc.args...) })
	}
	// Inject a DB failure after submission INSERT, before its mapping can commit.
	execute(t, pool, `CREATE FUNCTION reject_test_key() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='rollback-key' THEN RAISE EXCEPTION 'injected mapping failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER test_mapping_failure BEFORE INSERT ON submission_idempotency FOR EACH ROW EXECUTE FUNCTION reject_test_key()`)
	if _, _, err := repo.Create(context.Background(), "alice", "rollback-key", req); err == nil {
		t.Fatal("failure injection failed")
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM submissions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("orphan submission after rollback: %d %v", count, err)
	}
	execute(t, pool, `DROP TRIGGER test_mapping_failure ON submission_idempotency`)
	create(t, repo, "alice", "rollback-key", req)
}

func TestCursorPagination(t *testing.T) {
	pool := database(t, true)
	repo := storage.NewRepository(pool)
	req := request(t, pool)
	svc := submission.New(repo, time.Second)
	for i := range 5 {
		create(t, repo, "alice", fmt.Sprintf("page-%d", i), req)
	}
	create(t, repo, "bob", "private", req)
	// Insert additional rows in one transaction, giving them identical creation timestamps.
	execute(t, pool, `INSERT INTO submissions(id,problem_id,problem_version_id,principal_id,language,source_code)
 SELECT ('00000000-0000-4000-8000-' || lpad(n::text,12,'0'))::uuid,$1,current_version_id,'alice','go','x'
 FROM problems CROSS JOIN generate_series(1,3) n WHERE problems.id=$1`, req.ProblemID)
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := svc.List(context.Background(), "alice", 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 2 {
			t.Fatal("page not bounded")
		}
		for _, s := range page.Items {
			if seen[s.ID] || s.PrincipalID != "alice" {
				t.Fatal("duplicate or foreign row")
			}
			seen[s.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 8 {
		t.Fatalf("got %d rows", len(seen))
	}
}

func TestUnavailableDatabaseHTTP(t *testing.T) {
	pool := database(t, true)
	repo := storage.NewRepository(pool)
	pool.Close()
	h := api.Routes(time.Second, nil, nil, submission.New(repo, time.Second), api.DevelopmentPrincipal(true, "alice"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/submissions", nil))
	if w.Code != 503 || strings.Contains(w.Body.String(), "pool closed") {
		t.Fatalf("unsafe failure %d %s", w.Code, w.Body.String())
	}
}

func TestMissingAndUnpublishedProblems(t *testing.T) {
	pool := database(t, true)
	repo := storage.NewRepository(pool)
	req := request(t, pool)
	ctx := context.Background()
	req.ProblemID = 999999
	if _, _, err := repo.Create(ctx, "alice", "missing", req); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO problems(slug,title) VALUES('unpublished','Draft') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	req.ProblemID = id
	if _, _, err := repo.Create(ctx, "alice", "draft", req); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("unpublished: %v", err)
	}
	if _, err := repo.Problem(ctx, "unpublished"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("draft metadata exposed: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM submissions)+(SELECT count(*) FROM submission_idempotency)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan rows=%d %v", count, err)
	}
}
