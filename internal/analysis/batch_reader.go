package analysis

import (
	"context"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
)

// ReadChunks is a bounded bulk form of ReadChunk for downstream index builders.
// It authorizes/validates READY once and verifies each exact source file and span.
// No caller-provided manifest or unverified source bytes are accepted.
func (s *Service) ReadChunks(ctx context.Context, owner, index string, ids []string) ([]VerifiedChunk, error) {
	if len(ids) > 10000 {
		return nil, ErrBudget
	}
	a, m, e := s.Ready(ctx, owner, index)
	if e != nil {
		return nil, e
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		if wanted[id] {
			return nil, ErrIntegrity
		}
		wanted[id] = true
	}
	files := map[string]File{}
	for _, f := range m.Files {
		files[f.ID] = f
	}
	result := make([]VerifiedChunk, 0, len(ids))
	source := map[string]ingest.File{}
	total := 0
	sourceBytes := 0
	for _, c := range m.Chunks {
		if !wanted[c.ID] {
			continue
		}
		f := files[c.File]
		v, ok := source[f.ID]
		if !ok {
			v, e = s.Source.ReadFile(ctx, owner, a.Snapshot, f.Path)
			if e != nil {
				return nil, e
			}
			if v.Entry.ID != f.SourceID || v.Entry.SHA256 != f.SHA256 {
				return nil, ErrIntegrity
			}
			sourceBytes += len(v.Bytes)
			if sourceBytes > 32<<20 {
				return nil, ErrBudget
			}
			source[f.ID] = v
		}
		if c.Span.Start < 0 || c.Span.End > len(v.Bytes) || span(v.Bytes, c.Span.Start, c.Span.End) != c.Span {
			return nil, ErrIntegrity
		}
		b := v.Bytes[c.Span.Start:c.Span.End]
		total += len(b)
		if total > 32<<20 {
			return nil, ErrBudget
		}
		if ingest.Digest(b) != c.SHA256 {
			return nil, ErrIntegrity
		}
		result = append(result, VerifiedChunk{a, c, f, append([]byte(nil), b...)})
		delete(wanted, c.ID)
	}
	if len(wanted) != 0 {
		return nil, ErrDenied
	}
	return result, nil
}
