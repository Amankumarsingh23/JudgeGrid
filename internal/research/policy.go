package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Amankumarsingh23/JudgeGrid/internal/analysis"
)

var (
	ErrDenied  = errors.New("research access denied or claim lost")
	ErrPolicy  = errors.New("research policy denied")
	ErrBudget  = errors.New("research budget exhausted")
	ErrBackend = errors.New("research backend unavailable")
)

const (
	MaxSteps        = 12
	MaxAttempts     = 12
	MaxQueries      = 5
	MaxBytes        = 64 << 10
	MaxTokens       = 8192
	DecisionVersion = "research-decision-v1"
	FixturePolicy   = "repository-reference-fixture-v2"
)

type Scope struct {
	Repository string `json:"repository"`
	Snapshot   string `json:"snapshot"`
	Analysis   string `json:"analysis"`
	Retrieval  string `json:"retrieval"`
	Commit     string `json:"commit"`
}
type Decision struct {
	Version string          `json:"version"`
	Kind    string          `json:"kind"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Outcome string          `json:"outcome,omitempty"`
}
type Citation struct {
	ID     string        `json:"id"`
	Path   string        `json:"path"`
	File   string        `json:"file"`
	Span   analysis.Span `json:"span"`
	SHA256 string        `json:"sha256"`
	Direct bool          `json:"direct"`
}
type Hit struct {
	Chunk  string `json:"chunk"`
	Symbol string `json:"symbol,omitempty"`
	Name   string `json:"name,omitempty"`
	Path   string `json:"path"`
}
type Observation struct {
	Status         string          `json:"status"`
	Hits           []Hit           `json:"hits,omitempty"`
	Citations      []Citation      `json:"citations,omitempty"`
	Edges          []analysis.Edge `json:"graph_proposals,omitempty"`
	SourceBytes    int             `json:"source_bytes"`
	Tokens         int             `json:"context_wordpieces"`
	Pack           string          `json:"pack,omitempty"`
	ElapsedSeconds float64         `json:"elapsed_seconds,omitempty"`
	Uncertainty    []string        `json:"uncertainty,omitempty"`
}
type Step struct {
	Ordinal     int          `json:"ordinal"`
	Digest      string       `json:"action_sha256"`
	Decision    Decision     `json:"decision"`
	Observation *Observation `json:"observation,omitempty"`
	Attempts    int          `json:"attempts"`
}
type PlannerInput struct {
	Goal    string
	History []Step
}
type Planner interface {
	Identity() string
	Next(context.Context, PlannerInput) ([]byte, error)
}
type queryArgs struct {
	Query string `json:"query"`
}
type idArgs struct {
	ID string `json:"id"`
}

func strict(data []byte, dst any) error {
	if len(data) == 0 || len(data) > 4096 || !utf8.Valid(data) {
		return ErrPolicy
	}
	scan := json.NewDecoder(bytes.NewReader(data))
	if !uniqueJSON(scan, 0) {
		return ErrPolicy
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return ErrPolicy
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrPolicy
	}
	return nil
}

func uniqueJSON(decoder *json.Decoder, depth int) bool {
	if depth > 5 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	if token != json.Delim('{') && token != json.Delim('[') {
		return true
	}
	keys := map[string]bool{}
	for decoder.More() {
		if token == json.Delim('{') {
			key, err := decoder.Token()
			name, ok := key.(string)
			lower := strings.ToLower(name)
			if err != nil || !ok || name != lower || keys[lower] {
				return false
			}
			keys[lower] = true
		}
		if !uniqueJSON(decoder, depth+1) {
			return false
		}
	}
	_, err = decoder.Token()
	return err == nil
}
func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func validID(s string) bool {
	if len(s) != 64 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func decodeDecision(data []byte) (Decision, error) {
	var d Decision
	if strict(data, &d) != nil || d.Version != DecisionVersion {
		return d, ErrPolicy
	}
	switch d.Kind {
	case "FINISH":
		if d.Tool != "" || len(d.Args) != 0 || (d.Outcome != "NO_EVIDENCE" && d.Outcome != "INSUFFICIENT_EVIDENCE" && d.Outcome != "VERIFIED_REFERENCES") {
			return d, ErrPolicy
		}
	case "CALL_TOOL", "REQUEST_APPROVAL":
		if d.Outcome != "" {
			return d, ErrPolicy
		}
		var args any
		switch d.Tool {
		case "search_code", "inspect_evidence":
			var q queryArgs
			if strict(d.Args, &q) != nil || len(q.Query) > 256 || strings.TrimSpace(q.Query) == "" || strings.ContainsAny(q.Query, "\x00\r\n") {
				return d, ErrPolicy
			}
			args = q
		case "read_source", "lookup_symbol", "inspect_graph":
			var id idArgs
			if strict(d.Args, &id) != nil || !validID(id.ID) {
				return d, ErrPolicy
			}
			args = id
		default:
			return d, ErrPolicy
		}
		d.Args, _ = json.Marshal(args)
	default:
		return d, ErrPolicy
	}
	return d, nil
}
