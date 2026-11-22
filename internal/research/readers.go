package research

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/contextpack"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
)

type Readers struct {
	Retrieval retrieval.Reader
	Analysis  analysis.Reader
	Packs     contextpack.ContextBuilder
}

func (r Readers) resolve(ctx context.Context, owner, index string) (Scope, error) {
	if !validID(index) {
		return Scope{}, ErrPolicy
	}
	i, m, _, err := r.Retrieval.Ready(ctx, owner, index)
	if err != nil {
		return Scope{}, err
	}
	return Scope{Repository: i.Repository, Snapshot: i.Snapshot, Analysis: i.Analysis, Retrieval: i.ID, Commit: m.Commit}, nil
}

func (r Readers) read(ctx context.Context, owner string, scope Scope, d Decision) (o Observation, err error) {
	start := time.Now()
	defer func() {
		o.ElapsedSeconds = time.Since(start).Seconds()
		status := "ok"
		if err != nil {
			status = "error"
		}
		telemetry.Observe("research."+d.Tool, status, "", o.ElapsedSeconds)
	}()
	current, err := r.resolve(ctx, owner, scope.Retrieval)
	if err != nil {
		return o, err
	}
	if current != scope {
		return o, ErrDenied
	}
	var query queryArgs
	var id idArgs
	o.Status = "VERIFIED_METADATA"
	switch d.Tool {
	case "search_code":
		_ = json.Unmarshal(d.Args, &query)
		result, e := r.Retrieval.Search(ctx, owner, retrieval.Request{Index: scope.Retrieval, Snapshot: scope.Snapshot, Repository: scope.Repository, Query: query.Query, Mode: "hybrid", TopK: 8, Depth: 24})
		if e != nil {
			return o, e
		}
		for _, candidate := range result.Candidates {
			c := candidate.Document
			o.Hits = append(o.Hits, Hit{Chunk: c.Chunk.ID, Symbol: c.Chunk.Symbol, Name: c.Symbol, Path: c.Path})
		}
		o.Status = "CANDIDATES"
	case "read_source":
		_ = json.Unmarshal(d.Args, &id)
		v, e := r.Retrieval.ReadChunk(ctx, owner, scope.Retrieval, id.ID)
		if e != nil {
			return o, e
		}
		o.SourceBytes = len(v.Bytes)
		o.Citations = []Citation{{ID: v.Chunk.ID, File: v.File.ID, Path: v.File.Path, Span: v.Chunk.Span, SHA256: v.Chunk.SHA256, Direct: true}}
		o.Status = "VERIFIED_SOURCE"
	case "lookup_symbol":
		_ = json.Unmarshal(d.Args, &id)
		v, e := r.Analysis.ReadSymbol(ctx, owner, scope.Analysis, id.ID)
		if e != nil {
			return o, e
		}
		o.SourceBytes = len(v.Bytes)
		o.Citations = []Citation{{ID: v.Symbol.ID, File: v.File.ID, Path: v.File.Path, Span: v.Symbol.Span, SHA256: v.Symbol.SHA256, Direct: true}}
		o.Status = "VERIFIED_SOURCE"
	case "inspect_graph":
		_ = json.Unmarshal(d.Args, &id)
		symbol, e := r.Analysis.ReadSymbol(ctx, owner, scope.Analysis, id.ID)
		if e != nil {
			return o, e
		}
		o.SourceBytes = len(symbol.Bytes)
		_, m, e := r.Analysis.Ready(ctx, owner, scope.Analysis)
		if e != nil {
			return o, e
		}
		for _, edge := range m.Edges {
			if edge.From == id.ID || edge.Target == id.ID {
				o.Edges = append(o.Edges, edge)
			}
			if len(o.Edges) == 8 {
				break
			}
		}
		o.Status = "GRAPH_PROPOSALS"
	case "inspect_evidence":
		_ = json.Unmarshal(d.Args, &query)
		p, e := r.Packs.Build(ctx, owner, contextpack.Request{Repository: scope.Repository, Snapshot: scope.Snapshot, Index: scope.Retrieval, Query: query.Query, Policy: contextpack.DefaultPolicy()})
		if e != nil {
			return o, e
		}
		switch p.Status {
		case "READY", "INSUFFICIENT_EVIDENCE", "NO_CANDIDATES":
			if e = r.Packs.Verify(ctx, owner, p); e != nil {
				return o, e
			}
		default:
			return o, ErrBackend
		}
		o.Status = p.Status
		o.Uncertainty = p.Uncertainty
		o.Pack = p.ID
		o.Tokens = p.Consumed
		o.SourceBytes = len(p.Context)
		for _, v := range p.Evidence {
			o.Citations = append(o.Citations, Citation{ID: v.ChunkID, File: v.FileID, Path: v.Path, Span: v.Span, SHA256: v.SHA256, Direct: v.Direct})
		}
	default:
		return o, ErrPolicy
	}
	return o, nil
}

type Fixture struct{}

func (Fixture) Identity() string { return FixturePolicy }

func (Fixture) Next(_ context.Context, input PlannerInput) ([]byte, error) {
	queries := map[string]string{
		"lease":  "renewExecution heartbeat lease and guarded finalization completeExecution",
		"outbox": "publish outbox relay record acknowledgement retry crash recovery",
		"cache":  "compilation cache build restore reuse cache entry",
	}
	query := queries[input.Goal]
	if query == "" {
		query = input.Goal
	}
	call := func(tool string, args any) ([]byte, error) {
		body, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		return json.Marshal(Decision{Version: DecisionVersion, Kind: "CALL_TOOL", Tool: tool, Args: body})
	}
	history := input.History
	if len(history) == 0 {
		return call("search_code", queryArgs{Query: query})
	}
	last := history[len(history)-1]
	if last.Decision.Tool == "inspect_evidence" {
		outcome := "INSUFFICIENT_EVIDENCE"
		if last.Observation.Status == "READY" {
			outcome = "VERIFIED_REFERENCES"
		}
		return json.Marshal(Decision{Version: DecisionVersion, Kind: "FINISH", Outcome: outcome})
	}
	hits := history[0].Observation.Hits
	if len(history) == 1 && len(hits) > 0 {
		return call("read_source", idArgs{ID: hits[0].Chunk})
	}
	if len(history) == 2 {
		for _, hit := range hits {
			if hit.Symbol != "" {
				return call("lookup_symbol", idArgs{ID: hit.Symbol})
			}
		}
	}
	if last.Decision.Tool == "lookup_symbol" {
		return call("inspect_graph", idArgs{ID: last.Observation.Citations[0].ID})
	}
	if input.Goal == "lease" {
		const guardQuery = "authorizeWork"
		if last.Decision.Tool == "search_code" {
			for _, hit := range last.Observation.Hits {
				if hit.Name == guardQuery && hit.Symbol != "" {
					return call("lookup_symbol", idArgs{ID: hit.Symbol})
				}
			}
			if len(last.Observation.Hits) > 0 {
				return call("read_source", idArgs{ID: last.Observation.Hits[0].Chunk})
			}
		} else {
			found := false
			for _, step := range history[1:] {
				if step.Decision.Tool == "search_code" {
					found = true
					break
				}
			}
			if !found {
				return call("search_code", queryArgs{Query: guardQuery})
			}
		}
	}
	return call("inspect_evidence", queryArgs{Query: query})
}
