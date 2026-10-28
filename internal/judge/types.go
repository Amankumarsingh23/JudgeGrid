// Package judge evaluates trusted immutable suites and produces sanitized evidence, independently of dispatch.
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
)

const EvaluatorVersion = "judge-v1-ascii-tokens-stop-first"

// Failure exposes only a stable safe code. Never serialize hidden input, expected output or runtime output.
type Failure struct {
	Code  string
	Cause error
}

func (e *Failure) Error() string { return "local judging failed: " + e.Code }
func (e *Failure) Unwrap() error { return e.Cause }

var ErrConfiguration = errors.New("invalid judge configuration/evidence")

type Evidence struct {
	Outcome         execution.Outcome `json:"outcome"`
	ExitCode        int               `json:"exit_code"`
	WallTimeNS      int64             `json:"wall_time_ns"`
	OOMKilled       bool              `json:"oom_killed"`
	CPUTimeNS       *int64            `json:"cpu_time_ns"`
	PeakMemoryBytes *int64            `json:"peak_memory_bytes"`
	StdoutHash      string            `json:"stdout_hash"`
	StderrHash      string            `json:"stderr_hash"`
	OutputBytes     int               `json:"output_bytes"`
}

func evidence(p *execution.PhaseResult) *Evidence {
	if p == nil {
		return nil
	}
	return &Evidence{p.Outcome, p.ExitCode, int64(p.WallTime), p.OOMKilled, p.CPUTimeNS, p.PeakMemoryBytes, bundles.Hash([]byte(p.Stdout)), bundles.Hash([]byte(p.Stderr)), len(p.Stdout) + len(p.Stderr)}
}

type TestResult struct {
	CaseID   string         `json:"case_id"`
	Order    int            `json:"order"`
	Verdict  domain.Verdict `json:"verdict"`
	Match    *bool          `json:"output_match"`
	Evidence Evidence       `json:"evidence"`
}
type Report struct {
	CacheHit      bool           `json:"cache_hit,omitempty"`
	Verdict       domain.Verdict `json:"verdict"`
	Image         string         `json:"runtime_image"`
	Policy        string         `json:"execution_policy"`
	Compile       *Evidence      `json:"compile,omitempty"`
	CompileReason string         `json:"compile_reason,omitempty"`
	Tests         []TestResult   `json:"tests"`
	Cleaned       bool           `json:"cleaned"`
}

func (r Report) Fingerprint() string { data, _ := json.Marshal(r); return bundles.Hash(data) }

type Summary struct {
	CacheHit         bool            `json:"cache_hit,omitempty"`
	RunID            string          `json:"evaluation_run_id"`
	SubmissionID     string          `json:"submission_id"`
	ProblemVersionID int64           `json:"problem_version_id"`
	Generation       int             `json:"generation"`
	Status           string          `json:"status"`
	Verdict          *domain.Verdict `json:"verdict"`
	FailureCode      *string         `json:"failure_code,omitempty"`
	TestsRun         int             `json:"tests_run"`
	TestsPassed      int             `json:"tests_passed"`
	TestsTotal       int             `json:"tests_total"`
	Evaluator        string          `json:"evaluator_version"`
	Image            *string         `json:"runtime_image,omitempty"`
	StartedAt        time.Time       `json:"started_at"`
	FinishedAt       *time.Time      `json:"finished_at"`
	Replayed         bool            `json:"replayed"`
}
type Work struct {
	JobID                      string
	Owner                      string
	Generation                 int
	RunID                      string
	Submission                 domain.Submission
	Reference                  bundles.Reference
	TimeLimitMS, MemoryLimitKB int
	StateVersion               int64
	Existing                   *Summary
}
type Session interface {
	Begin(context.Context) (Work, error)
	Complete(context.Context, Work, Report) (Summary, error)
	Fail(context.Context, Work, string) (Summary, error)
	Close() error
}
type Repository interface {
	Acquire(context.Context, string, string) (Session, error)
}
