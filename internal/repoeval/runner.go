package repoeval

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/contextpack"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"sort"
	"time"
)

var Variants = []string{"V1-bm25", "V2-dense", "V3-hybrid", "V4-rerank", "V5-graph", "V6-context"}

type Item struct {
	Chunk       string               `json:"chunk_id"`
	Ref         Ref                  `json:"source"`
	FullRef     Ref                  `json:"original_chunk_source"`
	Symbol      string               `json:"symbol"`
	Score       float64              `json:"score"`
	RRF         float64              `json:"rrf"`
	Relevance   float64              `json:"declared_relevance"`
	BM25        float64              `json:"bm25"`
	Distance    float64              `json:"distance"`
	LexicalRank int                  `json:"lexical_rank"`
	DenseRank   int                  `json:"dense_rank"`
	Rerank      int                  `json:"rerank"`
	Direct      bool                 `json:"direct"`
	Graph       []contextpack.Reason `json:"graph"`
	Windows     *contextpack.Scored  `json:"scored_windows,omitempty"`
}
type Observation struct {
	Attempt               int                     `json:"attempt"`
	Query                 string                  `json:"query_id"`
	Variant               string                  `json:"variant"`
	Kind                  string                  `json:"ordering"`
	Policy                string                  `json:"stage_policy_sha256"`
	Components            []string                `json:"enabled_components"`
	Status                string                  `json:"status"`
	Error                 string                  `json:"error_class,omitempty"`
	Items                 []Item                  `json:"items"`
	Metrics               []Metric                `json:"exact_chunk_metrics"`
	SpanMetrics           []Metric                `json:"span_filtered_metrics"`
	Support               Support                 `json:"support"`
	UnionRecall           *float64                `json:"candidate_union_known_recall,omitempty"`
	UnionSupport          Support                 `json:"candidate_union_support"`
	FullCandidateSupport  Support                 `json:"full_candidate_support"`
	RankedWindowSupport   Support                 `json:"ranked_window_support"`
	Negative              string                  `json:"negative_classification,omitempty"`
	FailureStage          string                  `json:"failure_stage,omitempty"`
	Tokens                int                     `json:"context_tokens"`
	Blocks                int                     `json:"context_blocks"`
	RelevantBytesPerToken float64                 `json:"relevant_bytes_per_token"`
	Seconds               float64                 `json:"offline_stage_seconds"`
	SharedQuerySeconds    float64                 `json:"shared_verified_channel_seconds"`
	Diagnostics           contextpack.Diagnostics `json:"diagnostics"`
	ModelCalls            int                     `json:"new_cross_encoder_calls"`
	CacheHits             int                     `json:"cross_encoder_cache_hits"`
	ModelWindows          int                     `json:"new_scored_windows"`
	Pack                  *contextpack.Pack       `json:"verified_pack,omitempty"`
}

