package worker

import (
	"context"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
)

type ReceiptStore interface {
	RecordDispatch(context.Context, string, int64, string) (bool, error)
}

// DevelopmentHandler is not an executor. Wiring must explicitly require development mode and an isolated stream.
type DevelopmentHandler struct{ Store ReceiptStore }

func (h DevelopmentHandler) Handle(ctx context.Context, job queue.Job, consumer string) (Outcome, error) {
	if job.Kind != queue.DispatchTestJob {
		return Unsupported, nil
	}
	inserted, err := h.Store.RecordDispatch(ctx, job.SubmissionID, job.Attempt, consumer)
	if err != nil {
		return Retry, err
	}
	if !inserted {
		return AlreadyHandled, nil
	}
	return Handled, nil
}
