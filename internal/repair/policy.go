package repair

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"github.com/Amankumarsingh23/JudgeGrid/internal/ingest"
	"github.com/Amankumarsingh23/JudgeGrid/internal/validation"
	"github.com/Amankumarsingh23/JudgeGrid/internal/workspace"
)

const Policy = "reviewed-go-repair-v1"
const FixturePolicy = "repair-fixture-v1"
const ManualPolicy = "trusted-manual-v1"
const maxEvidence = 8192

var ErrDenied = errors.New("repair authority unavailable")
var ErrBusy = errors.New("repair has an active claim")
var ErrPolicy = errors.New("repair policy rejected input")
var ErrEvidence = errors.New("repair lacks bounded source evidence")

type Diagnosis struct {
	Workspace    string         `json:"workspace"`
	Version      string         `json:"version"`
	Validation   string         `json:"validation"`
	ReportDigest string         `json:"report_sha256"`
	Outcome      string         `json:"outcome"`
	Profile      string         `json:"profile"`
	Requirement  string         `json:"requirement"`
	File         workspace.File `json:"file"`
	FirstLine    int            `json:"first_line"`
	LastLine     int            `json:"last_line"`
	Source       string         `json:"verified_source"`
	UntrustedLog string         `json:"untrusted_log_excerpt"`
	Uncertainty  string         `json:"uncertainty"`
}
type Decision struct {
	Patch *workspace.Patch
	Stop  string
}
type RepairProposer interface {
	Identity() string
	Propose(context.Context, Diagnosis) (Decision, error)
}

type Fixture struct{}

func (Fixture) Identity() string { return FixturePolicy }

func (Fixture) Propose(_ context.Context, d Diagnosis) (Decision, error) {
	if d.Outcome != "TEST_FAILURE" {
		return Decision{Stop: "insufficient_evidence"}, nil
	}
	const one = "package fixture\n\nfunc Value() int { return 1 }\n"
	const three = "package fixture\n\nfunc Value() int { return 3 }\n"
	const two = "package fixture\n\nfunc Value() int { return 2 }\n"
	after := ""
	switch {
	case d.Requirement == "fixture-expect-two" && d.Source == one:
		after = two
	case d.Requirement == "fixture-two-attempts" && d.Source == one:
		after = three
	case d.Requirement == "fixture-two-attempts" && d.Source == three:
		after = two
	case d.Requirement == "fixture-exhaust-budget" && d.Source == one:
		after = three
	case d.Requirement == "fixture-exhaust-budget" && d.Source == three:
		after = "package fixture\n\nfunc Value() int { return 4 }\n"
	}
	if after == "" {
		return Decision{Stop: "insufficient_evidence"}, nil
	}
	return Decision{Patch: &workspace.Patch{Base: d.Version, Changes: []workspace.Change{{Kind: "MODIFY", Path: d.File.Path, Mode: "100644", Before: d.File.Hash, BeforeSize: len(d.Source), After: ingest.Digest([]byte(after)), AfterSize: len(after), Text: after}}}}, nil
}

func targetAllowed(target string) bool {
	if len(target) > 512 || path.Ext(target) != ".go" || strings.HasSuffix(strings.ToLower(target), "_test.go") {
		return false
	}
	for _, segment := range strings.Split(strings.ToLower(target), "/") {
		if segment == "" || segment == ".." || strings.HasPrefix(segment, ".") || strings.ContainsAny(segment, "\\:") {
			return false
		}
		for _, blocked := range []string{"test", "sandbox", "execution", "validation", "workspace", "repair", "auth", "security", "permission", "policy", "secrets", "credential", "migrations", "vendor", "config", "reconcile"} {
			if strings.Contains(segment, blocked) {
				return false
			}
		}
	}
	return true
}

func reportMatches(r validation.Request) bool {
	return r.State == "COMPLETED" && r.Report != nil && r.ReportDigest != "" &&
		r.Report.Validation == r.ID && r.Report.Version == r.Version && r.Report.Profile == r.Profile &&
		r.Report.Runtime.Image == r.Image && r.Report.Status == r.Outcome
}
func actionable(outcome string) bool {
	return outcome == "BUILD_FAILURE" || outcome == "TEST_FAILURE"
}
func verifiedGoReport(r validation.Request) bool {
	if !reportMatches(r) || r.Profile != "go-unit" || !r.Report.Phases.Cleaned || r.Report.Phases.Run == nil {
		return false
	}
	if actionable(r.Outcome) {
		return r.Report.Phases.Run.ExitCode > 0
	}
	return r.Outcome == "PASS" && r.Report.Phases.Run.ExitCode == 0
}
func fingerprint(p workspace.Patch) string {
	c := p.Changes[0]
	b, _ := json.Marshal([]string{c.Path, c.After})
	return ingest.Digest(b)
}

func bound(in string) string {
	if len(in) <= maxEvidence {
		return in
	}
	return strings.ToValidUTF8(in[:maxEvidence], "")
}
