package queue

import (
	"errors"
	"testing"
)

func TestJobSchema(t *testing.T) {
	valid := Job{Kind: DispatchTestJob, SubmissionID: "550e8400-e29b-41d4-a716-446655440000", Attempt: 1}
	values := valid.Values()
	got, err := (Message{Values: values}).Job()
	if err != nil || got != valid {
		t.Fatalf("%+v %v", got, err)
	}
	for _, tc := range []struct {
		name, key string
		value     any
		extra     bool
	}{
		{"unknown version", "schema_version", "2", false}, {"missing field", "kind", nil, false},
		{"unsupported kind", "kind", "unknown", false}, {"invalid ID", "submission_id", "not-uuid", false},
		{"zero attempt", "attempt", "0", false}, {"negative attempt", "attempt", "-1", false},
		{"noninteger", "attempt", "1.5", false}, {"typed value", "attempt", 1, false},
		{"extra field", "source_code", "never put code in the queue", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := valid.Values()
			if tc.value == nil {
				delete(v, tc.key)
			} else {
				v[tc.key] = tc.value
			}
			_, err := (Message{Values: v}).Job()
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("got %v", err)
			}
		})
	}
	real := valid
	real.Kind = SubmissionJob
	if real.Validate() != nil {
		t.Fatal("real job transport schema rejected")
	}
}
func TestDevelopmentStreamIsolation(t *testing.T) {
	for _, name := range []string{"submissions:jobs", "", "production", "devil:queue"} {
		if IsDevelopmentStream(name) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"dev:dispatch", "test:unique"} {
		if !IsDevelopmentStream(name) {
			t.Fatal(name)
		}
	}
}
