//go:build integration

package repoeval

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/contextpack"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type unavailableScore struct{ contextpack.Engine }

func (e unavailableScore) Score(context.Context, string, []contextpack.Input, contextpack.Policy) (contextpack.ScoreReply, error) {
	return contextpack.ScoreReply{}, contextpack.ErrModel
}

type injectedStore struct {
	ingest.BlobStore
	down, corrupt bool
}

func (s injectedStore) Get(ctx context.Context, k string, n int64) ([]byte, error) {
	if s.down {
		return nil, ingest.ErrStorage
	}
	v, e := s.BlobStore.Get(ctx, k, n)
	if e == nil && s.corrupt && len(v) > 0 {
		v = append([]byte(nil), v...)
		v[0] ^= 1
	}
	return v, e
}
func TestS16RealRetainedEvaluation(t *testing.T) {
	ctx := context.Background()
	url := os.Getenv("S16_RETAINED_DATABASE_URL")
	owner, index, bucket := "aman-s12", "754720b7e2731b6b4321754b75a1f99b5cfd694b459ebca858781068af1dd338", os.Getenv("S13_RETAINED_BUCKET")
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal(e)
	}
	cleanup := ""
	if url == "" {
		path := os.Getenv("S16_CI_HANDOFF_FILE")
		if path == "" {
			t.Fatal("explicit actual retained index or existing CI handoff required")
		}
		var h map[string]string
		data, e := os.ReadFile(path)
		if e != nil || json.Unmarshal(data, &h) != nil {
			t.Fatal("handoff", e)
		}
		if !strings.HasPrefix(h["schema"], "judgegrid_retrieval_") {
			t.Fatal("schema scope")
		}
		cfg, e = pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
		if e != nil {
			t.Fatal(e)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = h["schema"]
		cleanup = h["schema"]
		owner, index, bucket = h["owner"], h["retrieval"], h["bucket"]
	}
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if cleanup != "" {
		defer func() {
			admin, e := pgxpool.New(ctx, os.Getenv("TEST_DATABASE_URL"))
			if e != nil {
				t.Error(e)
				return
			}
			defer admin.Close()
			_, e = admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{cleanup}.Sanitize()+" CASCADE")
			if e != nil {
				t.Error(e)
			}
		}()
	}
	objects, e := ingest.OpenS3(os.Getenv("REPO_TEST_S3_ENDPOINT"), bucket, os.Getenv("REPO_TEST_S3_ACCESS_KEY"), os.Getenv("REPO_TEST_S3_SECRET_KEY"), false)
	if e != nil {
		t.Fatal(e)
	}
	source := &analysis.Service{DB: analysis.Database{Pool: db}, Source: &ingest.Service{DB: ingest.Database{Pool: db}, Objects: objects}}
	model, e := retrieval.OpenModel(os.Getenv("REPO_EMBEDDING_PYTHON"), os.Getenv("REPO_EMBEDDING_MODEL"))
	if e != nil {
		t.Fatal(e)
	}
	defer model.Close()
	engine, e := contextpack.OpenEngine(os.Getenv("REPO_CONTEXT_PYTHON"), os.Getenv("REPO_CONTEXT_MODEL"))
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	reader := &retrieval.Service{DB: retrieval.Database{Pool: db}, Source: source, Model: model}
	if _, e = Capture(ctx, "foreign-owner", index, reader, source, engine); e == nil {
		t.Fatal("foreign evaluation")
	}
	s, e := Capture(ctx, owner, index, reader, source, engine)
	if e != nil {
		t.Fatal(e)
	}
	var cacheBefore int
	if e = db.QueryRow(ctx, `SELECT count(*) FROM retrieval_embedding_cache WHERE repository_id=$1`, s.Scope.Repository).Scan(&cacheBefore); e != nil {
		t.Fatal(e)
	}
	var d retrieval.Document
	for _, candidate := range s.manifest.Documents {
		if candidate.Symbol == "Repository.renewExecution" {
			d = candidate
			break
		}
	}
	if d.Chunk.ID == "" {
		t.Fatal("known exposed implementation absent")
	}
	a := Annotation{"model-assisted-source-inspection", "Codex", "provisional", false, "unavailable", "Public, previously exposed Session 14/15 method-location smoke query. Runtime source inspection, not independent human benchmark ground truth."}
	ref := s.ref(d.Chunk, d.Path, d.Chunk.Span)
	q := Query{ID: "exposed-lease", Question: "Repository.renewExecution", Category: "exact", Intent: "exact", Topic: "lease", Split: "dev", Exposed: true, Answerable: true, Judgments: []Judgment{{"implementation", ref, 3, a}}, Groups: []Group{{ID: "definition", Role: "definition", Alternatives: []string{"implementation"}}}}
	negative := Query{ID: "absent-controller", Question: "Where is the Kubernetes autoscaling controller implemented?", Category: "negative", Intent: "implementation", Topic: "absent", Split: "test", Answerable: false, NegativeRationale: "Provisional absence judgment on the pinned public fixture; no independent human review.", Judgments: []Judgment{{"unrelated", ref, 0, a}}}
	b := Benchmark{Schema: 1, Name: "retained-public-smoke", Version: 1, Frozen: true, Scope: s.Scope, Policy: DefaultPolicy(), Annotation: a, Queries: []Query{q, negative}}
	b.SplitDigest = b.SplitsID()
	if e = s.Validate(ctx, owner, b); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Export(ctx, "foreign-owner"); e == nil {
		t.Fatal("foreign corpus export")
	}
	bad := b
	bad.Scope.Snapshot = strings.Repeat("f", 64)
	if e = s.Validate(ctx, owner, bad); e == nil {
		t.Fatal("foreign snapshot qrels")
	}
	before := canonical(b)
	dir := t.TempDir()
	report, e := s.Run(ctx, owner, b, dir, nil)
	if e != nil || report.State != "COMPLETE" || len(report.Rows) != 12 {
		t.Fatal(report.State, e)
	}
	if string(canonical(b)) != string(before) {
		t.Fatal("ranks mutated frozen labels")
	}
	for _, o := range report.Rows {
		if o.Error != "" {
			t.Fatal(o.Query, o.Variant, o.Error)
		}
		if o.Variant == Variants[0] || o.Variant == Variants[1] || o.Variant == Variants[2] {
			if o.ModelCalls != 0 || o.Blocks != 0 {
				t.Fatal("ranking ablation called cross-encoder or packed context")
			}
		}
		if o.Variant == Variants[3] && (o.Diagnostics.Edges != 0 || o.Diagnostics.Expanded != 0 || o.Blocks != 0) {
			t.Fatal("V4 graph/MMR/packing not disabled")
		}
		if o.Variant == Variants[4] && o.Blocks != 0 {
			t.Fatal("V5 must stop before MMR/packing")
		}
		if o.Variant == Variants[5] {
			if o.Tokens > 4096 || o.ModelCalls != 0 || o.CacheHits != 1 {
				t.Fatal("verified cached score reuse", o.ModelCalls, o.CacheHits)
			}
			if o.Pack != nil {
				if e = s.Builder().Verify(ctx, owner, *o.Pack); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	// Same code path, same actual source/engine: ranking-only output must match
	// the Session 15 full builder's pre-selection candidates exactly.
	rr := contextpack.Request{Repository: s.Scope.Repository, Snapshot: s.Scope.Snapshot, Index: s.Scope.Retrieval, Query: q.Question, Intent: q.Intent, Policy: contextpack.DefaultPolicy()}
	ranking, e := s.Builder().RankEvaluation(ctx, owner, rr)
	if e != nil {
		t.Fatal(e)
	}
	pack, e := s.Builder().Build(ctx, owner, rr)
	if e != nil || len(ranking.Candidates) != len(pack.Candidates) {
		t.Fatal("shared ranking parity", e)
	}
	for i, c := range ranking.Candidates {
		if c.Chunk.ID != pack.Candidates[i].Chunk.ID || c.RerankerScore != pack.Candidates[i].RerankerScore || c.WindowSpan != pack.Candidates[i].WindowSpan {
			t.Fatal("independent ranking drift")
		}
	}
	// Interrupted output remains incomplete and can resume only after rechecking
	// current authority/source and checkpoint identities.
	partial := Checkpoint{Schema: 1, Benchmark: b.ID(), Policy: b.Policy.ID(), Owner: owner, State: "INCOMPLETE", Rows: append([]Observation(nil), report.Rows[:6]...)}
	resume := t.TempDir()
	if e = SaveCheckpoint(resume, partial); e != nil {
		t.Fatal(e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.Run(canceled, owner, b, resume, nil); e == nil {
		t.Fatal("canceled run succeeded")
	}
	if _, e = os.Stat(filepath.Join(resume, "report.json")); !os.IsNotExist(e) {
		t.Fatal("interruption published complete report")
	}
	repeated, e := s.Run(ctx, owner, b, resume, nil)
	if e != nil || repeated.MetricsDigest != report.MetricsDigest {
		t.Fatal("resumed metric/bootstrap reproducibility", e)
	}
	// Collector availability does not participate in metric computation.
	t.Setenv("APP_ENV", "development")
	t.Setenv("TELEMETRY_ENABLED", "true")
	t.Setenv("TELEMETRY_ADDR", "127.0.0.1:0")
	t.Setenv("TELEMETRY_OTLP_URL", "http://127.0.0.1:1")
	tr, e := telemetry.Start("repo-eval-test")
	if e != nil {
		t.Fatal(e)
	}
	if tr != nil {
		tr.Close()
	}
	cp, e := LoadCheckpoint(dir, b)
	if e != nil {
		t.Fatal(e)
	}
	// Serialized summary metrics are not authority: rebuild them from the labels.
	cp.Rows[0].Metrics = nil
	if e = s.RecheckCheckpoint(ctx, owner, b, &cp); e != nil {
		t.Fatal("report source/metric recheck", e)
	}
	if BuildReport(b, cp, s).MetricsDigest != report.MetricsDigest {
		t.Fatal("telemetry changed metrics")
	}
	realEngine := s.engine.base
	s.engine.base = unavailableScore{realEngine}
	s.engine.scores = map[string]contextpack.ScoreReply{}
	out := s.observe(ctx, q, Variants[3])
	s.engine.base = realEngine
	if out.Error != "MODEL" || len(out.Items) != 0 {
		t.Fatal("model partial failure fabricated evidence")
	}
	if e = s.metricRows(q, &out, b.Policy); e != nil || len(out.Metrics) != 0 {
		t.Fatal("model failure scored as relevance")
	}
	realObjects := source.Source.(*ingest.Service).Objects
	for _, fault := range []injectedStore{{BlobStore: realObjects, down: true}, {BlobStore: realObjects, corrupt: true}} {
		source.Source.(*ingest.Service).Objects = fault
		if e = s.Reverify(ctx, owner); e == nil {
			t.Fatal("backing source failure ignored")
		}
	}
	source.Source.(*ingest.Service).Objects = realObjects
	if e = s.Reverify(ctx, owner); e != nil {
		t.Fatal(e)
	}
	var cacheAfter int
	if e = db.QueryRow(ctx, `SELECT count(*) FROM retrieval_embedding_cache WHERE repository_id=$1`, s.Scope.Repository).Scan(&cacheAfter); e != nil || cacheAfter != cacheBefore {
		t.Fatal("evaluation re-embedded corpus", e)
	}
	if !errors.Is(s.authorize(ctx, "foreign"), ErrDenied) {
		t.Fatal("foreign authority")
	}
	t.Logf("V actual retained READY index; %d documents; 2 public smoke queries x six genuine variants; unchanged embedding cache=%d; source/model/owner failures, checkpoint resume, ablation parity verified", len(s.manifest.Documents), cacheAfter)
}
