package agent

import (
	"context"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
	"github.com/Amankumarsingh23/JudgeGrid/internal/judge"
	"github.com/Amankumarsingh23/JudgeGrid/internal/queue"
	"github.com/Amankumarsingh23/JudgeGrid/internal/worker"
	"testing"
	"time"
)

func TestAgentBounds(t *testing.T) {
	o := DefaultOptions()
	if o.Validate() != nil {
		t.Fatal(o)
	}
	for _, mutate := range []func(*Options){func(o *Options) { o.Lease = 0 }, func(o *Options) { o.Heartbeat = o.Lease }, func(o *Options) { o.Attempts = 6 }, func(o *Options) { o.Concurrency = 0 }, func(o *Options) { o.Drain = 0 }} {
		o = DefaultOptions()
		mutate(&o)
		if o.Validate() == nil {
			t.Fatal(o)
		}
	}
}

type store struct {
	w      judge.Work
	writes int
}

func (s *store) ClaimExecution(context.Context, string, string, queue.Job, time.Duration, int) (judge.Work, error) {
	return s.w, nil
}
func (s *store) RenewExecution(context.Context, judge.Work, time.Duration) error {
	return errors.New("database unavailable")
}
func (s *store) CommitEvaluation(context.Context, judge.Work, judge.Report) (judge.Summary, error) {
	s.writes++
	return judge.Summary{}, nil
}
func (s *store) FailExecution(context.Context, judge.Work, string, bool) error {
	s.writes++
	return nil
}
func (s *store) RecoveryJobs(context.Context, string, int) ([]queue.Job, error) { return nil, nil }

type suite struct{ b bundles.Bundle }

func (s suite) Load(string) (bundles.Bundle, error) { return s.b, nil }

type waitingEngine struct{}

func (waitingEngine) Prepare(ctx context.Context, _ execution.Request) (execution.Prepared, execution.Result, error) {
	<-ctx.Done()
	return nil, execution.Result{}, ctx.Err()
}
func TestHeartbeatUncertaintyCancelsWithoutWrites(t *testing.T) {
	b, _ := bundles.Demo("two-sum")
	ref, _ := b.Reference()
	s := &store{w: judge.Work{Reference: ref, TimeLimitMS: 1000, MemoryLimitKB: 65536}}
	s.w.Submission.Language = "cpp17"
	s.w.Submission.SourceCode = "int main(){}"
	o := DefaultOptions()
	o.Lease = time.Second
	o.Heartbeat = 50 * time.Millisecond
	h, err := New(s, suite{b}, waitingEngine{}, "alice", "owner", o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	outcome, err := h.Handle(ctx, queue.Job{Kind: queue.SubmissionJob, EventID: "present"}, "consumer")
	if err == nil || outcome != worker.Retry || s.writes != 0 {
		t.Fatal(outcome, err, s.writes)
	}
}
func TestAgentRefusesLegacyAndHarnessJobs(t *testing.T) {
	h, _ := New(&store{}, suite{}, waitingEngine{}, "alice", "owner", DefaultOptions())
	for _, j := range []queue.Job{{Kind: queue.DispatchTestJob}, {Kind: queue.SubmissionJob}} {
		o, e := h.Handle(context.Background(), j, "consumer")
		if e != nil || o != worker.Unsupported {
			t.Fatal(o, e)
		}
	}
}
