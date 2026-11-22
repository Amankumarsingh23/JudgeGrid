package repoeval

import (
	"context"
	"encoding/json"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/contextpack"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func near(t *testing.T, a, b float64) {
	t.Helper()
	if math.Abs(a-b) > 1e-10 {
		t.Fatalf("got %.12f want %.12f", a, b)
	}
}
func TestS16IndependentMetricExamples(t *testing.T) {
	m := Metrics([]int{3, 0, 2, 0, 2}, []string{"A", "B", "C", "D", "E"}, map[string]int{"A": 3, "C": 2, "E": 2, "G": 2, "B": 0, "D": 0}, 5, 2)
	near(t, m.PrecisionLower, .6)
	near(t, *m.Recall, .75)
	near(t, *m.Hit, 1)
	near(t, *m.MRR, 1)
	near(t, *m.AP, (1.+2./3+3./5)/4)
	ideal := Metrics([]int{3, 2, 0}, []string{"a", "b", "z"}, map[string]int{"a": 3, "b": 2, "z": 0}, 3, 2)
	near(t, ideal.DCG, 7+3/math.Log2(3))
	near(t, *ideal.NDCG, 1)
	a := Metrics([]int{3}, []string{"a"}, map[string]int{"a": 3}, 4, 2)
	b := Metrics([]int{0, 3}, []string{"x", "a"}, map[string]int{"a": 3, "x": 0}, 4, 2)
	c := Metrics([]int{0, 0, 0, 3}, []string{"x", "y", "z", "a"}, map[string]int{"a": 3, "x": 0, "y": 0, "z": 0}, 4, 2)
	near(t, (*a.MRR+*b.MRR+*c.MRR)/3, 7./12)
	no := Metrics(nil, nil, map[string]int{"a": 3}, 5, 2)
	near(t, *no.Recall, 0)
	near(t, *no.NDCG, 0)
	zero := Metrics([]int{0}, []string{"z"}, map[string]int{"z": 0}, 5, 2)
	if zero.NDCG != nil || zero.Recall != nil || zero.AP != nil || zero.MRR != nil {
		t.Fatal("no-positive metrics must not invent a denominator")
	}
	unknown := Metrics([]int{-1, 3}, []string{"u", "a"}, map[string]int{"a": 3}, 5, 2)
	near(t, unknown.PrecisionLower, .2)
	near(t, unknown.PrecisionUpper, .4)
	near(t, *unknown.JudgedPrecision, 1)
	near(t, unknown.JudgmentCoverage, .5)
	near(t, *unknown.MRR, .5)
	duplicate := Metrics([]int{3, 3}, []string{"a", "a"}, map[string]int{"a": 3}, 2, 2)
	near(t, *duplicate.Recall, 1)
	near(t, duplicate.PrecisionLower, .5)
	near(t, *duplicate.NDCG, 1)
	tie := Metrics([]int{2, 2}, []string{"a", "b"}, map[string]int{"a": 2, "b": 2}, 2, 2)
	near(t, *tie.NDCG, 1)
}
func TestS16SeededQueryBootstrap(t *testing.T) {
	a := Bootstrap([]float64{1, 2, 3}, 17, 2000)
	b := Bootstrap([]float64{1, 2, 3}, 17, 2000)
	if a != b {
		t.Fatal("same query samples must reproduce")
	}
	near(t, a.Mean, 2)
	near(t, a.Low, 1)
	near(t, a.High, 3)
	if !a.Unstable {
		t.Fatal("three queries are a small category")
	}
	paired := Bootstrap([]float64{.25, .25, .25}, 123, 2000)
	near(t, paired.Mean, .25)
	near(t, paired.Low, .25)
	near(t, paired.High, .25)
	if Bootstrap(nil, 1, 2000).N != 0 {
		t.Fatal("empty sample")
	}
}
func fixture(t *testing.T) (Benchmark, *Session) {
	t.Helper()
	var f struct {
		Safe       bool   `json:"safe_to_publish"`
		Provenance string `json:"provenance"`
		Path       string `json:"path"`
		Source     string `json:"source"`
		Question   string `json:"question"`
		Fact       string `json:"critical_fact"`
	}
	raw, e := os.ReadFile("testdata/public-fixture.json")
	if e != nil || json.Unmarshal(raw, &f) != nil || !f.Safe {
		t.Fatal("safe fixture", e)
	}
	scope := Scope{Owner: "fixture-owner", Repository: "synthetic-repository", Snapshot: strings.Repeat("1", 64), Commit: strings.Repeat("c", 40), SourceManifest: strings.Repeat("2", 64), Analysis: strings.Repeat("3", 64), AnalysisManifest: strings.Repeat("4", 64), Retrieval: strings.Repeat("5", 64), RetrievalManifest: strings.Repeat("6", 64), RetrievalPolicy: strings.Repeat("7", 64), ContextPolicy: contextpack.DefaultPolicy().ID(), Tokenizer: contextpack.TokenizerIdentity()}
	file := analysis.File{ID: "synthetic-file", Path: f.Path, SHA256: ingest.Digest([]byte(f.Source)), Size: int64(len(f.Source))}
	s := &Session{Scope: scope, files: map[string]CapturedFile{file.ID: {Metadata: file, Source: f.Source}}}
	start := strings.Index(f.Source, f.Fact)
	end := start + len(f.Fact)
	sl, sc := point([]byte(f.Source), start)
	el, ec := point([]byte(f.Source), end)
	ref := Ref{scope.Snapshot, file.ID, file.Path, analysis.Span{Start: start, End: end, StartLine: sl, StartColumn: sc, EndLine: el, EndColumn: ec}, ingest.Digest([]byte(f.Fact)), []string{"gold-chunk"}}
	annotation := Annotation{"model-assisted-source-inspection", "Codex", "provisional", false, "unavailable", f.Provenance}
	q := Query{ID: "q1", Question: f.Question, Category: "synthetic", Intent: "implementation", Topic: "guard", Split: "test", Answerable: true, Judgments: []Judgment{{"guard", ref, 3, annotation}}, Groups: []Group{{ID: "guard", Role: "validation_guard", Alternatives: []string{"guard"}}}}
	b := Benchmark{Schema: 1, Name: "public-synthetic-fixture", Version: 1, Frozen: true, SafeFixture: true, Scope: scope, Policy: DefaultPolicy(), Annotation: annotation, Queries: []Query{q}}
	b.SplitDigest = b.SplitsID()
	return b, s
}
func TestS16SpanAndCriticalEvidence(t *testing.T) {
	b, s := fixture(t)
	q := b.Queries[0]
	g := q.Judgments[0].Ref
	if e := s.VerifyRef(context.Background(), b.Scope.Owner, g); e != nil {
		t.Fatal(e)
	}
	duplicate := EvidenceSupport(q, []Ref{g, g}, b.Policy)
	near(t, duplicate.GoldCoverage, 1)
	near(t, duplicate.Redundancy, .5)
	if duplicate.State != "ALL" {
		t.Fatal(duplicate)
	}
	cropped := g
	cropped.Span.Start++
	if EvidenceSupport(q, []Ref{cropped}, b.Policy).State == "ALL" {
		t.Fatal("one missing critical byte cannot satisfy a guard")
	}
	first, last := g, g
	mid := (g.Span.Start + g.Span.End) / 2
	first.Span.End = mid
	last.Span.Start = mid
	if EvidenceSupport(q, []Ref{first, last}, b.Policy).State != "ALL" {
		t.Fatal("union of exact complementary spans")
	}
	foreign := g
	foreign.File = "another-file"
	if _, e := IoU(g, foreign); e != ErrDenied {
		t.Fatal("cross-file")
	}
	foreign = g
	foreign.Snapshot = strings.Repeat("f", 64)
	if _, e := IoU(g, foreign); e != ErrDenied {
		t.Fatal("cross-snapshot")
	}
	near(t, Coverage(g, []Ref{foreign}), 0)
	altered := g
	altered.SHA256 = strings.Repeat("a", 64)
	if e := s.VerifyRef(context.Background(), b.Scope.Owner, altered); e != ErrIntegrity {
		t.Fatal("altered source digest", e)
	}
	if e := s.VerifyRef(context.Background(), "foreign", g); e != ErrDenied {
		t.Fatal("owner")
	}
	q.Groups[0] = Group{ID: "unknown", Role: "failure_behavior", Unknown: true, Uncertainty: "independent fact annotation unavailable"}
	if EvidenceSupport(q, []Ref{g}, b.Policy).State != "CANNOT_ASSESS" {
		t.Fatal("unknown annotation is not no evidence")
	}
}
func TestS16FrozenManifestAndAnnotationPolicy(t *testing.T) {
	b, _ := fixture(t)
	if e := b.Validate(); e != nil {
		t.Fatal(e)
	}
	original := b.ID()
	p := b.Policy
	p.GoldCoverage = .7
	if p.ID() == b.Policy.ID() {
		t.Fatal("matching policy identity")
	}
	clone := func() Benchmark { var v Benchmark; _ = json.Unmarshal(canonical(b), &v); return v }
	for name, alter := range map[string]func(*Benchmark){"unfrozen": func(v *Benchmark) { v.Frozen = false }, "human claim": func(v *Benchmark) { v.Annotation.HumanReviewed = true }, "missing annotation": func(v *Benchmark) { v.Queries[0].Judgments = nil }, "missing rationale": func(v *Benchmark) { v.Queries[0].Judgments[0].Annotation.Rationale = "" }, "foreign snapshot": func(v *Benchmark) { v.Queries[0].Judgments[0].Ref.Snapshot = strings.Repeat("f", 64) }, "exposed heldout": func(v *Benchmark) { v.Queries[0].Exposed = true }} {
		t.Run(name, func(t *testing.T) {
			v := clone()
			alter(&v)
			if v.Validate() == nil {
				t.Fatal("accepted invalid labels")
			}
		})
	}
	if b.ID() != original {
		t.Fatal("validation mutated frozen qrels")
	}
}
func TestS16InterruptedCheckpointAndPartialErrors(t *testing.T) {
	b, _ := fixture(t)
	dir := t.TempDir()
	cp, e := LoadCheckpoint(dir, b)
	if e != nil {
		t.Fatal(e)
	}
	cp.Rows = []Observation{{Attempt: 1, Query: "q1", Variant: Variants[0], Status: "NO_CANDIDATES"}}
	if e = SaveCheckpoint(dir, cp); e != nil {
		t.Fatal(e)
	}
	loaded, e := LoadCheckpoint(dir, b)
	if e != nil || loaded.State != "INCOMPLETE" || len(loaded.Rows) != 1 {
		t.Fatal("resumable incomplete", e)
	}
	cp.State = "COMPLETE"
	if e = SaveCheckpoint(dir, cp); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadCheckpoint(dir, b); e != ErrIntegrity {
		t.Fatal("partial result called complete", e)
	}
	cp.State = "INCOMPLETE"
	cp.Rows = append(cp.Rows, cp.Rows[0])
	_ = SaveCheckpoint(dir, cp)
	if _, e = LoadCheckpoint(dir, b); e != ErrIntegrity {
		t.Fatal("duplicate variant")
	}
	cp.Rows = nil
	for _, variant := range Variants {
		cp.Rows = append(cp.Rows, Observation{Attempt: 1, Query: "q1", Variant: variant, Status: "RANKED"})
	}
	cp.FreshPlan = []string{"q1"}
	cp.State = "COMPLETE"
	_ = SaveCheckpoint(dir, cp)
	if _, e = LoadCheckpoint(dir, b); e != ErrIntegrity {
		t.Fatal("missing planned fresh observations cannot be complete")
	}
	cp.Rows = []Observation{{Query: "q1", Variant: Variants[5], Status: "ERROR", Error: "MODEL"}}
	report := BuildReport(b, cp, nil)
	if report.Counts["error/MODEL"] != 1 || report.Counts["negative/correct_abstention_by_provisional_absence_label"] != 0 || len(report.Summary) != 0 {
		t.Fatal("infrastructure must not earn semantic quality", report.Counts)
	}
	if e = WriteReport(dir, report); e != ErrIncomplete {
		t.Fatal("no complete report for interrupted run")
	}
	_ = os.WriteFile(filepath.Join(dir, "checkpoint.json"), []byte(`{"schema":1,"payload_sha256":"forged"}`), 0600)
	if _, e = LoadCheckpoint(dir, b); e == nil {
		t.Fatal("forged checkpoint")
	}
}
func TestS16ReportPairingAndFrozenQrels(t *testing.T) {
	b, s := fixture(t)
	qrels := canonical(b)
	cp := Checkpoint{State: "COMPLETE", Benchmark: b.ID(), Policy: b.Policy.ID(), Owner: b.Scope.Owner}
	for _, v := range Variants {
		o := Observation{Query: "q1", Variant: v, Status: "RANKED", Items: []Item{{Chunk: "gold-chunk", Ref: b.Queries[0].Judgments[0].Ref, FullRef: b.Queries[0].Judgments[0].Ref}}}
		if e := s.metricRows(b.Queries[0], &o, b.Policy); e != nil {
			t.Fatal(e)
		}
		cp.Rows = append(cp.Rows, o)
	}
	one, two := BuildReport(b, cp, nil), BuildReport(b, cp, nil)
	if one.MetricsDigest != two.MetricsDigest || !reflect.DeepEqual(one.Comparisons, two.Comparisons) {
		t.Fatal("query-seeded reports")
	}
	if string(canonical(b)) != string(qrels) {
		t.Fatal("rank scores must not rewrite annotations")
	}
	for _, c := range one.Comparisons {
		near(t, c.Delta.Mean, 0)
		if c.Delta.N != 1 {
			t.Fatal("paired query denominator")
		}
	}
	dir := t.TempDir()
	if e := WriteReport(dir, one); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"report.json", "report.csv", "report.md"} {
		st, e := os.Stat(filepath.Join(dir, name))
		if e != nil || st.Size() == 0 {
			t.Fatal(name, e)
		}
	}
}
