package retrieval

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestS14CodeTokens(t *testing.T) {
	for _, s := range []string{"renewExecutionLease", "renew_execution_lease", "RenewExecutionLease"} {
		v := Tokens(s)
		for _, term := range []string{"renew", "execution", "lease", "=" + s, strings.ToLower(s)} {
			if v[term] == 0 {
				t.Fatal(s, term, v)
			}
		}
	}
	v := Tokens("HTTPServer internal/storage/ownership.go")
	for _, s := range []string{"http", "server", "internal", "storage", "ownership", "go"} {
		if v[s] == 0 {
			t.Fatal(s, v)
		}
	}
	if Tokens("renewExecution")["exec"] != 0 {
		t.Fatal("substring matching")
	}
}
func unitDoc(id, path, symbol, body string) Document {
	d, _, e := makeDocument(analysis.Chunk{ID: id, File: path, Language: "go", Span: analysis.Span{Start: 0, End: len(body)}}, analysis.File{Path: path, State: "PARSED"}, symbol, []byte(body))
	if e != nil {
		panic(e)
	}
	return d
}
func TestS14BM25FormulaAndFields(t *testing.T) {
	p := DefaultPolicy()
	p.Weights = [4]float64{1, 1, 1, 1}
	docs := []Document{{Chunk: analysis.Chunk{ID: "a"}, Fields: [4]map[string]int{{"rare": 2}, {}, {}, {}}, Lengths: [4]int{2, 0, 0, 0}}, {Chunk: analysis.Chunk{ID: "b"}, Fields: [4]map[string]int{{"common": 4}, {}, {}, {}}, Lengths: [4]int{4, 0, 0, 0}}}
	c := corpus(docs)
	if c.DF[0]["rare"] != 1 || c.Count != 2 || c.Totals[0] != 6 {
		t.Fatal(c)
	}
	got := BM25(docs, c, "rare", p)
	expected := math.Log(2) * (2 * 2.2) / (2 + 1.2*(.25+.75*2.0/3))
	if len(got) != 1 || math.Abs(got[0].Score-expected) > 1e-12 {
		t.Fatal(got, expected)
	}
	rareDocs := []Document{{Chunk: analysis.Chunk{ID: "a"}, Fields: [4]map[string]int{{"rare": 1, "common": 1}, {}, {}, {}}, Lengths: [4]int{2, 0, 0, 0}}, {Chunk: analysis.Chunk{ID: "b"}, Fields: [4]map[string]int{{"common": 2}, {}, {}, {}}, Lengths: [4]int{2, 0, 0, 0}}}
	rc := corpus(rareDocs)
	rs := BM25(rareDocs, rc, "rare", p)
	cs := BM25(rareDocs, rc, "common", p)
	if rc.DF[0]["rare"] != 1 || rc.DF[0]["common"] != 2 || rs[0].Score <= cs[0].Score {
		t.Fatal("rare-term IDF", rs, cs)
	}
	// Same TF, longer field receives lower score; saturation has diminishing gains.
	long := docs[0]
	long.Chunk.ID = "c"
	long.Lengths[0] = 20
	long.Fields[0] = map[string]int{"rare": 2, "other": 18}
	two := []Document{docs[0], long}
	r := BM25(two, corpus(two), "rare", p)
	if r[0].Document.Chunk.ID != "a" {
		t.Fatal("length normalization", r)
	}
	a := unitDoc("a", "one.go", "renewExecution", "misc")
	b := unitDoc("b", "two.go", "misc", "renewExecution")
	p = DefaultPolicy()
	r = BM25([]Document{a, b}, corpus([]Document{a, b}), "renewExecution", p)
	if r[0].Document.Chunk.ID != "a" {
		t.Fatal("symbol weights", r)
	}
	p.Weights = [4]float64{.01, 10, .01, 10}
	r = BM25([]Document{a, b}, corpus([]Document{a, b}), "renewExecution", p)
	if r[0].Document.Chunk.ID != "b" {
		t.Fatal("changed field weights", r)
	}
}
func TestS14RRFAndDiversity(t *testing.T) {
	p := DefaultPolicy()
	a := unitDoc("a", "a.go", "A", "aaa")
	b := unitDoc("b", "b.go", "B", "bbb")
	c := unitDoc("c", "c.go", "C", "ccc")
	d := unitDoc("d", "d.go", "D", "ddd")
	lex := []Candidate{{Document: a, LexicalRank: 1}, {Document: b, LexicalRank: 2}, {Document: c, LexicalRank: 3}}
	dense := []Candidate{{Document: c, DenseRank: 1}, {Document: a, DenseRank: 2}, {Document: d, DenseRank: 3}}
	v := Fuse(lex, dense, p)
	want := map[string]float64{"a": 1.0/61 + 1.0/62, "b": 1.0 / 62, "c": 1.0/63 + 1.0/61, "d": 1.0 / 63}
	for _, x := range v {
		if math.Abs(x.Score-want[x.Document.Chunk.ID]) > 1e-15 {
			t.Fatal(x)
		}
	}
	if v[0].Document.Chunk.ID != "a" || v[1].Document.Chunk.ID != "c" {
		t.Fatal(v)
	}
	nested := a
	nested.Chunk.ID = "nested"
	nested.Chunk.Span.End = 2
	out := diverse([]Candidate{{Document: a}, {Document: nested}, {Document: b}}, 3)
	if len(out) != 2 {
		t.Fatal("overlap", out)
	}
	same := a.Chunk
	same.ID = "duplicate"
	selected, excluded, e := selectChunks(analysis.Manifest{Chunks: []analysis.Chunk{a.Chunk, same, b.Chunk}}, p)
	if e != nil || len(selected) != 2 || len(excluded) != 1 || excluded[0].Reason != "duplicate-range" {
		t.Fatal("exact-range corpus collapse", selected, excluded, e)
	}
}
func TestS14VectorAndInputIdentity(t *testing.T) {
	good := make([]float32, 384)
	good[0] = 1
	if ValidateVector(good) != nil {
		t.Fatal("valid")
	}
	for _, bad := range [][]float32{good[:383], make([]float32, 384)} {
		if ValidateVector(bad) == nil {
			t.Fatal("bad dimension/norm")
		}
	}
	for _, x := range []float32{float32(math.NaN()), float32(math.Inf(1))} {
		bad := append([]float32(nil), good...)
		bad[2] = x
		if ValidateVector(bad) == nil {
			t.Fatal("non-finite")
		}
	}
	c := analysis.Chunk{ID: "x", Language: "go"}
	a, input, e := makeDocument(c, analysis.File{Path: "a.go", State: "PARSED"}, "A", []byte("func A(){}"))
	if e != nil {
		t.Fatal(e)
	}
	b, _, _ := makeDocument(c, analysis.File{Path: "b.go", State: "PARSED"}, "A", []byte("func A(){}"))
	if a.InputHash == b.InputHash || !strings.Contains(input, "path: a.go") {
		t.Fatal("context identity")
	}
	if _, _, e = makeDocument(c, analysis.File{}, "", []byte{0xff}); e == nil {
		t.Fatal("invalid UTF8")
	}
	p := DefaultPolicy()
	q := p
	q.Model = "another-model"
	if p.ID() == q.ID() || q.Validate() == nil {
		t.Fatal("model policy")
	}
	q = p
	q.RRF++
	if p.ID() == q.ID() {
		t.Fatal("ranking identity")
	}
}
func testModel(t *testing.T) *LocalModel {
	t.Helper()
	m, e := OpenModel(os.Getenv("REPO_EMBEDDING_PYTHON"), os.Getenv("REPO_EMBEDDING_MODEL"))
	if e != nil {
		t.Fatal("real pinned local model required", e)
	}
	t.Cleanup(m.Close)
	return m
}
func TestS14RealLocalEmbedding(t *testing.T) {
	m := testModel(t)
	texts := []string{"Where is the execution lease renewed?", "language: go\npath: ownership.go\nsymbol: Repository.renewExecution\ncode:\nfunc renewExecution() { extendLease() }"}
	v, e := m.Embed(context.Background(), texts)
	if e != nil {
		t.Fatal(e)
	}
	repeat, e := m.Embed(context.Background(), texts)
	if e != nil || !reflect.DeepEqual(v, repeat) {
		t.Fatal("stable actual CPU inference", e)
	}
	if reflect.DeepEqual(v[0], v[1]) {
		t.Fatal("query/document bytes ignored")
	}
	for _, x := range v {
		if ValidateVector(x) != nil {
			t.Fatal("finite normalized dimension")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = m.Embed(ctx, texts); e == nil {
		t.Fatal("cancellation")
	}
	m.Python, _ = os.Executable()
	if _, e = m.Embed(context.Background(), texts); !errors.Is(e, ErrModel) {
		t.Fatal("failed model never falls back", e)
	}
}
func TestS14RequestBounds(t *testing.T) {
	svc := &Service{}
	for _, q := range []Request{{Query: " ", TopK: 1, Depth: 1}, {Query: "ok", TopK: 51, Depth: 51}, {Query: "ok", TopK: 5, Depth: 4}, {Query: "ok", TopK: 1, Depth: 201}, {Query: string([]byte{0xff}), TopK: 1, Depth: 1}, {Query: "a\x00b", TopK: 1, Depth: 1}} {
		if _, e := svc.Search(context.Background(), "owner", q); !errors.Is(e, ErrBudget) {
			t.Fatal("request guard before data access", e)
		}
	}
}
