//go:build integration

package tests

import (
	"context"
	"github.com/Amankumarsingh23/JudgeGrid/internal/api"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/internal/submission"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDurableTelemetryContextAndFailureIsolation(t *testing.T) {
	db := database(t, true)
	repo := storage.NewRepository(db)
	r := telemetry.New("test", nil)
	restore := telemetry.Install(r)
	defer restore()
	defer r.Close()
	ctx, end := telemetry.Begin(context.Background(), "create", "cpp17")
	defer end(nil)
	req := request(t, db)
	s := create(t, repo, "trace-owner", "trace-key", req)
	var absent *string
	db.QueryRow(ctx, `SELECT traceparent FROM outbox_events WHERE submission_id=$1`, s.ID).Scan(&absent)
	if absent == nil || (*absent)[:35] == telemetry.Parent(ctx)[:35] {
		t.Fatal("unrelated request did not receive its own trace")
	}
	traced, replay, e := repo.Create(ctx, "trace-owner", "traced", req)
	if e != nil || replay {
		t.Fatal(e)
	}
	var parent, event string
	if e = db.QueryRow(ctx, `SELECT traceparent,id::text FROM outbox_events WHERE submission_id=$1`, traced.ID).Scan(&parent, &event); e != nil || !telemetry.ValidParent(parent) {
		t.Fatal(parent, e)
	}
	constraint(t, db, "P0001", `UPDATE outbox_events SET traceparent=NULL WHERE id=$1`, event)
	stream, _ := streams(t)
	if e = (outbox.Relay{Store: repo, Publisher: stream, Options: relayOptions()}).Step(ctx); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), `operation="accepted",outcome="ok"`) {
		t.Fatal(w.Body.String())
	}
	telemetry.Count("outbox.publish", "ok", "")
	ctxPoll, cancel := context.WithCancel(context.Background())
	defer cancel()
	go telemetry.Poll(ctxPoll, db)
	eventually(t, 2*time.Second, func() bool {
		w := httptest.NewRecorder()
		r.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
		return strings.Contains(w.Body.String(), `kind="snapshot_ok",service="test",state="ok"} 1`)
	})
	job := queue.Job{Kind: queue.SubmissionJob, SubmissionID: traced.ID, EventID: event, Attempt: 1, TraceParent: parent}
	decoded, e := (queue.Message{Values: job.Values()}).Job()
	if e != nil || decoded.TraceParent != parent || job.Values()["schema_version"] != "4" {
		t.Fatal(decoded, e)
	}
	h := telemetry.HTTP(api.Routes(time.Second, nil, nil, submission.New(repo, time.Second), api.DevelopmentPrincipal(true, "trace-owner")))
	q := httptest.NewRequest("GET", "/api/v1/submissions/"+traced.ID, nil)
	q.Header.Set("traceparent", "malformed\nsecret")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, q)
	if out.Code != 200 {
		t.Fatal(out.Code)
	}
	db.Close()
	cached := httptest.NewRecorder()
	r.Handler().ServeHTTP(cached, httptest.NewRequest("GET", "/metrics", nil))
	if cached.Code != 200 || !strings.Contains(cached.Body.String(), "judgegrid_snapshot_updated_timestamp_seconds") {
		t.Fatal("scrape depended on live database")
	}
	t.Log("producer context persists atomically, immutable; schema 4 transport round trip; DB snapshot collected outside scrape; telemetry disabled receiver preserves owner GET")
}

func TestRetryBudgetTerminalTelemetryIsCommittedOnce(t *testing.T) {
	db, repo, _ := schedulerSetup(t)
	r := telemetry.New("test", nil)
	restore := telemetry.Install(r)
	defer restore()
	defer r.Close()
	ctx := context.Background()
	s := create(t, repo, "principal-a", "last-attempt", request(t, db))
	publishScheduled(t, repo)
	next, e := repo.NextScheduled(ctx, []string{"principal-a", "principal-b"})
	if e != nil || next == nil {
		t.Fatal(next, e)
	}
	w, e := repo.ClaimExecution(ctx, "principal-a", "counter-owner", next.Job, 30*time.Second, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = repo.FailExecution(ctx, w, "sandbox_infrastructure", true); e != nil {
		t.Fatal(e)
	}
	got, e := repo.Get(ctx, "principal-a", s.ID)
	if e != nil || got.Status != "FAILED" {
		t.Fatal(got, e)
	}
	if e = repo.FailExecution(ctx, w, "sandbox_infrastructure", true); e == nil {
		t.Fatal("terminal failure replay retained authority")
	}
	rows, e := r.Registry.Gather()
	if e != nil {
		t.Fatal(e)
	}
	var count float64
	for _, row := range rows {
		if row.GetName() != "judgegrid_operations_total" {
			continue
		}
		for _, metric := range row.Metric {
			op, result := "", ""
			for _, label := range metric.Label {
				if label.GetName() == "operation" {
					op = label.GetValue()
				}
				if label.GetName() == "outcome" {
					result = label.GetValue()
				}
			}
			if op == "terminal" && result == "FAILED" {
				count += metric.GetCounter().GetValue()
			}
		}
	}
	if count != 1 {
		t.Fatal("terminal counter did not match one committed logical failure", count)
	}
}
