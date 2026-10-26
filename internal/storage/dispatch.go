package storage

import "context"

// RecordDispatch records one development-only receipt; it never changes submission state.
func (r *Repository) RecordDispatch(ctx context.Context, id string, attempt int64, consumer string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `INSERT INTO development_dispatch_receipts(submission_id,attempt,consumer_id) VALUES($1,$2,$3) ON CONFLICT(submission_id,attempt) DO NOTHING`, id, attempt, consumer)
	return tag.RowsAffected() == 1, err
}
