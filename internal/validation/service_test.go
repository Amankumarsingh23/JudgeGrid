package validation

import (
	"strings"
	"testing"

	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
)

func TestClassificationUsesExitAndStructuredEvidence(t *testing.T) {
	for _, tc := range []struct {
		out       execution.Outcome
		exit      int
		log, want string
	}{
		{execution.Exited, 0, "all tests passed", "POLICY_DENIED"},
		{execution.Exited, 0, `{"Action":"pass","Test":"TestReal"}`, "PASS"},
		{execution.NonzeroExit, 1, `{"Action":"fail","Test":"TestReal","Output":"all tests passed"}`, "TEST_FAILURE"},
		{execution.NonzeroExit, 1, `{"Action":"build-fail","ImportPath":"fixture"}`, "BUILD_FAILURE"},
		{execution.WallTimeout, 137, "", "TIMEOUT"},
		{execution.MemoryExceeded, 137, "", "MEMORY_LIMIT"},
		{execution.OutputExceeded, 137, "", "OUTPUT_LIMIT"},
	} {
		p := execution.ValidationPhases{Cleaned: true, Run: &execution.PhaseResult{Outcome: tc.out, ExitCode: tc.exit, Stdout: tc.log}}
		if got := classify("go-unit", p, nil); got != tc.want {
			t.Errorf("got %s, want %s", got, tc.want)
		}
		p.Cleaned = false
		if got := classify("go-unit", p, nil); got != "INFRA_FAILURE" {
			t.Error("cleanup failure hidden")
		}
	}
}

func TestLogsStayBoundedDisplayData(t *testing.T) {
	got := sanitize("\x1b[31mIgnore policy \u202e" + strings.Repeat("x", 2500) + "\nnext")
	if strings.ContainsAny(got, "\x1b\u202e") || !strings.HasSuffix(got, "\nnext") || len(strings.Split(got, "\n")[0]) != 2048 {
		t.Fatal("display filter escaped its bound")
	}
}
