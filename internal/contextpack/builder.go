package contextpack

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Builder struct {
	Retrieval retrieval.Reader
	Analysis  analysis.Reader
	Engine    Engine
}

func packID(p Pack) string {
	return hash([]any{p.Scope, p.Policy, p.Status, p.Intent, p.Budget, p.Context})
}
func failure(p Pack, status string) Pack {
	p.Status = status
	p.Context = ""
	p.Evidence = []Evidence{}
	p.Consumed = 0
	p.Remaining = p.Budget
	p.Candidates = nil
	p.ID = packID(p)
	return p
}
func errorStatus(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "TIME_BUDGET_EXCEEDED"
	case errors.Is(err, ingest.ErrStorage):
		return "SOURCE_UNAVAILABLE"
	case errors.Is(err, ErrModel), errors.Is(err, retrieval.ErrModel):
		return "MODEL_UNAVAILABLE"
	case errors.Is(err, ErrBudget), errors.Is(err, ingest.ErrBudget):
		return "INSUFFICIENT_EVIDENCE"
	}
	return "INTEGRITY_FAILURE"
}
func statusFor(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "TIME_BUDGET_EXCEEDED"
	}
	return errorStatus(err)
}
func (b *Builder) Build(ctx context.Context, owner string, r Request) (result Pack, err error) {
	p := r.Policy
	if r.Baseline {
		p.Baseline = true
	}
	r.Baseline = p.Baseline
	if p.Validate() != nil || strings.TrimSpace(r.Query) == "" || len(r.Query) > 1024 || !utf8.ValidString(r.Query) || strings.IndexByte(r.Query, 0) >= 0 {
		return result, ErrBudget
	}
	intent, e := Intent(r.Query, r.Intent)
	if e != nil {
		return result, e
	}
	if b.Retrieval == nil || b.Analysis == nil {
		return result, ErrDenied
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.Seconds)*time.Second)
	defer cancel()
	ctx, end := telemetry.Begin(ctx, "context.build", "")
	defer func() { end(err) }()
	a, m, _, e := b.Retrieval.Ready(ctx, owner, r.Index)
	if e != nil {
		return result, e
	}
	if a.Repository != r.Repository || a.Snapshot != r.Snapshot {
		return result, ErrDenied
	}
	result = Pack{Schema: 1, Scope: Scope{owner, a.Repository, a.Snapshot, m.Commit, a.Analysis, a.AnalysisManifest, a.ID, a.Policy, a.Manifest, p.ID(), ingest.Digest([]byte(r.Query))}, Policy: p, Intent: intent, Budget: p.Tokens, Remaining: p.Tokens, Evidence: []Evidence{}, Omitted: []Omission{}, Uncertainty: []string{"syntax relationships are not runtime type proof", "reranker logits and sigmoid scores are not relevance probabilities"}}
	result.Diagnostics.Scope = time.Since(started).Seconds()
	defer func() {
		result.Diagnostics.Total = time.Since(started).Seconds()
		if result.Status != "" {
			result.ID = packID(result)
		}
	}()
	tick := time.Now()
	baseline, e := b.Retrieval.Search(ctx, owner, retrieval.Request{Index: a.ID, Repository: a.Repository, Snapshot: a.Snapshot, Query: r.Query, Mode: p.RetrievalMode, TopK: p.Candidates, Depth: p.ChannelDepth})
	result.Diagnostics.Retrieval = time.Since(tick).Seconds()
	if e != nil {
		if ctx.Err() != nil {
			e = ctx.Err()
		}
		if errors.Is(e, ErrDenied) {
			return Pack{}, e
		}
		return failure(result, statusFor(ctx, e)), nil
	}
	if baseline.Index != a.ID || baseline.Snapshot != a.Snapshot || baseline.Analysis != a.Analysis || baseline.Policy != a.Policy || baseline.Commit != m.Commit {
		return failure(result, "INTEGRITY_FAILURE"), nil
	}
	if len(baseline.Candidates) == 0 {
		return failure(result, "NO_CANDIDATES"), nil
	}
	if b.Engine == nil || b.Engine.Identity() != p.Model || b.Engine.TokenizerIdentity() != p.Tokenizer {
		return failure(result, "MODEL_UNAVAILABLE"), nil
	}
	docs := map[string]retrieval.Document{}
	for _, d := range m.Documents {
		docs[d.Chunk.ID] = d
	}
	candidates := []Candidate{}
	seen := map[string]bool{}
	add := func(d retrieval.Document, direct bool, rank int, lex, dense int, rrf float64) {
		candidates = append(candidates, Candidate{Chunk: d.Chunk, Path: d.Path, Symbol: d.Symbol, Parse: d.State, Direct: direct, BaselineRank: rank, LexicalRank: lex, DenseRank: dense, RRF: rrf, Graph: []Reason{}, SourceType: sourceType(d.Path)})
		seen[d.Chunk.ID] = true
	}
	for i, c := range baseline.Candidates {
		d, ok := docs[c.Document.Chunk.ID]
		if !ok || hash(d) != hash(c.Document) || seen[d.Chunk.ID] {
			return failure(result, "INTEGRITY_FAILURE"), nil
		}
		add(d, true, i+1, c.LexicalRank, c.DenseRank, c.Score)
	}
	tick = time.Now()
	aa, am, e := b.Analysis.Ready(ctx, owner, a.Analysis)
	if e != nil {
		return failure(result, statusFor(ctx, e)), nil
	}
	if aa.Manifest != a.AnalysisManifest || am.Index != a.Analysis || am.Repository != a.Repository || am.Snapshot != a.Snapshot || am.Commit != m.Commit {
		return failure(result, "INTEGRITY_FAILURE"), nil
	}
	if !r.Baseline {
		expanded, count, limited, x := Expand(ctx, am, m, candidates, p)
		if x != nil {
			return failure(result, statusFor(ctx, x)), nil
		}
		result.Diagnostics.Edges = count
		if limited {
			result.Uncertainty = append(result.Uncertainty, "graph expansion reached a policy bound")
		}
		ids := make([]string, 0, len(expanded))
		for id := range expanded {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if !seen[id] {
				d, ok := docs[id]
				if !ok {
					return failure(result, "INTEGRITY_FAILURE"), nil
				}
				add(d, false, 0, 0, 0, 0)
				result.Diagnostics.Expanded++
			}
		}
		for i := range candidates {
			if reasons, ok := expanded[candidates[i].Chunk.ID]; ok {
				candidates[i].Graph = reasons
			}
		}
	}
	result.Diagnostics.Graph = time.Since(tick).Seconds()
	tick = time.Now()
	ids := make([]string, len(candidates))
	fileSizes := map[string]int64{}
	for _, f := range am.Files {
		fileSizes[f.ID] = f.Size
	}
	fileSeen := map[string]bool{}
	planned := 0
	fullFiles := int64(0)
	for i, c := range candidates {
		ids[i] = c.Chunk.ID
		planned += c.Chunk.Span.End - c.Chunk.Span.Start
		if !fileSeen[c.Chunk.File] {
			fileSeen[c.Chunk.File] = true
			fullFiles += fileSizes[c.Chunk.File]
		}
	}
	if planned > p.MaxBytes || fullFiles > int64(p.MaxBytes) {
		return failure(result, "INSUFFICIENT_EVIDENCE"), nil
	}
	verified, e := b.read(ctx, owner, a.Analysis, a.ID, ids)
	result.Diagnostics.Verification = time.Since(tick).Seconds()
	if e != nil {
		return failure(result, statusFor(ctx, e)), nil
	}
	inputs := []Input{}
	valid := []Candidate{}
	bytes := 0
	ranges := map[string]bool{}
	for _, c := range candidates {
		v, ok := verified[c.Chunk.ID]
		if !ok || v.Index.ID != a.Analysis || v.Chunk != c.Chunk || v.File.Path != c.Path || v.File.State != c.Parse || ingest.Digest(v.Bytes) != c.Chunk.SHA256 || !utf8.Valid(v.Bytes) {
			return failure(result, "INTEGRITY_FAILURE"), nil
		}
		bytes += len(v.Bytes)
		if bytes > p.MaxBytes {
			return failure(result, "INSUFFICIENT_EVIDENCE"), nil
		}
		key := hash([]any{c.Chunk.File, c.Chunk.Span.Start, c.Chunk.Span.End})
		if ranges[key] {
			result.Omitted = append(result.Omitted, Omission{c.Chunk.ID, "duplicate_range"})
			continue
		}
		ranges[key] = true
		if strings.TrimSpace(string(v.Bytes)) == "" {
			result.Omitted = append(result.Omitted, Omission{c.Chunk.ID, "empty_source"})
			continue
		}
		valid = append(valid, c)
		inputs = append(inputs, Input{c.Chunk.ID, c.Path, c.Symbol, c.Chunk.Language, string(v.Bytes)})
	}
	candidates = valid
	if len(candidates) == 0 {
		return failure(result, "INSUFFICIENT_EVIDENCE"), nil
	}
	if r.Baseline {
		for i := range candidates {
			c := &candidates[i]
			c.WindowSpan = c.Chunk.Span
			c.Relevance = 1 / float64(i+1)
			c.Scoring = Scored{ID: c.Chunk.ID, Windows: []Window{{Start: 0, End: len(verified[c.Chunk.ID].Bytes)}}}
		}
	} else {
		if len(inputs) > p.MaxPairs {
			return failure(result, "INSUFFICIENT_EVIDENCE"), nil
		}
		scoreStart := time.Now()
		scored, x := b.Engine.Score(ctx, r.Query, inputs, p)
		result.Diagnostics.RerankWall = time.Since(scoreStart).Seconds()
		if x != nil {
			return failure(result, statusFor(ctx, x)), nil
		}
		result.Diagnostics.ModelLoad = scored.Load
		result.Diagnostics.Inference = scored.Inference
		result.Diagnostics.Tokenize = scored.Tokenize
		result.Diagnostics.Pairs = scored.Pairs
		result.Diagnostics.QueryTruncated = scored.QueryTruncated
		by := map[string]Scored{}
		pairCount := 0
		for _, s := range scored.Documents {
			if _, ok := by[s.ID]; ok {
				return failure(result, "INTEGRITY_FAILURE"), nil
			}
			by[s.ID] = s
			pairCount += len(s.Windows)
		}
		if len(by) != len(candidates) || pairCount != scored.Pairs || pairCount > p.MaxPairs {
			return failure(result, "INTEGRITY_FAILURE"), nil
		}
		for i := range candidates {
			c := &candidates[i]
			s, ok := by[c.Chunk.ID]
			v := verified[c.Chunk.ID]
			if !ok || len(s.Windows) == 0 || len(s.Windows) > p.WindowsPerDoc || s.TotalWindows < len(s.Windows) {
				return failure(result, "INTEGRITY_FAILURE"), nil
			}
			best := 0
			covered := 0
			pos := 0
			for j, w := range s.Windows {
				if w.Start < 0 || w.End <= w.Start || w.End > len(v.Bytes) || !utf8.Valid(v.Bytes[w.Start:w.End]) || w.Tokens < 1 || w.Tokens > 512 || !finite(w.Score) || (j > 0 && w.Start < s.Windows[j-1].Start) {
					return failure(result, "INTEGRITY_FAILURE"), nil
				}
				covered += max(0, w.End-max(pos, w.Start))
				pos = max(pos, w.End)
				if w.Score > s.Windows[best].Score {
					best = j
				}
			}
			if covered != s.CoveredBytes || s.Truncated != (covered < len(v.Bytes)) {
				return failure(result, "INTEGRITY_FAILURE"), nil
			}
			c.Scoring = s
			c.RerankerScore = s.Windows[best].Score
			c.WindowSpan = subspan(c.Chunk.Span, v.Bytes, s.Windows[best].Start, s.Windows[best].End)
		}
		rank(candidates, r.Query, intent)
	}
	result.Candidates = candidates
	selected, omitted := Select(candidates, p, intent)
	result.Omitted = append(result.Omitted, omitted...)
	blocks := []Evidence{}
	for i, c := range selected {
		v := verified[c.Chunk.ID]
		span := c.WindowSpan
		left, right := span.Start-c.Chunk.Span.Start, span.End-c.Chunk.Span.Start
		source := string(v.Bytes[left:right])
		reason := "mmr"
		if i == 0 && (intent == "implementation" || intent == "exact") {
			reason = "implementation-anchor"
		}
		if r.Baseline {
			reason = "hybrid-baseline"
		}
		blocks = append(blocks, Evidence{ID: "E" + strconv.Itoa(i+1), ChunkID: c.Chunk.ID, FileID: c.Chunk.File, SymbolID: c.Chunk.Symbol, Path: c.Path, Symbol: c.Symbol, Language: c.Chunk.Language, Span: span, SHA256: ingest.Digest([]byte(source)), ChunkSHA256: c.Chunk.SHA256, Source: source, Direct: c.Direct, Graph: c.Graph, BaselineRank: c.BaselineRank, LexicalRank: c.LexicalRank, DenseRank: c.DenseRank, RRF: c.RRF, RerankerScore: c.RerankerScore, Rerank: c.Rerank, Selection: reason, Parse: c.Parse, Partial: span != c.Chunk.Span})
	}
	tick = time.Now()
	packed, x := b.Engine.Pack(ctx, result.Scope, blocks, p.Tokens)
	result.Diagnostics.Assembly = time.Since(tick).Seconds()
	if x != nil {
		return failure(result, statusFor(ctx, x)), nil
	}
	result.Omitted = append(result.Omitted, packed.Omitted...)
	if packed.Tokens > p.Tokens || len(packed.Evidence) == 0 {
		return failure(result, "INSUFFICIENT_EVIDENCE"), nil
	}
	result.Context = packed.Text
	result.Evidence = packed.Evidence
	result.Consumed = packed.Tokens
	result.Remaining = p.Tokens - packed.Tokens
	result.Status = "READY"
	if weakEvidence(r.Query, selected) && !r.Baseline {
		result.Status = "INSUFFICIENT_EVIDENCE"
		result.Uncertainty = append(result.Uncertainty, "weak model and lexical identity evidence; heuristic abstention")
	}
	result.ID = packID(result)
	tick = time.Now()
	if e = b.Verify(ctx, owner, result); e != nil {
		return failure(result, statusFor(ctx, e)), nil
	}
	result.Diagnostics.Verification += time.Since(tick).Seconds()
	return result, nil
}
func (b *Builder) read(ctx context.Context, owner, analysisID, retrievalID string, ids []string) (map[string]analysis.VerifiedChunk, error) {
	out := map[string]analysis.VerifiedChunk{}
	if len(ids) > 48 {
		return nil, ErrBudget
	}
	if bulk, ok := b.Analysis.(interface {
		ReadChunks(context.Context, string, string, []string) ([]analysis.VerifiedChunk, error)
	}); ok {
		v, e := bulk.ReadChunks(ctx, owner, analysisID, ids)
		if e != nil {
			return nil, e
		}
		for _, c := range v {
			if _, ok := out[c.Chunk.ID]; ok {
				return nil, ErrIntegrity
			}
			out[c.Chunk.ID] = c
		}
	} else {
		for _, id := range ids {
			v, e := b.Retrieval.ReadChunk(ctx, owner, retrievalID, id)
			if e != nil {
				return nil, e
			}
			out[id] = v
		}
	}
	if len(out) != len(ids) {
		return nil, ErrIntegrity
	}
	return out, nil
}
func subspan(base analysis.Span, source []byte, left, right int) analysis.Span {
	point := func(n int) (int, int) {
		prefix := source[:n]
		line := base.StartLine + strings.Count(string(prefix), "\n")
		column := base.StartColumn + n
		if pos := strings.LastIndexByte(string(prefix), '\n'); pos >= 0 {
			column = n - pos - 1
		}
		return line, column
	}
	sl, sc := point(left)
	el, ec := point(right)
	return analysis.Span{Start: base.Start + left, End: base.Start + right, StartLine: sl, StartColumn: sc, EndLine: el, EndColumn: ec}
}
func (b *Builder) Verify(ctx context.Context, owner string, p Pack) error {
	if owner == "" || owner != p.Scope.Owner {
		return ErrDenied
	}
	if p.Schema != 1 || p.Policy.Validate() != nil || p.Scope.ContextPolicy != p.Policy.ID() || p.Budget != p.Policy.Tokens || p.Consumed < 0 || p.Consumed > p.Budget || p.Remaining != p.Budget-p.Consumed || p.ID != packID(p) || len(p.Evidence) > p.Policy.MaxBlocks {
		return ErrIntegrity
	}
	switch p.Status {
	case "READY":
		if len(p.Evidence) == 0 || p.Context == "" {
			return ErrIntegrity
		}
	case "NO_CANDIDATES", "MODEL_UNAVAILABLE", "SOURCE_UNAVAILABLE", "INTEGRITY_FAILURE", "TIME_BUDGET_EXCEEDED":
		if len(p.Evidence) > 0 || p.Context != "" {
			return ErrIntegrity
		}
	case "INSUFFICIENT_EVIDENCE":
	default:
		return ErrIntegrity
	}
	a, m, _, e := b.Retrieval.Ready(ctx, owner, p.Scope.Retrieval)
	if e != nil {
		return e
	}
	if a.Repository != p.Scope.Repository || a.Snapshot != p.Scope.Snapshot || a.Policy != p.Scope.RetrievalPolicy || a.Manifest != p.Scope.RetrievalManifest || a.Analysis != p.Scope.Analysis || a.AnalysisManifest != p.Scope.AnalysisManifest || m.Commit != p.Scope.Commit {
		return ErrIntegrity
	}
	aa, am, e := b.Analysis.Ready(ctx, owner, a.Analysis)
	if e != nil {
		return e
	}
	if aa.Manifest != a.AnalysisManifest || am.Repository != a.Repository || am.Snapshot != a.Snapshot || am.Commit != m.Commit {
		return ErrIntegrity
	}
	edges := map[string]analysis.Edge{}
	for _, edge := range am.Edges {
		edges[edge.ID] = edge
	}
	ids := []string{}
	docs := map[string]retrieval.Document{}
	for _, d := range m.Documents {
		docs[d.Chunk.ID] = d
	}
	fileSizes := map[string]int64{}
	for _, f := range am.Files {
		fileSizes[f.ID] = f.Size
	}
	planned, fullFiles := 0, int64(0)
	files := map[string]bool{}
	for i, v := range p.Evidence {
		if v.ID != "E"+strconv.Itoa(i+1) {
			return ErrIntegrity
		}
		d, ok := docs[v.ChunkID]
		if !ok {
			return ErrIntegrity
		}
		planned += d.Chunk.Span.End - d.Chunk.Span.Start
		if !files[d.Chunk.File] {
			files[d.Chunk.File] = true
			fullFiles += fileSizes[d.Chunk.File]
		}
		if planned > p.Policy.MaxBytes || fullFiles > int64(p.Policy.MaxBytes) {
			return ErrBudget
		}
		ids = append(ids, v.ChunkID)
	}
	verified, e := b.read(ctx, owner, a.Analysis, a.ID, ids)
	if e != nil {
		return e
	}
	ranges := map[string]bool{}
	for _, v := range p.Evidence {
		d, ok := docs[v.ChunkID]
		read := verified[v.ChunkID]
		left, right := v.Span.Start-d.Chunk.Span.Start, v.Span.End-d.Chunk.Span.Start
		if !ok || read.Chunk != d.Chunk || v.ChunkSHA256 != d.Chunk.SHA256 || v.FileID != d.Chunk.File || v.SymbolID != d.Chunk.Symbol || v.Path != d.Path || v.Symbol != d.Symbol || v.Language != d.Chunk.Language || v.Parse != d.State || left < 0 || right <= left || right > len(read.Bytes) || !utf8.Valid(read.Bytes[left:right]) || subspan(d.Chunk.Span, read.Bytes, left, right) != v.Span || string(read.Bytes[left:right]) != v.Source || ingest.Digest([]byte(v.Source)) != v.SHA256 {
			return ErrIntegrity
		}
		key := hash([]any{v.FileID, v.Span.Start, v.Span.End})
		if ranges[key] {
			return ErrIntegrity
		}
		ranges[key] = true
	}
	for _, v := range p.Evidence {
		for _, reason := range v.Graph {
			edge, ok := edges[reason.Edge]
			_, seedExists := docs[reason.Seed]
			if !ok || !seedExists || reason.Class != "resolved" || edge.Class != "resolved" || edge.Kind != reason.Kind || edge.Span != reason.Span || edgePriority(edge.Kind) > 3 || reason.Depth < 1 || reason.Depth > p.Policy.GraphDepth || !reasonConnects(am, docs[reason.Seed], docs[v.ChunkID], edge, reason.Depth, p.Policy.TraversalEdges) {
				return ErrIntegrity
			}
		}
	}
	if p.Context != "" {
		var wire struct {
			Schema   string     `json:"schema"`
			Scope    Scope      `json:"scope"`
			Evidence []Evidence `json:"evidence"`
		}
		if json.Unmarshal([]byte(p.Context), &wire) != nil || wire.Schema != "judgegrid-evidence-v1" || wire.Scope != p.Scope || hash(wire.Evidence) != hash(p.Evidence) {
			return ErrIntegrity
		}
		if b.Engine == nil || b.Engine.Identity() != p.Policy.Model || b.Engine.TokenizerIdentity() != p.Policy.Tokenizer {
			return ErrModel
		}
		again, e := b.Engine.Pack(ctx, p.Scope, p.Evidence, p.Budget)
		if e != nil {
			return e
		}
		if again.Text != p.Context || again.Tokens != p.Consumed || hash(again.Evidence) != hash(p.Evidence) {
			return ErrIntegrity
		}
		counts, e := b.Engine.Count(ctx, []string{p.Context})
		if e != nil {
			return e
		}
		if len(counts) != 1 || counts[0] != p.Consumed {
			return ErrIntegrity
		}
	} else if p.Consumed != 0 || len(p.Evidence) > 0 {
		return ErrIntegrity
	}
	return nil
}
