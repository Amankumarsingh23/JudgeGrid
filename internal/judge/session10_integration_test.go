//go:build agentintegration && session10integration

package judge_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/agent"
	"github.com/Amankumarsingh23/JudgeGrid/internal/api"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"github.com/Amankumarsingh23/JudgeGrid/internal/scheduling"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/internal/submission"
)

func TestS10FairAutomaticJudgeSSEAndRejudge(t *testing.T) {
	f := auto(t)
	p := scheduling.Default()
	p.Enabled = true
	p.GlobalActive = 2
	p.PrincipalActive = 1
	p.Principals = []string{"agent-user", "second-user"}
	if e := f.repo.ConfigureScheduler(f.ctx, p); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		o := agent.DefaultOptions()
		o.Concurrency = 1
		o.Recovery = 100 * time.Millisecond
		h, e := agent.New(f.repo, f.b, f.engine, "agent-user", fmt.Sprint("s10-agent-", time.Now().UnixNano()), o)
		if e != nil {
			t.Fatal(e)
		}
		if e = h.EnableFair(p.Principals); e != nil {
			t.Fatal(e)
		}
		startAuto(t, h, f.s)
	}
	server := httptest.NewServer(api.Routes(time.Second, nil, nil, submission.New(f.repo, 3*time.Second), api.DevelopmentPrincipal(true, "agent-user")))
	t.Cleanup(server.Close)
	// The exact cookie-auth boundary is exercised separately with two real API servers.
	observe := func(id, verdict string) map[string]bool {
		t.Helper()
		ctx, cancel := context.WithTimeout(f.ctx, 60*time.Second)
		defer cancel()
		q, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/submissions/"+id+"/events", nil)
		q.Header.Set("Last-Event-ID", id+":0")
		resp, e := server.Client().Do(q)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal("SSE", resp.StatusCode)
		}
		kinds := map[string]bool{}
		scan := bufio.NewScanner(resp.Body)
		kind := ""
		last := int64(0)
		for scan.Scan() {
			line := scan.Text()
			if strings.HasPrefix(line, "event: ") {
				kind = strings.TrimPrefix(line, "event: ")
				kinds[kind] = true
			}
			if strings.HasPrefix(line, "id: ") {
				var n int64
				fmt.Sscanf(strings.TrimPrefix(line, "id: "), id+":%d", &n)
				if n < last {
					t.Fatal("SSE cursor moved backwards")
				}
				last = n
			}
			if strings.HasPrefix(line, "data: ") {
				var d struct {
					Verdict string `json:"verdict"`
				}
				if e = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d); e != nil {
					t.Fatal(e)
				}
				if (kind == "selected" || kind == "completed") && d.Verdict == verdict {
					var committed time.Time
					if e = f.db.QueryRow(f.ctx, `SELECT max(created_at) FROM public_events WHERE submission_id=$1 AND kind IN ('completed','selected')`, id).Scan(&committed); e != nil {
						t.Fatal(e)
					}
					t.Logf("actual %s: cursor=%d, journal-timestamp-to-client=%s (includes commit)", verdict, last, time.Since(committed))
					return kinds
				}
			}
		}
		t.Fatal("real verdict never reached SSE", verdict, scan.Err())
		return nil
	}
	var original string
	for _, tc := range []struct{ name, lang, source, want string }{
		{"cppAC", "cpp17", "#include <iostream>\nint main(){long long a,b;std::cin>>a>>b;if(a==42&&b==0)std::cout<<0;else std::cout<<a+b;}", "AC"},
		{"cppWA", "cpp17", "#include <iostream>\nint main(){std::cout<<999;}", "WA"},
		{"cppCE", "cpp17", "int main( {", "CE"},
		{"pyAC", "python3", "a,b=map(int,input().split());print(a+b)", "AC"},
		{"pyRE", "python3", "raise RuntimeError('private diagnostic')", "RE"},
		{"pyTLE", "python3", "while True: pass", "TLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := f.submit(t, tc.lang, tc.source)
			if tc.name == "cppAC" {
				original = j.SubmissionID
			}
			kinds := observe(j.SubmissionID, tc.want)
			if !kinds["accepted"] || !kinds["claimed"] || (tc.lang == "cpp17" && !kinds["compiling"]) || (tc.want != "CE" && !kinds["running"]) {
				t.Fatal("missing actual observed phases", kinds)
			}
			f.finished(t, j, tc.want)
			var active int
			f.db.QueryRow(f.ctx, `SELECT count(*) FROM sandbox_authority WHERE state='ACTIVE' AND lease_expires_at>clock_timestamp()`).Scan(&active)
			if active != 0 {
				t.Fatal("verdict leaked scheduled capacity", tc.want, active)
			}
			events, _ := f.repo.ProgressEvents(f.ctx, "agent-user", j.SubmissionID, 0, 0, 32)
			safe, _ := json.Marshal(events)
			for _, secret := range []string{"source_code", "private diagnostic", "bundle_sha", "expected_output"} {
				if strings.Contains(string(safe), secret) {
					t.Fatal("public journal leaked", secret)
				}
			}
		})
	}
	b, _ := bundles.Demo("two-sum")
	b.Tests = append(b.Tests, bundles.Case{ID: "edge-42", Order: 7, Input: []byte("42 0\n"), Expected: []byte("42\n")})
	version, e := f.repo.PublishVersion(f.ctx, "agent-user", "s10-corrected", storage.Publication{ProblemID: f.problem, Statement: "additional edge case", TimeLimitMS: 5000, MemoryLimitKB: 262144, Bundle: b}, f.b)
	if e != nil {
		t.Fatal(e)
	}
	req, e := f.repo.RequestRejudge(f.ctx, "agent-user", "s10-rejudge", original, version)
	if e != nil {
		t.Fatal(e)
	}
	if e = (outbox.Relay{Store: f.repo, Publisher: f.s, Options: outbox.DefaultOptions()}).Step(f.ctx); e != nil {
		t.Fatal(e)
	}
	kinds := observe(original, "WA")
	for _, kind := range []string{"rejudge_requested", "rejudge_running", "rejudge_completed", "selected"} {
		if !kinds[kind] {
			t.Fatal("missing rejudge phase", kind, kinds)
		}
	}
	current, e := f.repo.Get(f.ctx, "agent-user", original)
	if e != nil || current.OriginalVerdict == nil || *current.OriginalVerdict != "AC" || current.Verdict == nil || *current.Verdict != "WA" {
		t.Fatal("selected/original verdict", current, e)
	}
	history, e := f.repo.History(f.ctx, "agent-user", original)
	if e != nil || len(history) != 2 || *history[0].Verdict != "AC" || *history[1].Verdict != "WA" {
		t.Fatal(history, e)
	}
	t.Logf("two fair agent loops; actual C++ AC/WA/CE and Python AC/RE/TLE; every terminal verdict released capacity; rejudge %s preserved AC and selected WA", req.JobID)
}
