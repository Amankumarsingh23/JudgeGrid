//go:build agentintegration && session11integration

package judge_test

import (
	"context"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/agent"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/scheduling"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The optional test bootstrap instruments existing process-crash helpers too.
// No behavior is skipped when the diagnostics backend is absent.
func TestMain(m *testing.M) {
	var r *telemetry.Runtime
	if os.Getenv("S11_TEST_TELEMETRY") == "1" {
		var e error
		r, e = telemetry.Start("judge-agent")
		if e != nil {
			fmt.Fprintln(os.Stderr, "test telemetry configuration invalid")
			os.Exit(2)
		}
	}
	code := m.Run()
	if r != nil {
		r.Close()
	}
	os.Exit(code)
}
func TestS11FairWorkSurvivesTelemetryAndRedisFault(t *testing.T) {
	f := auto(t)
	var received atomic.Int64
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(200) }))
	t.Cleanup(receiver.Close)
	t.Setenv("APP_ENV", "development")
	t.Setenv("TELEMETRY_ENABLED", "true")
	t.Setenv("TELEMETRY_ADDR", "127.0.0.1:0")
	t.Setenv("TELEMETRY_OTLP_URL", receiver.URL)
	restore := telemetry.Install(nil)
	defer restore()
	r, e := telemetry.Start("test")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()

	_, ready := telemetry.Begin(context.Background(), "cache.lookup", "")
	ready(nil)
	waitAuto(t, 3*time.Second, func() bool { return received.Load() > 0 })
	p := scheduling.Default()
	p.Enabled = true
	p.GlobalActive = 2
	p.PrincipalActive = 1
	p.Principals = []string{"agent-user", "second-user"}
	if e = f.repo.ConfigureScheduler(f.ctx, p); e != nil {
		t.Fatal(e)
	}
	gate := gateAuto(t, os.Getenv("TEST_REDIS_ADDR"))
	client := queue.Open(gate.ln.Addr().String(), "")
	defer client.Close()
	for range 2 {
		o := agent.DefaultOptions()
		o.Concurrency = 1
		o.Recovery = 100 * time.Millisecond
		h, e := agent.New(f.repo, f.b, f.engine, "agent-user", fmt.Sprint("s11-", time.Now().UnixNano()), o)
		if e != nil {
			t.Fatal(e)
		}
		h.EnableFair(p.Principals)
		startAuto(t, h, client.Streams(f.s.Stream, f.s.Group))
	}
	j := f.submit(t, "python3", "a,b=map(int,input().split());print(a+b)")
	waitAuto(t, 10*time.Second, func() bool {
		var n int
		f.db.QueryRow(f.ctx, `SELECT count(*) FROM execution_ownership WHERE submission_id=$1 AND state='ACTIVE'`, j.SubmissionID).Scan(&n)
		return n == 1
	})
	// Both faults are explicitly owned gates, not shared database/service stops.
	gate.set(false)
	receiver.CloseClientConnections()
	receiver.Close()
	begin := time.Now()
	for range 1000 {
		_, end := telemetry.Begin(context.Background(), "cache.lookup", "")
		end(nil)
	}
	if time.Since(begin) > 2*time.Second {
		t.Fatal("exporter blocked execution caller")
	}
	f.finished(t, j, "AC")
	gate.set(true)
	// Notification reclaim follows the configured 30s lease/min-idle budget.
	waitAuto(t, 45*time.Second, f.delivered)
	var runs, active int
	f.db.QueryRow(f.ctx, `SELECT count(*) FROM evaluation_runs WHERE submission_id=$1`, j.SubmissionID).Scan(&runs)
	f.db.QueryRow(f.ctx, `SELECT count(*) FROM sandbox_authority WHERE state='ACTIVE' AND lease_expires_at>clock_timestamp()`).Scan(&active)
	if runs != 1 || active != 0 {
		t.Fatal("fault violated authoritative effects", runs, active)
	}
	out := httptest.NewRecorder()
	r.Handler().ServeHTTP(out, httptest.NewRequest("GET", "/metrics", nil))
	text := out.Body.String()
	if !strings.Contains(text, `operation="exporter.drop"`) || !strings.Contains(text, `operation="terminal",outcome="AC"`) {
		t.Fatal("missing failure/outcome telemetry")
	}
	t.Logf("I/J: receiver stopped and Redis gate offline during fair execution; AC durably committed once; queue bounded/dropped; valid active=%d; receiver requests before fault=%d", active, received.Load())
}
