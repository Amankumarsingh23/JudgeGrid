// Package retrieval searches one owner-authorized immutable analysis index.
package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"math"
)

var (
	ErrDenied    = ingest.ErrDenied
	ErrIntegrity = errors.New("retrieval integrity failure")
	ErrBudget    = errors.New("retrieval budget exceeded")
	ErrModel     = errors.New("trusted local model failed")
	ErrFence     = errors.New("retrieval claim fenced")
	ErrBusy      = errors.New("retrieval claim unavailable")
)

func canonical(v any) []byte { b, _ := json.Marshal(v); return b }
func hash(v any) string      { return ingest.Digest(canonical(v)) }

type Policy struct {
	Version        string     `json:"version"`
	Tokenizer      string     `json:"tokenizer"`
	Model          string     `json:"model"`
	Input          string     `json:"input"`
	Dimension      int        `json:"dimension"`
	K1             float64    `json:"k1"`
	B              float64    `json:"b"`
	Weights        [4]float64 `json:"weights"`
	RRF            int        `json:"rrf_k"`
	ChannelWeights [2]float64 `json:"channel_weights"`
	MaxDocuments   int        `json:"max_documents"`
	MaxBytes       int        `json:"max_bytes"`
	MaxSeconds     int        `json:"max_seconds"`
	Batch          int        `json:"batch"`
}

func DefaultPolicy() Policy {
	return Policy{"retrieval-v1", TokenizerIdentity(), ModelIdentity(), "context-content-v1", 384, 1.2, .75, [4]float64{4, 2, 1.5, 1}, 60, [2]float64{1, 1}, 10000, 32 << 20, 1800, 32}
}
func (p Policy) ID() string { return hash(p) }
func (p Policy) Validate() error {
	if p.Version != "retrieval-v1" || p.Tokenizer != TokenizerIdentity() || p.Model != ModelIdentity() || p.Input != "context-content-v1" || p.Dimension != 384 || !finite(p.K1) || p.K1 <= 0 || p.K1 > 4 || !finite(p.B) || p.B < 0 || p.B > 1 || p.RRF < 1 || p.RRF > 1000 || p.MaxDocuments < 1 || p.MaxDocuments > 10000 || p.MaxBytes < 1 || p.MaxBytes > 32<<20 || p.MaxSeconds < 1 || p.MaxSeconds > 1800 || p.Batch < 1 || p.Batch > 32 {
		return ErrBudget
	}
	for _, w := range p.Weights {
		if !finite(w) || w <= 0 || w > 10 {
			return ErrBudget
		}
	}
	for _, w := range p.ChannelWeights {
		if !finite(w) || w <= 0 || w > 10 {
			return ErrBudget
		}
	}
	return nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func ValidateVector(v []float32) error {
	if len(v) != 384 {
		return ErrIntegrity
	}
	n := 0.0
	for _, x := range v {
		if !finite(float64(x)) {
			return ErrIntegrity
		}
		n += float64(x) * float64(x)
	}
	if math.Abs(n-1) > 1e-4 {
		return ErrIntegrity
	}
	return nil
}

type Document struct {
	Chunk      analysis.Chunk    `json:"chunk"`
	Path       string            `json:"path"`
	Symbol     string            `json:"symbol"`
	State      string            `json:"parse_state"`
	InputHash  string            `json:"input_sha256"`
	VectorHash string            `json:"vector_sha256"`
	Fields     [4]map[string]int `json:"fields"`
	Lengths    [4]int            `json:"lengths"`
}
type Corpus struct {
	Count  int               `json:"count"`
	Totals [4]int            `json:"totals"`
	DF     [4]map[string]int `json:"document_frequency"`
}
type Exclusion struct {
	Chunk  string `json:"chunk"`
	Reason string `json:"reason"`
}
type Manifest struct {
	Schema           int         `json:"schema"`
	Index            string      `json:"index"`
	Repository       string      `json:"repository"`
	Snapshot         string      `json:"snapshot"`
	Commit           string      `json:"commit"`
	Analysis         string      `json:"analysis"`
	AnalysisManifest string      `json:"analysis_manifest"`
	Policy           string      `json:"policy"`
	Documents        []Document  `json:"documents"`
	Excluded         []Exclusion `json:"excluded"`
	Corpus           Corpus      `json:"corpus"`
}
type Stats struct {
	Documents int     `json:"documents"`
	Excluded  int     `json:"excluded"`
	Tokens    int     `json:"tokens"`
	Hits      int     `json:"cache_hits"`
	Misses    int     `json:"cache_misses"`
	Seconds   float64 `json:"seconds"`
}
type Index struct {
	ID               string  `json:"index_id"`
	Repository       string  `json:"repository_id"`
	Snapshot         string  `json:"snapshot_id"`
	Analysis         string  `json:"analysis_index_id"`
	AnalysisManifest string  `json:"analysis_manifest"`
	Policy           string  `json:"policy_id"`
	State            string  `json:"state"`
	Generation       int64   `json:"generation"`
	Manifest         string  `json:"manifest_sha256"`
	Failure          string  `json:"failure_code"`
	Stats            Stats   `json:"stats"`
	Replayed         bool    `json:"replayed"`
	Seconds          float64 `json:"attempt_seconds"`
}
type Filter struct {
	Path     string `json:"path,omitempty"`
	Language string `json:"language,omitempty"`
	State    string `json:"parse_state,omitempty"`
}
type Request struct {
	Index      string `json:"index"`
	Snapshot   string `json:"snapshot"`
	Repository string `json:"repository"`
	Query      string `json:"query"`
	Mode       string `json:"mode"`
	TopK       int    `json:"top_k"`
	Depth      int    `json:"candidate_depth"`
	ANN        bool   `json:"ann"`
	Filter     Filter `json:"filter"`
}
type Candidate struct {
	Document     Document `json:"document"`
	LexicalRank  int      `json:"lexical_rank"`
	DenseRank    int      `json:"dense_rank"`
	LexicalScore float64  `json:"bm25"`
	Distance     float64  `json:"cosine_distance"`
	Score        float64  `json:"score"`
}
type Results struct {
	Index      string      `json:"index"`
	Snapshot   string      `json:"snapshot"`
	Commit     string      `json:"commit"`
	Analysis   string      `json:"analysis"`
	Policy     string      `json:"policy"`
	Mode       string      `json:"mode"`
	ANN        bool        `json:"ann"`
	Fallback   bool        `json:"exact_fallback"`
	Candidates []Candidate `json:"candidates"`
	Seconds    float64     `json:"seconds"`
}
type Embedder interface {
	Identity() string
	Embed(context.Context, []string) ([][]float32, error)
}
type Reader interface {
	Ready(context.Context, string, string) (Index, Manifest, Policy, error)
	Search(context.Context, string, Request) (Results, error)
	ReadChunk(context.Context, string, string, string) (analysis.VerifiedChunk, error)
}
