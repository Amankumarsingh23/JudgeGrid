package retrieval

import (
	"context"
	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
)

// EvaluationDense reuses the production exact/ANN SQL ranker. The caller must
// supply a manifest obtained through Ready; current owner/identity is checked
// again before the SQL operation. This is not a public search endpoint.
func (s *Service) EvaluationDense(ctx context.Context, owner string, expected Index, manifest Manifest, vector []float32, depth int) ([]Candidate, error) {
	current, e := s.DB.Status(ctx, owner, expected.ID)
	if e != nil {
		return nil, e
	}
	if current.State != "READY" || current.Manifest != expected.Manifest || current.Snapshot != expected.Snapshot || current.AnalysisManifest != expected.AnalysisManifest || depth < 1 || depth > 200 {
		return nil, ErrIntegrity
	}
	if ingest.Digest(canonical(manifest)) != current.Manifest {
		return nil, ErrIntegrity
	}
	if e = s.DB.verifyRows(ctx, owner, manifest); e != nil {
		return nil, e
	}
	out, _, e := s.DB.dense(ctx, owner, current, manifest.Documents, vector, depth, false)
	return out, e
}

// EvaluationSelect uses the same overlap grouping as Search, not a new ranker.
func EvaluationSelect(in []Candidate, k int) []Candidate { return diverse(in, k) }
