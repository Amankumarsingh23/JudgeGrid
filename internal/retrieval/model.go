package retrieval

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

//go:embed helper/embed.py helper/model.lock.json helper/requirements.lock lexical.go dense.go
var trusted embed.FS

func ModelIdentity() string {
	a, _ := trusted.ReadFile("helper/embed.py")
	b, _ := trusted.ReadFile("helper/model.lock.json")
	r, _ := trusted.ReadFile("helper/requirements.lock")
	return hash([]string{"onnx-minilm-v1", ingest.Digest(bytes.ReplaceAll(a, []byte("\r\n"), []byte("\n"))), ingest.Digest(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))), ingest.Digest(bytes.ReplaceAll(r, []byte("\r\n"), []byte("\n")))})
}
func TokenizerIdentity() string {
	b, _ := trusted.ReadFile("lexical.go")
	d, _ := trusted.ReadFile("dense.go")
	return "code-fields-ranking-v1:" + hash([]string{ingest.Digest(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))), ingest.Digest(bytes.ReplaceAll(d, []byte("\r\n"), []byte("\n")))})
}

type LocalModel struct {
	Python, Root, helper string
	dir                  string
}

func (m *LocalModel) Identity() string { return ModelIdentity() }

func OpenModel(python, root string) (*LocalModel, error) {
	if !filepath.IsAbs(python) || !filepath.IsAbs(root) {
		return nil, ErrDenied
	}
	d, e := os.MkdirTemp("", "judgegrid-trusted-embedding-")
	if e != nil {
		return nil, e
	}
	m := &LocalModel{Python: python, Root: root, helper: filepath.Join(d, "embed.py"), dir: d}
	for _, name := range []string{"embed.py", "model.lock.json"} {
		b, _ := trusted.ReadFile("helper/" + name)
		if e = os.WriteFile(filepath.Join(d, name), b, 0600); e != nil {
			m.Close()
			return nil, e
		}
	}
	return m, nil
}
func (m *LocalModel) Close() {
	if m != nil {
		_ = os.RemoveAll(m.dir)
	}
}
func (m *LocalModel) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) < 1 || len(texts) > 32 {
		return nil, ErrBudget
	}
	for _, s := range texts {
		if len(s) == 0 || len(s) > 20000 {
			return nil, ErrBudget
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	b, e := ingest.RunTrustedModel(ctx, m.Python, []string{"-I", "-B", m.helper}, canonical(struct {
		Root  string   `json:"root"`
		Texts []string `json:"texts"`
	}{m.Root, texts}), 1<<20)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrModel
	}
	var out [][]float32
	if json.Unmarshal(b, &out) != nil || len(out) != len(texts) {
		return nil, ErrIntegrity
	}
	for _, v := range out {
		if ValidateVector(v) != nil {
			return nil, ErrIntegrity
		}
	}
	return out, nil
}
