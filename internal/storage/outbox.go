package storage

import (
	"context"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/outbox"
	"time"
)

func (r *Repository) Claim(ctx context.Context, limit int, ttl time.Duration) ([]outbox.Event, error) {
	if limit < 1 || limit > 16 || ttl < time.Second || ttl > 5*time.Minute {
		return nil, domain.ErrInvalid
	}
	// Separate indexed candidate sets bound both due and expired scans. Transaction ends before XADD.
	rows, err := r.pool.Query(ctx, `WITH candidates AS (
 SELECT id FROM (SELECT id,created_at FROM outbox_events WHERE status='PENDING' AND next_attempt_at<=statement_timestamp() ORDER BY next_attempt_at,created_at,id LIMIT $1) p
 UNION SELECT id FROM (SELECT id,created_at FROM outbox_events WHERE status='CLAIMED' AND claim_expires_at<=statement_timestamp() ORDER BY claim_expires_at,id LIMIT $1) c
 ), selected AS (SELECT e.id FROM outbox_events e JOIN candidates c ON c.id=e.id
 WHERE (e.status='PENDING' AND e.next_attempt_at<=statement_timestamp()) OR(e.status='CLAIMED' AND e.claim_expires_at<=statement_timestamp())
 ORDER BY e.created_at,e.id FOR UPDATE OF e SKIP LOCKED LIMIT $1)
 UPDATE outbox_events e SET status='CLAIMED',claim_generation=claim_generation+1,claim_token=gen_random_uuid(),claim_expires_at=clock_timestamp()+$2::bigint*interval '1 millisecond',attempts=attempts+1
	 FROM selected s WHERE e.id=s.id RETURNING e.id::text,e.submission_id::text,e.claim_token::text,e.claim_generation,e.intent_generation,e.payload_version,e.attempts-e.retry_base,COALESCE(e.job_id::text,'')`, limit, ttl.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []outbox.Event{}
	for rows.Next() {
		var e outbox.Event
		if err = rows.Scan(&e.ID, &e.SubmissionID, &e.Token, &e.Generation, &e.IntentGeneration, &e.Version, &e.Attempts, &e.JobID); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
func (r *Repository) Published(ctx context.Context, e outbox.Event, streamID string) error {
	if streamID == "" || len(streamID) > 100 {
		return domain.ErrInvalid
	}
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events SET status='PUBLISHED',published_at=clock_timestamp(),stream_id=$4,claim_token=NULL,claim_expires_at=NULL,last_error_code=NULL WHERE id=$1 AND status='CLAIMED' AND claim_token=$2 AND claim_generation=$3 AND claim_expires_at>clock_timestamp()`, e.ID, e.Token, e.Generation, streamID)
	if err == nil && tag.RowsAffected() != 1 {
		return outbox.ErrClaimLost
	}
	return err
}
func (r *Repository) Failed(ctx context.Context, e outbox.Event, code string, quarantine bool, delay time.Duration) error {
	switch code {
	case "malformed_event", "redis_configuration", "retry_exhausted", "redis_unavailable_or_uncertain":
	default:
		return domain.ErrInvalid
	}
	if delay < 0 || delay > time.Hour {
		return domain.ErrInvalid
	}
	status := "PENDING"
	if quarantine {
		status = "QUARANTINED"
	}
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events SET status=$4,next_attempt_at=clock_timestamp()+$5::bigint*interval '1 millisecond',last_error_code=$6,claim_token=NULL,claim_expires_at=NULL WHERE id=$1 AND status='CLAIMED' AND claim_token=$2 AND claim_generation=$3 AND claim_expires_at>clock_timestamp()`, e.ID, e.Token, e.Generation, status, delay.Milliseconds(), code)
	if err == nil && tag.RowsAffected() != 1 {
		return outbox.ErrClaimLost
	}
	return err
}
func (r *Repository) Stats(ctx context.Context) (outbox.Stats, error) {
	var s outbox.Stats
	for _, status := range []string{"PENDING", "CLAIMED", "PUBLISHED", "QUARANTINED"} {
		var count, retries int64
		var age float64
		err := r.pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(GREATEST(attempts-1,0)),0),COALESCE(EXTRACT(EPOCH FROM clock_timestamp()-min(created_at)),0) FROM(SELECT created_at,attempts FROM outbox_events WHERE status=$1 ORDER BY created_at,id LIMIT 10001) bounded`, status).Scan(&count, &retries, &age)
		if err != nil {
			return s, err
		}
		if count > 10000 {
			s.Capped = true
		}
		s.RetryCount += retries
		switch status {
		case "PENDING":
			s.Pending = count
			s.OldestAgeSeconds = age
		case "CLAIMED":
			s.Claimed = count
			if age > s.OldestAgeSeconds {
				s.OldestAgeSeconds = age
			}
		case "PUBLISHED":
			s.Published = count
		case "QUARANTINED":
			s.Quarantined = count
		}
	}
	return s, nil
}
func (r *Repository) RetryOutbox(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE outbox_events SET status='PENDING',next_attempt_at=clock_timestamp(),last_error_code='operator_retry',retry_base=attempts,claim_token=NULL,claim_expires_at=NULL WHERE id=$1 AND (status='QUARANTINED' OR(status='CLAIMED' AND claim_expires_at<=clock_timestamp()))`, id)
	if err == nil && tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return err
}
func (r *Repository) BackfillOutbox(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM submissions WHERE id=$1 FOR UPDATE`, id).Scan(&status); err != nil {
		return err
	}
	if status != "QUEUED" {
		return domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(submission_id) VALUES($1) ON CONFLICT(submission_id,event_type,intent_generation) WHERE job_id IS NULL DO NOTHING`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
