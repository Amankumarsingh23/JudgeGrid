package judge

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"strings"
	"testing"
)

func TestComparators(t *testing.T) {
	for _, tc := range []struct {
		policy, a, b string
		want         bool
	}{
		{"exact", "", "", true}, {"exact", "5\n", "5", false}, {"tokens", " 5\t\r\n", "5", true},
		{"tokens", "", " \n", true}, {"tokens", "5 6", "5", false}, {"tokens", "1.0", "1", false},
		{"tokens", "a\u00a0b", "a b", false}, {"tokens", "\xff", "\xfe", false},
		{"exact", strings.Repeat("x", 65536), strings.Repeat("x", 65536), true},
	} {
		got, err := Compare(tc.policy, []byte(tc.a), []byte(tc.b))
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %v %v", tc, got, err)
		}
	}
	if _, err := Compare("unknown", nil, nil); err == nil {
		t.Fatal("unknown checker accepted")
	}
}
func TestRuntimeClassification(t *testing.T) {
	for _, tc := range []struct {
		o    execution.Outcome
		exit int
		oom  bool
		want domain.Verdict
	}{
		{execution.Exited, 0, false, "AC"}, {execution.NonzeroExit, 137, false, "RE"},
		{execution.MemoryExceeded, 137, true, "MLE"}, {execution.WallTimeout, 137, false, "TLE"},
		{execution.OutputExceeded, 137, false, "OLE"},
	} {
		got, err := RuntimeVerdict(&execution.PhaseResult{Outcome: tc.o, ExitCode: tc.exit, OOMKilled: tc.oom})
		if err != nil || got != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	if _, err := RuntimeVerdict(&execution.PhaseResult{Outcome: execution.MemoryExceeded, ExitCode: 137}); err == nil {
		t.Fatal("unconfirmed OOM accepted")
	}
	if _, err := RuntimeVerdict(&execution.PhaseResult{Outcome: execution.Canceled}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Fakes exercise orchestration only. Real verdict demonstrations use Docker integration tests.
type fakeEngine struct {
	p       *fakeArtifact
	compile *execution.PhaseResult
	calls   int
	err     error
}

func (f *fakeEngine) Prepare(context.Context, execution.Request) (execution.Prepared, execution.Result, error) {
	f.calls++
	r := execution.Result{Policy: execution.PolicyVersion, Compile: f.compile, Cleaned: f.p == nil}
	if f.p == nil {
		return nil, r, f.err
	}
	return f.p, r, f.err
}

type fakeArtifact struct {
	runs     int
	closed   bool
	out      []string
	closeErr error
}

func (f *fakeArtifact) Run(context.Context, []byte, execution.Limits) (*execution.PhaseResult, error) {
	s := f.out[f.runs]
	f.runs++
	return &execution.PhaseResult{Outcome: execution.Exited, Stdout: s}, nil
}
func (f *fakeArtifact) Summary() execution.Result {
	return execution.Result{Cleaned: f.closed && f.closeErr == nil}
}
func (f *fakeArtifact) Close() error { f.closed = true; return f.closeErr }
func testWork() (Work, bundles.Bundle) {
	b, _ := bundles.Demo("two-sum")
	ref, _ := b.Reference()
	return Work{Submission: domain.Submission{Language: "cpp17", SourceCode: "int main(){}"}, Reference: ref, TimeLimitMS: 1000, MemoryLimitKB: 65536}, b
}
func TestPrepareOnceStopFirstAndCleanup(t *testing.T) {
	w, b := testWork()
	p := &fakeArtifact{out: []string{"5", "wrong"}}
	e := &fakeEngine{p: p, compile: &execution.PhaseResult{Outcome: execution.Exited, Image: "sha256:" + strings.Repeat("a", 64)}}
	r, err := Evaluate(context.Background(), e, w, b)
	if err != nil || r.Verdict != "WA" || len(r.Tests) != 2 || e.calls != 1 || p.runs != 2 || !r.Cleaned {
		t.Fatalf("%+v %v", r, err)
	}
	if err = ValidateReport(w, r); err != nil {
		t.Fatal(err)
	}
	r.CacheHit = true
	if ValidateReport(w, r) != nil {
		t.Fatal("verified C++ cache evidence rejected")
	}
	w.Submission.Language = "python3"
	if ValidateReport(w, r) == nil {
		t.Fatal("Python claimed compilation-cache evidence")
	}
	w.Submission.Language = "cpp17"
	r.Verdict = "AC"
	if ValidateReport(w, r) == nil {
		t.Fatal("false AC accepted")
	}
}
func TestCompileLimitAndCleanupFailure(t *testing.T) {
	w, b := testWork()
	for _, o := range []execution.Outcome{execution.NonzeroExit, execution.WallTimeout, execution.MemoryExceeded, execution.OutputExceeded} {
		e := &fakeEngine{compile: &execution.PhaseResult{Outcome: o, ExitCode: 137, OOMKilled: o == execution.MemoryExceeded, Image: "sha256:" + strings.Repeat("a", 64)}}
		r, err := Evaluate(context.Background(), e, w, b)
		if err != nil || r.Verdict != "CE" || r.CompileReason != "compile_"+string(o) || ValidateReport(w, r) != nil {
			t.Fatalf("%+v %v", r, err)
		}
		r.CacheHit = true
		if ValidateReport(w, r) == nil {
			t.Fatal("compilation failure claimed cache hit")
		}
	}
	p := &fakeArtifact{out: []string{"wrong"}, closeErr: errors.New("cleanup unavailable")}
	_, err := Evaluate(context.Background(), &fakeEngine{p: p}, w, b)
	if err == nil {
		t.Fatal("cleanup failure became user verdict")
	}
}

type fakeRepository struct{ s *fakeSession }

func (r fakeRepository) Acquire(context.Context, string, string) (Session, error) { return r.s, nil }

type fakeStore struct{ b bundles.Bundle }

func (s fakeStore) Load(string) (bundles.Bundle, error) { return s.b, nil }

type fakeSession struct {
	w           Work
	completes   int
	failures    int
	completeErr error
}

func (s *fakeSession) Begin(context.Context) (Work, error) { return s.w, nil }
func (s *fakeSession) Complete(context.Context, Work, Report) (Summary, error) {
	s.completes++
	if s.completes < 3 || s.completeErr != nil {
		return Summary{}, errors.New("database unavailable")
	}
	return Summary{Status: "COMPLETED"}, nil
}
func (s *fakeSession) Fail(context.Context, Work, string) (Summary, error) {
	s.failures++
	return Summary{Status: "FAILED"}, nil
}
func (s *fakeSession) Close() error { return nil }
func TestPersistenceRetryDoesNotReexecute(t *testing.T) {
	w, b := testWork()
	p := &fakeArtifact{out: []string{"wrong"}}
	engine := &fakeEngine{p: p}
	session := &fakeSession{w: w}
	summary, err := (Service{Repository: fakeRepository{session}, Bundles: fakeStore{b}, Engine: engine}).Run(context.Background(), "alice", "id")
	if err != nil || summary.Status != "COMPLETED" || session.completes != 3 || engine.calls != 1 || p.runs != 1 {
		t.Fatal(summary, err, session.completes, engine.calls, p.runs)
	}
	p = &fakeArtifact{out: []string{"wrong"}}
	engine = &fakeEngine{p: p}
	session = &fakeSession{w: w, completeErr: errors.New("outage")}
	summary, err = (Service{Repository: fakeRepository{session}, Bundles: fakeStore{b}, Engine: engine}).Run(context.Background(), "alice", "id")
	if err == nil || summary.Status == "COMPLETED" || session.completes != 3 || engine.calls != 1 {
		t.Fatal("false durable completion", summary, err)
	}
}
