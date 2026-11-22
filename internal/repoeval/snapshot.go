package repoeval

import (
	"context"
	"encoding/json"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/contextpack"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"time"
)

type CapturedFile struct {
	Metadata analysis.File `json:"metadata"`
	Source   string        `json:"source"`
}
type Catalog struct {
	Scope     Scope                `json:"scope"`
	Files     []CapturedFile       `json:"files"`
	Symbols   []analysis.Symbol    `json:"symbols"`
	Documents []retrieval.Document `json:"documents"`
}
type Session struct {
	Scope                Scope
	base                 *retrieval.Service
	source               *analysis.Service
	index                retrieval.Index
	manifest             retrieval.Manifest
	rp                   retrieval.Policy
	ai                   analysis.Index
	am                   analysis.Manifest
	chunks               map[string]analysis.VerifiedChunk
	files                map[string]CapturedFile
	engine               *memoEngine
	vectors              map[string][]float32
	channels             map[string][2][]retrieval.Candidate
	SourceCaptureSeconds float64
	EmbeddingSeconds     float64
	EmbeddingCalls       int
}

func Capture(ctx context.Context, owner, index string, r *retrieval.Service, a *analysis.Service, e contextpack.Engine) (*Session, error) {
	start := time.Now()
	ri, rm, rp, err := r.Ready(ctx, owner, index)
	if err != nil {
		return nil, err
	}
	ai, am, err := a.Ready(ctx, owner, ri.Analysis)
	if err != nil {
		return nil, err
	}
	if ai.Manifest != ri.AnalysisManifest || am.Repository != ri.Repository || am.Snapshot != ri.Snapshot || am.Commit != rm.Commit || ingest.Digest(canonical(am)) != ri.AnalysisManifest {
		return nil, ErrIntegrity
	}
	cp := contextpack.DefaultPolicy()
	s := &Session{base: r, source: a, index: ri, manifest: rm, rp: rp, ai: ai, am: am, chunks: map[string]analysis.VerifiedChunk{}, files: map[string]CapturedFile{}, vectors: map[string][]float32{}, channels: map[string][2][]retrieval.Candidate{}}
	s.Scope = Scope{owner, ri.Repository, ri.Snapshot, rm.Commit, am.SourceManifest, ri.Analysis, ri.AnalysisManifest, ri.ID, ri.Manifest, ri.Policy, cp.ID(), cp.Tokenizer}
	s.engine = &memoEngine{base: e, scores: map[string]contextpack.ScoreReply{}}
	ids := []string{}
	for _, c := range am.Chunks {
		ids = append(ids, c.ID)
	}
	reads, err := a.ReadChunks(ctx, owner, ai.ID, ids)
	if err != nil {
		return nil, err
	}
	raw := map[string][]byte{}
	ranges := map[string][]analysis.Span{}
	metas := map[string]analysis.File{}
	total := int64(0)
	for _, v := range reads {
		if v.Index.ID != ai.ID || ingest.Digest(v.Bytes) != v.Chunk.SHA256 {
			return nil, ErrIntegrity
		}
		s.chunks[v.Chunk.ID] = v
		if _, ok := raw[v.File.ID]; !ok {
			total += v.File.Size
			if v.File.Size < 0 || total > 32<<20 {
				return nil, ErrInvalid
			}
			raw[v.File.ID] = make([]byte, int(v.File.Size))
			metas[v.File.ID] = v.File
		}
		copy(raw[v.File.ID][v.Chunk.Span.Start:v.Chunk.Span.End], v.Bytes)
		ranges[v.File.ID] = append(ranges[v.File.ID], v.Chunk.Span)
	}
	for id, b := range raw {
		if UnionLength(ranges[id]) != len(b) || ingest.Digest(b) != metas[id].SHA256 {
			return nil, ErrIntegrity
		}
		s.files[id] = CapturedFile{metas[id], string(b)}
	}
	s.SourceCaptureSeconds = time.Since(start).Seconds()
	return s, nil
}

