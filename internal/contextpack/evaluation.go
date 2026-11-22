package contextpack

import (
	"bytes"
	"context"
	"embed"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed evaluation.go
var evaluationSource embed.FS

func EvaluationIdentity() string {
	b, _ := evaluationSource.ReadFile("evaluation.go")
	return ingest.Digest(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")))
}

type EvaluationRanking struct {
	Candidates  []Candidate `json:"candidates"`
	Diagnostics Diagnostics `json:"diagnostics"`
	Policy      Policy      `json:"policy"`
}

func mapDocuments(in []retrieval.Document) map[string]retrieval.Document {
	out := map[string]retrieval.Document{}
	for _, d := range in {
		out[d.Chunk.ID] = d
	}
	return out
}
func structRequest(r Request, p Policy) retrieval.Request {
	return retrieval.Request{Index: r.Index, Repository: r.Repository, Snapshot: r.Snapshot, Query: r.Query, Mode: p.RetrievalMode, TopK: p.Candidates, Depth: p.ChannelDepth}
}

// RankEvaluation observes the ranking stage with MMR and packing genuinely
// absent. It reuses Search, Expand, the actual Engine, rank and subspan from
// Session 15. Production Build and its immutable policy identity are unchanged.
func (b *Builder) RankEvaluation(ctx context.Context, owner string, r Request) (out EvaluationRanking, err error) {
	started := time.Now()
	defer func() { out.Diagnostics.Total = time.Since(started).Seconds() }()
	p := r.Policy
	out.Policy = p
	if p.Validate() != nil || p.Baseline || len(r.Query) > 1024 || strings.TrimSpace(r.Query) == "" || !utf8.ValidString(r.Query) || strings.ContainsRune(r.Query, 0) || b.Retrieval == nil || b.Analysis == nil {
		return out, ErrBudget
	}
	intent, e := Intent(r.Query, r.Intent)
	if e != nil {
		return out, e
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.Seconds)*time.Second)
	defer cancel()
	a, m, _, e := b.Retrieval.Ready(ctx, owner, r.Index)
	if e != nil {
		return out, e
	}
	if a.Repository != r.Repository || a.Snapshot != r.Snapshot {
		return out, ErrDenied
	}
	out.Diagnostics.Scope = time.Since(started).Seconds()
	tick := time.Now()
	results, e := b.Retrieval.Search(ctx, owner, structRequest(r, p))
	out.Diagnostics.Retrieval = time.Since(tick).Seconds()
	if e != nil {
		return out, e
	}
	if results.Index != a.ID || results.Snapshot != a.Snapshot || results.Commit != m.Commit || results.Analysis != a.Analysis || results.Policy != a.Policy {
		return out, ErrIntegrity
	}
	aa, am, e := b.Analysis.Ready(ctx, owner, a.Analysis)
	if e != nil {
		return out, e
	}
	if aa.Manifest != a.AnalysisManifest || am.Repository != a.Repository || am.Snapshot != a.Snapshot || am.Commit != m.Commit {
		return out, ErrIntegrity
	}
	docs := mapDocuments(m.Documents)
	candidates := []Candidate{}
	seen := map[string]bool{}
	for i, v := range results.Candidates {
		d, ok := docs[v.Document.Chunk.ID]
		if !ok || hash(d) != hash(v.Document) || seen[d.Chunk.ID] {
			return out, ErrIntegrity
		}
		seen[d.Chunk.ID] = true
		candidates = append(candidates, Candidate{Chunk: d.Chunk, Path: d.Path, Symbol: d.Symbol, Parse: d.State, Direct: true, BaselineRank: i + 1, LexicalRank: v.LexicalRank, DenseRank: v.DenseRank, RRF: v.Score, Graph: []Reason{}, SourceType: sourceType(d.Path)})
	}
	tick = time.Now()
	if p.GraphDepth > 0 && p.Expanded > 0 {
		expanded, n, _, x := Expand(ctx, am, m, candidates, p)
		if x != nil {
			return out, x
		}
		out.Diagnostics.Edges = n
		ids := []string{}
		for id := range expanded {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if !seen[id] {
				d, ok := docs[id]
				if !ok {
					return out, ErrIntegrity
				}
				seen[id] = true
				candidates = append(candidates, Candidate{Chunk: d.Chunk, Path: d.Path, Symbol: d.Symbol, Parse: d.State, SourceType: sourceType(d.Path)})
				out.Diagnostics.Expanded++
			}
		}
		for i := range candidates {
			candidates[i].Graph = expanded[candidates[i].Chunk.ID]
		}
	}
	out.Diagnostics.Graph = time.Since(tick).Seconds()
	if len(candidates) == 0 {
		return out, nil
	}
	sizes := map[string]int64{}
	for _, f := range am.Files {
		sizes[f.ID] = f.Size
	}
	files := map[string]bool{}
	planned, full := 0, int64(0)
	ids := []string{}
	for _, c := range candidates {
		ids = append(ids, c.Chunk.ID)
		planned += c.Chunk.Span.End - c.Chunk.Span.Start
		if !files[c.Chunk.File] {
			files[c.Chunk.File] = true
			full += sizes[c.Chunk.File]
		}
	}
	if planned > p.MaxBytes || full > int64(p.MaxBytes) {
		return out, ErrBudget
	}
	tick = time.Now()
	reads, e := b.read(ctx, owner, a.Analysis, a.ID, ids)
	out.Diagnostics.Verification = time.Since(tick).Seconds()
	if e != nil {
		return out, e
	}
	valid := []Candidate{}
	inputs := []Input{}
	ranges := map[string]bool{}
	for _, c := range candidates {
		v, ok := reads[c.Chunk.ID]
		if !ok || v.Index.ID != a.Analysis || v.Chunk != c.Chunk || v.File.Path != c.Path || v.File.State != c.Parse || ingest.Digest(v.Bytes) != c.Chunk.SHA256 || !utf8.Valid(v.Bytes) {
			return out, ErrIntegrity
		}
		key := hash([]any{c.Chunk.File, c.Chunk.Span.Start, c.Chunk.Span.End})
		if ranges[key] || strings.TrimSpace(string(v.Bytes)) == "" {
			continue
		}
		ranges[key] = true
		valid = append(valid, c)
		inputs = append(inputs, Input{ID: c.Chunk.ID, Path: c.Path, Symbol: c.Symbol, Language: c.Chunk.Language, Text: string(v.Bytes)})
	}
	candidates = valid
	if len(candidates) == 0 {
		return out, nil
	}
	if b.Engine == nil || b.Engine.Identity() != p.Model || b.Engine.TokenizerIdentity() != p.Tokenizer {
		return out, ErrModel
	}
	tick = time.Now()
	scored, e := b.Engine.Score(ctx, r.Query, inputs, p)
	out.Diagnostics.RerankWall = time.Since(tick).Seconds()
	if e != nil {
		return out, e
	}
	out.Diagnostics.ModelLoad = scored.Load
	out.Diagnostics.Inference = scored.Inference
	out.Diagnostics.Tokenize = scored.Tokenize
	out.Diagnostics.Pairs = scored.Pairs
	out.Diagnostics.QueryTruncated = scored.QueryTruncated
	by := map[string]Scored{}
	pairs := 0
	for _, v := range scored.Documents {
		if _, ok := by[v.ID]; ok {
			return out, ErrIntegrity
		}
		by[v.ID] = v
		pairs += len(v.Windows)
	}
	if len(by) != len(candidates) || pairs != scored.Pairs || pairs > p.MaxPairs {
		return out, ErrIntegrity
	}
	for i := range candidates {
		c := &candidates[i]
		v, ok := by[c.Chunk.ID]
		raw := reads[c.Chunk.ID].Bytes
		if !ok || len(v.Windows) < 1 || len(v.Windows) > p.WindowsPerDoc || v.TotalWindows < len(v.Windows) {
			return out, ErrIntegrity
		}
		best, covered, end := 0, 0, 0
		for j, w := range v.Windows {
			if w.Start < 0 || w.End <= w.Start || w.End > len(raw) || !utf8.Valid(raw[w.Start:w.End]) || w.Tokens < 1 || w.Tokens > 512 || !finite(w.Score) || (j > 0 && w.Start < v.Windows[j-1].Start) {
				return out, ErrIntegrity
			}
			covered += max(0, w.End-max(end, w.Start))
			end = max(end, w.End)
			if w.Score > v.Windows[best].Score {
				best = j
			}
		}
		if covered != v.CoveredBytes || v.Truncated != (covered < len(raw)) {
			return out, ErrIntegrity
		}
		c.Scoring = v
		c.RerankerScore = v.Windows[best].Score
		c.WindowSpan = subspan(c.Chunk.Span, raw, v.Windows[best].Start, v.Windows[best].End)
	}
	rank(candidates, r.Query, intent)
	out.Candidates = candidates
	return out, nil
}
