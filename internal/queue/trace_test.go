package queue

import (
	"strings"
	"testing"
)

func TestTraceEnvelopeCompatibilityAndUntrustedContext(t *testing.T) {
	for _, kind := range []string{SubmissionJob, RejudgeJob} {
		j := Job{Kind: kind, SubmissionID: "550e8400-e29b-41d4-a716-446655440000", EventID: "550e8400-e29b-41d4-a716-446655440001", Attempt: 1, TraceParent: "00-" + strings.Repeat("1", 32) + "-" + strings.Repeat("2", 16) + "-01"}
		if kind == RejudgeJob {
			j.JobID = "550e8400-e29b-41d4-a716-446655440002"
		}
		v := j.Values()
		round, e := (Message{Values: v}).Job()
		if e != nil || round != j {
			t.Fatal(round, e)
		}
		v["traceparent"] = "malformed or private baggage"
		round, e = (Message{Values: v}).Job()
		if e != nil || round.TraceParent != "" {
			t.Fatal("bad metadata changed business message", e)
		}
		j.TraceParent = ""
		round, e = (Message{Values: j.Values()}).Job()
		if e != nil || round != j {
			t.Fatal("legacy schema broken", e)
		}
	}
}