// Every cached read is owner/READY/current-identity authorized. Frozen source
// bytes are rehashed on use; full backing-source verification brackets a run.
// This scoped, explicit offline mode never changes production Reader behavior.
func (s *Session) authorize(ctx context.Context, owner string) error {
	if owner != s.Scope.Owner {
		return ErrDenied
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var im, am, sm, pol, aid, sid, rid string
	err := s.base.DB.Pool.QueryRow(ctx, `SELECT i.manifest_sha256,a.manifest_sha256,x.manifest_sha256,i.policy_id,i.analysis_id,i.snapshot_id,i.repository_id::text FROM retrieval_indexes i JOIN analysis_indexes a ON a.id=i.analysis_id JOIN source_snapshots x ON x.id=i.snapshot_id JOIN source_repositories r ON r.id=i.repository_id WHERE i.id=$1 AND r.owner=$2 AND i.state='READY' AND a.state='READY' AND x.state='READY'`, s.Scope.Retrieval, owner).Scan(&im, &am, &sm, &pol, &aid, &sid, &rid)
	if err != nil {
		return ErrDenied
	}
	if im != s.Scope.RetrievalManifest || am != s.Scope.AnalysisManifest || sm != s.Scope.SourceManifest || pol != s.Scope.RetrievalPolicy || aid != s.Scope.Analysis || sid != s.Scope.Snapshot || rid != s.Scope.Repository {
		return ErrIntegrity
	}
	return nil
}
func (s *Session) Reverify(ctx context.Context, owner string) error {
	if e := s.authorize(ctx, owner); e != nil {
		return e
	}
	a, m, _, e := s.base.Ready(ctx, owner, s.Scope.Retrieval)
	if e != nil {
		return e
	}
	if a.Manifest != s.Scope.RetrievalManifest || digest(m) != digest(s.manifest) {
		return ErrIntegrity
	}
	ids := []string{}
	for _, c := range s.am.Chunks {
		ids = append(ids, c.ID)
	}
	v, e := s.source.ReadChunks(ctx, owner, s.Scope.Analysis, ids)
	if e != nil {
		return e
	}
	if len(v) != len(s.chunks) {
		return ErrIntegrity
	}
	for _, c := range v {
		old, ok := s.chunks[c.Chunk.ID]
		if !ok || c.Chunk != old.Chunk || ingest.Digest(c.Bytes) != old.Chunk.SHA256 || ingest.Digest(old.Bytes) != old.Chunk.SHA256 {
			return ErrIntegrity
		}
	}
	return nil
}
func (s *Session) Export(ctx context.Context, owner string) (Catalog, error) {
	if e := s.authorize(ctx, owner); e != nil {
		return Catalog{}, e
	}
	c := Catalog{Scope: s.Scope, Symbols: s.am.Symbols, Documents: s.manifest.Documents}
	for _, f := range s.am.Files {
		if v, ok := s.files[f.ID]; ok {
			c.Files = append(c.Files, v)
		}
	}
	return c, nil
}
func point(raw []byte, n int) (int, int) {
	line, column := 1, 0
	for _, b := range raw[:n] {
		if b == '\n' {
			line++
			column = 0
		} else {
			column++
		}
	}
	return line, column
}
func (s *Session) VerifyRef(ctx context.Context, owner string, r Ref) error {
	if owner != s.Scope.Owner {
		return ErrDenied
	}
	f, ok := s.files[r.File]
	if !ok || r.Snapshot != s.Scope.Snapshot || r.Path != f.Metadata.Path || r.Span.Start < 0 || r.Span.End <= r.Span.Start || r.Span.End > len(f.Source) {
		return ErrIntegrity
	}
	raw := []byte(f.Source)
	if ingest.Digest(raw) != f.Metadata.SHA256 || ingest.Digest(raw[r.Span.Start:r.Span.End]) != r.SHA256 {
		return ErrIntegrity
	}
	sl, sc := point(raw, r.Span.Start)
	el, ec := point(raw, r.Span.End)
	if r.Span.StartLine != sl || r.Span.StartColumn != sc || r.Span.EndLine != el || r.Span.EndColumn != ec {
		return ErrIntegrity
	}
	return ctx.Err()
}
func (s *Session) Validate(ctx context.Context, owner string, b Benchmark) error {
	if e := s.authorize(ctx, owner); e != nil {
		return e
	}
	if b.Validate() != nil || b.Scope != s.Scope {
		return ErrInvalid
	}
	docs := map[string]retrieval.Document{}
	for _, d := range s.manifest.Documents {
		docs[d.Chunk.ID] = d
	}
	for _, q := range b.Queries {
		for _, j := range q.Judgments {
			if e := s.VerifyRef(ctx, owner, j.Ref); e != nil {
				return e
			}
			seen := map[string]bool{}
			for _, id := range j.Ref.ChunkIDs {
				d, ok := docs[id]
				if !ok || seen[id] || d.Chunk.File != j.Ref.File || float64(Intersection(d.Chunk.Span, j.Ref.Span))/float64(j.Ref.Span.End-j.Ref.Span.Start) < b.Policy.GoldCoverage {
					return ErrInvalid
				}
				seen[id] = true
			}
		}
	}
	return nil
}
func (s *Session) ref(c analysis.Chunk, path string, span analysis.Span) Ref {
	f := s.files[c.File]
	raw := []byte(f.Source)
	return Ref{s.Scope.Snapshot, c.File, path, span, ingest.Digest(raw[span.Start:span.End]), []string{c.ID}}
}
func (s *Session) Embeddings(ctx context.Context, b Benchmark) error {
	start := time.Now()
	defer func() { s.EmbeddingSeconds = time.Since(start).Seconds() }()
	if s.base.Model == nil || s.base.Model.Identity() != s.rp.Model {
		return retrieval.ErrModel
	}
	for at := 0; at < len(b.Queries); at += 32 {
		end := min(at+32, len(b.Queries))
		text := []string{}
		for _, q := range b.Queries[at:end] {
			text = append(text, q.Question)
		}
		v, e := s.base.Model.Embed(ctx, text)
		s.EmbeddingCalls++
		if e != nil {
			return e
		}
		if len(v) != len(text) {
			return ErrIntegrity
		}
		for i, vec := range v {
			if retrieval.ValidateVector(vec) != nil {
				return ErrIntegrity
			}
			s.vectors[text[i]] = vec
		}
	}
	return nil
}

type frozenRetrieval struct{ s *Session }

func (f frozenRetrieval) Ready(ctx context.Context, o, id string) (retrieval.Index, retrieval.Manifest, retrieval.Policy, error) {
	if id != f.s.Scope.Retrieval {
		return retrieval.Index{}, retrieval.Manifest{}, retrieval.Policy{}, ErrDenied
	}
	if e := f.s.authorize(ctx, o); e != nil {
		return retrieval.Index{}, retrieval.Manifest{}, retrieval.Policy{}, e
	}
	return f.s.index, f.s.manifest, f.s.rp, nil
}
func (f frozenRetrieval) ReadChunk(ctx context.Context, o, index, id string) (analysis.VerifiedChunk, error) {
	if index != f.s.Scope.Retrieval {
		return analysis.VerifiedChunk{}, ErrDenied
	}
	return (frozenAnalysis{f.s}).ReadChunk(ctx, o, f.s.Scope.Analysis, id)
}
func (f frozenRetrieval) Search(ctx context.Context, o string, q retrieval.Request) (retrieval.Results, error) {
	start := time.Now()
	s := f.s
	if e := s.authorize(ctx, o); e != nil {
		return retrieval.Results{}, e
	}
	if q.Index != s.Scope.Retrieval || q.Repository != s.Scope.Repository || q.Snapshot != s.Scope.Snapshot || q.TopK < 1 || q.TopK > 50 || q.Depth < q.TopK || q.Depth > 200 || q.Filter != (retrieval.Filter{}) || q.ANN {
		return retrieval.Results{}, ErrInvalid
	}
	key := digest([]any{q.Query, q.Depth})
	pair, ok := s.channels[key]
	if !ok {
		lex := retrieval.BM25(s.manifest.Documents, s.manifest.Corpus, q.Query, s.rp)
		lex = lex[:min(len(lex), q.Depth)]
		pair = [2][]retrieval.Candidate{lex, nil}
	}
	if q.Mode != "bm25" && pair[1] == nil {
		vec, ok := s.vectors[q.Query]
		if !ok {
			return retrieval.Results{}, retrieval.ErrModel
		}
		dense, e := s.base.EvaluationDense(ctx, o, s.index, s.manifest, vec, q.Depth)
		if e != nil {
			return retrieval.Results{}, e
		}
		pair[1] = dense
	}
	s.channels[key] = pair
	out := retrieval.Results{Index: s.Scope.Retrieval, Snapshot: s.Scope.Snapshot, Commit: s.Scope.Commit, Analysis: s.Scope.Analysis, Policy: s.Scope.RetrievalPolicy, Mode: q.Mode}
	switch q.Mode {
	case "bm25":
		out.Candidates = pair[0]
	case "dense":
		out.Candidates = pair[1]
	case "hybrid":
		out.Candidates = retrieval.Fuse(pair[0], pair[1], s.rp)
	default:
		return out, ErrInvalid
	}
	out.Candidates = retrieval.EvaluationSelect(out.Candidates, q.TopK)
	for _, c := range out.Candidates {
		v, e := (frozenAnalysis{s}).ReadChunk(ctx, o, s.Scope.Analysis, c.Document.Chunk.ID)
		if e != nil || v.Chunk != c.Document.Chunk {
			return retrieval.Results{}, ErrIntegrity
		}
	}
	out.Seconds = time.Since(start).Seconds()
	return out, nil
}

type frozenAnalysis struct{ s *Session }

func (f frozenAnalysis) Ready(ctx context.Context, o, id string) (analysis.Index, analysis.Manifest, error) {
	if id != f.s.Scope.Analysis {
		return analysis.Index{}, analysis.Manifest{}, ErrDenied
	}
	if e := f.s.authorize(ctx, o); e != nil {
		return analysis.Index{}, analysis.Manifest{}, e
	}
	return f.s.ai, f.s.am, nil
}
func (f frozenAnalysis) ReadChunk(ctx context.Context, o, index, id string) (analysis.VerifiedChunk, error) {
	if index != f.s.Scope.Analysis {
		return analysis.VerifiedChunk{}, ErrDenied
	}
	if e := f.s.authorize(ctx, o); e != nil {
		return analysis.VerifiedChunk{}, e
	}
	v, ok := f.s.chunks[id]
	if !ok {
		return v, ErrDenied
	}
	if ingest.Digest(v.Bytes) != v.Chunk.SHA256 {
		return analysis.VerifiedChunk{}, ErrIntegrity
	}
	v.Bytes = append([]byte(nil), v.Bytes...)
	return v, nil
}
func (f frozenAnalysis) ReadChunks(ctx context.Context, o, index string, ids []string) ([]analysis.VerifiedChunk, error) {
	if len(ids) > 10000 {
		return nil, ErrInvalid
	}
	out := []analysis.VerifiedChunk{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, ErrIntegrity
		}
		seen[id] = true
		v, e := f.ReadChunk(ctx, o, index, id)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (f frozenAnalysis) ReadSymbol(context.Context, string, string, string) (analysis.VerifiedSymbol, error) {
	return analysis.VerifiedSymbol{}, ErrDenied
}

type memoEngine struct {
	base                 contextpack.Engine
	scores               map[string]contextpack.ScoreReply
	Calls, Hits, Windows int
}

func (m *memoEngine) Identity() string {
	if m.base == nil {
		return ""
	}
	return m.base.Identity()
}
func (m *memoEngine) TokenizerIdentity() string {
	if m.base == nil {
		return ""
	}
	return m.base.TokenizerIdentity()
}
func (m *memoEngine) Score(ctx context.Context, q string, d []contextpack.Input, p contextpack.Policy) (contextpack.ScoreReply, error) {
	key := digest([]any{m.Identity(), m.TokenizerIdentity(), q, d, p.WindowTokens, p.WindowOverlap, p.WindowsPerDoc, p.MaxPairs})
	if v, ok := m.scores[key]; ok {
		m.Hits++
		var copy contextpack.ScoreReply
		_ = json.Unmarshal(canonical(v), &copy)
		copy.Load = 0
		copy.Inference = 0
		copy.Tokenize = 0
		return copy, nil
	}
	v, e := m.base.Score(ctx, q, d, p)
	m.Calls++
	if e == nil {
		m.scores[key] = v
		m.Windows += v.Pairs
	}
	return v, e
}
func (m *memoEngine) Pack(ctx context.Context, s contextpack.Scope, b []contextpack.Evidence, budget int) (contextpack.Packing, error) {
	return m.base.Pack(ctx, s, b, budget)
}
func (m *memoEngine) Count(ctx context.Context, texts []string) ([]int, error) {
	return m.base.Count(ctx, texts)
}
