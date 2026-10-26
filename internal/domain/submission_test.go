package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestStateMachine(t *testing.T) {
	states := []Status{Queued, Running, Evaluating, Completed, Failed, "UNKNOWN"}
	legal := map[[2]Status]bool{{Queued, Running}: true, {Queued, Failed}: true, {Running, Evaluating}: true, {Running, Failed}: true, {Evaluating, Completed}: true, {Evaluating, Failed}: true}
	for _, from := range states {
		for _, to := range states {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				if CanTransition(from, to) != legal[[2]Status{from, to}] {
					t.Fatal("incorrect transition")
				}
			})
		}
	}
}
func TestTransitionValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transition Transition
		want       error
	}{
		{"wrong answer is completed", Transition{From: Evaluating, To: Completed, Verdict: "WA"}, nil},
		{"failure needs reason", Transition{From: Running, To: Failed}, ErrInvalid},
		{"infrastructure failure", Transition{From: Running, To: Failed, FailureReason: "executor unavailable"}, nil},
		{"missing verdict", Transition{From: Evaluating, To: Completed}, ErrInvalid},
		{"invalid verdict", Transition{From: Evaluating, To: Completed, Verdict: "UNKNOWN"}, ErrInvalid},
		{"nonterminal verdict", Transition{From: Queued, To: Running, Verdict: "AC"}, ErrInvalid},
		{"terminal cannot restart", Transition{From: Completed, To: Running}, ErrConflict},
		{"negative version", Transition{From: Queued, To: Running, ExpectedVersion: -1}, ErrConflict},
		{"unexpected failure reason", Transition{From: Queued, To: Running, FailureReason: "oops"}, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.transition.Validate(), tc.want) {
				t.Fatal("unexpected error")
			}
		})
	}
}
func TestValidationAndFingerprints(t *testing.T) {
	base := CreateRequest{ProblemID: 1, Language: "cpp17", SourceCode: "int main() {}"}
	normalized := base
	normalized.Language = " CPP17 "
	if base.Fingerprint() != normalized.Fingerprint() {
		t.Fatal("language normalization differs")
	}
	changed := base
	changed.SourceCode += "\n"
	if base.Fingerprint() == changed.Fingerprint() {
		t.Fatal("source whitespace must be preserved")
	}
	for _, tc := range []struct {
		name string
		req  CreateRequest
		want error
	}{
		{"valid", base, nil}, {"bad problem", CreateRequest{Language: "go", SourceCode: "x"}, ErrInvalid},
		{"bad language", CreateRequest{ProblemID: 1, Language: "java99", SourceCode: "x"}, ErrInvalid},
		{"empty source", CreateRequest{ProblemID: 1, Language: "go", SourceCode: " \n"}, ErrInvalid},
		{"NUL source", CreateRequest{ProblemID: 1, Language: "go", SourceCode: "a\x00b"}, ErrInvalid},
		{"large source", CreateRequest{ProblemID: 1, Language: "go", SourceCode: strings.Repeat("a", MaxSourceBytes+1)}, ErrTooLarge},
		{"boundary", CreateRequest{ProblemID: 1, Language: "go", SourceCode: strings.Repeat("a", MaxSourceBytes)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.req.Validate(), tc.want) {
				t.Fatal("unexpected validation")
			}
		})
	}
	for _, v := range []Verdict{"AC", "WA", "TLE", "MLE", "RE", "CE", "OLE"} {
		if !ValidVerdict(v) {
			t.Fatal(v)
		}
	}
}
