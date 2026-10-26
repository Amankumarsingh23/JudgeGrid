package worker

import (
	"context"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
)

// ProgramProcessor is the typed Session 5 handoff. It has no database, ACK or verdict-writing authority.
// The existing Redis DevelopmentHandler never invokes it.
type ProgramProcessor struct{ Executor execution.Executor }

func (p ProgramProcessor) Process(ctx context.Context, request execution.Request) (execution.Result, error) {
	if p.Executor == nil {
		return execution.Result{}, fmt.Errorf("execution backend not configured")
	}
	return p.Executor.Execute(ctx, request)
}
