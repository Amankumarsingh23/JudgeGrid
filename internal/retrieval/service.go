package retrieval

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Service struct {
	DB     Database
	Source analysis.Reader
	Model  Embedder
	Lease  time.Duration
	fault  func(string, int)
}

func (s *Service) stage(name string, n int) {
	if s.fault != nil {
		s.fault(name, n)
	}
}
func failure(e error) string {
	switch {
	case errors.Is(e, ErrModel):
		return "model"
	case errors.Is(e, ErrBudget):
		return "budget"
	case errors.Is(e, ErrIntegrity), errors.Is(e, ingest.ErrIntegrity), errors.Is(e, ingest.ErrStorage), errors.Is(e, analysis.ErrIntegrity):
		return "integrity"
	case errors.Is(e, context.Canceled), errors.Is(e, context.DeadlineExceeded):
		return "canceled"
	}
	return "internal"
}
func (s *Service) Build(ctx context.Context, owner, index string, p Policy, retry bool) (result Index, err error) {
	if e := p.Validate(); e != nil {
		return result, e
	}
	if s.Source == nil {
		return result, ErrDenied
	}
	start := time.Now()
	ctx, end := telemetry.Begin(ctx, "retrieval.build", "")
	defer func() { end(err) }()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.MaxSeconds)*time.Second)
	defer cancel()
	a, am, e := s.Source.Ready(ctx, owner, index)
	if e != nil {
		return result, e
	}
	if s.Model != nil && s.Model.Identity() != p.Model {
		return result, ErrModel
	}
	r, e := s.DB.request(ctx, owner, a, p)
	if e != nil {
		return result, e
	}
	if r.State == "READY" {
		r, _, _, e = s.Ready(ctx, owner, r.ID)
		r.Replayed = true
		r.Seconds = time.Since(start).Seconds()
		return r, e
	}
	ttl := s.Lease
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	c, e := s.DB.claim(ctx, owner, r.ID, ttl, retry)
	if e != nil {
		return result, e
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	stop := sync.OnceFunc(func() { close(done); <-stopped })
	go func() {
		defer close(stopped)
		t := time.NewTicker(ttl / 3)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if s.DB.renew(ctx, c) != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		stop()
		if err != nil {
			fctx, fc := context.WithTimeout(context.Background(), 3*time.Second)
			defer fc()
			_ = s.DB.fail(fctx, c, failure(err))
		}
	}()
	m := Manifest{Schema: 1, Index: r.ID, Repository: a.Repository, Snapshot: a.Snapshot, Commit: am.Commit, Analysis: a.ID, AnalysisManifest: a.Manifest, Policy: p.ID(), Documents: []Document{}, Excluded: []Exclusion{}}
	chunks, excluded, e := selectChunks(am, p)
	if e != nil {
		return result, e
	}
	m.Excluded = excluded
	ids := make([]string, len(chunks))
	for i, c := range chunks {
		ids[i] = c.ID
	}
	// Prefer the Session 13 bounded verified bulk reader; the original typed
	// ReadChunk contract remains valid for other implementations.
	var verified []analysis.VerifiedChunk
	if bulk, ok := s.Source.(interface {
		ReadChunks(context.Context, string, string, []string) ([]analysis.VerifiedChunk, error)
	}); ok {
		verified, e = bulk.ReadChunks(ctx, owner, index, ids)
	} else {
		for _, id := range ids {
			v, x := s.Source.ReadChunk(ctx, owner, index, id)
			if x != nil {
				return result, x
			}
			verified = append(verified, v)
		}
	}
	if e != nil {
		return result, e
	}
	if len(verified) != len(chunks) {
		return result, ErrIntegrity
	}
	by := map[string]analysis.VerifiedChunk{}
	for _, v := range verified {
		by[v.Chunk.ID] = v
	}
	symbols := map[string]string{}
	for _, v := range am.Symbols {
		symbols[v.ID] = v.Qualified
	}
	vectors := make([][]float32, len(chunks))
	inputs := make([]string, len(chunks))
	stats := Stats{Documents: len(chunks), Excluded: len(excluded)}
	total := 0
	for i, c := range chunks {
		v, ok := by[c.ID]
		if !ok || v.Index.ID != a.ID || v.Chunk != c || ingest.Digest(v.Bytes) != c.SHA256 {
			return result, ErrIntegrity
		}
		total += len(v.Bytes)
		if total > p.MaxBytes {
			return result, ErrBudget
		}
		doc, input, x := makeDocument(c, v.File, symbols[c.Symbol], v.Bytes)
		if x != nil {
			return result, x
		}
		m.Documents = append(m.Documents, doc)
		inputs[i] = input
	}
	// Cache access is fenced by the build claim and scoped to repository/model/input.
	for i := 0; i < len(chunks); {
		pending := []int{}
		batch := []string{}
		for i < len(chunks) && len(pending) < p.Batch {
			v, hit, x := s.DB.cached(ctx, c, p, m.Documents[i].InputHash)
			if x != nil {
				return result, x
			}
			if hit {
				vectors[i] = v
				stats.Hits++
			} else {
				pending = append(pending, i)
				batch = append(batch, inputs[i])
				stats.Misses++
			}
			i++
		}
		if len(batch) > 0 {
			if s.Model == nil {
				return result, ErrModel
			}
			vv, x := s.Model.Embed(ctx, batch)
			if x != nil {
				return result, x
			}
			if len(vv) != len(batch) {
				return result, ErrIntegrity
			}
			for j, pos := range pending {
				if ValidateVector(vv[j]) != nil {
					return result, ErrIntegrity
				}
				if x = s.DB.cache(ctx, c, p, m.Documents[pos].InputHash, vv[j]); x != nil {
					return result, x
				}
				vectors[pos] = vv[j]
			}
		}
		s.stage("embedded", i)
		if e = ctx.Err(); e != nil {
			return result, e
		}
	}
	for i := range m.Documents {
		m.Documents[i].VectorHash = hash(vectors[i])
	}
	m.Corpus = corpus(m.Documents)
	for _, n := range m.Corpus.Totals {
		stats.Tokens += n
	}
	stats.Seconds = time.Since(start).Seconds()
	s.stage("publish", len(chunks))
	stop()
	if e = s.DB.renew(ctx, c); e != nil {
		return result, e
	}
	if e = s.DB.publish(ctx, c, m, p, vectors, stats); e != nil {
		return result, e
	}
	result, e = s.DB.Status(ctx, owner, r.ID)
	result.Seconds = time.Since(start).Seconds()
	return result, e
}
func selectChunks(am analysis.Manifest, p Policy) ([]analysis.Chunk, []Exclusion, error) {
	chunks := append([]analysis.Chunk(nil), am.Chunks...)
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].ID < chunks[j].ID })
	out := []analysis.Chunk{}
	excluded := []Exclusion{}
	ranges := map[string]bool{}
	for _, c := range chunks {
		key := hash([]any{c.File, c.Span.Start, c.Span.End})
		if ranges[key] {
			excluded = append(excluded, Exclusion{c.ID, "duplicate-range"})
			continue
		}
		ranges[key] = true
		out = append(out, c)
	}
	if len(out) > p.MaxDocuments {
		return nil, nil, ErrBudget
	}
	return out, excluded, nil
}
func validate(m Manifest, p Policy) error {
	if p.Validate() != nil || m.Schema != 1 || m.Policy != p.ID() || m.Index != hash([]string{"retrieval-v1", m.Repository, m.Snapshot, m.Analysis, m.AnalysisManifest, p.ID()}) || len(m.Documents) > p.MaxDocuments {
		return ErrIntegrity
	}
	ids := map[string]bool{}
	ranges := map[string]bool{}
	last := ""
	total := 0
	for _, d := range m.Documents {
		c := d.Chunk
		if c.ID <= last || ids[c.ID] || c.Span.Start < 0 || c.Span.End <= c.Span.Start || len(d.InputHash) != 64 || len(d.VectorHash) != 64 || d.Path == "" || (d.State != "PARSED" && d.State != "PARTIAL" && d.State != "UNSUPPORTED") {
			return ErrIntegrity
		}
		key := hash([]any{c.File, c.Span.Start, c.Span.End})
		if ranges[key] {
			return ErrIntegrity
		}
		ranges[key] = true
		ids[c.ID] = true
		last = c.ID
		total += c.Span.End - c.Span.Start
		for i, f := range d.Fields {
			n := 0
			for t, v := range f {
				if len(t) > 257 || v < 1 {
					return ErrIntegrity
				}
				n += v
			}
			if n != d.Lengths[i] {
				return ErrIntegrity
			}
		}
	}
	if total > p.MaxBytes {
		return ErrBudget
	}
	for _, x := range m.Excluded {
		if ids[x.Chunk] || x.Reason != "duplicate-range" {
			return ErrIntegrity
		}
		ids[x.Chunk] = true
	}
	if hash(corpus(m.Documents)) != hash(m.Corpus) {
		return ErrIntegrity
	}
	return nil
}
func (s *Service) Ready(ctx context.Context, owner, id string) (Index, Manifest, Policy, error) {
	a, m, p, e := s.DB.manifest(ctx, owner, id)
	if e != nil {
		return a, m, p, e
	}
	if s.Source == nil {
		return a, Manifest{}, p, ErrDenied
	}
	source, am, e := s.Source.Ready(ctx, owner, a.Analysis)
	if e != nil {
		return a, Manifest{}, p, e
	}
	if source.Manifest != a.AnalysisManifest || am.Commit != m.Commit || am.Repository != m.Repository || am.Snapshot != m.Snapshot {
		return a, Manifest{}, p, ErrIntegrity
	}
	expected, excluded, e := selectChunks(am, p)
	if e != nil || len(expected) != len(m.Documents) || hash(excluded) != hash(m.Excluded) {
		return a, Manifest{}, p, ErrIntegrity
	}
	files := map[string]analysis.File{}
	symbols := map[string]string{}
	for _, f := range am.Files {
		files[f.ID] = f
	}
	for _, v := range am.Symbols {
		symbols[v.ID] = v.Qualified
	}
	for i, c := range expected {
		d := m.Documents[i]
		f := files[c.File]
		if d.Chunk != c || d.Path != f.Path || d.State != f.State || d.Symbol != symbols[c.Symbol] {
			return a, Manifest{}, p, ErrIntegrity
		}
	}
	if e = s.DB.verifyRows(ctx, owner, m); e != nil {
		return a, Manifest{}, p, e
	}
	return a, m, p, nil
}
func (s *Service) ReadChunk(ctx context.Context, owner, index, id string) (analysis.VerifiedChunk, error) {
	a, m, _, e := s.Ready(ctx, owner, index)
	if e != nil {
		return analysis.VerifiedChunk{}, e
	}
	for _, d := range m.Documents {
		if d.Chunk.ID == id {
			return s.Source.ReadChunk(ctx, owner, a.Analysis, id)
		}
	}
	return analysis.VerifiedChunk{}, ErrDenied
}
func (s *Service) Search(ctx context.Context, owner string, q Request) (Results, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if q.TopK < 1 || q.TopK > 50 || q.Depth < q.TopK || q.Depth > 200 || strings.TrimSpace(q.Query) == "" || len(q.Query) > 1024 || !utf8.ValidString(q.Query) || strings.IndexByte(q.Query, 0) >= 0 || len(q.Filter.Path) > 1024 || len(q.Filter.Language) > 32 || len(q.Filter.State) > 32 {
		return Results{}, ErrBudget
	}
	if q.Mode != "bm25" && q.Mode != "dense" && q.Mode != "hybrid" {
		return Results{}, ErrDenied
	}
	a, m, p, e := s.Ready(ctx, owner, q.Index)
	if e != nil {
		return Results{}, e
	}
	if a.Snapshot != q.Snapshot || a.Repository != q.Repository {
		return Results{}, ErrDenied
	}
	result := Results{Index: a.ID, Snapshot: a.Snapshot, Commit: m.Commit, Analysis: a.Analysis, Policy: a.Policy, Mode: q.Mode, ANN: q.ANN, Candidates: []Candidate{}}
	docs := []Document{}
	for _, d := range m.Documents {
		if matches(d, q.Filter) {
			docs = append(docs, d)
		}
	}
	var lex, dense []Candidate
	if q.Mode != "dense" {
		lex = BM25(docs, m.Corpus, q.Query, p)
		if len(lex) > q.Depth {
			lex = lex[:q.Depth]
		}
	}
	if q.Mode != "bm25" {
		if s.Model == nil || s.Model.Identity() != p.Model {
			return result, ErrModel
		}
		v, x := s.Model.Embed(ctx, []string{q.Query})
		if x != nil {
			return result, x
		}
		if len(v) != 1 || ValidateVector(v[0]) != nil {
			return result, ErrIntegrity
		}
		dense, result.Fallback, e = s.DB.dense(ctx, owner, a, docs, v[0], q.Depth, q.ANN)
		if e != nil {
			return result, e
		}
	}
	switch q.Mode {
	case "bm25":
		result.Candidates = lex
	case "dense":
		result.Candidates = dense
	case "hybrid":
		result.Candidates = Fuse(lex, dense, p)
	}
	result.Candidates = diverse(result.Candidates, q.TopK)
	// Ranked source references are returned only after exact verified span reads.
	ids := make([]string, len(result.Candidates))
	for i, v := range result.Candidates {
		ids[i] = v.Document.Chunk.ID
	}
	verified := map[string]analysis.VerifiedChunk{}
	if len(ids) > 0 {
		if bulk, ok := s.Source.(interface {
			ReadChunks(context.Context, string, string, []string) ([]analysis.VerifiedChunk, error)
		}); ok {
			reads, x := bulk.ReadChunks(ctx, owner, a.Analysis, ids)
			if x != nil {
				if ctx.Err() != nil {
					return Results{}, ctx.Err()
				}
				return Results{}, x
			}
			for _, read := range reads {
				verified[read.Chunk.ID] = read
			}
		} else {
			for _, id := range ids {
				read, x := s.Source.ReadChunk(ctx, owner, a.Analysis, id)
				if x != nil {
					if ctx.Err() != nil {
						return Results{}, ctx.Err()
					}
					return Results{}, x
				}
				verified[id] = read
			}
		}
	}
	for _, v := range result.Candidates {
		read, ok := verified[v.Document.Chunk.ID]
		if !ok || read.Chunk != v.Document.Chunk || ingest.Digest(read.Bytes) != v.Document.Chunk.SHA256 {
			return Results{}, ErrIntegrity
		}
	}
	result.Seconds = time.Since(start).Seconds()
	telemetry.Observe("retrieval.search", "ok", "", result.Seconds)
	return result, nil
}
