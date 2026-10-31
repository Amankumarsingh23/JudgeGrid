// Package analysis derives private syntax facts from authorized immutable snapshots.
package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"time"
)

var (
	ErrDenied    = ingest.ErrDenied
	ErrIntegrity = errors.New("analysis integrity failure")
	ErrBudget    = errors.New("analysis budget exceeded")
	ErrParser    = errors.New("trusted parser failed")
	ErrFence     = errors.New("analysis claim expired or replaced")
	ErrBusy      = errors.New("analysis busy or retry unavailable")
)

func canonical(v any) []byte { b, _ := json.Marshal(v); return b }
func hash(v any) string      { return ingest.Digest(canonical(v)) }

type Policy struct {
	Version       string `json:"version"`
	Parser        string `json:"parser_identity"`
	MaxFileBytes  int    `json:"max_file_bytes"`
	MaxTotalBytes int    `json:"max_total_bytes"`
	ParseMillis   int    `json:"parse_milliseconds"`
	MaxNodes      int    `json:"max_nodes"`
	MaxDepth      int    `json:"max_depth"`
	MaxSymbols    int    `json:"max_symbols_per_file"`
	MaxCalls      int    `json:"max_calls_per_file"`
	MaxImports    int    `json:"max_imports_per_file"`
	MaxReferences int    `json:"max_references_per_file"`
	MaxChunks     int    `json:"max_chunks_per_file"`
	ChunkBytes    int    `json:"chunk_bytes"`
	Overlap       int    `json:"overlap_bytes"`
	MaxRows       int    `json:"max_index_rows"`
	MaxFiles      int    `json:"max_files"`
	MaxSeconds    int    `json:"max_seconds"`
	Parallel      int    `json:"parallel_files"`
	Batch         int    `json:"database_batch"`
}

func DefaultPolicy() Policy {
	return Policy{"structure-v1", ParserIdentity(), 1 << 20, 32 << 20, 3000, 200000, 256, 2000, 5000, 1024, 5000, 2000, 8192, 128, 150000, 20000, 300, 1, 128}
}
func (p Policy) ID() string { return hash(p) }
func (p Policy) Validate() error {
	if p.Version != "structure-v1" || p.Parser != ParserIdentity() || p.MaxFileBytes < 1 || p.MaxFileBytes > 1<<20 || p.MaxTotalBytes < 1 || p.MaxTotalBytes > 32<<20 || p.ParseMillis < 1 || p.ParseMillis > 10000 || p.MaxNodes < 1 || p.MaxNodes > 300000 || p.MaxDepth < 1 || p.MaxDepth > 512 || p.MaxSymbols < 1 || p.MaxSymbols > 5000 || p.MaxCalls < 1 || p.MaxCalls > 10000 || p.MaxImports < 1 || p.MaxImports > 2000 || p.MaxReferences < 1 || p.MaxReferences > 10000 || p.MaxChunks < 1 || p.MaxChunks > 5000 || p.ChunkBytes < 64 || p.ChunkBytes > 16384 || p.Overlap < 0 || p.Overlap > p.ChunkBytes/4 || p.MaxRows < 1 || p.MaxRows > 200000 || p.MaxFiles < 1 || p.MaxFiles > 20000 || p.MaxSeconds < 1 || p.MaxSeconds > 600 || p.Parallel != 1 || p.Batch < 1 || p.Batch > 128 {
		return ErrBudget
	}
	return nil
}

type Span struct {
	Start       int `json:"start"`
	End         int `json:"end"`
	StartLine   int `json:"start_line"`
	StartColumn int `json:"start_column"`
	EndLine     int `json:"end_line"`
	EndColumn   int `json:"end_column"`
}
type LocalSymbol struct {
	ScopeStart  int    `json:"scope_start"`
	ScopeEnd    int    `json:"scope_end"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	NameStart   int    `json:"name_start"`
	NameEnd     int    `json:"name_end"`
	Start       int    `json:"start"`
	End         int    `json:"end"`
	Parent      int    `json:"parent"`
	Receiver    string `json:"receiver"`
	ReceiverVar string `json:"receiver_var"`
	Export      string `json:"export"`
	Partial     bool   `json:"partial"`
}
type LocalImport struct {
	Module string `json:"module"`
	Name   string `json:"name"`
	Alias  string `json:"alias"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}
