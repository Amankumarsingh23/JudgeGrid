//go:build agentintegration

package judge_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/agent"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/judge"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type autoFixture struct {
	ctx       context.Context
	db        *pgxpool.Pool
	repo      *storage.Repository
	b         *bundles.Local
	root, dir string
	engine    *execution.Docker
	client    *queue.Client
	raw       *redis.Client
	s         *queue.Streams
	problem   int64
}

func auto(t *testing.T) *autoFixture {
	t.Helper()
	if os.Getenv("JUDGEGRID_AGENT_TESTS") != "1" || os.Getenv("TEST_DATABASE_URL") == "" || os.Getenv("TEST_REDIS_ADDR") == "" {
		t.Fatal("requires explicit real agent test environment")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("judgegrid_agent_%d", time.Now().UnixNano())
	q := pgx.Identifier{schema}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+q); e != nil {
		t.Fatal(e)
	}
	cfg, _ := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 25
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f := &autoFixture{ctx: ctx, db: db, repo: storage.NewRepository(db), root: ".", dir: t.TempDir()}
	if _, e = os.Stat("infra/sandbox/seccomp.json"); e != nil {
		f.root = "../.."
	}
	f.root, _ = filepath.Abs(f.root)
	if e = migrations.Apply(ctx, db); e != nil {
		t.Fatal(e)
	}
	if e = storage.Seed(ctx, db); e != nil {
		t.Fatal(e)
	}
	f.b, e = bundles.Open(f.dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = storage.SeedSuites(ctx, db, f.b); e != nil {
		t.Fatal(e)
	}
	images, e := execution.LoadImages(filepath.Join(f.root, ".sandbox-images.local.json"))
	if e != nil {
		t.Fatal(e)
	}
	f.engine, e = execution.NewDocker(images, filepath.Join(f.root, "infra/sandbox/seccomp.json"), 2)
	if e != nil {
		t.Fatal(e)
	}
	f.client = queue.Open(os.Getenv("TEST_REDIS_ADDR"), "")
	f.raw = redis.NewClient(&redis.Options{Addr: os.Getenv("TEST_REDIS_ADDR"), MaxRetries: -1})
	f.s = f.client.Streams(fmt.Sprintf("test:agent:%d", time.Now().UnixNano()), "auto")
	if e = f.s.EnsureGroup(ctx); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow(ctx, `SELECT id FROM problems WHERE slug='two-sum'`).Scan(&f.problem); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		f.engine.Close()
		f.raw.Del(ctx, f.s.Stream)
		f.client.Close()
		f.raw.Close()
		db.Close()
		_, e = admin.Exec(ctx, "DROP SCHEMA "+q+" CASCADE")
		admin.Close()
		if e != nil {
			t.Error(e)
		}
	})
	return f
}
func (f *autoFixture) submit(t *testing.T, lang, source string) queue.Job {
	t.Helper()
	sub, _, e := f.repo.Create(f.ctx, "agent-user", fmt.Sprintf("auto-%d", time.Now().UnixNano()), domain.CreateRequest{ProblemID: f.problem, Language: lang, SourceCode: source})
	if e != nil {
		t.Fatal(e)
	}
	if e = (outbox.Relay{Store: f.repo, Publisher: f.s, Options: outbox.DefaultOptions()}).Step(f.ctx); e != nil {
		t.Fatal(e)
	}
	var id string
	if e = f.db.QueryRow(f.ctx, `SELECT id::text FROM outbox_events WHERE submission_id=$1`, sub.ID).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return queue.Job{Kind: queue.SubmissionJob, SubmissionID: sub.ID, EventID: id, Attempt: 1}
}
func autoOptions() agent.Options {
	o := agent.DefaultOptions()
	o.Lease = 3 * time.Second
	o.Heartbeat = 500 * time.Millisecond
	o.Recovery = 100 * time.Millisecond
	o.Drain = 5 * time.Second
	o.Concurrency = 1
	return o
}
func waitAuto(t *testing.T, d time.Duration, fn func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("automatic judging deadline")
}
func (f *autoFixture) handler(t *testing.T, store agent.Store, engine execution.Preparer) *agent.Handler {
	t.Helper()
	h, e := agent.New(store, f.b, engine, "agent-user", "test-agent-"+fmt.Sprint(time.Now().UnixNano()), autoOptions())
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func startAuto(t *testing.T, h *agent.Handler, s *queue.Streams) (context.CancelFunc, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx, s) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(40 * time.Second):
			t.Error("agent goroutines did not drain")
		}
	})
	return cancel, done
}
func (f *autoFixture) finished(t *testing.T, j queue.Job, want string) {
	t.Helper()
	waitAuto(t, 45*time.Second, func() bool {
		v, e := f.repo.Get(f.ctx, "agent-user", j.SubmissionID)
		return e == nil && ((want == "FAILED" && v.Status == domain.Failed) || (v.Verdict != nil && string(*v.Verdict) == want))
	})
}

