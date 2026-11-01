package contextpack

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"math"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func engine(t *testing.T) *LocalEngine {
	t.Helper()
	m, e := OpenEngine(os.Getenv("REPO_CONTEXT_PYTHON"), os.Getenv("REPO_CONTEXT_MODEL"))
	if e != nil {
		t.Fatal("real pinned reranker required", e)
	}
	t.Cleanup(m.Close)
	return m
}
func TestS15IntentAndPolicy(t *testing.T) {
	for q, want := range map[string]string{"Repository.renewExecution": "exact", "where does publication happen?": "implementation", "how is a worker recovered?": "conceptual", "show tests for fencing": "tests", "explain the documentation": "documentation", "queue lease maybe": "mixed"} {
		got, e := Intent(q, "")
		if e != nil || got != want {
			t.Fatal(q, got, e)
		}
	}
	if got, e := Intent("Repository.renewExecution", "documentation"); e != nil || got != "documentation" {
		t.Fatal(got, e)
	}
	if _, e := Intent("a", "arbitrary"); e == nil {
		t.Fatal("invalid intent")
	}
	p := DefaultPolicy()
	if p.Validate() != nil {
		t.Fatal("default")
	}
	q := p
	q.Lambda = .25
	if q.ID() == p.ID() {
		t.Fatal("diversity identity")
	}
	q = p
	q.Model = "wrong"
	if q.Validate() == nil {
		t.Fatal("model mismatch")
	}
	q = p
	q.Tokenizer = "wrong"
	if q.Validate() == nil {
		t.Fatal("tokenizer mismatch")
	}
	q = p
	q.MaxPairs = 1000
	if q.Validate() == nil {
		t.Fatal("pair limit")
	}
	q = p
	q.Baseline = true
	if q.ID() == p.ID() {
		t.Fatal("baseline identity")
	}
}
func TestS15MMRIndependentArithmetic(t *testing.T) {
	if math.Abs(MMRScore(.8, .5, .75)-.475) > 1e-12 {
		t.Fatal("independent MMR calculation")
	}
	a := Candidate{Chunk: analysis.Chunk{ID: "a", File: "f"}, WindowSpan: analysis.Span{Start: 0, End: 100}, Relevance: .9}
	b := Candidate{Chunk: analysis.Chunk{ID: "b", File: "f"}, WindowSpan: analysis.Span{Start: 50, End: 150}, Relevance: .8}
	c := Candidate{Chunk: analysis.Chunk{ID: "c", File: "g"}, WindowSpan: analysis.Span{Start: 0, End: 100}, Relevance: .7}
	p := DefaultPolicy()
	p.MaxBlocks = 2
	p.Lambda = .95
	v, _ := Select([]Candidate{a, b, c}, p, "mixed")
	if v[1].Chunk.ID != "b" {
		t.Fatal("high relevance lambda", v)
	}
	p.Lambda = .2
	v, _ = Select([]Candidate{a, b, c}, p, "mixed")
	if v[1].Chunk.ID != "c" {
		t.Fatal("diversity lambda", v)
	}
	b.WindowSpan = a.WindowSpan
	p.MaxBlocks = 3
	v, omitted := Select([]Candidate{a, b, c}, p, "mixed")
	if len(v) != 2 || len(omitted) != 1 || omitted[0].Reason != "overlap" {
		t.Fatal("exact/overlap collapse", v, omitted)
	}
	if Redundancy(a.WindowSpan, a.WindowSpan, false) != 0 {
		t.Fatal("different files/common identifiers are not duplicate evidence")
	}
	if !weakEvidence("nonexistentterm", []Candidate{{Path: "a.go", Symbol: "Guard", RerankerScore: -5}}) || weakEvidence("Guard", []Candidate{{Path: "a.go", Symbol: "Guard", RerankerScore: -5}}) {
		t.Fatal("explicit weak-evidence heuristic")
	}
}
func TestS15GraphSafetyAndBounds(t *testing.T) {
	am := analysis.Manifest{Index: "analysis", Repository: "repo", Snapshot: "snap", Commit: "commit", Files: []analysis.File{{ID: "f"}}, Symbols: []analysis.Symbol{{ID: "a", File: "f"}, {ID: "b", File: "f"}, {ID: "c", File: "f"}}, Edges: []analysis.Edge{{ID: "ab", File: "f", From: "a", Target: "b", TargetKind: "symbol", Kind: "CALLS", Class: "resolved"}, {ID: "ba", File: "f", From: "b", Target: "a", TargetKind: "symbol", Kind: "CALLS", Class: "resolved"}, {ID: "bc", File: "f", From: "b", Target: "c", TargetKind: "symbol", Kind: "CALLS", Class: "candidate"}}}
	rm := retrieval.Manifest{Analysis: "analysis", Repository: "repo", Snapshot: "snap", Commit: "commit", AnalysisManifest: ingest.Digest(canonical(am)), Documents: []retrieval.Document{{Chunk: analysis.Chunk{ID: "ca", File: "f", Symbol: "a"}}, {Chunk: analysis.Chunk{ID: "cb", File: "f", Symbol: "b"}}, {Chunk: analysis.Chunk{ID: "cc", File: "f", Symbol: "c"}}}}
	p := DefaultPolicy()
	p.GraphSeeds = 1
	if !reasonConnects(am, rm.Documents[0], rm.Documents[1], am.Edges[0], 1, p.TraversalEdges) || reasonConnects(am, rm.Documents[2], rm.Documents[1], am.Edges[0], 1, p.TraversalEdges) {
		t.Fatal("graph reason must connect its actual seed and cited chunk")
	}
	seeds := []Candidate{{Chunk: rm.Documents[0].Chunk}}
	got, _, _, e := Expand(context.Background(), am, rm, seeds, p)
	if e != nil || len(got["cb"]) == 0 || len(got["cc"]) != 0 {
		t.Fatal("resolved only/cycle", got, e)
	}
	p.GraphDepth = 0
	got, _, _, e = Expand(context.Background(), am, rm, seeds, p)
	if e != nil || len(got) != 0 {
		t.Fatal("depth zero")
	}
	p = DefaultPolicy()
	p.TraversalEdges = 1
	_, n, limited, e := Expand(context.Background(), am, rm, seeds, p)
	if e != nil || n != 1 || !limited {
		t.Fatal("edge bound", n, limited, e)
	}
	bad := am
	bad.Snapshot = "foreign"
	if _, _, _, e = Expand(context.Background(), bad, rm, seeds, p); !errors.Is(e, ErrIntegrity) {
		t.Fatal("foreign graph", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, e = Expand(ctx, am, rm, seeds, p); !errors.Is(e, context.Canceled) {
		t.Fatal("graph cancellation", e)
	}
}
func TestS15RealCrossEncoderWindows(t *testing.T) {
	m := engine(t)
	p := DefaultPolicy()
	text := strings.Repeat("This passage discusses gardening, flowers and growing roses. ", 130) + "\n世界 😀 A stale worker must not finalize an old result: check the ownership generation and reject it."
	docs := []Input{{"tail", "guard.go", "Guard", "go", text}, {"other", "flowers.md", "", "markdown", "The roses require water and sunshine."}}
	one, e := m.Score(context.Background(), "where are stale worker results rejected?", docs, p)
	if e != nil {
		t.Fatal(e)
	}
	two, e := m.Score(context.Background(), "where are stale worker results rejected?", docs, p)
	if e != nil {
		t.Fatal(e)
	}
	if one.Pairs < 3 || len(one.Documents) != 2 || !one.Documents[0].Truncated {
		t.Fatal("actual windows", one)
	}
	best := Window{Score: math.Inf(-1)}
	for i, d := range one.Documents {
		for j, w := range d.Windows {
			if !finite(w.Score) || w.Tokens > 512 || !utf8.ValidString(docs[i].Text[w.Start:w.End]) || math.Abs(w.Score-two.Documents[i].Windows[j].Score) > 1e-5 {
				t.Fatal("finite/repeat/UTF8", w)
			}
			if i == 0 && w.Score > best.Score {
				best = w
			}
		}
	}
	if best.End != len(text) {
		t.Fatal("actual tail relevance not retained", best, len(text))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = m.Score(ctx, "worker", docs, p); !errors.Is(e, context.Canceled) {
		t.Fatal("native cancellation", e)
	}
}
func TestS15ActualTokenizerAndCropping(t *testing.T) {
	m := engine(t)
	source := strings.Repeat("lease ownership generation protects stale worker results 世界 😀\n", 80)
	c := analysis.Span{Start: 40, End: 40 + len(source), StartLine: 3, StartColumn: 2, EndLine: 83, EndColumn: 0}
	block := Evidence{ChunkID: "chunk", FileID: "file", Path: "guard.go", Span: c, SHA256: ingest.Digest([]byte(source)), ChunkSHA256: ingest.Digest([]byte(source)), Source: source, Selection: "mmr", Parse: "PARSED"}
	scope := Scope{Owner: "owner", Repository: "repo", Snapshot: "snap", Commit: "commit", ContextPolicy: DefaultPolicy().ID()}
	out, e := m.Pack(context.Background(), scope, []Evidence{block}, 800)
	if e != nil {
		t.Fatal(e)
	}
	if out.Tokens > 800 || len(out.Evidence) != 1 || !out.Evidence[0].Partial {
		t.Fatal("actual header/source token budget", out.Tokens, len(out.Evidence))
	}
	v := out.Evidence[0]
	left := v.Span.Start - c.Start
	if string([]byte(source)[left:]) != v.Source || subspan(c, []byte(source), left, len(source)) != v.Span || ingest.Digest([]byte(v.Source)) != v.SHA256 {
		t.Fatal("cropped actual bytes")
	}
	counts, e := m.Count(context.Background(), []string{out.Text, v.Source})
	if e != nil || counts[0] != out.Tokens || counts[0] <= counts[1] {
		t.Fatal("full header/separator accounting", counts, e)
	}
	tiny, e := m.Pack(context.Background(), scope, []Evidence{block}, 128)
	if e != nil {
		t.Fatal(e)
	}
	if tiny.Tokens <= 128 && len(tiny.Evidence) > 0 && tiny.Evidence[0].Source == "" {
		t.Fatal("fabricated empty fill")
	}
}