func errorClass(e error) string {
	switch {
	case errors.Is(e, ErrDenied):
		return "AUTHORIZATION"
	case errors.Is(e, context.Canceled), errors.Is(e, context.DeadlineExceeded):
		return "DEADLINE"
	case errors.Is(e, retrieval.ErrModel), errors.Is(e, contextpack.ErrModel):
		return "MODEL"
	case errors.Is(e, ingest.ErrStorage):
		return "STORAGE"
	case errors.Is(e, ErrInvalid), errors.Is(e, contextpack.ErrBudget), errors.Is(e, retrieval.ErrBudget):
		return "BUDGET"
	default:
		return "INTEGRITY_OR_INFRASTRUCTURE"
	}
}
func (s *Session) Builder() *contextpack.Builder {
	return &contextpack.Builder{Retrieval: frozenRetrieval{s}, Analysis: frozenAnalysis{s}, Engine: s.engine}
}
func (s *Session) observe(ctx context.Context, q Query, v string) (out Observation) {
	start := time.Now()
	calls, hits, windows := s.engine.Calls, s.engine.Hits, s.engine.Windows
	out = Observation{Attempt: 1, Query: q.ID, Variant: v, Kind: "document-rank", Status: "RANKED", Items: []Item{}}
	defer func() {
		out.Seconds = time.Since(start).Seconds()
		out.ModelCalls = s.engine.Calls - calls
		out.CacheHits = s.engine.Hits - hits
		out.ModelWindows = s.engine.Windows - windows
	}()
	// Named return is required so deferred timing/call counters reach the caller.
	p := contextpack.DefaultPolicy()
	out.Policy = s.Scope.RetrievalPolicy
	out.Components = map[string][]string{Variants[0]: {"bm25"}, Variants[1]: {"dense"}, Variants[2]: {"bm25", "dense", "rrf"}, Variants[3]: {"bm25", "dense", "rrf", "cross-encoder"}, Variants[4]: {"bm25", "dense", "rrf", "cross-encoder", "graph"}, Variants[5]: {"bm25", "dense", "rrf", "cross-encoder", "graph", "mmr", "token-packing"}}[v]
	r := contextpack.Request{Repository: s.Scope.Repository, Snapshot: s.Scope.Snapshot, Index: s.Scope.Retrieval, Query: q.Question, Intent: q.Intent, Policy: p}
	var err error
	switch v {
	case Variants[0], Variants[1], Variants[2]:
		mode := map[string]string{Variants[0]: "bm25", Variants[1]: "dense", Variants[2]: "hybrid"}[v]
		var result retrieval.Results
		result, err = (frozenRetrieval{s}).Search(ctx, s.Scope.Owner, retrieval.Request{Index: s.Scope.Retrieval, Snapshot: s.Scope.Snapshot, Repository: s.Scope.Repository, Query: q.Question, Mode: mode, TopK: p.Candidates, Depth: p.ChannelDepth})
		for _, c := range result.Candidates {
			fused := 0.
			if mode == "hybrid" {
				fused = c.Score
			}
			out.Items = append(out.Items, Item{Chunk: c.Document.Chunk.ID, Ref: s.ref(c.Document.Chunk, c.Document.Path, c.Document.Chunk.Span), Symbol: c.Document.Symbol, Score: c.Score, RRF: fused, BM25: c.LexicalScore, Distance: c.Distance, LexicalRank: c.LexicalRank, DenseRank: c.DenseRank, Direct: true})
		}
		if len(out.Items) == 0 && err == nil {
			out.Status = "NO_CANDIDATES"
		}
	case Variants[3], Variants[4]:
		if v == Variants[3] {
			r.Policy.GraphDepth = 0
			r.Policy.Expanded = 0
		}
		out.Policy = r.Policy.ID()
		var rank contextpack.EvaluationRanking
		rank, err = s.Builder().RankEvaluation(ctx, s.Scope.Owner, r)
		out.Diagnostics = rank.Diagnostics
		for _, c := range rank.Candidates {
			sc := c.Scoring
			out.Items = append(out.Items, Item{Chunk: c.Chunk.ID, Ref: s.ref(c.Chunk, c.Path, c.WindowSpan), Symbol: c.Symbol, Score: c.RerankerScore, RRF: c.RRF, Relevance: c.Relevance, LexicalRank: c.LexicalRank, DenseRank: c.DenseRank, Rerank: c.Rerank, Direct: c.Direct, Graph: c.Graph, Windows: &sc})
		}
		if len(out.Items) == 0 && err == nil {
			out.Status = "NO_CANDIDATES"
		}
	case Variants[5]:
		out.Policy = p.ID()
		out.Kind = "evidence-assembly-order-E1-to-En"
		var pack contextpack.Pack
		pack, err = s.Builder().Build(ctx, s.Scope.Owner, r)
		out.Status = pack.Status
		out.Diagnostics = pack.Diagnostics
		if err == nil && (pack.Status == "READY" || pack.Status == "INSUFFICIENT_EVIDENCE" || pack.Status == "NO_CANDIDATES") {
			if len(pack.Evidence) > 0 {
				err = s.Builder().Verify(ctx, s.Scope.Owner, pack)
			}
			if err == nil {
				out.Pack = &pack
				out.Tokens = pack.Consumed
				out.Blocks = len(pack.Evidence)
				for _, e := range pack.Evidence {
					out.Items = append(out.Items, Item{Chunk: e.ChunkID, Ref: Ref{s.Scope.Snapshot, e.FileID, e.Path, e.Span, e.SHA256, []string{e.ChunkID}}, Symbol: e.Symbol, Score: e.RerankerScore, RRF: e.RRF, LexicalRank: e.LexicalRank, DenseRank: e.DenseRank, Rerank: e.Rerank, Direct: e.Direct, Graph: e.Graph})
				}
			}
		} else if err == nil {
			switch pack.Status {
			case "MODEL_UNAVAILABLE":
				err = contextpack.ErrModel
			case "SOURCE_UNAVAILABLE":
				err = ingest.ErrStorage
			case "TIME_BUDGET_EXCEEDED":
				err = context.DeadlineExceeded
			default:
				err = ErrIntegrity
			}
		}
	default:
		err = ErrInvalid
	}
	for i := range out.Items {
		c, ok := s.chunks[out.Items[i].Chunk]
		if !ok {
			err = ErrIntegrity
			break
		}
		out.Items[i].FullRef = s.ref(c.Chunk, c.File.Path, c.Chunk.Span)
	}
	if err != nil {
		out.Status = "ERROR"
		out.Error = errorClass(err)
		out.Items = nil
		out.Pack = nil
		out.Tokens = 0
		out.Blocks = 0
	}
	return out
}
func (s *Session) metricRows(q Query, o *Observation, p Policy) error {
	if o.Error != "" {
		o.FailureStage = "infrastructure_or_authorization"
		return nil
	}
	gold := map[string]int{}
	for _, j := range q.Judgments {
		for _, id := range j.Ref.ChunkIDs {
			if old, ok := gold[id]; !ok || old < j.Grade {
				gold[id] = j.Grade
			}
		}
	}
	exact, span, ids := []int{}, []int{}, []string{}
	refs, fullRefs := []Ref{}, []Ref{}
	for _, item := range o.Items {
		if e := s.VerifyRef(context.Background(), s.Scope.Owner, item.Ref); e != nil {
			return e
		}
		ids = append(ids, item.Chunk)
		refs = append(refs, item.Ref)
		if e := s.VerifyRef(context.Background(), s.Scope.Owner, item.FullRef); e != nil {
			return e
		}
		fullRefs = append(fullRefs, item.FullRef)
		g := -1
		if v, ok := gold[item.Chunk]; ok {
			g = v
		}
		exact = append(exact, g)
		sg, known := MatchGrade(item.Ref, q, p, false, item.Chunk)
		if !known {
			sg = -1
		}
		span = append(span, sg)
	}
	for _, k := range p.K {
		o.Metrics = append(o.Metrics, Metrics(exact, ids, gold, k, p.Relevant))
		o.SpanMetrics = append(o.SpanMetrics, Metrics(span, ids, gold, k, p.Relevant))
	}
	o.Support = EvidenceSupport(q, refs, p)
	o.FullCandidateSupport = EvidenceSupport(q, fullRefs, p)
	o.RankedWindowSupport = o.Support
	if o.Pack != nil {
		allFull, allWindow := []Ref{}, []Ref{}
		for _, c := range o.Pack.Candidates {
			allFull = append(allFull, s.ref(c.Chunk, c.Path, c.Chunk.Span))
			allWindow = append(allWindow, s.ref(c.Chunk, c.Path, c.WindowSpan))
		}
		o.FullCandidateSupport = EvidenceSupport(q, allFull, p)
		o.RankedWindowSupport = EvidenceSupport(q, allWindow, p)
	}
	union := s.channels[digest([]any{q.Question, contextpack.DefaultPolicy().ChannelDepth})]
	have := map[string]bool{}
	unionRefs := []Ref{}
	for _, list := range union {
		for _, c := range list {
			if !have[c.Document.Chunk.ID] {
				unionRefs = append(unionRefs, s.ref(c.Document.Chunk, c.Document.Path, c.Document.Chunk.Span))
			}
			have[c.Document.Chunk.ID] = true
		}
	}
	positive, found := 0, 0
	for id, g := range gold {
		if g >= p.Relevant {
			positive++
			if have[id] {
				found++
			}
		}
	}
	if positive > 0 && union[1] != nil {
		o.UnionRecall = ptr(float64(found) / float64(positive))
	}
	o.UnionSupport = EvidenceSupport(q, unionRefs, p)
	if !q.Answerable {
		if o.Variant == Variants[5] {
			switch o.Status {
			case "NO_CANDIDATES", "INSUFFICIENT_EVIDENCE":
				o.Negative = "correct_abstention_by_provisional_absence_label"
			case "READY":
				o.Negative = "false_strong_support_by_provisional_absence_label"
			default:
				o.Negative = "not_semantically_scorable"
			}
		} else {
			o.Negative = "ranking_only_no_semantic_abstention_claim"
		}
	}
	if q.Answerable && o.Support.State != "ALL" {
		if o.Support.State == "CANNOT_ASSESS" {
			o.FailureStage = "missing_or_uncertain_judgment"
		} else if o.UnionRecall != nil && *o.UnionRecall == 0 {
			o.FailureStage = "candidate_generation_miss"
		} else if o.FullCandidateSupport.State == "ALL" && o.RankedWindowSupport.State != "ALL" {
			o.FailureStage = "scored_window_truncation_or_selection"
		} else if o.Variant == Variants[5] && o.RankedWindowSupport.State == "ALL" {
			o.FailureStage = "mmr_or_context_budget_loss"
		} else if o.UnionSupport.State == "ALL" && o.FullCandidateSupport.State != "ALL" {
			o.FailureStage = "candidate_cutoff_or_graph_miss"
		} else {
			o.FailureStage = "partial_candidate_recall_or_annotation_ambiguity"
		}
	}
	if o.Tokens > 0 {
		unique := map[string][]Item{}
		for _, i := range o.Items {
			unique[i.Ref.File] = append(unique[i.Ref.File], i)
		}
		n := 0
		for _, list := range unique {
			for _, i := range list {
				n += i.Ref.Span.End - i.Ref.Span.Start
			}
		}
		o.RelevantBytesPerToken = float64(n) * (1 - o.Support.Redundancy) * o.Support.RelevantByteFraction / float64(o.Tokens)
	}
	return nil
}