func (f *autoFixture) delivered() bool {
	p, e := f.s.Pending(f.ctx)
	if e != nil || p.Count != 0 {
		return false
	}
	groups, e := f.raw.XInfoGroups(f.ctx, f.s.Stream).Result()
	if e != nil {
		return false
	}
	for _, g := range groups {
		if g.Name == f.s.Group {
			return g.Lag == 0
		}
	}
	return false
}
func TestAutomaticJudgePipeline(t *testing.T) {
	f := auto(t)
	h := f.handler(t, f.repo, f.engine)
	startAuto(t, h, f.s)
	for _, tc := range []struct{ name, lang, source, want string }{{"cppAC", "cpp17", "#include <iostream>\nint main(){long long a,b;std::cin>>a>>b;std::cout<<a+b;}", "AC"}, {"cppWA", "cpp17", "#include <iostream>\nint main(){std::cout<<999;}", "WA"}, {"cppCE", "cpp17", "int main( {", "CE"}, {"pyAC", "python3", "a,b=map(int,input().split());print(a+b)", "AC"}, {"pyRE", "python3", "raise RuntimeError('private')", "RE"}, {"pyTLE", "python3", "while True: pass", "TLE"}, {"unsupported", "go", "package main\nfunc main(){}", "FAILED"}} {
		t.Run(tc.name, func(t *testing.T) {
			j := f.submit(t, tc.lang, tc.source)
			if _, e := f.s.Publish(f.ctx, j); e != nil {
				t.Fatal(e)
			}
			f.finished(t, j, tc.want)
			waitAuto(t, 8*time.Second, f.delivered)
			var runs int
			if e := f.db.QueryRow(f.ctx, `SELECT count(*) FROM evaluation_runs WHERE submission_id=$1`, j.SubmissionID).Scan(&runs); e != nil || ((tc.want != "FAILED" && runs != 1) || (tc.want == "FAILED" && runs != 0)) {
				t.Fatal("duplicate execution", runs, e)
			}
			t.Logf("genuine automatic %s -> %s, runs=%d", tc.name, tc.want, runs)
		})
	}
}

type crashStore struct {
	*storage.Repository
	mode string
}

func (s crashStore) ClaimExecution(ctx context.Context, p, o string, j queue.Job, ttl time.Duration, max int) (judge.Work, error) {
	w, e := s.Repository.ClaimExecution(ctx, p, o, j, ttl, max)
	if e == nil && s.mode == "claim" {
		boundary("")
	}
	return w, e
}
func (s crashStore) CommitEvaluation(ctx context.Context, w judge.Work, r judge.Report) (judge.Summary, error) {
	if s.mode == "before_commit" {
		boundary("")
	}
	v, e := s.Repository.CommitEvaluation(ctx, w, r)
	if e == nil && s.mode == "after_commit" {
		boundary("")
	}
	return v, e
}
func boundary(id string) { fmt.Println("AGENT_BOUNDARY " + id); time.Sleep(time.Minute); os.Exit(3) }

type crashEngine struct {
	execution.Preparer
	mode string
}

func (e crashEngine) Prepare(ctx context.Context, r execution.Request) (execution.Prepared, execution.Result, error) {
	p, v, err := e.Preparer.Prepare(ctx, r)
	if p != nil && e.mode == "during" {
		return crashArtifact{Prepared: p}, v, err
	}
	return p, v, err
}

type crashArtifact struct{ execution.Prepared }

