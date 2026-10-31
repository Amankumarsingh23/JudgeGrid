package analysis

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
)

func testParser(t *testing.T) *Parser {
	t.Helper()
	python := os.Getenv("REPO_ANALYSIS_PYTHON")
	if python == "" {
		t.Fatal("REPO_ANALYSIS_PYTHON required: install pinned helper requirements; parser tests must not skip")
	}
	p, e := OpenParser(python)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	return p
}
func parseTest(t *testing.T, language, source string) Facts {
	t.Helper()
	f, e := testParser(t).Parse(context.Background(), language, []byte(source), DefaultPolicy())
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func TestS13RealLanguagesAndSpans(t *testing.T) {
	cases := []struct {
		language, source string
		kinds            map[string]int
	}{
		{"go", "package café\ntype Box struct { N int }\ntype Reader interface { Read() }\ntype Alias = Box\nfunc Hello() {}\nfunc (b *Box) Get() { Hello() }\nfunc (b Box) Value() {}\n", map[string]int{"struct": 1, "interface": 1, "function": 1, "method": 2}},
		{"python", "class Café:\n    def greet(self):\n        def nested():\n            return '世界'\n        self.greet()\n        return nested()\n", map[string]int{"class": 1, "method": 1, "function": 1}},
		{"javascript", "export class Box { get() { return helper(); } }\nexport function helper() { return '世界'; }\nconst arrow = () => helper();\n", map[string]int{"class": 1, "method": 1, "function": 2}},
		{"typescript", "export interface Box { n: number }\nexport type Name = string;\nexport function hello(n: number): number { return n; }\n", map[string]int{"interface": 1, "type": 1, "function": 1}},
		{"tsx", "export function View() { return <div>世界</div>; }", map[string]int{"function": 1}},
	}
	for _, c := range cases {
		t.Run(c.language, func(t *testing.T) {
			f := parseTest(t, c.language, c.source)
			if f.Status != "PARSED" {
				t.Fatalf("status %s errors %+v", f.Status, f.Errors)
			}
			counts := map[string]int{}
			for _, s := range f.Symbols {
				counts[s.Kind]++
				if string([]byte(c.source)[s.NameStart:s.NameEnd]) != s.Name {
					t.Fatal("name bytes")
				}
				r := span([]byte(c.source), s.Start, s.End)
				if r.StartLine < 1 || r.EndLine < r.StartLine {
					t.Fatal("line span")
				}
			}
			for k, want := range c.kinds {
				if counts[k] != want {
					t.Fatalf("kind %s got %d want %d, %+v", k, counts[k], want, f.Symbols)
				}
			}
			if c.language == "go" {
				for _, s := range f.Symbols {
					if s.Kind == "method" && s.Receiver != "Box" {
						t.Fatalf("receiver %+v", s)
					}
				}
			}
		})
	}
}
func TestS13PartialBudgetAndCancellation(t *testing.T) {
	p := testParser(t)
	policy := DefaultPolicy()
	f, e := p.Parse(context.Background(), "go", []byte("package p\nfunc Broken( {\n"), policy)
	if e != nil || f.Status != "PARTIAL" || len(f.Errors) == 0 {
		t.Fatalf("partial: %+v %v", f, e)
	}
	policy.MaxNodes = 4
	if _, e = p.Parse(context.Background(), "go", []byte("package p\nfunc F() {}"), policy); !errors.Is(e, ErrBudget) {
		t.Fatalf("node budget %v", e)
	}
	policy = DefaultPolicy()
	policy.MaxDepth = 8
	if _, e = p.Parse(context.Background(), "python", []byte("x="+strings.Repeat("(", 40)+"1"+strings.Repeat(")", 40)), policy); !errors.Is(e, ErrBudget) {
		t.Fatalf("depth %v", e)
	}
	policy = DefaultPolicy()
	policy.MaxFileBytes = 4
	if _, e = p.Parse(context.Background(), "go", []byte("package p"), policy); !errors.Is(e, ErrBudget) {
		t.Fatalf("bytes %v", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, e = p.Parse(ctx, "go", []byte("package p"), DefaultPolicy()); !errors.Is(e, ErrParser) {
		t.Fatalf("canceled %v", e)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation did not kill helper")
	}
	policy = DefaultPolicy()
	policy.ParseMillis = 1
	if _, e = p.Parse(context.Background(), "go", []byte("package p"), policy); !errors.Is(e, ErrParser) {
		t.Fatalf("deadline %v", e)
	}
}
func TestS13StructuralChunksUnicodeDeterminism(t *testing.T) {
	unicodeBytes := []byte("α世界\n😀x")
	if got := span(unicodeBytes, 2, 8); got != (Span{Start: 2, End: 8, StartLine: 1, StartColumn: 2, EndLine: 1, EndColumn: 8}) {
		t.Fatalf("UTF-8 byte columns: %+v", got)
	}
	if got := span(unicodeBytes, 9, 13); got != (Span{Start: 9, End: 13, StartLine: 2, StartColumn: 0, EndLine: 2, EndColumn: 4}) {
		t.Fatalf("Unicode line boundary: %+v", got)
	}
	source := "package p\n// 世界 😀\nfunc Long() {\n" + strings.Repeat("println(\"世界 😀\")\n", 200) + "}\n"
	facts := parseTest(t, "go", source)
	p := DefaultPolicy()
	p.ChunkBytes = 128
	p.Overlap = 16
	f := File{ID: hash("file"), Language: "go"}
	u, a, e := materialize(hash("index"), f, []byte(source), facts, p)
	if e != nil {
		t.Fatal(e)
	}
	_, b, e := materialize(hash("index"), f, []byte(source), facts, p)
	if e != nil || hash(a) != hash(b) || len(a) < 10 {
		t.Fatal("determinism/splitting")
	}
	covered := make([]bool, len(source))
	for _, c := range a {
		raw := []byte(source)[c.Span.Start:c.Span.End]
		if !utf8.Valid(raw) || len(raw) > 128 || ingest.Digest(raw) != c.SHA256 {
			t.Fatal("chunk bytes")
		}
		for i := c.Span.Start; i < c.Span.End; i++ {
			covered[i] = true
		}
		if c.Kind == "symbol_part" && c.Symbol != u.symbols[0].ID {
			t.Fatal("symbol lost")
		}
	}
	for _, ok := range covered {
		if !ok {
			t.Fatal("uncovered bytes")
		}
	}
	p2 := DefaultPolicy()
	p2.ChunkBytes = 256
	if p.ID() == p2.ID() || indexID("s", p.ID()) == indexID("s", p2.ID()) {
		t.Fatal("policy identity")
	}
	p2.Parser = "changed"
	if p2.Validate() == nil {
		t.Fatal("untrusted grammar accepted")
	}
}
func testGraph(t *testing.T, sources map[string]string, langs map[string]string) Manifest {
	t.Helper()
	m := Manifest{Index: hash("index")}
	units := []unit{}
	paths := []string{}
	for path := range sources {
		paths = append(paths, path)
	}
	sortStrings(paths)
	for _, path := range paths {
		raw := sources[path]
		lang := langs[path]
		var facts Facts
		if Grammar(lang, path) != "" {
			facts = parseTest(t, Grammar(lang, path), raw)
		}
		f := File{ID: hash(path), Path: path, Language: lang, Grammar: Grammar(lang, path), Package: facts.Package, State: facts.Status}
		u, _, e := materialize(m.Index, f, []byte(raw), facts, DefaultPolicy())
		if e != nil {
			t.Fatal(e)
		}
		units = append(units, u)
		m.Symbols = append(m.Symbols, u.symbols...)
	}
	if e := relationships(context.Background(), &m, units, DefaultPolicy()); e != nil {
		t.Fatal(e)
	}
	return m
}
func sortStrings(a []string) {
	for i := range a {
		for j := i + 1; j < len(a); j++ {
			if a[j] < a[i] {
				a[i], a[j] = a[j], a[i]
			}
		}
	}
}
func TestS13ConservativeScopeAndImports(t *testing.T) {
	m := testGraph(t, map[string]string{
		"go.mod":      "module example.test/repo\n",
		"a/main.go":   "package a\nimport ( b \"example.test/repo/b\"; \"fmt\" )\ntype Box struct{}\nfunc (b *Box) Get(){}\nfunc Local(){}\nfunc Run(box *Box){ Local(); b.Target(); fmt.Println(\"hi\"); box.Get(); Other() }\nfunc Shadow(Local func(), b interface{}) { Local(); b.Target() }\n",
		"b/target.go": "package b\nfunc Target(){}\nfunc Other(){}\n",
		"c/other.go":  "package c\nfunc Other(){}\n",
	}, map[string]string{"go.mod": "gomod", "a/main.go": "go", "b/target.go": "go", "c/other.go": "go"})
	counts := map[string]int{}
	for _, c := range m.Calls {
		counts[c.Target+":"+c.Class]++
	}
	for key, want := range map[string]int{"Local:resolved": 1, "Local:unresolved": 1, "b.Target:resolved": 1, "b.Target:unresolved": 1, "fmt.Println:external": 1, "box.Get:resolved": 1, "Other:unresolved": 1} {
		if counts[key] != want {
			t.Fatalf("%s = %d want %d calls %+v", key, counts[key], want, m.Calls)
		}
	}
}
func TestS13PythonJSImportsAndDuplicateNames(t *testing.T) {
	m := testGraph(t, map[string]string{"p/a.py": "from .b import target as chosen\ndef same():\n    return 1\nclass Box:\n    def same(self):\n        self.same()\ndef run(obj):\n    chosen()\n    same()\n    obj.same()\n", "p/b.py": "def target():\n    return 1\n", "web/a.ts": "import { target as chosen } from './b';\nexport function run() { chosen(); unknown.same(); }", "web/b.ts": "export function target() {}"}, map[string]string{"p/a.py": "python", "p/b.py": "python", "web/a.ts": "typescript", "web/b.ts": "typescript"})
	names := map[string]int{}
	ids := map[string]bool{}
	for _, s := range m.Symbols {
		names[s.Name]++
		if ids[s.ID] {
			t.Fatal("duplicate symbol identity")
		}
		ids[s.ID] = true
	}
	if names["same"] != 2 {
		t.Fatal("duplicate names lost")
	}
	counts := map[string]int{}
	for _, c := range m.Calls {
		counts[c.Target+":"+c.Class]++
	}
	for key, want := range map[string]int{"chosen:resolved": 2, "self.same:candidate": 1, "obj.same:unresolved": 1, "unknown.same:unresolved": 1} {
		if counts[key] != want {
			t.Fatalf("%s got %d calls %+v", key, counts[key], m.Calls)
		}
	}
}

func TestS13AmbiguityAndExtractionLimits(t *testing.T) {
	imports := testGraph(t, map[string]string{"main.ts": "import { target } from './b';\nexport function run(){ target(); }", "b.ts": "export function target(){}", "b.js": "export function other(){}"}, map[string]string{"main.ts": "typescript", "b.ts": "typescript", "b.js": "javascript"})
	if len(imports.Calls) != 1 || imports.Calls[0].Class != "candidate" || len(imports.Calls[0].Targets) != 1 {
		t.Fatal("ambiguous import incorrectly resolved", imports.Calls)
	}
	blocks := testGraph(t, map[string]string{"a.js": "function run() { if (true) { function hidden() {} } hidden(); }\nlet dynamic; function call() { dynamic(); }\n"}, map[string]string{"a.js": "javascript"})
	for _, c := range blocks.Calls {
		if c.Target == "hidden" && c.Class == "resolved" {
			t.Fatal("block-local function escaped scope", c)
		}
		if c.Target == "dynamic" && c.Class == "resolved" {
			t.Fatal("unknown callable value resolved", c)
		}
	}
	m := testGraph(t, map[string]string{"a/a.go": "package a\nfunc Same(){}\nfunc Run(){ Same() }\n", "a/b.go": "package a\nfunc Same(){}\n"}, map[string]string{"a/a.go": "go", "a/b.go": "go"})
	if len(m.Calls) != 1 || m.Calls[0].Class != "candidate" || len(m.Calls[0].Targets) != 2 {
		t.Fatalf("ambiguous build declarations: %+v", m.Calls)
	}
	parser := testParser(t)
	source := []byte("package p\nimport (\"fmt\";\"os\")\nfunc A(){ B(); B() }\nfunc B(){}\n")
	for _, which := range []string{"symbols", "calls", "imports", "references"} {
		p := DefaultPolicy()
		raw := source
		switch which {
		case "symbols":
			p.MaxSymbols = 1
		case "calls":
			p.MaxCalls = 1
		case "imports":
			p.MaxImports = 1
		case "references":
			p.MaxReferences = 1
			raw = []byte("package p\ntype A struct{}\nvar x A\nvar y A\n")
		}
		if _, e := parser.Parse(context.Background(), "go", raw, p); !errors.Is(e, ErrBudget) {
			t.Fatalf("%s limit %v", which, e)
		}
	}
	p := DefaultPolicy()
	p.MaxChunks = 1
	f := parseTest(t, "go", string(source))
	if _, _, e := materialize(hash("index"), File{ID: hash("file"), Language: "go"}, source, f, p); !errors.Is(e, ErrBudget) {
		t.Fatalf("chunk limit %v", e)
	}
	p = DefaultPolicy()
	p.MaxRows = 1
	u, _, e := materialize(hash("index"), File{ID: hash("f"), Path: "x.go", Language: "go", Package: "p"}, source, f, DefaultPolicy())
	if e != nil {
		t.Fatal(e)
	}
	if e = relationships(context.Background(), &Manifest{Index: hash("i"), Symbols: u.symbols}, []unit{u}, p); !errors.Is(e, ErrBudget) {
		t.Fatal("graph budget", e)
	}
}