type Fresh struct {
	Query          string                  `json:"query_id"`
	Variant        string                  `json:"variant"`
	Attempt        string                  `json:"attempt"`
	Status         string                  `json:"status"`
	Seconds        float64                 `json:"seconds"`
	Diagnostics    contextpack.Diagnostics `json:"diagnostics"`
	Tokens         int                     `json:"context_tokens"`
	Blocks         int                     `json:"context_blocks"`
	EmbeddingWall  float64                 `json:"embedding_wall_seconds"`
	EmbeddingCalls int                     `json:"embedding_calls"`
}
type timedEmbedder struct {
	retrieval.Embedder
	wall  *float64
	calls *int
}

func (t timedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	start := time.Now()
	v, e := t.Embedder.Embed(ctx, texts)
	*t.wall += time.Since(start).Seconds()
	*t.calls++
	return v, e
}

func (s *Session) Fresh(ctx context.Context, q Query, v string, attempt string) Fresh {
	start := time.Now()
	f := Fresh{Query: q.ID, Variant: v, Attempt: attempt, Status: "OK"}
	p := contextpack.DefaultPolicy()
	freshReader := *s.base
	freshReader.Model = timedEmbedder{s.base.Model, &f.EmbeddingWall, &f.EmbeddingCalls}
	b := contextpack.Builder{Retrieval: &freshReader, Analysis: s.source, Engine: s.engine.base}
	r := contextpack.Request{Repository: s.Scope.Repository, Snapshot: s.Scope.Snapshot, Index: s.Scope.Retrieval, Query: q.Question, Intent: q.Intent, Policy: p}
	var e error
	switch v {
	case Variants[0], Variants[1], Variants[2]:
		_, e = freshReader.Search(ctx, s.Scope.Owner, retrieval.Request{Index: s.Scope.Retrieval, Snapshot: s.Scope.Snapshot, Repository: s.Scope.Repository, Query: q.Question, Mode: map[string]string{Variants[0]: "bm25", Variants[1]: "dense", Variants[2]: "hybrid"}[v], TopK: p.Candidates, Depth: p.ChannelDepth})
	case Variants[3], Variants[4]:
		if v == Variants[3] {
			r.Policy.GraphDepth = 0
			r.Policy.Expanded = 0
		}
		var out contextpack.EvaluationRanking
		out, e = b.RankEvaluation(ctx, s.Scope.Owner, r)
		f.Diagnostics = out.Diagnostics
	case Variants[5]:
		var pack contextpack.Pack
		pack, e = b.Build(ctx, s.Scope.Owner, r)
		f.Diagnostics = pack.Diagnostics
		f.Status = pack.Status
		f.Tokens = pack.Consumed
		f.Blocks = len(pack.Evidence)
	default:
		e = ErrInvalid
	}
	if e != nil {
		f.Status = errorClass(e)
	}
	f.Seconds = time.Since(start).Seconds()
	return f
}

