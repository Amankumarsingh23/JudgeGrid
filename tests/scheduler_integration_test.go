//go:build integration

package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"github.com/Amankumarsingh23/JudgeGrid/internal/scheduling"
	"github.com/Amankumarsingh23/JudgeGrid/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func schedulerSetup(t *testing.T) (*pgxpool.Pool, *storage.Repository, scheduling.Policy) {
	t.Helper()
	db := database(t, true)
	r := storage.NewRepository(db)
	local, _ := bundles.Open(t.TempDir())
	if e := storage.SeedSuites(context.Background(), db, local); e != nil {
		t.Fatal(e)
	}
	p := scheduling.Default()
	p.Enabled = true
	p.GlobalActive = 2
	p.PrincipalActive = 1
	p.Principals = []string{"principal-a", "principal-b"}
	if e := r.ConfigureScheduler(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	return db, r, p
}
func publishScheduled(t *testing.T, r *storage.Repository) {
	t.Helper()
	stream, _ := streams(t)
	o := relayOptions()
	o.Batch = 16
	for range 8 {
		if e := (outbox.Relay{Store: r, Publisher: stream, Options: o}).Step(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
}
func TestSchedulerAgentProcessHelper(t *testing.T) {
	if os.Getenv("S10_SCHEDULER_CHILD") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg, e := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = os.Getenv("S10_SCHEMA")
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	r := storage.NewRepository(db)
	for ctx.Err() == nil {
		c, e := r.NextScheduled(ctx, []string{"principal-a", "principal-b"})
		if e != nil {
			t.Fatal(e)
		}
		if c != nil {
			w, e := r.ClaimExecution(ctx, c.Principal, os.Getenv("S10_OWNER"), c.Job, 30*time.Second, 3)
			if e == nil {
				data, _ := json.Marshal(map[string]any{"principal": c.Principal, "submission": w.Submission.ID, "run": w.RunID})
				fmt.Println("S10_CLAIM " + string(data))
				bufio.NewReader(os.Stdin).ReadString('\n')
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no bounded scheduler claim")
}
func TestFairSchedulerIndependentProcessesAndSkew(t *testing.T) {
	ctx := context.Background()
	db, r, _ := schedulerSetup(t)
	req := request(t, db)
	first := ""
	for i := range 100 {
		s := create(t, r, "principal-a", fmt.Sprint("flood-", i), req)
		if first == "" {
			first = s.ID
		}
	}
	b := create(t, r, "principal-b", "single", req)
	publishScheduled(t, r)
	type child struct {
		cmd   *exec.Cmd
		stdin io.WriteCloser
	}
	children := []child{}
	claims := make(chan map[string]string, 2)
	for _, owner := range []string{"independent-agent-1", "independent-agent-2"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSchedulerAgentProcessHelper$", "-test.v")
		cmd.Env = append(os.Environ(), "S10_SCHEDULER_CHILD=1", "S10_SCHEMA="+db.Config().ConnConfig.RuntimeParams["search_path"], "S10_OWNER="+owner)
		pipe, e := cmd.StdoutPipe()
		if e != nil {
			t.Fatal(e)
		}
		input, e := cmd.StdinPipe()
		if e != nil {
			t.Fatal(e)
		}
		cmd.Stderr = os.Stderr
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		children = append(children, child{cmd, input})
		go func() {
			scan := bufio.NewScanner(pipe)
			for scan.Scan() {
				if len(scan.Text()) > 10 && scan.Text()[:10] == "S10_CLAIM " {
					var c map[string]string
					json.Unmarshal([]byte(scan.Text()[10:]), &c)
					claims <- c
					return
				}
			}
			claims <- nil
		}()
	}
	defer func() {
		for _, c := range children {
			io.WriteString(c.stdin, "stop\n")
			c.stdin.Close()
			c.cmd.Wait()
		}
	}()
	seen := map[string]map[string]string{}
	for range 2 {
		select {
		case c := <-claims:
			if c == nil {
				t.Fatal("independent agent exited without claim")
			}
			seen[c["principal"]] = c
		case <-time.After(20 * time.Second):
			for _, c := range children {
				c.cmd.Process.Kill()
			}
			t.Fatal("scheduler starvation")
		}
	}
	if len(seen) != 2 || seen["principal-a"]["submission"] != first || seen["principal-b"]["submission"] != b.ID {
		t.Fatal("FIFO/fair turn", seen)
	}
	var total, peak int
	if e := db.QueryRow(ctx, `SELECT count(*) FROM sandbox_authority WHERE state='ACTIVE' AND lease_expires_at>clock_timestamp()`).Scan(&total); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(ctx, `SELECT max(n) FROM(SELECT count(*) n FROM sandbox_authority a JOIN submissions s ON s.id=a.submission_id WHERE a.state='ACTIVE' AND a.lease_expires_at>clock_timestamp() GROUP BY s.principal_id)q`).Scan(&peak); e != nil {
		t.Fatal(e)
	}
	if total != 2 || peak != 1 {
		t.Fatal("shared capacity exceeded", total, peak)
	}
	if next, e := r.NextScheduled(ctx, []string{"principal-a", "principal-b"}); e != nil || next != nil {
		t.Fatal("full shared pool offered another job", next, e)
	}
	queued, e := r.RecoveryJobs(ctx, "principal-a", 1)
	if e != nil || len(queued) != 1 {
		t.Fatal("missing extra queued work", e)
	}
	var attempts sync.WaitGroup
	violations := make(chan bool, 20)
	for i := range 20 {
		attempts.Add(1)
		go func(i int) {
			defer attempts.Done()
			_, e := r.ClaimExecution(ctx, "principal-a", fmt.Sprint("overflow-", i), queued[0], 30*time.Second, 3)
			violations <- e == nil
		}(i)
	}
	attempts.Wait()
	close(violations)
	for admitted := range violations {
		if admitted {
			t.Fatal("concurrent extra work bypassed principal/global caps")
		}
	}
	var turn int
	if e := db.QueryRow(ctx, `SELECT last_turn FROM scheduler_principals WHERE principal='principal-b'`).Scan(&turn); e != nil || turn > 2 {
		t.Fatal("B admission bound", turn, e)
	}
	t.Logf("A=100 pending, B=1; independent process claims, B admitted by turn %d; global active peak=%d, principal peak=%d", turn, total, peak)
}
func TestSchedulerRecoveryDuplicateAndCapacityRelease(t *testing.T) {
	ctx := context.Background()
	db, r, p := schedulerSetup(t)
	p.GlobalActive = 1
	if e := r.ConfigureScheduler(ctx, p); e != nil {
		t.Fatal(e)
	}
	req := request(t, db)
	a := create(t, r, "principal-a", "a", req)
	create(t, r, "principal-a", "a2", req)
	b := create(t, r, "principal-b", "b", req)
	publishScheduled(t, r)
	next, e := r.NextScheduled(ctx, p.Principals)
	if e != nil || next == nil || next.Job.SubmissionID != a.ID {
		t.Fatal(next, e)
	}
	old, e := r.ClaimExecution(ctx, "principal-a", "old", next.Job, time.Second, 3)
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, e = r.ClaimExecution(ctx, "principal-a", "bypass", next.Job, 30*time.Second, 3); e == nil {
		t.Fatal("expired work bypassed B's fair turn")
	}
	next, e = r.NextScheduled(ctx, p.Principals)
	if e != nil || next == nil || next.Job.SubmissionID != b.ID {
		t.Fatal("B starved by recovery", next, e)
	}
	var wg sync.WaitGroup
	wins := make(chan string, 20)
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w, e := r.ClaimExecution(ctx, "principal-b", fmt.Sprint("agent-", i), next.Job, 30*time.Second, 3)
			if e == nil {
				wins <- w.RunID
			}
		}(i)
	}
	wg.Wait()
	close(wins)
	count := 0
	for range wins {
		count++
	}
	if count != 1 {
		t.Fatal("multiple valid scheduled owners", count)
	}
	var owner string
	if e = db.QueryRow(ctx, `SELECT owner_id FROM execution_ownership WHERE submission_id=$1`, b.ID).Scan(&owner); e != nil {
		t.Fatal(e)
	}
	// Recover the exact granted identity from durable state for a database-only result fixture.
	var run string
	db.QueryRow(ctx, `SELECT run_id::text FROM execution_ownership WHERE submission_id=$1`, b.ID).Scan(&run)
	wb := old
	wb.Submission.ID = b.ID
	wb.Submission.PrincipalID = "principal-b"
	wb.Owner = owner
	wb.RunID = run
	wb.Generation = 1
	if _, e = r.CommitEvaluation(ctx, wb, acEvidence(wb)); e != nil {
		t.Fatal(e)
	}
	next, e = r.NextScheduled(ctx, p.Principals)
	if e != nil || next == nil || next.Job.SubmissionID != a.ID {
		t.Fatal("FIFO recovery", next, e)
	}
	recovered, e := r.ClaimExecution(ctx, "principal-a", "replacement", next.Job, 30*time.Second, 3)
	if e != nil || recovered.Generation != 2 {
		t.Fatal(recovered, e)
	}
	if _, e = r.CommitEvaluation(ctx, old, acEvidence(old)); e == nil {
		t.Fatal("stale result crossed scheduler fence")
	}
	if _, e = r.CommitEvaluation(ctx, recovered, acEvidence(recovered)); e != nil {
		t.Fatal(e)
	}
	if e = r.ValidateNotification(ctx, p.Principals, next.Job); e != nil {
		t.Fatal(e)
	}
	again, e := r.ClaimExecution(ctx, "principal-a", "duplicate", next.Job, 30*time.Second, 3)
	if e != nil || again.Existing == nil {
		t.Fatal("terminal notification created extra attempt", again, e)
	}
}