func (p crashArtifact) Run(ctx context.Context, input []byte, l execution.Limits) (*execution.PhaseResult, error) {
	id := p.Prepared.Summary().ExecutionID
	done := make(chan struct{})
	go func() { p.Prepared.Run(ctx, input, l); close(done) }()
	for i := 0; i < 100; i++ {
		out, _ := exec.Command("docker", "ps", "--filter", "name="+id+"-run", "--format", "{{.Names}}").Output()
		if strings.Contains(string(out), id+"-run") {
			boundary(id)
		}
		select {
		case <-done:
			return nil, fmt.Errorf("runtime ended before crash boundary")
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("runtime never started")
}
func TestAgentCrashHelper(t *testing.T) {
	mode := os.Getenv("JUDGEGRID_AGENT_CRASH")
	if mode == "" {
		return
	}
	ctx := context.Background()
	cfg, _ := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	cfg.ConnConfig.RuntimeParams["search_path"] = os.Getenv("AGENT_CRASH_SCHEMA")
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	b, e := bundles.Open(os.Getenv("AGENT_CRASH_BUNDLES"))
	if e != nil {
		t.Fatal(e)
	}
	root := os.Getenv("AGENT_CRASH_ROOT")
	images, e := execution.LoadImages(filepath.Join(root, ".sandbox-images.local.json"))
	if e != nil {
		t.Fatal(e)
	}
	engine, e := execution.NewDocker(images, filepath.Join(root, "infra/sandbox/seccomp.json"), 1)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	h, e := agent.New(crashStore{storage.NewRepository(db), mode}, b, crashEngine{engine, mode}, "agent-user", "crash-process", autoOptions())
	if e != nil {
		t.Fatal(e)
	}
	var j queue.Job
	if json.Unmarshal([]byte(os.Getenv("AGENT_CRASH_JOB")), &j) != nil {
		t.Fatal("invalid job fixture")
	}
	client := queue.Open(os.Getenv("TEST_REDIS_ADDR"), "")
	defer client.Close()
	stream := client.Streams(os.Getenv("AGENT_CRASH_STREAM"), "auto")
	messages, e := stream.Read(ctx, "crash-process", 1, 10*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	if len(messages) == 0 {
		messages, _, e = stream.AutoClaim(ctx, "crash-process", "0-0", time.Millisecond, 1)
	}
	if e != nil || len(messages) != 1 {
		t.Fatal("crash process did not receive real delivery", e)
	}
	actual, e := messages[0].Job()
	if e != nil || actual != j {
		t.Fatal("wrong crash delivery", actual, e)
	}
	if _, e = h.Handle(ctx, j, "child"); e != nil {
		t.Fatal(e)
	}
	t.Fatal("crash boundary not reached")
}
func TestAgentProcessCrashRecovery(t *testing.T) {
	for _, mode := range []string{"claim", "during", "before_commit", "after_commit"} {
		t.Run(mode, func(t *testing.T) {
			f := auto(t)
			source := "a,b=map(int,input().split());print(a+b)"
			want := "AC"
			if mode == "during" {
				source = "import time;time.sleep(5)\n" + source
				want = "TLE"
			}
			j := f.submit(t, "python3", source)
			data, _ := json.Marshal(j)
			ctx, cancel := context.WithTimeout(f.ctx, 40*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentCrashHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "JUDGEGRID_AGENT_CRASH="+mode, "AGENT_CRASH_SCHEMA="+f.db.Config().ConnConfig.RuntimeParams["search_path"], "AGENT_CRASH_BUNDLES="+f.dir, "AGENT_CRASH_ROOT="+f.root, "AGENT_CRASH_JOB="+string(data), "AGENT_CRASH_STREAM="+f.s.Stream)
			pipe, e := cmd.StdoutPipe()
			if e != nil {
				t.Fatal(e)
			}
			cmd.Stderr = os.Stderr
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			ready := make(chan string, 1)
			go func() {
				scan := bufio.NewScanner(pipe)
				for scan.Scan() {
					if strings.HasPrefix(scan.Text(), "AGENT_BOUNDARY ") {
						ready <- strings.TrimPrefix(scan.Text(), "AGENT_BOUNDARY ")
						return
					}
				}
				ready <- "missing"
			}()
			var artifact string
			select {
			case artifact = <-ready:
				if artifact == "missing" {
					cmd.Process.Kill()
					cmd.Wait()
					t.Fatal("boundary missing")
				}
			case <-ctx.Done():
				cmd.Wait()
				t.Fatal("boundary timeout")
			}
			if e = cmd.Process.Kill(); e != nil {
				t.Fatal(e)
			}
			cmd.Wait()
			if artifact != "" {
				waitAuto(t, 8*time.Second, func() bool {
					out, _ := exec.Command("docker", "ps", "--filter", "name="+artifact, "--format", "{{.Names}}").Output()
					return strings.TrimSpace(string(out)) == ""
				})
				out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name="+artifact).Output()
				for _, id := range strings.Fields(string(out)) {
					if e = exec.Command("docker", "rm", "--force", id).Run(); e != nil {
						t.Fatal(e)
					}
				}
				if e = exec.Command("docker", "volume", "rm", artifact+"-work").Run(); e != nil {
					t.Fatal(e)
				}
			}
			before := f.engine.Counts()
			startAuto(t, f.handler(t, f.repo, f.engine), f.s)
			f.finished(t, j, want)
			waitAuto(t, 8*time.Second, f.delivered)
			var generation int
			if e = f.db.QueryRow(f.ctx, `SELECT generation FROM execution_ownership WHERE submission_id=$1`, j.SubmissionID).Scan(&generation); e != nil {
				t.Fatal(e)
			}
			if mode == "after_commit" {
				if generation != 1 || f.engine.Counts() != before {
					t.Fatal("L committed work reexecuted")
				}
			} else if generation != 2 {
				t.Fatal("I/J/K takeover missing", generation)
			}
			t.Logf("actual process crash at %s recovered with generation %d", mode, generation)
		})
	}
}

func TestAgentRepeatedProcessCrashBudget(t *testing.T) {
	f := auto(t)
	j := f.submit(t, "python3", "a,b=map(int,input().split());print(a+b)")
	data, _ := json.Marshal(j)
	for gen := 1; gen <= 3; gen++ {
		ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentCrashHelper$", "-test.v")
		cmd.Env = append(os.Environ(), "JUDGEGRID_AGENT_CRASH=claim", "AGENT_CRASH_SCHEMA="+f.db.Config().ConnConfig.RuntimeParams["search_path"], "AGENT_CRASH_BUNDLES="+f.dir, "AGENT_CRASH_ROOT="+f.root, "AGENT_CRASH_JOB="+string(data), "AGENT_CRASH_STREAM="+f.s.Stream)
		pipe, e := cmd.StdoutPipe()
		if e != nil {
			t.Fatal(e)
		}
		cmd.Stderr = os.Stderr
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		ready := make(chan bool, 1)
		go func() {
			scan := bufio.NewScanner(pipe)
			for scan.Scan() {
				if strings.HasPrefix(scan.Text(), "AGENT_BOUNDARY ") {
					ready <- true
					return
				}
			}
			ready <- false
		}()
		select {
		case ok := <-ready:
			if !ok {
				cmd.Process.Kill()
				cmd.Wait()
				cancel()
				t.Fatal("repeated crash boundary missing")
			}
		case <-ctx.Done():
			cmd.Wait()
			cancel()
			t.Fatal("crash timeout")
		}
		cmd.Process.Kill()
		cmd.Wait()
		cancel()
		time.Sleep(3200 * time.Millisecond)
	}
	startAuto(t, f.handler(t, f.repo, f.engine), f.s)
	f.finished(t, j, "FAILED")
	waitAuto(t, 8*time.Second, f.delivered)
	var generation int
	var state, reason string
	if e := f.db.QueryRow(f.ctx, `SELECT o.generation,o.state,s.failure_reason FROM execution_ownership o JOIN submissions s ON s.id=o.submission_id WHERE s.id=$1`, j.SubmissionID).Scan(&generation, &state, &reason); e != nil || generation != 3 || state != "EXHAUSTED" || reason != "attempt_limit" {
		t.Fatal(generation, state, reason, e)
	}
	if f.engine.Counts().RuntimeStarts != 0 {
		t.Fatal("O exhausted crashes started another execution")
	}
	t.Log("three real worker-process deaths exhausted the same submission's immutable attempt budget")
}

// TCP gate used for real PostgreSQL/Redis connectivity loss, without stopping shared services.
type autoGate struct {
	ln          net.Listener
	target      string
	mu          sync.Mutex
	online      bool
	connections map[net.Conn]bool
	wg          sync.WaitGroup
}

func gateAuto(t *testing.T, target string) *autoGate {
	t.Helper()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	g := &autoGate{ln: ln, target: target, online: true, connections: map[net.Conn]bool{}}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			g.mu.Lock()
			if !g.online {
				g.mu.Unlock()
				c.Close()
				continue
			}
			g.connections[c] = true
			g.mu.Unlock()
			g.wg.Add(1)
			go func() {
				defer g.wg.Done()
				defer c.Close()
				defer func() { g.mu.Lock(); delete(g.connections, c); g.mu.Unlock() }()
				up, e := net.DialTimeout("tcp", target, time.Second)
				if e != nil {
					return
				}
				defer up.Close()
				copied := make(chan struct{})
				go func() { io.Copy(up, c); up.Close(); close(copied) }()
				io.Copy(c, up)
				c.Close()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); g.set(false); g.wg.Wait() })
	return g
}
func (g *autoGate) set(online bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.online = online
	if !online {
		for c := range g.connections {
			c.Close()
		}
	}
}
func TestAgentRedisOutageAfterClaim(t *testing.T) {
	f := auto(t)
	gate := gateAuto(t, os.Getenv("TEST_REDIS_ADDR"))
	client := queue.Open(gate.ln.Addr().String(), "")
	defer client.Close()
	j := f.submit(t, "python3", "import time;time.sleep(.3)\na,b=map(int,input().split());print(a+b)")
	startAuto(t, f.handler(t, f.repo, f.engine), client.Streams(f.s.Stream, f.s.Group))
	waitAuto(t, 10*time.Second, func() bool {
		var n int
		_ = f.db.QueryRow(f.ctx, `SELECT count(*) FROM execution_ownership WHERE submission_id=$1 AND state='ACTIVE'`, j.SubmissionID).Scan(&n)
		return n == 1
	})
	gate.set(false)
	f.finished(t, j, "AC")
	counts := f.engine.Counts()
	gate.set(true)
	waitAuto(t, 10*time.Second, f.delivered)
	if f.engine.Counts() != counts {
		t.Fatal("N Redis failure reexecuted work")
	}
}
func TestAgentDatabaseHeartbeatOutage(t *testing.T) {
	f := auto(t)
	cfg := f.db.Config().Copy()
	gate := gateAuto(t, net.JoinHostPort(cfg.ConnConfig.Host, fmt.Sprint(cfg.ConnConfig.Port)))
	host, port, _ := net.SplitHostPort(gate.ln.Addr().String())
	cfg.ConnConfig.Host = host
	fmt.Sscan(port, &cfg.ConnConfig.Port)
	proxy, e := pgxpool.NewWithConfig(f.ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer proxy.Close()
	j := f.submit(t, "python3", "import time;time.sleep(.3)\na,b=map(int,input().split());print(a+b)")
	h := f.handler(t, storage.NewRepository(proxy), f.engine)
	done := make(chan error, 1)
	go func() { _, e := h.Handle(f.ctx, j, "uncertain"); done <- e }()
	waitAuto(t, 10*time.Second, func() bool { return f.engine.Counts().RuntimeStarts > 0 })
	gate.set(false)
	select {
	case e = <-done:
		if e == nil {
			t.Fatal("M outage reported success")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("M uncertainty did not cancel")
	}
	var results int
	if e = f.db.QueryRow(f.ctx, `SELECT count(*) FROM evaluation_test_results`).Scan(&results); e != nil || results != 0 {
		t.Fatal("M wrote during uncertainty", results, e)
	}
	gate.set(true)
	startAuto(t, f.handler(t, f.repo, f.engine), f.s)
	f.finished(t, j, "AC")
	var gen int
	if e = f.db.QueryRow(f.ctx, `SELECT generation FROM execution_ownership WHERE submission_id=$1`, j.SubmissionID).Scan(&gen); e != nil || gen != 2 {
		t.Fatal("M expired generation resurrected", gen, e)
	}
}

func TestAgentGracefulAndExpiredExecutionDrain(t *testing.T) {
	for _, finish := range []bool{true, false} {
		t.Run(fmt.Sprint(finish), func(t *testing.T) {
			f := auto(t)
			j := f.submit(t, "python3", "import time;time.sleep(.3)\na,b=map(int,input().split());print(a+b)")
			o := autoOptions()
			o.Drain = 20 * time.Second
			if !finish {
				o.Drain = time.Second
			}
			h, e := agent.New(f.repo, f.b, f.engine, "agent-user", "drain-owner", o)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(f.ctx)
			done := make(chan error, 1)
			go func() { done <- h.Run(ctx, f.s) }()
			waitAuto(t, 10*time.Second, func() bool { return f.engine.Counts().RuntimeStarts > 0 })
			cancel()
			select {
			case e = <-done:
			case <-time.After(35 * time.Second):
				t.Fatal("unbounded execution drain")
			}
			v, e := f.repo.Get(f.ctx, "agent-user", j.SubmissionID)
			if e != nil {
				t.Fatal(e)
			}
			if finish {
				if v.Verdict == nil || *v.Verdict != "AC" {
					t.Fatal("P graceful drain lost verdict", v)
				}
			} else {
				if v.Verdict != nil || v.Status == domain.Completed {
					t.Fatal("P expired drain fabricated verdict", v)
				}
				startAuto(t, f.handler(t, f.repo, f.engine), f.s)
				f.finished(t, j, "AC")
			}
		})
	}
}
