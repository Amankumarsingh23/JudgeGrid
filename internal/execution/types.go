// Package execution defines bounded program execution, independently of judging and queue delivery.
package execution

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const PolicyVersion = "docker-local-v1"
const SeccompSHA256 = "6416b47770785a41ac59073cdc77d9fe98517df2799dc83ef207e622de3053f6"

var ErrInvalid = errors.New("invalid execution request")

type InfrastructureError struct {
	Phase     string
	Operation string
	Cause     error
}

func (e *InfrastructureError) Error() string {
	return fmt.Sprintf("sandbox infrastructure %s/%s: %v", e.Phase, e.Operation, e.Cause)
}
func (e *InfrastructureError) Unwrap() error { return e.Cause }

type Executor interface {
	Execute(context.Context, Request) (Result, error)
}

// Preparer holds one bounded execution slot and one private artifact until Close.
// Each Run starts a fresh restricted container; no mutable runtime state is reused.
type Preparer interface {
	Prepare(context.Context, Request) (Prepared, Result, error)
}
type Prepared interface {
	Run(context.Context, []byte, Limits) (*PhaseResult, error)
	Summary() Result
	Close() error
}
type Limits struct {
	WallTime    time.Duration `json:"wall_time_ns"`
	MemoryBytes int64         `json:"memory_bytes"`
	OutputBytes int64         `json:"output_bytes"`
}

func DefaultRunLimits() Limits {
	return Limits{WallTime: 3 * time.Second, MemoryBytes: 64 << 20, OutputBytes: 64 << 10}
}
func DefaultCompileLimits() Limits {
	return Limits{WallTime: 15 * time.Second, MemoryBytes: 256 << 20, OutputBytes: 64 << 10}
}

type Request struct {
	Language      string
	Source        []byte
	Stdin         []byte
	CompileLimits Limits
	RunLimits     Limits
}

func (r Request) Validate() error {
	if r.Language != "cpp17" && r.Language != "python3" {
		return ErrInvalid
	}
	if len(r.Source) == 0 || len(r.Source) > 64<<10 || strings.TrimSpace(string(r.Source)) == "" || strings.ContainsRune(string(r.Source), 0) || len(r.Stdin) > 64<<10 {
		return ErrInvalid
	}
	for _, l := range []Limits{r.CompileLimits, r.RunLimits} {
		if l.WallTime < 50*time.Millisecond || l.WallTime > 30*time.Second || l.MemoryBytes < 32<<20 || l.MemoryBytes > 512<<20 || l.OutputBytes < 1 || l.OutputBytes > 1<<20 {
			return ErrInvalid
		}
	}
	return nil
}

type Outcome string

const (
	Exited         Outcome = "exited"
	NonzeroExit    Outcome = "nonzero_exit"
	WallTimeout    Outcome = "wall_timeout"
	OutputExceeded Outcome = "output_limit"
	MemoryExceeded Outcome = "confirmed_oom"
	Canceled       Outcome = "canceled"
)

type PhaseResult struct {
	Phase           string        `json:"phase"`
	Container       string        `json:"container"`
	Image           string        `json:"image"`
	Limits          Limits        `json:"limits"`
	Outcome         Outcome       `json:"outcome"`
	ExitCode        int           `json:"exit_code"`
	Stdout          string        `json:"stdout"`
	Stderr          string        `json:"stderr"`
	WallTime        time.Duration `json:"controller_wall_time_ns"`
	OOMKilled       bool          `json:"oom_killed"`
	CPUTimeNS       *int64        `json:"cpu_time_ns"`
	PeakMemoryBytes *int64        `json:"peak_memory_bytes"`
	MeasurementNote string        `json:"measurement_note"`
}
type Result struct {
	ExecutionID string       `json:"execution_id"`
	Policy      string       `json:"policy"`
	Prepare     *PhaseResult `json:"prepare,omitempty"`
	Compile     *PhaseResult `json:"compile,omitempty"`
	Run         *PhaseResult `json:"run,omitempty"`
	Cleaned     bool         `json:"cleaned"`
}

func classify(exit int, oom, timedOut, output, canceled bool) Outcome {
	if canceled {
		return Canceled
	}
	if output {
		return OutputExceeded
	}
	if timedOut {
		return WallTimeout
	}
	if oom {
		return MemoryExceeded
	}
	if exit != 0 {
		return NonzeroExit
	}
	return Exited
}

type Images struct {
	CPP        string `json:"cpp17"`
	Python     string `json:"python3"`
	CPPBase    string `json:"cpp_base,omitempty"`
	PythonBase string `json:"python_base,omitempty"`
}

var imageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (i Images) Validate() error {
	if !imageID.MatchString(i.CPP) || !imageID.MatchString(i.Python) {
		return fmt.Errorf("runtime images must be immutable local sha256 image IDs")
	}
	return nil
}
