package storage

import (
	"context"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/judge"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/scheduling"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"time"
)

func captureTrace(ctx context.Context, tx pgx.Tx) error {
	parent := telemetry.Parent(ctx)
	if parent == "" {
		return nil
	}
	_, e := tx.Exec(ctx, `SELECT set_config('judgegrid.traceparent',$1,true)`, parent)
	return e
}
func recordFailedTerminal(w judge.Work) {
	telemetry.Count("terminal", "FAILED", w.Submission.Language)
	telemetry.Since("accepted_terminal", w.Submission.Language, w.AcceptedAt)
}
func (r *Repository) TraceParent(ctx context.Context, id string) string {
	parent, _, _ := r.traceMetadata(ctx, id)
	return parent
}
func (r *Repository) traceMetadata(ctx context.Context, id string) (parent string, accepted, published time.Time) {
	if !telemetry.Enabled() {
		return
	}
	check, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_ = r.pool.QueryRow(check, `SELECT COALESCE(traceparent,''),created_at,COALESCE(published_at,created_at) FROM outbox_events WHERE id=$1`, id).Scan(&parent, &accepted, &published)
	if !telemetry.ValidParent(parent) {
		parent = ""
	}
	return
}
func (r *Repository) Create(ctx context.Context, p, key string, req domain.CreateRequest) (s domain.Submission, replay bool, e error) {
	ctx, end := telemetry.Begin(ctx, "create", req.Language)
	defer func() {
		end(e)
		if e == nil && !replay {
			telemetry.Count("accepted", "ok", req.Language)
		}
	}()
	return r.create(ctx, p, key, req)
}
func (r *Repository) ClaimExecution(ctx context.Context, p, owner string, j queue.Job, ttl time.Duration, max int) (w judge.Work, e error) {
	parent, accepted, published := r.traceMetadata(ctx, j.EventID)
	ctx, end := telemetry.Link(ctx, "lease.claim", "", parent)
	defer func() {
		end(e)
		if e == nil && w.NewTerminal {
			telemetry.Count("terminal", "FAILED", w.Submission.Language)
			telemetry.Since("accepted_terminal", w.Submission.Language, accepted)
		}
		if e == nil && w.Existing == nil {
			telemetry.Count("scheduler.admit", "ok", w.Submission.Language)
			w.TraceParent = parent
			w.AcceptedAt = accepted
			telemetry.Since("accepted_admission", w.Submission.Language, accepted)
			telemetry.Since("eligible_admission", w.Submission.Language, published)
		}
		if e == ErrOwned {
			telemetry.Count("fence.reject", "fenced", "")
		}
	}()
	return r.claimExecution(ctx, p, owner, j, ttl, max)
}
func (r *Repository) RenewExecution(ctx context.Context, w judge.Work, ttl time.Duration) (e error) {
	ctx, end := telemetry.Begin(ctx, "lease.renew", w.Submission.Language)
	defer func() {
		end(e)
		if e == ErrOwned {
			telemetry.Count("fence.reject", "fenced", w.Submission.Language)
		}
	}()
	return r.renewExecution(ctx, w, ttl)
}
func (r *Repository) FailExecution(ctx context.Context, w judge.Work, code string, retry bool) (e error) {
	ctx, end := telemetry.Begin(ctx, "lease.fail", w.Submission.Language)
	defer func() {
		end(e)
	}()
	return r.failExecution(ctx, w, code, retry)
}
func (r *Repository) CommitEvaluation(ctx context.Context, w judge.Work, report judge.Report) (s judge.Summary, e error) {
	ctx, end := telemetry.Begin(ctx, "result.persist", w.Submission.Language)
	defer func() {
		end(e)
		if e == nil && !s.Replayed {
			telemetry.Count("terminal", string(report.Verdict), w.Submission.Language)
			telemetry.Since("accepted_terminal", w.Submission.Language, w.AcceptedAt)
		}
		if e == ErrOwned {
			telemetry.Count("fence.reject", "fenced", w.Submission.Language)
		}
	}()
	return r.commitEvaluation(ctx, w, report)
}
func (r *Repository) NextScheduled(ctx context.Context, allowed []string) (c *scheduling.Candidate, e error) {
	ctx, end := telemetry.Begin(ctx, "scheduler.poll", "")
	defer func() { end(e) }()
	return r.nextScheduled(ctx, allowed)
}

func (r *Repository) RequestRejudge(ctx context.Context, p, key, id string, version int64) (result RejudgeRequest, err error) {
	ctx, end := telemetry.Begin(ctx, "rejudge.request", "")
	defer func() {
		end(err)
		if err == nil && !result.Replayed {
			telemetry.Count("rejudge.accepted", "ok", "")
		}
	}()
	return r.requestRejudge(ctx, p, key, id, version)
}
