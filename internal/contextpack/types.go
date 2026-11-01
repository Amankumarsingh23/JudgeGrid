// Package contextpack builds private, verified evidence; it never executes it.
package contextpack

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"math"
)

var (
	ErrDenied    = ingest.ErrDenied
	ErrIntegrity = errors.New("context integrity failure")
	ErrBudget    = errors.New("context budget exceeded")
	ErrModel     = errors.New("context model unavailable")
)

func canonical(v any) []byte { b, _ := json.Marshal(v); return b }
func hash(v any) string      { return ingest.Digest(canonical(v)) }
func finite(x float64) bool  { return !math.IsNaN(x) && !math.IsInf(x, 0) }

type Policy struct {
	Version        string  `json:"version"`
	Implementation string  `json:"implementation"`
	Model          string  `json:"model"`
	Tokenizer      string  `json:"tokenizer"`
	RetrievalMode  string  `json:"retrieval_mode"`
	Candidates     int     `json:"candidates"`
	ChannelDepth   int     `json:"channel_depth"`
	WindowTokens   int     `json:"window_tokens"`
	WindowOverlap  int     `json:"window_overlap"`
	WindowsPerDoc  int     `json:"windows_per_document"`
	MaxPairs       int     `json:"max_pairs"`
	GraphDepth     int     `json:"graph_depth"`
	GraphSeeds     int     `json:"graph_seeds"`
	Fanout         int     `json:"fanout"`
	Expanded       int     `json:"expanded"`
	TraversalEdges int     `json:"traversal_edges"`
	MaxBytes       int     `json:"max_source_bytes"`
	Lambda         float64 `json:"mmr_lambda"`
	MaxBlocks      int     `json:"max_blocks"`
	Tokens         int     `json:"context_tokens"`
	Seconds        int     `json:"seconds"`
	Baseline       bool    `json:"hybrid_only"`
}

func DefaultPolicy() Policy {
	return Policy{"context-v1", ImplementationIdentity(), ModelIdentity(), TokenizerIdentity(), "hybrid", 24, 100, 320, 48, 4, 128, 2, 8, 3, 12, 30000, 512 << 10, .75, 6, 4096, 180, false}
}
func (p Policy) ID() string { return hash(p) }
func (p Policy) Validate() error {
	if p.Version != "context-v1" || p.Implementation != ImplementationIdentity() || p.Model != ModelIdentity() || p.Tokenizer != TokenizerIdentity() || (p.RetrievalMode != "hybrid" && p.RetrievalMode != "bm25" && p.RetrievalMode != "dense") || p.Candidates < 1 || p.Candidates > 32 || p.ChannelDepth < p.Candidates || p.ChannelDepth > 200 || p.WindowTokens < 32 || p.WindowTokens > 320 || p.WindowOverlap < 0 || p.WindowOverlap > p.WindowTokens/3 || p.WindowsPerDoc < 1 || p.WindowsPerDoc > 6 || p.MaxPairs < p.Candidates || p.MaxPairs > 160 || p.GraphDepth < 0 || p.GraphDepth > 2 || p.GraphSeeds < 0 || p.GraphSeeds > 12 || p.Fanout < 1 || p.Fanout > 6 || p.Expanded < 0 || p.Expanded > 16 || p.TraversalEdges < 1 || p.TraversalEdges > 40000 || p.MaxBytes < 1 || p.MaxBytes > 1<<20 || !finite(p.Lambda) || p.Lambda < 0 || p.Lambda > 1 || p.MaxBlocks < 1 || p.MaxBlocks > 10 || p.Tokens < 128 || p.Tokens > 8192 || p.Seconds < 1 || p.Seconds > 300 {
		return ErrBudget
	}
	return nil
}

