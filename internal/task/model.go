package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/Amankumarsingh23/JudgeGrid/internal/repair"
	"github.com/Amankumarsingh23/JudgeGrid/internal/research"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
)

const Policy = "private-coding-task-v1"
const Schema = "judgegrid-task-decision-v1"

var ErrDenied = errors.New("task authority unavailable")
var ErrPolicy = errors.New("task policy rejected input")
var ErrBusy = errors.New("task has an active claim")
var ErrModelUnavailable = errors.New("no authorized coding model is configured")

type Input struct {
	Schema                string
	UserGoal              string
	State                 string
	Version               string
	Citations             []research.Citation
	Repair                *repair.Diagnosis
	Allowed               []string
	CallsRemaining        int
	InputTokensRemaining  int
	OutputTokensRemaining int
}
type Usage struct{ InputTokens, OutputTokens int }
type Output struct {
	JSON  []byte
	Usage Usage
}
type ModelClient interface {
	Identity() string
	Decide(context.Context, Input) (Output, error)
}
type decision struct {
	Action string
	Patch  *workspace.Patch
}

func decode(data []byte) (decision, error) {
	if len(data) == 0 || len(data) > 384<<10 {
		return decision{}, ErrPolicy
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if first, e := d.Token(); e != nil || first != json.Delim('{') {
		return decision{}, ErrPolicy
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, e := d.Token()
		key, ok := token.(string)
		if e != nil || !ok || (key != "schema" && key != "action" && key != "patch") || fields[key] != nil {
			return decision{}, ErrPolicy
		}
		var v json.RawMessage
		if e = d.Decode(&v); e != nil {
			return decision{}, ErrPolicy
		}
		fields[key] = v
	}
	if end, e := d.Token(); e != nil || end != json.Delim('}') {
		return decision{}, ErrPolicy
	}
	if _, e := d.Token(); e != io.EOF {
		return decision{}, ErrPolicy
	}
	var schema, action string
	if json.Unmarshal(fields["schema"], &schema) != nil || schema != Schema || json.Unmarshal(fields["action"], &action) != nil {
		return decision{}, ErrPolicy
	}
	result := decision{Action: action}
	switch action {
	case "PREPARE_WORKSPACE", "FINISH", "ABSTAIN":
		if fields["patch"] != nil {
			return decision{}, ErrPolicy
		}
	case "PROPOSE_PATCH":
		p, e := workspace.Decode(bytes.NewReader(fields["patch"]))
		if e != nil {
			return decision{}, ErrPolicy
		}
		result.Patch = &p
	default:
		return decision{}, ErrPolicy
	}
	return result, nil
}

type Fixture struct{ FirstWrong bool }

func (f Fixture) Identity() string {
	if f.FirstWrong {
		return "task-fixture-two-v1"
	}
	return "task-fixture-one-v1"
}
func (f Fixture) Decide(ctx context.Context, in Input) (Output, error) {
	action := "ABSTAIN"
	var patch *workspace.Patch
	if in.Repair != nil {
		d := *in.Repair
		d.Requirement = "fixture-expect-two"
		if f.FirstWrong {
			d.Requirement = "fixture-two-attempts"
		}
		value, e := (repair.Fixture{}).Propose(ctx, d)
		if e != nil {
			return Output{}, e
		}
		if value.Patch != nil {
			action, patch = "PROPOSE_PATCH", value.Patch
		}
	} else if len(in.Allowed) > 0 && in.Allowed[0] == "FINISH" {
		action = "FINISH"
	} else if len(in.Citations) > 0 {
		action = "PREPARE_WORKSPACE"
	}
	body, e := json.Marshal(struct {
		Schema string           `json:"schema"`
		Action string           `json:"action"`
		Patch  *workspace.Patch `json:"patch,omitempty"`
	}{Schema, action, patch})
	return Output{JSON: body}, e
}

// ResearchFixture uses fixed typed Session 17 actions. It yields verified
// citation provenance and deliberately leaves task sufficiency unassessed.
type ResearchFixture struct{}

func (ResearchFixture) Identity() string { return "task-research-fixture-v1" }
func (ResearchFixture) Next(_ context.Context, in research.PlannerInput) ([]byte, error) {
	d := research.Decision{Version: research.DecisionVersion, Kind: "FINISH", Outcome: "INSUFFICIENT_EVIDENCE"}
	if len(in.History) == 0 {
		d = research.Decision{Version: research.DecisionVersion, Kind: "CALL_TOOL", Tool: "search_code"}
		d.Args, _ = json.Marshal(map[string]string{"query": in.Goal})
	}
	if len(in.History) == 1 && in.History[0].Observation != nil && len(in.History[0].Observation.Hits) > 0 {
		d = research.Decision{Version: research.DecisionVersion, Kind: "CALL_TOOL", Tool: "read_source"}
		d.Args, _ = json.Marshal(map[string]string{"id": in.History[0].Observation.Hits[0].Chunk})
	}
	return json.Marshal(d)
}
