//go:build agentintegration

package judge_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/reconcile"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func killOrphanController(t *testing.T, f *autoFixture, j queue.Job, mode string) string {
	t.Helper()
	data, _ := json.Marshal(j)
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
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
	var id string
	select {
	case id = <-ready:
	case <-ctx.Done():
		cmd.Wait()
		t.Fatal("orphan boundary timed out")
	}
	cmd.Process.Kill()
	cmd.Wait()
	if id == "" || id == "missing" {
		t.Fatal("orphan identity missing")
	}
	return id
}
func ownedResources(t *testing.T, id string) []execution.Resource {
	t.Helper()
	rows, e := execution.ListResources(context.Background(), 128)
	if e != nil {
		t.Fatal(e)
	}
	out := []execution.Resource{}
	for _, r := range rows {
		if r.Labels["judgegrid.execution"] == id {
			out = append(out, r)
		}
	}
	return out
}
func expireWait(t *testing.T, f *autoFixture, j queue.Job) {
	t.Helper()
	waitAuto(t, 5*time.Second, func() bool {
		var expired bool
		e := f.db.QueryRow(f.ctx, `SELECT lease_expires_at<=clock_timestamp() FROM execution_ownership WHERE submission_id=$1`, j.SubmissionID).Scan(&expired)
		return e == nil && expired
	})
}

type interruptedDocker struct{ calls int }

