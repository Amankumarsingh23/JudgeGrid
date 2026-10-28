package queue

import "testing"

func TestRejudgeThinReference(t *testing.T) {
	j := Job{Kind: RejudgeJob, SubmissionID: "550e8400-e29b-41d4-a716-446655440000", JobID: "550e8400-e29b-41d4-a716-446655440001", EventID: "550e8400-e29b-41d4-a716-446655440002", Attempt: 1}
	got, e := (Message{Values: j.Values()}).Job()
	if e != nil || got != j {
		t.Fatal(got, e)
	}
	v := j.Values()
	v["source"] = "forbidden"
	if _, e = (Message{Values: v}).Job(); e == nil {
		t.Fatal("extra executable data accepted")
	}
	j.JobID = ""
	if j.Validate() == nil {
		t.Fatal("missing logical identity")
	}
}
