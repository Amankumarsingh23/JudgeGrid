package contextpack

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"os"
	"path/filepath"
	"time"
)

//go:embed helper/context.py helper/model.lock.json helper/requirements.lock ranking.go graph.go builder.go
var trusted embed.FS

func digestFile(name string) string {
	b, _ := trusted.ReadFile(name)
	return ingest.Digest(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")))
}
func ModelIdentity() string {
	return hash([]string{"cross-encoder-v1", digestFile("helper/context.py"), digestFile("helper/model.lock.json"), digestFile("helper/requirements.lock")})
}
func TokenizerIdentity() string {
	var m struct {
		Artifacts map[string]struct {
			SHA string `json:"sha256"`
		} `json:"artifacts"`
	}
	b, _ := trusted.ReadFile("helper/model.lock.json")
	_ = json.Unmarshal(b, &m)
	return hash([]string{"wordpiece-context-wire-v1", m.Artifacts["tokenizer.json"].SHA, digestFile("helper/context.py")})
}
func ImplementationIdentity() string {
	return hash([]string{"context-v1", digestFile("ranking.go"), digestFile("graph.go"), digestFile("builder.go")})
}

type LocalEngine struct{ Python, Root, dir string }

func OpenEngine(python, root string) (*LocalEngine, error) {
	if !filepath.IsAbs(python) || !filepath.IsAbs(root) {
		return nil, ErrModel
	}
	dir, e := os.MkdirTemp("", "judgegrid-context-helper-")
	if e != nil {
		return nil, e
	}
	m := &LocalEngine{python, root, dir}
	for _, name := range []string{"context.py", "model.lock.json"} {
		b, _ := trusted.ReadFile("helper/" + name)
		if e = os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			m.Close()
			return nil, e
		}
	}
	return m, nil
}
func (m *LocalEngine) Close() {
	if m != nil {
		_ = os.RemoveAll(m.dir)
	}
}
func (m *LocalEngine) Identity() string          { return ModelIdentity() }
func (m *LocalEngine) TokenizerIdentity() string { return TokenizerIdentity() }
func (m *LocalEngine) call(ctx context.Context, v map[string]any, out any) error {
	v["root"] = m.Root
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	b, e := ingest.RunTrustedModel(ctx, m.Python, []string{"-I", "-B", filepath.Join(m.dir, "context.py")}, canonical(v), 2<<20)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrModel
	}
	if json.Unmarshal(b, out) != nil {
		return ErrIntegrity
	}
	return nil
}
func (m *LocalEngine) Score(ctx context.Context, q string, docs []Input, p Policy) (ScoreReply, error) {
	var out ScoreReply
	e := m.call(ctx, map[string]any{"action": "score", "query": q, "documents": docs, "policy": p}, &out)
	return out, e
}
func (m *LocalEngine) Pack(ctx context.Context, scope Scope, blocks []Evidence, budget int) (Packing, error) {
	var out Packing
	e := m.call(ctx, map[string]any{"action": "pack", "scope": scope, "blocks": blocks, "budget": budget}, &out)
	return out, e
}
func (m *LocalEngine) Count(ctx context.Context, texts []string) ([]int, error) {
	var out []int
	e := m.call(ctx, map[string]any{"action": "count", "texts": texts}, &out)
	return out, e
}
