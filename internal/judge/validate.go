package judge

import (
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"regexp"
)

var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var pinnedImage = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validEvidence(e Evidence) bool {
	return e.WallTimeNS >= 0 && e.OutputBytes >= 0 && e.OutputBytes <= 65536 && digest.MatchString(e.StdoutHash) && digest.MatchString(e.StderrHash) && (e.CPUTimeNS == nil || *e.CPUTimeNS >= 0) && (e.PeakMemoryBytes == nil || *e.PeakMemoryBytes >= 0)
}

// ValidateReport rechecks deterministic aggregation before SQL writes; it does not trust a supplied final verdict alone.
func ValidateReport(work Work, r Report) error {
	if !r.Cleaned || !pinnedImage.MatchString(r.Image) || r.Policy != execution.PolicyVersion || len(work.Reference.Cases) == 0 {
		return ErrConfiguration
	}
	if (work.Submission.Language == "cpp17" && r.Compile == nil) || (work.Submission.Language == "python3" && r.Compile != nil) {
		return ErrConfiguration
	}
	if r.Compile != nil && !validEvidence(*r.Compile) {
		return ErrConfiguration
	}
	if r.Verdict == "CE" {
		if r.Compile == nil || len(r.Tests) != 0 || r.CompileReason != "compile_"+string(r.Compile.Outcome) {
			return ErrConfiguration
		}
		switch r.Compile.Outcome {
		case execution.NonzeroExit:
			if r.Compile.ExitCode == 0 {
				return ErrConfiguration
			}
		case execution.WallTimeout, execution.OutputExceeded:
		case execution.MemoryExceeded:
			if !r.Compile.OOMKilled {
				return ErrConfiguration
			}
		default:
			return ErrConfiguration
		}
		return nil
	}
	if r.CompileReason != "" || (r.Compile != nil && (r.Compile.Outcome != execution.Exited || r.Compile.ExitCode != 0 || r.Compile.OOMKilled)) || len(r.Tests) == 0 || len(r.Tests) > len(work.Reference.Cases) {
		return ErrConfiguration
	}
	for i, row := range r.Tests {
		if row.CaseID != work.Reference.Cases[i].ID || row.Order != work.Reference.Cases[i].Order || !validEvidence(row.Evidence) {
			return ErrConfiguration
		}
		e := row.Evidence
		v, err := RuntimeVerdict(&execution.PhaseResult{Outcome: e.Outcome, ExitCode: e.ExitCode, OOMKilled: e.OOMKilled})
		if err != nil {
			return ErrConfiguration
		}
		if v == "AC" {
			if row.Match == nil {
				return ErrConfiguration
			}
			if !*row.Match {
				v = "WA"
			}
		} else if row.Match != nil {
			return ErrConfiguration
		}
		if row.Verdict != v || (i < len(r.Tests)-1 && v != "AC") {
			return ErrConfiguration
		}
	}
	last := r.Tests[len(r.Tests)-1].Verdict
	if r.Verdict != last || (last == "AC" && len(r.Tests) != len(work.Reference.Cases)) {
		return ErrConfiguration
	}
	return nil
}
