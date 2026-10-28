package reconcile

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"strings"
	"testing"
	"time"
)

type authority struct {
	decision string
	err      error
	calls    int
}

func (a *authority) ReconcileSandbox(_ context.Context, _ execution.Provenance, apply bool, action func() error) (string, error) {
	a.calls++
	if a.err != nil {
		return "unavailable", a.err
	}
	if apply && a.decision == "eligible" {
		return "reaped", action()
	}
	return a.decision, nil
}

type runtime struct {
	resources []execution.Resource
	deleted   int
}

func (r *runtime) List(context.Context, int) ([]execution.Resource, error) { return r.resources, nil }
func (r *runtime) Delete(context.Context, execution.Resource) error        { r.deleted++; return nil }
func resources() []execution.Resource {
	p := execution.Provenance{ExecutionID: "judgegrid-sandbox-" + strings.Repeat("a", 26), ControllerID: strings.Repeat("b", 26), RunID: "550e8400-e29b-41d4-a716-446655440000", SubmissionID: "550e8400-e29b-41d4-a716-446655440001", OwnerHash: strings.Repeat("c", 64), Generation: 1, Created: time.Now().UTC().Format(time.RFC3339Nano)}
	return []execution.Resource{{Kind: "volume", ID: p.ExecutionID + "-work", Name: p.ExecutionID + "-work", Labels: p.Labels("artifact")}, {Kind: "container", ID: strings.Repeat("a", 64), Name: p.ExecutionID + "-run", Labels: p.Labels("run")}}
}
func TestDryRunAndActivePreservation(t *testing.T) {
	for _, decision := range []string{"active", "unknown", "mismatch", "eligible"} {
		r := &runtime{resources: resources()}
		db := &authority{decision: decision}
		report, e := Run(context.Background(), db, r, false, 16)
		if e != nil || len(report) != 1 || report[0].Decision != decision || r.deleted != 0 {
			t.Fatal(report, e)
		}
	}
	r := &runtime{resources: resources()}
	_, e := Run(context.Background(), &authority{decision: "active"}, r, true, 16)
	if e != nil || r.deleted != 0 {
		t.Fatal(e)
	}
}
func TestAmbiguityAndAuthorityOutage(t *testing.T) {
	r := &runtime{resources: resources()}
	r.resources[1].Labels["judgegrid.generation"] = "2"
	db := &authority{decision: "eligible"}
	report, e := Run(context.Background(), db, r, true, 16)
	if e != nil || report[0].Decision != "mismatch" || db.calls != 0 || r.deleted != 0 {
		t.Fatal(report, e)
	}
	r = &runtime{resources: resources()}
	_, e = Run(context.Background(), &authority{err: errors.New("database unavailable")}, r, true, 16)
	if e == nil || r.deleted != 0 {
		t.Fatal("outage deletion", e)
	}
}
func TestEligibleRemovalPlan(t *testing.T) {
	r := &runtime{resources: resources()}
	report, e := Run(context.Background(), &authority{decision: "eligible"}, r, true, 16)
	if e != nil || r.deleted != 2 || report[0].Decision != "reaped" {
		t.Fatal(report, e, r.deleted)
	}
}