type Request struct {
	Repository string `json:"repository"`
	Snapshot   string `json:"snapshot"`
	Index      string `json:"index"`
	Query      string `json:"query"`
	Intent     string `json:"intent,omitempty"`
	Policy     Policy `json:"policy"`
	Baseline   bool   `json:"hybrid_only,omitempty"`
}
type Reason struct {
	Seed  string        `json:"seed"`
	Edge  string        `json:"edge"`
	Kind  string        `json:"kind"`
	Class string        `json:"class"`
	Depth int           `json:"depth"`
	Span  analysis.Span `json:"edge_span"`
}
type Window struct {
	Start  int     `json:"start"`
	End    int     `json:"end"`
	Tokens int     `json:"tokens"`
	Score  float64 `json:"score"`
}
type Scored struct {
	ID                string   `json:"id"`
	Windows           []Window `json:"windows"`
	TotalWindows      int      `json:"total_windows"`
	CoveredBytes      int      `json:"covered_bytes"`
	Truncated         bool     `json:"truncated"`
	MetadataTruncated bool     `json:"metadata_truncated"`
}
type Input struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Symbol   string `json:"symbol"`
	Language string `json:"language"`
	Text     string `json:"text"`
}
type ScoreReply struct {
	Documents      []Scored `json:"documents"`
	Pairs          int      `json:"pairs"`
	Load           float64  `json:"load_seconds"`
	Inference      float64  `json:"inference_seconds"`
	Tokenize       float64  `json:"tokenize_seconds"`
	QueryTruncated bool     `json:"query_truncated"`
}
type Candidate struct {
	Chunk         analysis.Chunk `json:"chunk"`
	Path          string         `json:"path"`
	Symbol        string         `json:"symbol"`
	Parse         string         `json:"parse_status"`
	Direct        bool           `json:"direct"`
	BaselineRank  int            `json:"baseline_rank"`
	LexicalRank   int            `json:"lexical_rank"`
	DenseRank     int            `json:"dense_rank"`
	RRF           float64        `json:"rrf"`
	Graph         []Reason       `json:"graph"`
	Scoring       Scored         `json:"scoring"`
	RerankerScore float64        `json:"reranker_score"`
	Relevance     float64        `json:"relevance"`
	Rerank        int            `json:"rerank"`
	SourceType    string         `json:"source_type"`
	WindowSpan    analysis.Span  `json:"window_span"`
}
type Evidence struct {
	ID            string        `json:"evidence_id"`
	ChunkID       string        `json:"chunk_id"`
	FileID        string        `json:"file_id"`
	SymbolID      string        `json:"symbol_id"`
	Path          string        `json:"path"`
	Symbol        string        `json:"symbol"`
	Language      string        `json:"language"`
	Span          analysis.Span `json:"span"`
	SHA256        string        `json:"sha256"`
	ChunkSHA256   string        `json:"chunk_sha256"`
	Source        string        `json:"source"`
	Direct        bool          `json:"direct"`
	Graph         []Reason      `json:"graph"`
	BaselineRank  int           `json:"baseline_rank"`
	LexicalRank   int           `json:"lexical_rank"`
	DenseRank     int           `json:"dense_rank"`
	RRF           float64       `json:"rrf"`
	RerankerScore float64       `json:"reranker_score"`
	Rerank        int           `json:"rerank"`
	Selection     string        `json:"selection"`
	Parse         string        `json:"parse_status"`
	Partial       bool          `json:"partial_span"`
}
type Omission struct {
	Chunk  string `json:"chunk"`
	Reason string `json:"reason"`
}
type Scope struct {
	Owner             string `json:"owner"`
	Repository        string `json:"repository"`
	Snapshot          string `json:"snapshot"`
	Commit            string `json:"commit"`
	Analysis          string `json:"analysis"`
	AnalysisManifest  string `json:"analysis_manifest"`
	Retrieval         string `json:"retrieval"`
	RetrievalPolicy   string `json:"retrieval_policy"`
	RetrievalManifest string `json:"retrieval_manifest"`
	ContextPolicy     string `json:"context_policy"`
	QueryDigest       string `json:"query_digest"`
}
type Packing struct {
	Text     string     `json:"text"`
	Evidence []Evidence `json:"evidence"`
	Omitted  []Omission `json:"omitted"`
	Tokens   int        `json:"tokens"`
}
type Diagnostics struct {
	Scope          float64 `json:"scope_verification_seconds"`
	RerankWall     float64 `json:"reranker_wall_seconds"`
	Retrieval      float64 `json:"retrieval_seconds"`
	Graph          float64 `json:"graph_seconds"`
	Verification   float64 `json:"verification_seconds"`
	ModelLoad      float64 `json:"model_load_seconds"`
	Inference      float64 `json:"inference_seconds"`
	Tokenize       float64 `json:"tokenize_seconds"`
	Assembly       float64 `json:"assembly_seconds"`
	Total          float64 `json:"total_seconds"`
	Pairs          int     `json:"scored_pairs"`
	Expanded       int     `json:"expanded"`
	Edges          int     `json:"traversed_edges"`
	QueryTruncated bool    `json:"query_truncated"`
}
type Pack struct {
	Schema      int         `json:"schema"`
	ID          string      `json:"pack_id"`
	Scope       Scope       `json:"scope"`
	Policy      Policy      `json:"policy"`
	Status      string      `json:"status"`
	Intent      string      `json:"intent"`
	Budget      int         `json:"token_budget"`
	Consumed    int         `json:"consumed_tokens"`
	Remaining   int         `json:"remaining_tokens"`
	Context     string      `json:"context"`
	Evidence    []Evidence  `json:"evidence"`
	Candidates  []Candidate `json:"candidates"`
	Omitted     []Omission  `json:"omitted"`
	Uncertainty []string    `json:"uncertainty"`
	Diagnostics Diagnostics `json:"diagnostics"`
}
type Engine interface {
	Identity() string
	TokenizerIdentity() string
	Score(context.Context, string, []Input, Policy) (ScoreReply, error)
	Pack(context.Context, Scope, []Evidence, int) (Packing, error)
	Count(context.Context, []string) ([]int, error)
}
type ContextBuilder interface {
	Build(context.Context, string, Request) (Pack, error)
	Verify(context.Context, string, Pack) error
}
type Reader interface{ retrieval.Reader }
