package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
)

type Service struct {
	DB     Database
	Source ingest.Reader
	Python string
	Lease  time.Duration
	fault  func(string, int)
}

func (s *Service) stage(stage string, n int) {
	if s.fault != nil {
		s.fault(stage, n)
	}
}
func code(e error) string {
	switch {
	case errors.Is(e, ErrParser):
		return "parser"
	case errors.Is(e, ErrBudget), errors.Is(e, ingest.ErrBudget):
		return "budget"
	case errors.Is(e, ErrIntegrity), errors.Is(e, ingest.ErrIntegrity), errors.Is(e, ingest.ErrSource), errors.Is(e, ingest.ErrStorage):
		return "integrity"
	case errors.Is(e, context.Canceled), errors.Is(e, context.DeadlineExceeded):
		return "canceled"
	}
	return "internal"
}
func (s *Service) Analyze(ctx context.Context, owner, snapshot string, p Policy, retry bool) (result Index, err error) {
	if e := p.Validate(); e != nil {
		return result, e
	}
	if s.Source == nil {
		return result, ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.MaxSeconds)*time.Second)
	defer cancel()
	ctx, end := telemetry.Begin(ctx, "analysis.total", "")
	defer func() { end(err) }()
	start := time.Now()
	source, inventory, e := s.Source.Ready(ctx, owner, snapshot)
	if e != nil {
		return result, e
	}
	if len(inventory.Entries) > p.MaxFiles {
		return result, ErrBudget
	}
	a, e := s.DB.Request(ctx, owner, source, p)
	if e != nil {
		return result, e
	}
	if a.State == "READY" {
		a, _, e = s.Ready(ctx, owner, a.ID)
		a.Replayed = true
		a.Attempt = &Stats{Seconds: time.Since(start).Seconds()}
		return a, e
	}
	ttl := s.Lease
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	c, e := s.DB.Claim(ctx, owner, a.ID, ttl, retry)
	if e != nil {
		return result, e
	}
	// Renewal is independent of native parsing. A lost lease cancels work; every
	// durable write checks the database clock, token and generation again.
	work, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-work.Done():
				return
			case <-ticker.C:
				if s.DB.Renew(work, c) != nil {
					cancelWork()
					return
				}
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
		if err != nil {
			failCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
			defer done()
			_ = s.DB.Fail(failCtx, c, code(err))
		}
	}()
	parser, e := OpenParser(s.Python)
	if e != nil {
		return c.Index, e
	}
	defer parser.Close()
	m := Manifest{Schema: 1, Index: a.ID, Repository: a.Repository, Snapshot: a.Snapshot, SourceManifest: source.Manifest, Commit: source.Commit, Policy: p.ID(), Files: []File{}, Symbols: []Symbol{}, Imports: []Import{}, Calls: []Call{}, Edges: []Edge{}, Chunks: []Chunk{}}
	stats := Stats{Files: map[string]int{}, Languages: map[string]int{}, Symbols: map[string]int{}, Calls: map[string]int{}, Imports: map[string]int{}, Edges: map[string]int{}}
	units := []unit{}
	localRows := 0
	for n, entry := range inventory.Entries {
		if e = work.Err(); e != nil {
			return c.Index, e
		}
		f := File{ID: hash([]string{"analysis-file-v1", a.ID, entry.ID}), SourceID: entry.ID, Path: entry.Path, Language: entry.Language, Grammar: Grammar(entry.Language, entry.Path), SHA256: entry.SHA256, Size: entry.Size, State: "EXCLUDED", Reason: entry.Reason, Errors: []ParseError{}}
		var b []byte
		facts := Facts{Schema: 1, Symbols: []LocalSymbol{}, Imports: []LocalImport{}, Calls: []LocalCall{}, Bindings: []Binding{}, References: []LocalReference{}, Errors: []ParseError{}}
		if entry.Reason == "included" {
			if entry.Size > int64(p.MaxFileBytes) || stats.SourceBytes+entry.Size > int64(p.MaxTotalBytes) {
				_ = s.DB.Progress(work, c, entry.ID, "FAILED", "budget")
				return c.Index, ErrBudget
			}
			if e = s.DB.Progress(work, c, entry.ID, "PARSING", ""); e != nil {
				return c.Index, e
			}
			s.stage("before-read", n)
			read, x := s.Source.ReadFile(work, owner, snapshot, entry.Path)
			if x != nil {
				_ = s.DB.Progress(work, c, entry.ID, "FAILED", code(x))
				return c.Index, x
			}
			if read.Entry != entry || read.Snapshot.ID != source.ID || int64(len(read.Bytes)) != entry.Size || ingest.Digest(read.Bytes) != entry.SHA256 {
				return c.Index, ErrIntegrity
			}
			b = read.Bytes
			stats.SourceBytes += entry.Size
			f.State = "UNSUPPORTED"
			if f.Grammar != "" {
				f.Cache = hash([]string{"file-facts-v1", source.Repository, entry.SHA256, f.Grammar, p.ID()})
				var hit bool
				facts, hit, e = s.DB.Cached(work, c, f.Cache)
				if e != nil {
					return c.Index, e
				}
				if hit {
					if e = validateFacts(facts, b, p); e != nil {
						return c.Index, e
					}
					stats.CacheHits++
				} else {
					stats.CacheMisses++
					parseCtx, endParse := telemetry.Begin(work, "analysis.parse", "")
					facts, e = parser.Parse(parseCtx, f.Grammar, b, p)
					endParse(e)
					if e != nil {
						_ = s.DB.Progress(work, c, entry.ID, "FAILED", code(e))
						return c.Index, e
					}
					if e = s.DB.Cache(work, c, f.Cache, facts); e != nil {
						return c.Index, e
					}
				}
				f.State = facts.Status
				f.Package = facts.Package
				f.Errors = facts.Errors
			}
		}
		u, chunks, x := materialize(a.ID, f, b, facts, p)
		if x != nil {
			_ = s.DB.Progress(work, c, entry.ID, "FAILED", code(x))
			return c.Index, x
		}
		units = append(units, u)
		localRows += 1 + len(u.symbols) + len(chunks) + len(facts.Calls) + len(facts.Imports) + len(facts.References) + len(facts.Bindings)
		if localRows > p.MaxRows {
			return c.Index, ErrBudget
		}
		m.Files = append(m.Files, f)
		m.Symbols = append(m.Symbols, u.symbols...)
		m.Chunks = append(m.Chunks, chunks...)
		stats.Files[f.State]++
		if entry.Reason == "included" {
			stats.Languages[f.Language]++
		}
		if e = s.DB.Progress(work, c, entry.ID, f.State, ""); e != nil {
			return c.Index, e
		}
		s.stage("file", n)
	}
	if e = relationships(work, &m, units, p); e != nil {
		return c.Index, e
	}
	if e = validateManifest(m, inventory, p); e != nil {
		return c.Index, e
	}
	s.stage("publish", len(m.Files))
	for _, v := range m.Symbols {
		stats.Symbols[v.Kind]++
	}
	for _, v := range m.Calls {
		stats.Calls[v.Class]++
	}
	for _, v := range m.Imports {
		stats.Imports[v.Class]++
	}
	for _, v := range m.Edges {
		stats.Edges[v.Kind+":"+v.Class]++
	}
	stats.Chunks = len(m.Chunks)
	for _, v := range m.Chunks {
		stats.ChunkBytes += int64(v.Span.End - v.Span.Start)
	}
	stats.Seconds = time.Since(start).Seconds()
	if e = s.DB.Publish(work, c, m, stats, p); e != nil {
		return c.Index, e
	}
	telemetry.Count("analysis.completed", "ok", "")
	result, err = s.DB.Status(ctx, owner, a.ID)
	stats.Seconds = time.Since(start).Seconds()
	result.Attempt = &stats
	return result, err
}

