package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalid      = errors.New("invalid request")
	ErrTooLarge     = errors.New("source code exceeds limit")
	ErrNotFound     = errors.New("resource not found")
	ErrConflict     = errors.New("state or idempotency conflict")
	ErrUnauthorized = errors.New("authentication required")
	ErrBacklog      = errors.New("dispatch backlog admission threshold reached")
)

type Status string
type Verdict string

const (
	Queued         Status = "QUEUED"
	Running        Status = "RUNNING"
	Evaluating     Status = "EVALUATING"
	Completed      Status = "COMPLETED"
	Failed         Status = "FAILED"
	MaxSourceBytes        = 64 * 1024
	MaxBodyBytes          = 512 * 1024
)

func CanTransition(from, to Status) bool {
	switch from {
	case Queued:
		return to == Running || to == Failed
	case Running:
		return to == Evaluating || to == Failed
	case Evaluating:
		return to == Completed || to == Failed
	default:
		return false
	}
}

func ValidVerdict(v Verdict) bool {
	switch v {
	case "AC", "WA", "TLE", "MLE", "RE", "CE", "OLE":
		return true
	}
	return false
}

type Transition struct {
	From            Status
	To              Status
	ExpectedVersion int64
	Verdict         Verdict
	FailureReason   string
}

func (t Transition) Validate() error {
	if !CanTransition(t.From, t.To) || t.ExpectedVersion < 0 {
		return ErrConflict
	}
	if (t.To == Completed && !ValidVerdict(t.Verdict)) || (t.To != Completed && t.Verdict != "") {
		return ErrInvalid
	}
	if (t.To == Failed && strings.TrimSpace(t.FailureReason) == "") || (t.To != Failed && t.FailureReason != "") {
		return ErrInvalid
	}
	return nil
}

type CreateRequest struct {
	ProblemID  int64  `json:"problem_id"`
	Language   string `json:"language"`
	SourceCode string `json:"source_code"`
}

func (r CreateRequest) Normalize() CreateRequest {
	r.Language = strings.ToLower(strings.TrimSpace(r.Language))
	return r
}
func (r CreateRequest) Validate() error {
	if len(r.SourceCode) > MaxSourceBytes {
		return ErrTooLarge
	}
	if r.ProblemID <= 0 || strings.TrimSpace(r.SourceCode) == "" || strings.ContainsRune(r.SourceCode, '\x00') {
		return ErrInvalid
	}
	switch r.Language {
	case "cpp17", "python3", "go":
		return nil
	}
	return ErrInvalid
}
func (r CreateRequest) Fingerprint() string {
	data, _ := json.Marshal(r.Normalize())
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type Submission struct {
	ID               string    `json:"submission_id"`
	ProblemID        int64     `json:"problem_id"`
	ProblemVersionID int64     `json:"problem_version_id"`
	ProblemVersion   int       `json:"problem_version"`
	PrincipalID      string    `json:"-"`
	Language         string    `json:"language"`
	SourceCode       string    `json:"source_code"`
	Status           Status    `json:"status"`
	Verdict          *Verdict  `json:"verdict"`
	StateVersion     int64     `json:"state_version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type Problem struct {
	ID            int64  `json:"problem_id"`
	Slug          string `json:"slug"`
	Title         string `json:"title"`
	VersionID     int64  `json:"problem_version_id"`
	Version       int    `json:"problem_version"`
	Statement     string `json:"statement"`
	TimeLimitMS   int    `json:"time_limit_ms"`
	MemoryLimitKB int    `json:"memory_limit_kb"`
}

type Cursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}
type Page struct {
	Items      []Submission `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
}
