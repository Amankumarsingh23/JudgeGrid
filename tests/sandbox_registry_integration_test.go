//go:build integration

package tests

import (
	"context"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func registryIdentity(id int, run, sub, owner string, generation int) execution.Provenance {
	return execution.Provenance{ExecutionID: "judgegrid-sandbox-" + strings.Repeat(string(rune('a'+id)), 26), ControllerID: strings.Repeat("b", 26), RunID: run, SubmissionID: sub, OwnerHash: execution.OwnerHash(owner), Generation: generation, Created: time.Now().UTC().Format(time.RFC3339Nano)}
}
func TestSandboxRegistryAuthorityAndConcurrency(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo, j := executionJob(t, db, "cpp17")
	w, e := repo.ClaimExecution(ctx, "lease-user", "registry-owner", j, time.Second, 3)
	if e != nil {
		t.Fatal(e)
	}
	p := registryIdentity(0, w.RunID, w.Submission.ID, w.Owner, w.Generation)
	if e = repo.RegisterSandbox(ctx, p); e != nil {
		t.Fatal(e)
	}
	called := false
	decision, e := repo.ReconcileSandbox(ctx, p, true, func() error { called = true; return nil })
	if e != nil || decision != "active" || called {
		t.Fatal("active deleted", decision, e)
	}
	wrong := p
	wrong.OwnerHash = strings.Repeat("f", 64)
	decision, e = repo.ReconcileSandbox(ctx, wrong, true, func() error { called = true; return nil })
	if e != nil || decision != "mismatch" || called {
		t.Fatal(decision, e)
	}
	time.Sleep(1100 * time.Millisecond)
	var count atomic.Int32
	var wg sync.WaitGroup
	fail := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := repo.ReconcileSandbox(ctx, p, true, func() error { count.Add(1); return nil })
			fail <- e
		}()
	}
	wg.Wait()
	close(fail)
	for e := range fail {
		if e != nil {
			t.Fatal(e)
		}
	}
	if count.Load() != 1 {
		t.Fatal("duplicate cleanup effect", count.Load())
	}
	if e = repo.ProtectSandbox(ctx, p.ExecutionID, func() error { called = true; return nil }); e == nil || called {
		t.Fatal("reaped execution created resources", e)
	}
}
func TestSandboxRegistryInterruptedCleanupAndOutage(t *testing.T) {
	ctx := context.Background()
	db := database(t, true)
	repo, j := executionJob(t, db, "cpp17")
	w, e := repo.ClaimExecution(ctx, "lease-user", "registry-owner", j, time.Second, 3)
	if e != nil {
		t.Fatal(e)
	}
	p := registryIdentity(1, w.RunID, w.Submission.ID, w.Owner, w.Generation)
	if e = repo.RegisterSandbox(ctx, p); e != nil {
		t.Fatal(e)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, e = repo.ReconcileSandbox(ctx, p, true, func() error { return fmt.Errorf("injected cleanup interruption") }); e == nil {
		t.Fatal("interruption ignored")
	}
	decision, e := repo.ReconcileSandbox(ctx, p, false, nil)
	if e != nil || decision != "eligible" {
		t.Fatal(decision, e)
	}
	db.Close()
	called := false
	if _, e = repo.ReconcileSandbox(ctx, p, true, func() error { called = true; return nil }); e == nil || called {
		t.Fatal("outage deleted", e)
	}
}