func validateManifest(m Manifest, source ingest.Manifest, p Policy) error {
	if m.Schema != 1 || m.Snapshot != source.Snapshot || m.Repository != source.Repository || m.Commit != source.Commit || len(m.Files) != len(source.Entries) || m.Index != indexID(m.Snapshot, m.Policy) || m.Policy != p.ID() {
		return ErrIntegrity
	}
	ids := map[string]string{}
	symbols := map[string]Symbol{}
	files := map[string]File{}
	add := func(id, kind string) bool {
		if len(id) != 64 || ids[id] != "" {
			return false
		}
		ids[id] = kind
		return true
	}
	for i, f := range m.Files {
		e := source.Entries[i]
		if !add(f.ID, "file") || f.ID != hash([]string{"analysis-file-v1", m.Index, e.ID}) || f.SourceID != e.ID || f.Path != e.Path || f.Size != e.Size || f.SHA256 != e.SHA256 || f.Reason != e.Reason || f.Language != e.Language || f.Grammar != Grammar(e.Language, e.Path) {
			return ErrIntegrity
		}
		if e.Reason != "included" {
			if f.State != "EXCLUDED" {
				return ErrIntegrity
			}
		} else if f.State != "PARSED" && f.State != "PARTIAL" && f.State != "UNSUPPORTED" {
			return ErrIntegrity
		}
		if e.Reason == "included" && ((f.Grammar == "") != (f.State == "UNSUPPORTED")) {
			return ErrIntegrity
		}
		files[f.ID] = f
	}
	valid := func(file string, s Span) bool {
		f, ok := files[file]
		return ok && f.State != "EXCLUDED" && s.Start >= 0 && s.End >= s.Start && int64(s.End) <= f.Size && s.StartLine >= 1 && s.EndLine >= s.StartLine
	}
	for _, s := range m.Symbols {
		if !add(s.ID, "symbol") || !valid(s.File, s.Span) || !valid(s.File, s.NameSpan) || !valid(s.File, s.Scope) || s.Span.Start < s.Scope.Start || s.Span.End > s.Scope.End {
			return ErrIntegrity
		}
		if s.NameSpan.Start < s.Span.Start || s.NameSpan.End > s.Span.End {
			return ErrIntegrity
		}
		symbols[s.ID] = s
	}
	for _, s := range m.Symbols {
		if s.Parent != "" && (ids[s.Parent] != "symbol" || symbols[s.Parent].File != s.File || symbols[s.Parent].Span.Start > s.Span.Start || symbols[s.Parent].Span.End < s.Span.End) {
			return ErrIntegrity
		}
	}
	for _, s := range m.Imports {
		if !add(s.ID, "import") || !valid(s.File, s.Span) {
			return ErrIntegrity
		}
		for _, t := range s.Targets {
			if ids[t] != "file" {
				return ErrIntegrity
			}
		}
	}
	for _, s := range m.Calls {
		if !add(s.ID, "call") || !valid(s.File, s.Span) || (s.Parent != "" && ids[s.Parent] != "symbol") {
			return ErrIntegrity
		}
		for _, t := range s.Targets {
			if ids[t] != "symbol" {
				return ErrIntegrity
			}
		}
	}
	for _, s := range m.Edges {
		if !add(s.ID, "edge") || !valid(s.File, s.Span) || (ids[s.From] != "file" && ids[s.From] != "symbol") {
			return ErrIntegrity
		}
		if (s.TargetKind == "symbol" || s.TargetKind == "file") && ids[s.Target] != s.TargetKind {
			return ErrIntegrity
		}
	}
	perFile := map[string]int{}
	coverage := map[string][][2]int{}
	symbolCoverage := map[string][][2]int{}
	for _, s := range m.Chunks {
		perFile[s.File]++
		if !add(s.ID, "chunk") || !valid(s.File, s.Span) || s.Span.End-s.Span.Start > p.ChunkBytes || s.Span.End <= s.Span.Start || perFile[s.File] > p.MaxChunks || (s.Symbol != "" && ids[s.Symbol] != "symbol") || (s.Parent != "" && ids[s.Parent] != "symbol") {
			return ErrIntegrity
		}
		coverage[s.File] = append(coverage[s.File], [2]int{s.Span.Start, s.Span.End})
		if s.Symbol != "" {
			v := symbols[s.Symbol]
			if v.File != s.File || v.Parent != s.Parent || s.Span.Start < v.Span.Start || s.Span.End > v.Span.End {
				return ErrIntegrity
			}
			symbolCoverage[s.Symbol] = append(symbolCoverage[s.Symbol], [2]int{s.Span.Start, s.Span.End})
		}
	}
	covered := func(ranges [][2]int, start, end int) bool {
		sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
		pos := start
		for _, r := range ranges {
			if r[0] > pos {
				return false
			}
			if r[1] > pos {
				pos = r[1]
			}
		}
		return pos == end
	}
	for _, f := range m.Files {
		if f.Reason == "included" && !covered(coverage[f.ID], 0, int(f.Size)) {
			return ErrIntegrity
		}
	}
	for _, v := range m.Symbols {
		if !covered(symbolCoverage[v.ID], v.Span.Start, v.Span.End) {
			return ErrIntegrity
		}
	}
	if len(ids) > p.MaxRows {
		return ErrBudget
	}
	return nil
}

