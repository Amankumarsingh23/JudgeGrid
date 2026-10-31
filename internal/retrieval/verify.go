package retrieval

import (
	"context"
	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
)

// Verify recomputes persisted lexical facts and canonical embedding input hashes
// from the actual authorized source. Ready separately verifies stored vectors.
func (s *Service) Verify(ctx context.Context, owner, index string) error {
	a, m, _, e := s.Ready(ctx, owner, index)
	if e != nil {
		return e
	}
	ids := make([]string, len(m.Documents))
	for i, d := range m.Documents {
		ids[i] = d.Chunk.ID
	}
	var chunks []analysis.VerifiedChunk
	if bulk, ok := s.Source.(interface {
		ReadChunks(context.Context, string, string, []string) ([]analysis.VerifiedChunk, error)
	}); ok {
		chunks, e = bulk.ReadChunks(ctx, owner, a.Analysis, ids)
	} else {
		for _, id := range ids {
			v, x := s.Source.ReadChunk(ctx, owner, a.Analysis, id)
			if x != nil {
				return x
			}
			chunks = append(chunks, v)
		}
	}
	if e != nil {
		return e
	}
	by := map[string]analysis.VerifiedChunk{}
	for _, c := range chunks {
		by[c.Chunk.ID] = c
	}
	for _, d := range m.Documents {
		c, ok := by[d.Chunk.ID]
		if !ok {
			return ErrIntegrity
		}
		v, _, x := makeDocument(c.Chunk, c.File, d.Symbol, c.Bytes)
		if x != nil {
			return x
		}
		v.VectorHash = d.VectorHash
		if hash(v) != hash(d) {
			return ErrIntegrity
		}
	}
	return nil
}