// Run is serial and deadline bounded. A checkpoint records only completed query
// rows; revalidation precedes both resumption and publication of COMPLETE.
func (s *Session) Run(ctx context.Context, owner string, b Benchmark, dir string, freshIDs []string) (Report, error) {
	if e := s.Validate(ctx, owner, b); e != nil {
		return Report{}, e
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(b.Policy.Seconds)*time.Second)
	defer cancel()
	cp, e := LoadCheckpoint(dir, b)
	if e != nil {
		return Report{}, e
	}
	if len(freshIDs) > 3 {
		return Report{}, ErrInvalid
	}
	normalized := append([]string{}, freshIDs...)
	if len(cp.Rows) > 0 && digest(append([]string{}, cp.FreshPlan...)) != digest(normalized) {
		return Report{}, ErrInvalid
	}
	cp.FreshPlan = normalized
	if e = SaveCheckpoint(dir, cp); e != nil {
		return Report{}, e
	}
	if e = s.Reverify(ctx, owner); e != nil {
		return Report{}, e
	}
	embedErr := s.Embeddings(ctx, b)
	_ = embedErr // V1 remains evaluable; missing vectors produce explicit per-variant model errors.
	// Failed/canceled rows can retry at most twice; preserve every old attempt.
	retained := []Observation{}
	attempts := map[string]int{}
	for _, old := range cp.Attempts {
		key := old.Query + "/" + old.Variant
		attempts[key] = max(attempts[key], old.Attempt)
	}
	for _, old := range cp.Rows {
		key := old.Query + "/" + old.Variant
		attempts[key] = max(attempts[key], old.Attempt)
		if old.Error != "" && old.Attempt < 3 {
			cp.Attempts = append(cp.Attempts, old)
		} else {
			retained = append(retained, old)
		}
	}
	cp.Rows = retained
	known := map[string]bool{}
	for _, r := range cp.Rows {
		known[r.Query+"/"+r.Variant] = true
	}
	for qi, q := range b.Queries {
		if ctx.Err() != nil {
			_ = SaveCheckpoint(dir, cp)
			return BuildReport(b, cp, s), ctx.Err()
		}
		if e = s.authorize(ctx, owner); e != nil {
			return BuildReport(b, cp, s), e
		}
		sharedStart := time.Now()
		_, _ = (frozenRetrieval{s}).Search(ctx, owner, retrieval.Request{Index: s.Scope.Retrieval, Snapshot: s.Scope.Snapshot, Repository: s.Scope.Repository, Query: q.Question, Mode: "hybrid", TopK: contextpack.DefaultPolicy().Candidates, Depth: contextpack.DefaultPolicy().ChannelDepth})
		sharedSeconds := time.Since(sharedStart).Seconds()
		for i := range cp.Rows {
			if cp.Rows[i].Query == q.ID {
				cp.Rows[i].Metrics = nil
				cp.Rows[i].SpanMetrics = nil
				if e = s.metricRows(q, &cp.Rows[i], b.Policy); e != nil {
					return BuildReport(b, cp, s), e
				}
				if cp.Rows[i].Pack != nil {
					if e = s.Builder().Verify(ctx, owner, *cp.Rows[i].Pack); e != nil {
						return BuildReport(b, cp, s), e
					}
				}
			}
		}
		for _, v := range Variants {
			if known[q.ID+"/"+v] {
				continue
			}
			o := s.observe(ctx, q, v)
			o.Attempt = attempts[q.ID+"/"+v] + 1
			o.SharedQuerySeconds = sharedSeconds
			if e = s.metricRows(q, &o, b.Policy); e != nil {
				return BuildReport(b, cp, s), e
			}
			cp.Rows = append(cp.Rows, o)
			known[q.ID+"/"+v] = true
		}
		if e = SaveCheckpoint(dir, cp); e != nil {
			return BuildReport(b, cp, s), e
		}
		if (qi+1)%b.Policy.ReverifyEvery == 0 {
			if e = s.Reverify(ctx, owner); e != nil {
				return BuildReport(b, cp, s), e
			}
		}
	}
	if len(freshIDs) > 3 {
		return BuildReport(b, cp, s), ErrInvalid
	}
	qBy := map[string]Query{}
	for _, q := range b.Queries {
		qBy[q.ID] = q
	}
	freshDone := map[string]bool{}
	for _, f := range cp.Fresh {
		freshDone[f.Query+"/"+f.Variant+"/"+f.Attempt] = true
	}
	for _, id := range freshIDs {
		q, ok := qBy[id]
		if !ok {
			return BuildReport(b, cp, s), ErrInvalid
		}
		for _, attempt := range []string{"first-process-cold", "repeat-process-cold"} {
			for _, v := range Variants {
				key := id + "/" + v + "/" + attempt
				if freshDone[key] {
					continue
				}
				if ctx.Err() != nil {
					_ = SaveCheckpoint(dir, cp)
					return BuildReport(b, cp, s), ctx.Err()
				}
				cp.Fresh = append(cp.Fresh, s.Fresh(ctx, q, v, attempt))
				if e = SaveCheckpoint(dir, cp); e != nil {
					return BuildReport(b, cp, s), e
				}
			}
		}
	}
	if e = s.Reverify(ctx, owner); e != nil {
		return BuildReport(b, cp, s), e
	}
	cp.State = "COMPLETE"
	for _, r := range cp.Rows {
		if r.Error != "" {
			cp.State = "COMPLETE_WITH_ERRORS"
		}
	}
	for _, f := range cp.Fresh {
		if f.Status != "OK" && f.Status != "READY" && f.Status != "INSUFFICIENT_EVIDENCE" && f.Status != "NO_CANDIDATES" {
			cp.State = "COMPLETE_WITH_ERRORS"
		}
	}
	if e = SaveCheckpoint(dir, cp); e != nil {
		return BuildReport(b, cp, s), e
	}
	report := BuildReport(b, cp, s)
	return report, WriteReport(dir, report)
}

