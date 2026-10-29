// Package progress contains only allowlisted public state, never evaluator inputs or output.
package progress

import (
	"context"
	"encoding/json"
	"time"
)

const PageSize = 32
const ReplayWindow = 256

type Event struct {
	Sequence int64           `json:"sequence"`
	Kind     string          `json:"kind"`
	Data     json.RawMessage `json:"data"`
	At       time.Time       `json:"at"`
}
type Snapshot struct {
	SubmissionID    string  `json:"submission_id"`
	Status          string  `json:"status"`
	Verdict         *string `json:"verdict"`
	OriginalVerdict *string `json:"original_verdict,omitempty"`
	OriginalVersion int64   `json:"original_problem_version_id"`
	CurrentVersion  int64   `json:"current_problem_version_id"`
	SelectedRun     *string `json:"selected_evaluation_run_id,omitempty"`
	JobID           *string `json:"evaluation_job_id,omitempty"`
	JobStatus       *string `json:"evaluation_job_status,omitempty"`
	Cursor          int64   `json:"cursor"`
	First           int64   `json:"first_sequence"`
}
type Store interface {
	ProgressSnapshot(context.Context, string, string) (Snapshot, error)
	ProgressEvents(context.Context, string, string, int64, int64, int) ([]Event, error)
	AcquireSSE(context.Context, string) (string, error)
	RenewSSE(context.Context, string, string) error
	ReleaseSSE(context.Context, string, string) error
}