func (d *interruptedDocker) List(ctx context.Context, max int) ([]execution.Resource, error) {
	return execution.ListResources(ctx, max)
}
func (d *interruptedDocker) Delete(ctx context.Context, r execution.Resource) error {
	if e := execution.DeleteResource(ctx, r); e != nil {
		return e
	}
	d.calls++
	if d.calls == 1 {
		return fmt.Errorf("injected reconciliation process interruption")
	}
	return nil
}
func TestOrphanCrashReconciliation(t *testing.T) {
	for _, scenario := range []string{"A-created", "B-running", "C-watchdog", "D-volume-only", "I-concurrent", "J-interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			f := auto(t)
			mode := "container_created"
			source := "print(5)"
			if scenario == "B-running" || scenario == "C-watchdog" {
				mode = "during"
				source = "import time;time.sleep(20);print(5)"
				var version int64
				if e := f.db.QueryRow(f.ctx, `INSERT INTO problem_versions(problem_id,version_number,statement,time_limit_ms,memory_limit_kb) SELECT problem_id,2,statement,10000,memory_limit_kb FROM problem_versions WHERE id=(SELECT current_version_id FROM problems WHERE id=$1) RETURNING id`, f.problem).Scan(&version); e != nil {
					t.Fatal(e)
				}
				b, _ := f.b.Load(func() string {
					var hash string
					f.db.QueryRow(f.ctx, `SELECT bundle_sha FROM problem_test_suites WHERE problem_version_id=(SELECT current_version_id FROM problems WHERE id=$1)`, f.problem).Scan(&hash)
					return hash
				}())
				ref, e := b.Reference()
				if e != nil {
					t.Fatal(e)
				}
				if e = storage.BindSuite(f.ctx, f.db, version, ref); e != nil {
					t.Fatal(e)
				}
				if _, e = f.db.Exec(f.ctx, `UPDATE problems SET current_version_id=$2 WHERE id=$1`, f.problem, version); e != nil {
					t.Fatal(e)
				}
			}
			j := f.submit(t, "python3", source)
			id := killOrphanController(t, f, j, mode)
			rows := ownedResources(t, id)
			if len(rows) < 2 {
				t.Fatal("crash did not leave exact owned resources", rows)
			}
			report, e := reconcile.Run(f.ctx, f.repo, reconcile.Docker{}, false, 128)
			if e != nil || len(report) == 0 {
				t.Fatal(report, e)
			}
			if len(ownedResources(t, id)) != len(rows) {
				t.Fatal("dry-run changed resources")
			}
			if scenario == "D-volume-only" {
				for _, r := range rows {
					if r.Kind == "container" {
						if e = execution.DeleteResource(f.ctx, r); e != nil {
							t.Fatal(e)
						}
					}
				}
				if r := ownedResources(t, id); len(r) != 1 || r[0].Kind != "volume" {
					t.Fatal(r)
				}
			}
			if scenario == "C-watchdog" {
				waitAuto(t, 13*time.Second, func() bool {
					for _, r := range ownedResources(t, id) {
						if r.Running {
							return false
						}
					}
					return true
				})
			}
			expireWait(t, f, j)
			if scenario == "B-running" {
				running := false
				for _, r := range ownedResources(t, id) {
					running = running || r.Running
				}
				if !running {
					t.Fatal("running orphan fixture ended before reconciliation")
				}
			}
			if scenario == "J-interrupted" {
				fault := &interruptedDocker{}
				if _, e = reconcile.Run(f.ctx, f.repo, fault, true, 128); e == nil {
					t.Fatal("cleanup interruption not injected")
				}
				if len(ownedResources(t, id)) == 0 {
					t.Fatal("interruption had no remaining resources")
				}
			}
			start := time.Now()
			if scenario == "I-concurrent" {
				var wg sync.WaitGroup
				errs := make(chan error, 2)
				for range 2 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_, e := reconcile.Run(f.ctx, f.repo, reconcile.Docker{}, true, 128)
						errs <- e
					}()
				}
				wg.Wait()
				close(errs)
				for e := range errs {
					if e != nil {
						t.Fatal(e)
					}
				}
			} else {
				report, e = reconcile.Run(f.ctx, f.repo, reconcile.Docker{}, true, 128)
				if e != nil {
					t.Fatal(report, e)
				}
			}
			if len(ownedResources(t, id)) != 0 {
				t.Fatal("orphan not reaped")
			}
			if _, e = reconcile.Run(f.ctx, f.repo, reconcile.Docker{}, true, 128); e != nil {
				t.Fatal("non-idempotent repeat", e)
			}
			t.Logf("%s real controller death reconciled; bounded scan/cleanup elapsed=%s", scenario, time.Since(start))
		})
	}
}
func preparedOwned(t *testing.T, f *autoFixture, owner string, lease time.Duration) (execution.Prepared, execution.Provenance, queue.Job) {
	t.Helper()
	j := f.submit(t, "python3", "print(5)")
	w, e := f.repo.ClaimExecution(f.ctx, "agent-user", owner, j, lease, 3)
	if e != nil {
		t.Fatal(e)
	}
	r, _ := execution.Fixture("python-hello")
	r.Source = []byte(w.Submission.SourceCode)
	ctx := execution.WithIdentity(execution.WithRegistry(f.ctx, f.repo), w.RunID, w.Submission.ID, w.Owner, w.Generation)
	p, _, e := f.engine.Prepare(ctx, r)
	if e != nil || p == nil {
		t.Fatal(e)
	}
	rows := ownedResources(t, p.Summary().ExecutionID)
	if len(rows) < 2 {
		t.Fatal(rows)
	}
	prov, _, e := execution.ParseLabels(rows[0].Labels)
	if e != nil {
		t.Fatal(e)
	}
	return p, prov, j
}
func TestOrphanActiveAndStaleGeneration(t *testing.T) {
	f := auto(t)
	p, prov, j := preparedOwned(t, f, "old", 5*time.Second)
	defer p.Close()
	expireWait(t, f, j)
	w, e := f.repo.ClaimExecution(f.ctx, "agent-user", "new", j, 15*time.Second, 3)
	if e != nil {
		t.Fatal(e)
	}
	r, _ := execution.Fixture("python-hello")
	r.Source = []byte(w.Submission.SourceCode)
	ctx := execution.WithIdentity(execution.WithRegistry(f.ctx, f.repo), w.RunID, w.Submission.ID, w.Owner, w.Generation)
	newP, _, e := f.engine.Prepare(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	defer newP.Close()
	id := newP.Summary().ExecutionID
	report, e := reconcile.Run(f.ctx, f.repo, reconcile.Docker{}, true, 128)
	if e != nil {
		t.Fatal(report, e)
	}
	if len(ownedResources(t, prov.ExecutionID)) != 0 || len(ownedResources(t, id)) < 2 {
		t.Fatal("E/F wrong-generation cleanup")
	}
	phase, e := newP.Run(f.ctx, nil, r.RunLimits)
	if e != nil || phase.Outcome != execution.Exited || strings.TrimSpace(phase.Stdout) != "5" {
		t.Fatal("current execution affected", phase, e)
	}
	if e = f.repo.RenewExecution(f.ctx, w, 15*time.Second); e != nil {
		t.Fatal("cleanup blocked active renewal", e)
	}
	t.Log("E/F stale resources removed; current generation executed and renewed")
}
func TestOrphanDatabaseOutageAndChangedIdentity(t *testing.T) {
	for _, scenario := range []string{"G-outage", "H-reused-volume"} {
		t.Run(scenario, func(t *testing.T) {
			f := auto(t)
			p, prov, j := preparedOwned(t, f, "orphan", 5*time.Second)
			rows := ownedResources(t, prov.ExecutionID)
			expireWait(t, f, j)
			if scenario == "G-outage" {
				cfg := f.db.Config().Copy()
				f.db.Close()
				if _, e := reconcile.Run(f.ctx, f.repo, reconcile.Docker{}, true, 128); e == nil {
					t.Fatal("database outage did not fail closed")
				}
				if len(ownedResources(t, prov.ExecutionID)) != len(rows) {
					t.Fatal("outage deleted resources")
				}
				db, e := pgxpool.NewWithConfig(f.ctx, cfg)
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
				if _, e = reconcile.Run(f.ctx, storage.NewRepository(db), reconcile.Docker{}, true, 128); e != nil {
					t.Fatal(e)
				}
				_ = p.Close()
			} else {
				var volume execution.Resource
				for _, r := range rows {
					if e := execution.DeleteResource(f.ctx, r); e != nil {
						t.Fatal(e)
					}
					if r.Kind == "volume" {
						volume = r
					}
				}
				cmd := exec.Command("docker", "volume", "create", "--label", "judgegrid.namespace="+execution.ResourceNamespace, "--label", "judgegrid.execution="+prov.ExecutionID, volume.Name)
				if out, e := cmd.CombinedOutput(); e != nil {
					t.Fatal(string(out), e)
				}
				defer exec.Command("docker", "volume", "rm", volume.Name).Run()
				if e := execution.DeleteResource(f.ctx, volume); e == nil {
					t.Fatal("reused mismatched volume accepted")
				}
				if _, e := reconcile.Run(f.ctx, f.repo, reconcile.Docker{}, true, 128); e != nil {
					t.Fatal(e)
				}
				if _, e := execution.InspectResource(f.ctx, "volume", volume.Name); e != nil {
					t.Fatal("foreign reused volume deleted", e)
				}
				_ = p.Close()
			}
		})
	}
}