// RecheckCheckpoint derives report metrics again from the frozen source labels.
// A checksum is an accidental-corruption check, not trusted metric provenance.
func (s *Session) RecheckCheckpoint(ctx context.Context, owner string, b Benchmark, cp *Checkpoint) error {
	if e := s.Validate(ctx, owner, b); e != nil {
		return e
	}
	if len(cp.Rows) != len(b.Queries)*len(Variants) || len(cp.Fresh) != len(cp.FreshPlan)*12 {
		return ErrIncomplete
	}
	queries := map[string]Query{}
	if e := s.Embeddings(ctx, b); e != nil {
		return e
	}
	for _, q := range b.Queries {
		queries[q.ID] = q
		if _, e := (frozenRetrieval{s}).Search(ctx, owner, retrieval.Request{Index: s.Scope.Retrieval, Snapshot: s.Scope.Snapshot, Repository: s.Scope.Repository, Query: q.Question, Mode: "hybrid", TopK: contextpack.DefaultPolicy().Candidates, Depth: contextpack.DefaultPolicy().ChannelDepth}); e != nil {
			return e
		}
	}
	for i := range cp.Rows {
		o := &cp.Rows[i]
		o.Metrics, o.SpanMetrics = nil, nil
		if e := s.metricRows(queries[o.Query], o, b.Policy); e != nil {
			return e
		}
		if o.Pack != nil {
			if e := s.Builder().Verify(ctx, owner, *o.Pack); e != nil {
				return e
			}
		}
	}
	if e := s.Reverify(ctx, owner); e != nil {
		return e
	}
	cp.State = "COMPLETE"
	for _, o := range cp.Rows {
		if o.Error != "" {
			cp.State = "COMPLETE_WITH_ERRORS"
		}
	}
	for _, f := range cp.Fresh {
		if f.Status != "OK" && f.Status != "READY" && f.Status != "INSUFFICIENT_EVIDENCE" && f.Status != "NO_CANDIDATES" {
			cp.State = "COMPLETE_WITH_ERRORS"
		}
	}
	return nil
}

func sortedRows(rows []Observation) []Observation {
	out := append([]Observation(nil), rows...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Query != out[j].Query {
			return out[i].Query < out[j].Query
		}
		return out[i].Variant < out[j].Variant
	})
	return out
}
