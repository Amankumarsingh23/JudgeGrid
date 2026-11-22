//go:build integration

package research

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/contextpack"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"github.com/Amankumarsingh23/JudgeGrid/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

type scripted struct{ first Decision }

type unavailableSource struct{ retrieval.Reader }

func (unavailableSource) ReadChunk(context.Context, string, string, string) (analysis.VerifiedChunk, error) {
	return analysis.VerifiedChunk{}, ingest.ErrStorage
}

func (scripted) Identity() string { return "research-test-script-v1" }
func (p scripted) Next(_ context.Context, in PlannerInput) ([]byte, error) {
	if len(in.History) == 0 {
		return json.Marshal(p.first)
	}
	return json.Marshal(Decision{Version: DecisionVersion, Kind: "FINISH", Outcome: "VERIFIED_REFERENCES"})
}

func TestS17RetainedResearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	url := os.Getenv("S17_RETAINED_DATABASE_URL")
	h := map[string]string{
		"owner": "aman-s12", "retrieval": "754720b7e2731b6b4321754b75a1f99b5cfd694b459ebca858781068af1dd338",
		"bucket": os.Getenv("S13_RETAINED_BUCKET"),
	}
	if url == "" {
		data, err := os.ReadFile(os.Getenv("S16_CI_HANDOFF_FILE"))
		if err != nil || json.Unmarshal(data, &h) != nil || !strings.HasPrefix(h["schema"], "judgegrid_retrieval_") {
			t.Fatal("explicit retained database or CI handoff required", err)
		}
		url = os.Getenv("TEST_DATABASE_URL")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if h["schema"] != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = h["schema"]
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = migrations.Check(ctx, db); err != nil {
		t.Fatal(err)
	}
	objects, err := ingest.OpenS3(os.Getenv("REPO_TEST_S3_ENDPOINT"), h["bucket"], os.Getenv("REPO_TEST_S3_ACCESS_KEY"), os.Getenv("REPO_TEST_S3_SECRET_KEY"), false)
	if err != nil {
		t.Fatal(err)
	}
	source := &analysis.Service{DB: analysis.Database{Pool: db}, Source: &ingest.Service{DB: ingest.Database{Pool: db}, Objects: objects}}
	model, err := retrieval.OpenModel(os.Getenv("REPO_EMBEDDING_PYTHON"), os.Getenv("REPO_EMBEDDING_MODEL"))
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	engine, err := contextpack.OpenEngine(os.Getenv("REPO_CONTEXT_PYTHON"), os.Getenv("REPO_CONTEXT_MODEL"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	reader := &retrieval.Service{DB: retrieval.Database{Pool: db}, Source: source, Model: model}
	svc := Service{DB: db, Planner: Fixture{}, Readers: Readers{Retrieval: reader, Analysis: source, Packs: &contextpack.Builder{Retrieval: reader, Analysis: source, Engine: engine}}}
	owner, index := h["owner"], h["retrieval"]
	_, manifest, _, err := reader.Ready(ctx, owner, index)
	if err != nil || manifest.Commit != "56ea76e421eca19066362fdecedd4e9aae04521e" || len(manifest.Documents) != 2221 {
		t.Fatalf("wrong retained scope: %d %v", len(manifest.Documents), err)
	}
	if _, err = svc.Create(ctx, "foreign", index, "lease"); err == nil {
		t.Fatal("foreign scope accepted")
	}
	for _, goal := range []string{"lease", "outbox", "cache"} {
		t.Run(goal, func(t *testing.T) {
			start := time.Now()
			r, e := svc.Create(ctx, owner, index, goal)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = svc.View(ctx, "foreign", r.ID); !errors.Is(e, ErrDenied) {
				t.Fatal("foreign journal", e)
			}
			if e = svc.Resume(ctx, owner, r.ID); e != nil {
				t.Fatal(e)
			}
			got, e := svc.View(ctx, owner, r.ID)
			if e != nil || got.State != "COMPLETED" || len(got.Steps) < 3 {
				t.Fatalf("bad result %s %v %#v", got.State, e, got.Steps)
			}
			if got.Steps[0].Decision.Tool != "search_code" || len(got.Steps[0].Observation.Hits) == 0 {
				t.Fatal("real candidates missing")
			}
			citations := 0
			for _, step := range got.Steps {
				if step.Observation == nil || step.Digest != digestStep(got, step.Ordinal, step.Decision) {
					t.Fatal("bad durable step")
				}
				citations += len(step.Observation.Citations)
				audit, _ := json.Marshal(step.Observation)
				t.Logf("goal=%s step=%d tool=%s attempts=%d observation=%s", goal, step.Ordinal, step.Decision.Tool, step.Attempts, audit)
			}
			if citations == 0 {
				t.Fatal("no verified citations")
			}
			if e = svc.Resume(ctx, owner, r.ID); !errors.Is(e, ErrDenied) {
				t.Fatal("terminal replay accepted", e)
			}
			t.Logf("goal=%s commit=%s outcome=%s elapsed=%s", goal, got.Scope.Commit, got.Outcome, time.Since(start))
		})
	}

	svc.Planner = scripted{first: Decision{Version: DecisionVersion, Kind: "REQUEST_APPROVAL", Tool: "search_code", Args: json.RawMessage(`{"query":"renewExecution"}`)}}
	r, err := svc.Create(ctx, owner, index, "approval")
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Resume(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = svc.View(ctx, owner, r.ID)
	if err != nil || r.State != "WAITING_APPROVAL" || len(r.Approvals) != 1 {
		t.Fatalf("approval not waiting %s %v", r.State, err)
	}
	a := r.Approvals[0]
	if e := svc.Decide(ctx, "foreign", r.ID, a.Ordinal, a.Digest, true); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e := svc.Decide(ctx, owner, r.ID, a.Ordinal, strings.Repeat("0", 64), true); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e := svc.Decide(ctx, owner, r.ID, a.Ordinal, a.Digest, true); e != nil {
			t.Fatal(e)
		}
	}
	if e := svc.Decide(ctx, owner, r.ID, a.Ordinal, a.Digest, false); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if err = svc.Resume(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = svc.View(ctx, owner, r.ID)
	if err != nil || r.State != "COMPLETED" || r.Outcome != "NO_EVIDENCE" || r.Steps[0].Attempts != 1 {
		t.Fatal("approved search or uncertainty did not persist", err)
	}
	for _, mode := range []string{"deny", "expire", "cancel"} {
		waiting, e := svc.Create(ctx, owner, index, mode)
		if e != nil {
			t.Fatal(e)
		}
		if e = svc.Resume(ctx, owner, waiting.ID); e != nil {
			t.Fatal(e)
		}
		waiting, e = svc.View(ctx, owner, waiting.ID)
		if e != nil || len(waiting.Approvals) != 1 {
			t.Fatal("missing approval", e)
		}
		approval := waiting.Approvals[0]
		want := "CANCELLED"
		switch mode {
		case "deny":
			e = svc.Decide(ctx, owner, waiting.ID, approval.Ordinal, approval.Digest, false)
		case "expire":
			want = "EXPIRED"
			_, e = db.Exec(ctx, `UPDATE research_approvals SET expires_at=clock_timestamp()-interval '1 second' WHERE run_id=$1`, waiting.ID)
		case "cancel":
			e = svc.Cancel(ctx, owner, waiting.ID)
		}
		if e != nil {
			t.Fatal(e)
		}
		waiting, e = svc.View(ctx, owner, waiting.ID)
		if e != nil || waiting.State != want {
			t.Fatalf("%s: %s %v", mode, waiting.State, e)
		}
		if e = svc.Resume(ctx, owner, waiting.ID); !errors.Is(e, ErrDenied) {
			t.Fatalf("resumed %s: %v", mode, e)
		}
	}

	svc.Planner = scripted{first: Decision{Version: DecisionVersion, Kind: "CALL_TOOL", Tool: "search_code", Args: json.RawMessage(`{"query":"renewExecution"}`)}}
	r, err = svc.Create(ctx, owner, index, "recovery")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	winners := make(chan claim, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := svc.acquire(ctx, owner, r.ID)
			if e == nil {
				winners <- c
			} else if !errors.Is(e, ErrDenied) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("claims won: %d", len(winners))
	}
	old := <-winners
	step := Step{Ordinal: 1, Decision: svc.Planner.(scripted).first}
	step.Digest = digestStep(r, 1, step.Decision)
	if err = svc.saveDecision(ctx, old, step); err != nil {
		t.Fatal(err)
	}
	if err = svc.reserveTool(ctx, old, step); err != nil {
		t.Fatal(err)
	}
	o, err := svc.Readers.read(ctx, owner, r.Scope, step.Decision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE research_runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := svc.acquire(ctx, owner, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if e := svc.record(ctx, old, step, o, "VERIFIED_REFERENCES"); !errors.Is(e, ErrDenied) {
		t.Fatal("stale worker wrote observation", e)
	}
	pending, err := svc.View(ctx, owner, r.ID)
	if err != nil || len(pending.Steps) != 1 || pending.Steps[0].Attempts != 1 || pending.Steps[0].Observation != nil {
		t.Fatal("missing durable attempt before takeover", err)
	}
	step = pending.Steps[0]
	if err = svc.reserveTool(ctx, replacement, step); err != nil {
		t.Fatal(err)
	}
	o, err = svc.Readers.read(ctx, owner, r.Scope, step.Decision)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.record(ctx, replacement, step, o, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE research_runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.Resume(ctx, owner, r.ID); err != nil {
		t.Fatal(err)
	}
	if e := svc.record(ctx, replacement, step, o, "VERIFIED_REFERENCES"); !errors.Is(e, ErrDenied) {
		t.Fatal("stale replacement finalized", e)
	}
	got, err := svc.View(ctx, owner, r.ID)
	if err != nil || got.Generation != old.generation+2 || len(got.Steps) != 2 || got.Steps[0].Attempts != 2 || got.State != "COMPLETED" {
		t.Fatalf("recovery failed: %s %v", got.State, err)
	}
	t.Logf("recovery generations=%d/%d/%d logical_tool_calls=1 attempts=%d status=%s", old.generation, replacement.generation, got.Generation, got.Steps[0].Attempts, got.State)

	for _, denial := range []Decision{
		{Version: DecisionVersion, Kind: "CALL_TOOL", Tool: "execute_command", Args: json.RawMessage(`{}`)},
		{Version: DecisionVersion, Kind: "CALL_TOOL", Tool: "search_code", Args: json.RawMessage(`{"query":"leases","owner":"foreign"}`)},
	} {
		svc.Planner = scripted{first: denial}
		run, e := svc.Create(ctx, owner, index, "denied")
		if e != nil {
			t.Fatal(e)
		}
		if e = svc.Resume(ctx, owner, run.ID); e == nil {
			t.Fatal("forbidden decision completed")
		}
		run, e = svc.View(ctx, owner, run.ID)
		if e != nil || run.State != "FAILED" || run.Failure != "policy" || len(run.Steps) != 0 {
			t.Fatalf("forbidden decision reached journal or dispatch: %s %v", run.State, e)
		}
	}
	ready := svc.Readers
	svc.Readers.Retrieval = unavailableSource{Reader: reader}
	svc.Planner = scripted{first: Decision{Version: DecisionVersion, Kind: "CALL_TOOL", Tool: "read_source", Args: json.RawMessage(`{"id":"50278bc0a89a722a19d9939836e5b4c6cd047ecf37df0c8aec2733328ae5d947"}`)}}
	run, e := svc.Create(ctx, owner, index, "source outage")
	if e != nil {
		t.Fatal(e)
	}
	if e = svc.Resume(ctx, owner, run.ID); e == nil {
		t.Fatal("unavailable source completed")
	}
	run, e = svc.View(ctx, owner, run.ID)
	if e != nil || run.Failure != "backend" || len(run.Steps) != 1 || run.Steps[0].Attempts != 1 || run.Steps[0].Observation != nil {
		t.Fatalf("bad outage journal: %s %v", run.State, e)
	}
	svc.Readers = ready
	run, e = svc.Create(ctx, owner, index, "planner budget")
	if e != nil {
		t.Fatal(e)
	}
	budgetClaim, e := svc.acquire(ctx, owner, run.ID)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < MaxSteps; i++ {
		if e = svc.reserveModel(ctx, budgetClaim); e != nil {
			t.Fatal(e)
		}
	}
	if e = svc.reserveModel(ctx, budgetClaim); !errors.Is(e, ErrBudget) {
		t.Fatal("model-call budget did not deny next reservation", e)
	}
	if e = svc.Cancel(ctx, owner, run.ID); e != nil {
		t.Fatal(e)
	}
}