func (s *Service) Ready(ctx context.Context, owner, id string) (Index, Manifest, error) {
	a, m, e := s.DB.Manifest(ctx, owner, id)
	if e != nil {
		return a, m, e
	}
	source, inventory, e := s.Source.Ready(ctx, owner, a.Snapshot)
	if e != nil {
		return a, Manifest{}, e
	}
	if source.Manifest != m.SourceManifest {
		return a, Manifest{}, ErrIntegrity
	}
	var body []byte
	e = s.DB.Pool.QueryRow(ctx, `SELECT body FROM analysis_policies WHERE id=$1`, a.Policy).Scan(&body)
	if e != nil {
		return a, Manifest{}, ErrIntegrity
	}
	var p Policy
	if jsonErr := json.Unmarshal(body, &p); jsonErr != nil || p.ID() != a.Policy {
		return a, Manifest{}, ErrIntegrity
	}
	if e = validateManifest(m, inventory, p); e != nil {
		return a, Manifest{}, e
	}
	return a, m, nil
}
func (s *Service) ReadChunk(ctx context.Context, owner, index, id string) (VerifiedChunk, error) {
	a, m, e := s.Ready(ctx, owner, index)
	if e != nil {
		return VerifiedChunk{}, e
	}
	for _, c := range m.Chunks {
		if c.ID == id {
			for _, f := range m.Files {
				if f.ID == c.File {
					b, e := s.readSpan(ctx, owner, a, f, c.Span, c.SHA256)
					return VerifiedChunk{a, c, f, b}, e
				}
			}
		}
	}
	return VerifiedChunk{}, ErrDenied
}
func (s *Service) ReadSymbol(ctx context.Context, owner, index, id string) (VerifiedSymbol, error) {
	a, m, e := s.Ready(ctx, owner, index)
	if e != nil {
		return VerifiedSymbol{}, e
	}
	for _, v := range m.Symbols {
		if v.ID == id {
			for _, f := range m.Files {
				if f.ID == v.File {
					b, e := s.readSpan(ctx, owner, a, f, v.Span, v.SHA256)
					return VerifiedSymbol{a, v, f, b}, e
				}
			}
		}
	}
	return VerifiedSymbol{}, ErrDenied
}
func (s *Service) readSpan(ctx context.Context, owner string, a Index, f File, r Span, digest string) ([]byte, error) {
	v, e := s.Source.ReadFile(ctx, owner, a.Snapshot, f.Path)
	if e != nil {
		return nil, e
	}
	if v.Entry.ID != f.SourceID || v.Entry.SHA256 != f.SHA256 || r.Start < 0 || r.End > len(v.Bytes) || span(v.Bytes, r.Start, r.End) != r {
		return nil, ErrIntegrity
	}
	b := v.Bytes[r.Start:r.End]
	if ingest.Digest(b) != digest {
		return nil, ErrIntegrity
	}
	return b, nil
}
