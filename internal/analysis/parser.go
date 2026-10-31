package analysis

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
)

//go:embed helper/parse.py helper/lock.json helper/requirements.lock graph.go chunks.go
var helper embed.FS

func ParserIdentity() string {
	a, _ := helper.ReadFile("helper/parse.py")
	b, _ := helper.ReadFile("helper/lock.json")
	graph, _ := helper.ReadFile("graph.go")
	chunks, _ := helper.ReadFile("chunks.go")
	digest := func(b []byte) string { return ingest.Digest(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))) }
	return hash([]string{"cst-extractor-v1", digest(a), digest(b), digest(graph), digest(chunks)})
}
func Grammar(language, path string) string {
	if language == "typescript" && strings.HasSuffix(path, ".tsx") {
		return "tsx"
	}
	switch language {
	case "go", "python", "javascript", "typescript":
		return language
	}
	return ""
}

type Parser struct {
	Python string
	dir    string
}

func OpenParser(python string) (*Parser, error) {
	if !filepath.IsAbs(python) {
		return nil, ErrParser
	}
	d, e := os.MkdirTemp("", "judgegrid-trusted-parser-")
	if e != nil {
		return nil, e
	}
	p := &Parser{python, d}
	for _, name := range []string{"parse.py", "lock.json"} {
		b, _ := helper.ReadFile("helper/" + name)
		b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
		if e = os.WriteFile(filepath.Join(d, name), b, 0600); e != nil {
			p.Close()
			return nil, e
		}
	}
	return p, nil
}
func (p *Parser) Close() {
	if p != nil && p.dir != "" {
		_ = os.RemoveAll(p.dir)
		p.dir = ""
	}
}
func (p *Parser) Parse(ctx context.Context, language string, source []byte, policy Policy) (Facts, error) {
	if e := policy.Validate(); e != nil {
		return Facts{}, e
	}
	if len(source) > policy.MaxFileBytes || !utf8.Valid(source) {
		return Facts{}, ErrBudget
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(policy.ParseMillis)*time.Millisecond)
	defer cancel()
	input := canonical(struct {
		Language string `json:"language"`
		Source   []byte `json:"source"`
		Policy   Policy `json:"policy"`
	}{language, source, policy})
	b, e := ingest.RunTrustedHelper(ctx, p.Python, []string{"-I", "-B", filepath.Join(p.dir, "parse.py")}, input, 8<<20)
	if e != nil {
		return Facts{}, ErrParser
	}
	var failure struct {
		Failure string `json:"failure"`
	}
	_ = json.Unmarshal(b, &failure)
	if failure.Failure == "budget" {
		return Facts{}, ErrBudget
	}
	if failure.Failure != "" {
		return Facts{}, ErrParser
	}
	var f Facts
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&f); e != nil {
		return Facts{}, ErrParser
	}
	if e = validateFacts(f, source, policy); e != nil {
		return Facts{}, e
	}
	return f, nil
}
func validateFacts(f Facts, b []byte, p Policy) error {
	if f.Schema != 1 || (f.Status != "PARSED" && f.Status != "PARTIAL") || f.Nodes < 1 || f.Nodes > p.MaxNodes || len(f.Symbols) > p.MaxSymbols || len(f.Calls) > p.MaxCalls || len(f.Imports) > p.MaxImports || len(f.References) > p.MaxReferences || len(f.Errors) > 64 || len(f.Bindings) > p.MaxNodes {
		return ErrIntegrity
	}
	valid := func(s, e int) bool {
		return s >= 0 && e >= s && e <= len(b) && (s == len(b) || utf8.RuneStart(b[s])) && (e == len(b) || utf8.RuneStart(b[e]))
	}
	for i, s := range f.Symbols {
		if !valid(s.ScopeStart, s.ScopeEnd) || s.Start < s.ScopeStart || s.End > s.ScopeEnd {
			return ErrIntegrity
		}
		if !valid(s.Start, s.End) || !valid(s.NameStart, s.NameEnd) || s.NameStart < s.Start || s.NameEnd > s.End || s.Name != string(b[s.NameStart:s.NameEnd]) || s.Parent < -1 || s.Parent >= i {
			return ErrIntegrity
		}
	}
	for _, c := range f.Calls {
		if !valid(c.Start, c.End) || c.Target != string(b[c.Start:c.End]) || c.Parent < -1 || c.Parent >= len(f.Symbols) {
			return ErrIntegrity
		}
	}
	for _, i := range f.Imports {
		if !valid(i.Start, i.End) {
			return ErrIntegrity
		}
	}
	for _, r := range f.References {
		if !valid(r.Start, r.End) || r.Name != string(b[r.Start:r.End]) || r.Parent < -1 || r.Parent >= len(f.Symbols) {
			return ErrIntegrity
		}
	}
	for _, v := range f.Bindings {
		if !valid(v.Start, v.End) || !valid(v.ScopeStart, v.ScopeEnd) || v.Name != string(b[v.Start:v.End]) {
			return ErrIntegrity
		}
	}
	for _, e := range f.Errors {
		if !valid(e.Start, e.End) {
			return ErrIntegrity
		}
	}
	return nil
}
