package storage

import (
	"context"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
)

// RecordDispatch records one development-only receipt; it never changes submission state.
func (r *Repository) RecordDispatch(ctx context.Context, id string, attempt int64, consumer string) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var status string
	var version int64
	if err = tx.QueryRow(ctx, `SELECT status,state_version FROM submissions WHERE id=$1 FOR SHARE`, id).Scan(&status, &version); err != nil {
		return false, err
	}
	if status != "QUEUED" || version != 0 {
		return false, domain.ErrConflict
	}
	tag, err := tx.Exec(ctx, `INSERT INTO development_dispatch_receipts(submission_id,attempt,consumer_id) VALUES($1,$2,$3) ON CONFLICT(submission_id,attempt) DO NOTHING`, id, attempt, consumer)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, tx.Commit(ctx)
}
