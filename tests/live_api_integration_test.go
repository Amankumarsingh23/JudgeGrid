//go:build integration

package tests

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/api"
	"github.com/Amankumarsingh23/JudgeGrid/internal/identity"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/internal/submission"
)

type webClient struct {
	token   string
	session identity.Session
}

func localAPI(t *testing.T, r *storage.Repository) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(nil)
	w, e := api.NewLocalWeb(r, "http://"+s.Listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	s.Config.Handler = w.Wrap(api.Routes(time.Second, nil, nil, submission.New(r, 3*time.Second), w.Resolve))
	s.Start()
	s.Client().Timeout = 20 * time.Second
	t.Cleanup(s.Close)
	return s
}
func loginWeb(t *testing.T, s *httptest.Server, name string) webClient {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"username": name, "password": "local-test-password-123"})
	q, _ := http.NewRequest("POST", s.URL+"/api/v1/session", bytes.NewReader(data))
	q.Header.Set("Origin", s.URL)
	resp, e := s.Client().Do(q)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("login", resp.StatusCode)
	}
	var c webClient
	json.NewDecoder(resp.Body).Decode(&c.session)
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "judgegrid_session" {
			c.token = cookie.Value
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
				t.Fatal("unsafe session cookie")
			}
		}
	}
	return c
}
func webRequest(t *testing.T, s *httptest.Server, c webClient, method, path, key string, body []byte) *http.Response {
	t.Helper()
	q, _ := http.NewRequest(method, s.URL+"/api/v1"+path, bytes.NewReader(body))
	q.AddCookie(&http.Cookie{Name: "judgegrid_session", Value: c.token})
	q.Header.Set("Origin", s.URL)
	q.Header.Set("X-CSRF-Token", c.session.CSRF)
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("Idempotency-Key", key)
	q.Header.Set("X-User-ID", "principal-a")
	out, e := s.Client().Do(q)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func status(t *testing.T, r *http.Response, want int) {
	t.Helper()
	defer r.Body.Close()
	if r.StatusCode != want {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("HTTP %d wanted %d: %s", r.StatusCode, want, b)
	}
}

func TestLocalAuthSharedLimitsAndOwnerIsolation(t *testing.T) {
	db, r, p := schedulerSetup(t)
	ctx := context.Background()
	for _, name := range p.Principals {
		if e := r.CreateLocalUser(ctx, name, "local-test-password-123"); e != nil {
			t.Fatal(e)
		}
	}
	one, two := localAPI(t, r), localAPI(t, r)
	a, b := loginWeb(t, one, "principal-a"), loginWeb(t, two, "principal-b")
	req := request(t, db)
	body, _ := json.Marshal(req)
	var wg sync.WaitGroup
	codes := make(chan int, 20)
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := one
			if i%2 == 0 {
				s = two
			}
			resp := webRequest(t, s, a, "POST", "/submissions", "same-key", body)
			codes <- resp.StatusCode
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
	close(codes)
	created := 0
	for n := range codes {
		if n == 201 {
			created++
		} else if n != 200 {
			t.Fatal("20-way replay", n)
		}
	}
	if created != 1 {
		t.Fatal("creation winners", created)
	}
	var id string
	var n int
	db.QueryRow(ctx, `SELECT id::text FROM submissions WHERE principal_id='principal-a'`).Scan(&id)
	db.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE submission_id=$1`, id).Scan(&n)
	if n != 1 {
		t.Fatal("duplicate durable intent", n)
	}
	// A replay still spends a shared rate token; a second API cannot reset it.
	p.Burst = 1
	p.Rate = .01
	if e := r.ConfigureScheduler(ctx, p); e != nil {
		t.Fatal(e)
	}
	execute(t, db, `DELETE FROM request_buckets WHERE bucket='submit:principal-a'`)
	status(t, webRequest(t, one, a, "POST", "/submissions", "same-key", body), 200)
	limited := webRequest(t, two, a, "POST", "/submissions", "same-key", body)
	if limited.Header.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	status(t, limited, 429)
	p.Burst = 120
	p.Pending = 1
	if e := r.ConfigureScheduler(ctx, p); e != nil {
		t.Fatal(e)
	}
	execute(t, db, `DELETE FROM request_buckets WHERE bucket='submit:principal-a'`)
	status(t, webRequest(t, two, a, "POST", "/submissions", "other", body), 429)
	status(t, webRequest(t, two, a, "POST", "/submissions", "same-key", body), 200)
	p.Pending = 128
	if e := r.ConfigureScheduler(ctx, p); e != nil {
		t.Fatal(e)
	}
	create(t, r, "principal-a", "cursor-second", req)
	page, e := submission.New(r, time.Second).List(ctx, "principal-a", 1, "")
	if e != nil || page.NextCursor == "" {
		t.Fatal("missing owner cursor", e)
	}
	status(t, webRequest(t, two, b, "GET", "/submissions?limit=1&cursor="+page.NextCursor, "", nil), 400)
	for _, path := range []string{"/submissions/" + id, "/submissions/" + id + "/evaluations", "/submissions/" + id + "/events?cursor=forged"} {
		status(t, webRequest(t, two, b, "GET", path, "", nil), 404)
	}
	list := webRequest(t, two, b, "GET", "/submissions", "", nil)
	data, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if list.StatusCode != 200 || bytes.Contains(data, []byte(id)) || bytes.Contains(data, []byte(req.SourceCode)) {
		t.Fatal("foreign list leaked", string(data))
	}
	status(t, webRequest(t, two, webClient{}, "GET", "/submissions/"+id, "", nil), 401)
	q, _ := http.NewRequest("POST", one.URL+"/api/v1/submissions", bytes.NewReader(body))
	q.AddCookie(&http.Cookie{Name: "judgegrid_session", Value: a.token})
	q.Header.Set("Origin", "http://evil.invalid")
	res, e := one.Client().Do(q)
	if e != nil {
		t.Fatal(e)
	}
	status(t, res, 403)
	status(t, webRequest(t, one, a, "DELETE", "/session", "", nil), 204)
	status(t, webRequest(t, two, a, "GET", "/submissions/"+id, "", nil), 401)
	t.Log("20 concurrent POSTs across two APIs: one submission/intent; replay consumes rate; pending 429; forged header/cross-owner GET/history/SSE/list denied; shared logout revokes session")
}

func TestPublicJournalCommitOrderAndRollback(t *testing.T) {
	db, r, _ := schedulerSetup(t)
	ctx := context.Background()
	s := create(t, r, "principal-a", "journal", request(t, db))
	a, e := db.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Rollback(ctx)
	var first int64
	if e = a.QueryRow(ctx, `SELECT append_public_event($1,'preparing','{"status":"RUNNING"}')`, s.ID).Scan(&first); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := db.Exec(ctx, `SELECT append_public_event($1,'running','{"status":"RUNNING"}')`, s.ID)
		done <- e
	}()
	select {
	case e := <-done:
		t.Fatal("later event committed across earlier transaction", e)
	case <-time.After(150 * time.Millisecond):
	}
	snapshot, e := r.ProgressSnapshot(ctx, "principal-a", s.ID)
	if e != nil || snapshot.Cursor != first-1 {
		t.Fatal("uncommitted cursor visible", snapshot, e)
	}
	if e = a.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	events, e := r.ProgressEvents(ctx, "principal-a", s.ID, 0, 0, 32)
	if e != nil || len(events) != 3 || events[1].Sequence != 2 || events[2].Sequence != 3 {
		t.Fatal(events, e)
	}
	tx, _ := db.Begin(ctx)
	tx.Exec(ctx, `SELECT append_public_event($1,'running','{}')`, s.ID)
	tx.Rollback(ctx)
	snapshot, e = r.ProgressSnapshot(ctx, "principal-a", s.ID)
	if e != nil || snapshot.Cursor != 3 {
		t.Fatal("rollback left cursor gap", snapshot, e)
	}
	constraint(t, db, "23514", `SELECT append_public_event($1,'running','{"source_code":"secret"}')`, s.ID)
	t.Log("A allocates sequence 2 and delays commit; B blocks; reader remains at 1; then sees contiguous 2,3; rollback consumes no cursor")
}

func TestSSEReplaySharedCapsAndLagReset(t *testing.T) {
	db, r, p := schedulerSetup(t)
	ctx := context.Background()
	p.SSEGlobal = 1
	p.SSEPrincipal = 1
	if e := r.ConfigureScheduler(ctx, p); e != nil {
		t.Fatal(e)
	}
	r.CreateLocalUser(ctx, "principal-a", "local-test-password-123")
	one, two := localAPI(t, r), localAPI(t, r)
	a := loginWeb(t, one, "principal-a")
	s := create(t, r, "principal-a", "stream", request(t, db))
	open := func(server *httptest.Server, after string) *http.Response {
		q, _ := http.NewRequest("GET", server.URL+"/api/v1/submissions/"+s.ID+"/events", nil)
		q.AddCookie(&http.Cookie{Name: "judgegrid_session", Value: a.token})
		if after != "" {
			q.Header.Set("Last-Event-ID", after)
		}
		resp, e := server.Client().Do(q)
		if e != nil {
			t.Fatal(e)
		}
		return resp
	}
	resp := open(one, "")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("SSE headers", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	readUntil := func(scan *bufio.Scanner, match string) {
		t.Helper()
		for scan.Scan() {
			if strings.Contains(scan.Text(), match) {
				return
			}
		}
		t.Fatal("SSE ended before", match, scan.Err())
	}
	readUntil(scanner, "event: snapshot")
	status(t, open(two, ""), 429)
	readUntil(scanner, ": heartbeat")
	resp.Body.Close()
	eventually(t, 3*time.Second, func() bool {
		var n int
		db.QueryRow(ctx, `SELECT count(*) FROM sse_connections`).Scan(&n)
		return n == 0
	})
	execute(t, db, `SELECT append_public_event($1,'running','{"status":"RUNNING"}')`, s.ID)
	start := time.Now()
	resp = open(two, s.ID+":1")
	scanner = bufio.NewScanner(resp.Body)
	readUntil(scanner, "event: running")
	readUntil(scanner, "event: snapshot")
	t.Logf("missed event replay and authoritative snapshot received in %s", time.Since(start))
	resp.Body.Close()
	eventually(t, 3*time.Second, func() bool {
		var n int
		db.QueryRow(ctx, `SELECT count(*) FROM sse_connections`).Scan(&n)
		return n == 0
	})
	resp = open(one, "")
	scanner = bufio.NewScanner(resp.Body)
	readUntil(scanner, "event: snapshot")
	// A consumer that falls behind the bounded journal window gets a reset and closes.
	execute(t, db, `SELECT append_public_event($1,'running','{"status":"RUNNING"}') FROM generate_series(1,300)`, s.ID)
	readUntil(scanner, "event: reset")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	eventually(t, 3*time.Second, func() bool {
		var n int
		db.QueryRow(ctx, `SELECT count(*) FROM sse_connections`).Scan(&n)
		return n == 0
	})
	resp = open(two, s.ID+":1")
	scanner = bufio.NewScanner(resp.Body)
	readUntil(scanner, "event: reset")
	status(t, webRequest(t, one, a, "DELETE", "/session", "", nil), 204)
	revoked := time.Now()
	io.Copy(io.Discard, resp.Body)
	if time.Since(revoked) > 2*time.Second {
		t.Fatal("revoked stream remained authorized")
	}
	resp.Body.Close()
	t.Log(fmt.Sprintf("shared SSE cap=%d; disconnect releases slot; Last-Event-ID replays contiguous history; lag>256 resets bounded stream", p.SSEGlobal))
}
