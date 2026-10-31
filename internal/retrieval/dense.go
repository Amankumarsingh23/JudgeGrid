package retrieval

import (
	"context"
	"github.com/jackc/pgx/v5"
	"sort"
)

func (d Database) dense(ctx context.Context, owner string, a Index, docs []Document, q []float32, depth int, ann bool) ([]Candidate, bool, error) {
	if ValidateVector(q) != nil {
		return nil, false, ErrIntegrity
	}
	if len(docs) == 0 {
		return []Candidate{}, false, nil
	}
	allowed := make([]string, 0, len(docs))
	by := map[string]Document{}
	for _, v := range docs {
		allowed = append(allowed, v.Chunk.ID)
		by[v.Chunk.ID] = v
	}
	sort.Strings(allowed)
	tx, e := d.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, false, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `SET LOCAL statement_timeout='5s'; SET LOCAL hnsw.ef_search=200; SET LOCAL hnsw.iterative_scan='strict_order'; SET LOCAL hnsw.max_scan_tuples=10000`)
	if e != nil {
		return nil, false, e
	}
	search := func(approx bool) ([]Candidate, error) {
		// Per-index partition pruning and SQL owner/index joins precede all results.
		if approx {
			_, e = tx.Exec(ctx, `SET LOCAL enable_seqscan=off; SET LOCAL enable_bitmapscan=off; SET LOCAL enable_sort=off`)
		} else {
			_, e = tx.Exec(ctx, `SET LOCAL enable_indexscan=off; SET LOCAL enable_bitmapscan=off; SET LOCAL enable_seqscan=on; SET LOCAL enable_sort=on`)
		}
		if e != nil {
			return nil, e
		}
		// ANN's operator-only inner ORDER enables HNSW; deterministic final tie sorting
		// is applied to its bounded candidate set. Exact search ties by chunk ID.
		sql := `SELECT d.chunk_id,d.embedding OPERATOR(public.<=>) $3::public.vector AS distance FROM retrieval_documents d JOIN retrieval_indexes i ON i.id=d.index_id JOIN source_repositories r ON r.id=i.repository_id WHERE d.index_id=$1 AND r.owner=$2 AND i.state='READY' AND i.snapshot_id=$5 AND i.analysis_id=$6 AND d.chunk_id=ANY($4::text[]) ORDER BY d.embedding OPERATOR(public.<=>) $3::public.vector`
		if !approx {
			sql += `,d.chunk_id`
		}
		sql += ` LIMIT $7`
		rows, x := tx.Query(ctx, sql, a.ID, owner, vectorText(q), allowed, a.Snapshot, a.Analysis, depth)
		if x != nil {
			return nil, x
		}
		defer rows.Close()
		out := []Candidate{}
		for rows.Next() {
			var id string
			var distance float64
			if rows.Scan(&id, &distance) != nil || !finite(distance) {
				return nil, ErrIntegrity
			}
			doc, ok := by[id]
			if !ok {
				return nil, ErrIntegrity
			}
			out = append(out, Candidate{Document: doc, Distance: distance, Score: 1 - distance})
		}
		if x = rows.Err(); x != nil {
			return nil, x
		}
		sortCandidates(out)
		for i := range out {
			out[i].DenseRank = i + 1
		}
		return out, nil
	}
	out, e := search(ann)
	if e != nil {
		return nil, false, e
	}
	// Selective filters deliberately use exact fallback even when ANN fills: a
	// quarter-corpus cutoff is versioned policy, not an inferred recall guarantee.
	fallback := ann && (len(out) < min(depth, len(docs)) || len(docs)*4 < a.Stats.Documents)
	if fallback {
		out, e = search(false)
	}
	if e != nil {
		return nil, false, e
	}
	return out, fallback, tx.Commit(ctx)
}
