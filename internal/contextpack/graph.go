package contextpack

import (
	"context"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/retrieval"
	"sort"
)

type neighbor struct {
	node string
	edge analysis.Edge
}

// Verify the claimed bounded path, independently of exported candidate diagnostics.
func reasonConnects(am analysis.Manifest, seed, target retrieval.Document, last analysis.Edge, depth, limit int) bool {
	nodes := map[string]bool{}
	for _, f := range am.Files {
		nodes[f.ID] = true
	}
	for _, s := range am.Symbols {
		nodes[s.ID] = true
	}
	allowed := func(e analysis.Edge) bool {
		return e.Class == "resolved" && edgePriority(e.Kind) <= 3 && (e.TargetKind == "file" || e.TargetKind == "symbol") && nodes[e.From] && nodes[e.Target] && nodes[e.File]
	}
	if !allowed(last) {
		return false
	}
	start := seed.Chunk.Symbol
	if start == "" {
		start = seed.Chunk.File
	}
	for _, direction := range [][2]string{{last.From, last.Target}, {last.Target, last.From}} {
		before, end := direction[0], direction[1]
		if end != target.Chunk.File && (target.Chunk.Symbol == "" || end != target.Chunk.Symbol) {
			continue
		}
		if depth == 1 && before == start {
			return true
		}
		if depth == 2 {
			for _, e := range am.Edges[:min(limit, len(am.Edges))] {
				if allowed(e) && ((e.From == start && e.Target == before) || (e.Target == start && e.From == before)) {
					return true
				}
			}
		}
	}
	return false
}

func edgePriority(kind string) int {
	switch kind {
	case "CALLS":
		return 0
	case "RECEIVER_OF":
		return 1
	case "CONTAINS":
		return 2
	case "IMPORTS":
		return 3
	}
	return 9
}
func Expand(ctx context.Context, am analysis.Manifest, rm retrieval.Manifest, seeds []Candidate, p Policy) (map[string][]Reason, int, bool, error) {
	if am.Index != rm.Analysis || am.Snapshot != rm.Snapshot || am.Repository != rm.Repository || am.Commit != rm.Commit || ingest.Digest(canonical(am)) != rm.AnalysisManifest {
		return nil, 0, false, ErrIntegrity
	}
	result := map[string][]Reason{}
	if p.GraphDepth == 0 || p.Expanded == 0 {
		return result, 0, false, nil
	}
	nodes := map[string]string{}
	for _, f := range am.Files {
		nodes[f.ID] = "file"
	}
	for _, s := range am.Symbols {
		nodes[s.ID] = "symbol"
	}
	adj := map[string][]neighbor{}
	touched := 0
	limited := false
	for _, edge := range am.Edges {
		if ctx.Err() != nil {
			return nil, touched, false, ctx.Err()
		}
		if touched >= p.TraversalEdges {
			limited = true
			break
		}
		touched++
		if edge.Class != "resolved" || edgePriority(edge.Kind) > 3 || (edge.TargetKind != "file" && edge.TargetKind != "symbol") {
			continue
		}
		if nodes[edge.From] == "" || nodes[edge.Target] != edge.TargetKind || nodes[edge.File] != "file" {
			return nil, touched, false, ErrIntegrity
		}
		adj[edge.From] = append(adj[edge.From], neighbor{edge.Target, edge})
		adj[edge.Target] = append(adj[edge.Target], neighbor{edge.From, edge})
	}
	for node := range adj {
		sort.Slice(adj[node], func(i, j int) bool {
			a, b := adj[node][i], adj[node][j]
			if edgePriority(a.edge.Kind) != edgePriority(b.edge.Kind) {
				return edgePriority(a.edge.Kind) < edgePriority(b.edge.Kind)
			}
			return a.edge.ID < b.edge.ID
		})
	}
	chunks := map[string][]string{}
	direct := map[string]bool{}
	for _, c := range seeds {
		direct[c.Chunk.ID] = true
	}
	for _, d := range rm.Documents {
		chunks[d.Chunk.File] = append(chunks[d.Chunk.File], d.Chunk.ID)
		if d.Chunk.Symbol != "" {
			chunks[d.Chunk.Symbol] = append(chunks[d.Chunk.Symbol], d.Chunk.ID)
		}
	}
	for k := range chunks {
		sort.Strings(chunks[k])
	}
	type step struct {
		node  string
		depth int
	}
	visitedTotal := 0
	expanded := map[string]bool{}
	for _, seed := range seeds[:min(p.GraphSeeds, len(seeds))] {
		node := seed.Chunk.Symbol
		if node == "" {
			node = seed.Chunk.File
		}
		queue := []step{{node, 0}}
		visited := map[string]bool{node: true}
		for len(queue) > 0 {
			if ctx.Err() != nil {
				return nil, touched, false, ctx.Err()
			}
			current := queue[0]
			queue = queue[1:]
			if current.depth >= p.GraphDepth {
				continue
			}
			fan := 0
			for _, n := range adj[current.node] {
				if fan >= p.Fanout {
					break
				}
				if visited[n.node] {
					continue
				}
				visited[n.node] = true
				visitedTotal++
				if visitedTotal > 128 {
					return result, touched, true, nil
				}
				fan++
				reason := Reason{seed.Chunk.ID, n.edge.ID, n.edge.Kind, n.edge.Class, current.depth + 1, n.edge.Span}
				for _, id := range chunks[n.node] {
					if !direct[id] && !expanded[id] {
						if len(expanded) >= p.Expanded {
							limited = true
							continue
						}
						expanded[id] = true
					}
					if len(result[id]) < 4 {
						result[id] = append(result[id], reason)
					}
				}
				queue = append(queue, step{n.node, current.depth + 1})
			}
		}
	}
	return result, touched, limited, nil
}