type LocalCall struct {
	Target  string `json:"target"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Parent  int    `json:"parent"`
	Partial bool   `json:"partial"`
}
type Binding struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	ScopeStart int    `json:"scope_start"`
	ScopeEnd   int    `json:"scope_end"`
	Receiver   bool   `json:"receiver"`
}
type LocalReference struct {
	Name   string `json:"name"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Parent int    `json:"parent"`
}
type ParseError struct {
	Kind  string `json:"kind"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}
type Facts struct {
	Schema     int              `json:"schema"`
	Status     string           `json:"status"`
	Package    string           `json:"package"`
	Nodes      int              `json:"nodes"`
	Symbols    []LocalSymbol    `json:"symbols"`
	Imports    []LocalImport    `json:"imports"`
	Calls      []LocalCall      `json:"calls"`
	Bindings   []Binding        `json:"bindings"`
	References []LocalReference `json:"references"`
	Errors     []ParseError     `json:"errors"`
}
type File struct {
	ID       string       `json:"id"`
	SourceID string       `json:"source_id"`
	Path     string       `json:"path"`
	Language string       `json:"language"`
	Grammar  string       `json:"grammar"`
	SHA256   string       `json:"sha256"`
	Size     int64        `json:"size"`
	State    string       `json:"state"`
	Reason   string       `json:"reason"`
	Package  string       `json:"package"`
	Cache    string       `json:"cache_identity,omitempty"`
	Errors   []ParseError `json:"errors"`
}
type Symbol struct {
	Scope       Span   `json:"scope"`
	ID          string `json:"id"`
	File        string `json:"file"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Qualified   string `json:"qualified_name"`
	Parent      string `json:"parent,omitempty"`
	Receiver    string `json:"receiver,omitempty"`
	ReceiverVar string `json:"receiver_var,omitempty"`
	Export      string `json:"export"`
	State       string `json:"state"`
	Span        Span   `json:"span"`
	NameSpan    Span   `json:"name_span"`
	SHA256      string `json:"sha256"`
}
type Import struct {
	ID      string   `json:"id"`
	File    string   `json:"file"`
	Module  string   `json:"module"`
	Name    string   `json:"name"`
	Alias   string   `json:"alias"`
	Class   string   `json:"class"`
	Span    Span     `json:"span"`
	Targets []string `json:"targets"`
}
type Call struct {
	ID      string   `json:"id"`
	File    string   `json:"file"`
	Parent  string   `json:"parent,omitempty"`
	Target  string   `json:"target"`
	Class   string   `json:"class"`
	Reason  string   `json:"reason"`
	Span    Span     `json:"span"`
	Targets []string `json:"targets"`
}
type Edge struct {
	ID         string `json:"id"`
	File       string `json:"file"`
	From       string `json:"from"`
	Kind       string `json:"kind"`
	Class      string `json:"class"`
	TargetKind string `json:"target_kind"`
	Target     string `json:"target"`
	Span       Span   `json:"span"`
}
type Chunk struct {
	ID       string `json:"id"`
	File     string `json:"file"`
	Symbol   string `json:"symbol,omitempty"`
	Parent   string `json:"parent,omitempty"`
	Kind     string `json:"kind"`
	Language string `json:"language"`
	Span     Span   `json:"span"`
	SHA256   string `json:"sha256"`
}
type Manifest struct {
	Schema         int      `json:"schema"`
	Index          string   `json:"index"`
	Repository     string   `json:"repository"`
	Snapshot       string   `json:"snapshot"`
	SourceManifest string   `json:"source_manifest"`
	Commit         string   `json:"commit"`
	Policy         string   `json:"policy"`
	Files          []File   `json:"files"`
	Symbols        []Symbol `json:"symbols"`
	Imports        []Import `json:"imports"`
	Calls          []Call   `json:"calls"`
	Edges          []Edge   `json:"edges"`
	Chunks         []Chunk  `json:"chunks"`
}
type Stats struct {
	Files       map[string]int `json:"files"`
	Languages   map[string]int `json:"languages"`
	Symbols     map[string]int `json:"symbols"`
	Calls       map[string]int `json:"calls"`
	Imports     map[string]int `json:"imports"`
	Edges       map[string]int `json:"edges"`
	Chunks      int            `json:"chunks"`
	ChunkBytes  int64          `json:"chunk_bytes"`
	SourceBytes int64          `json:"source_bytes"`
	CacheHits   int            `json:"cache_hits"`
	CacheMisses int            `json:"cache_misses"`
	Seconds     float64        `json:"seconds"`
}
type Index struct {
	ID         string `json:"index_id"`
	Snapshot   string `json:"snapshot_id"`
	Repository string `json:"repository_id"`
	Policy     string `json:"policy_id"`
	State      string `json:"state"`
	Generation int64  `json:"generation"`
	Manifest   string `json:"manifest_sha256"`
	Failure    string `json:"failure_code"`
	Stats      Stats  `json:"stats"`
	Replayed   bool   `json:"replayed"`
	Attempt    *Stats `json:"attempt,omitempty"`
}
type Claim struct {
	Index Index
	Token string
	TTL   time.Duration
}
type VerifiedChunk struct {
	Index Index
	Chunk Chunk
	File  File
	Bytes []byte
}
type VerifiedSymbol struct {
	Index  Index
	Symbol Symbol
	File   File
	Bytes  []byte
}
type Reader interface {
	Ready(context.Context, string, string) (Index, Manifest, error)
	ReadChunk(context.Context, string, string, string) (VerifiedChunk, error)
	ReadSymbol(context.Context, string, string, string) (VerifiedSymbol, error)
}

func indexID(snapshot, policy string) string { return hash([]string{"analysis-v1", snapshot, policy}) }
