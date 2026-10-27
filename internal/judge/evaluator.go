package judge

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/bundles"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/execution"
)

func Compare(policy string, actual, expected []byte) (bool, error) {
	switch policy {
	case "exact":
		return bytes.Equal(actual, expected), nil
	case "tokens":
		a, b := tokens(actual), tokens(expected)
		if len(a) != len(b) {
			return false, nil
		}
		for i := range a {
			if !bytes.Equal(a[i], b[i]) {
				return false, nil
			}
		}
		return true, nil
	default:
		return false, ErrConfiguration
	}
}

// Only ASCII whitespace is a separator. Every other byte, including Unicode and invalid UTF-8, is significant.
func tokens(data []byte) [][]byte {
	space := func(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f' }
	var result [][]byte
	for i := 0; i < len(data); {
		if space(data[i]) {
			i++
			continue
		}
		start := i
		for i < len(data) && !space(data[i]) {
			i++
		}
		result = append(result, data[start:i])
	}
	return result
}
func RuntimeVerdict(p *execution.PhaseResult) (domain.Verdict, error) {
	if p == nil {
		return "", ErrConfiguration
	}
	switch p.Outcome {
	case execution.Exited:
		if p.ExitCode != 0 || p.OOMKilled {
			return "", ErrConfiguration
		}
		return "AC", nil
	case execution.NonzeroExit:
		if p.ExitCode == 0 {
			return "", ErrConfiguration
		}
		if p.OOMKilled {
			return "MLE", nil
		}
		return "RE", nil
	case execution.WallTimeout:
		return "TLE", nil
	case execution.MemoryExceeded:
		if !p.OOMKilled {
			return "", ErrConfiguration
		}
		return "MLE", nil
	case execution.OutputExceeded:
		return "OLE", nil
	case execution.Canceled:
		return "", context.Canceled
	default:
		return "", ErrConfiguration
	}
}
func Evaluate(ctx context.Context, engine execution.Preparer, work Work, suite bundles.Bundle) (report Report, err error) {
	if err = suite.Verify(work.Reference); err != nil {
		return report, err
	}
	limits := execution.Limits{WallTime: time.Duration(work.TimeLimitMS) * time.Millisecond, MemoryBytes: int64(work.MemoryLimitKB) * 1024, OutputBytes: 64 << 10}
	req := execution.Request{Language: work.Submission.Language, Source: []byte(work.Submission.SourceCode), CompileLimits: execution.DefaultCompileLimits(), RunLimits: limits}
	if err = req.Validate(); err != nil {
		return report, ErrConfiguration
	}
	artifact, prepared, err := engine.Prepare(ctx, req)
	report.Policy = prepared.Policy
	report.Tests = []TestResult{}
	report.Compile = evidence(prepared.Compile)
	if prepared.Compile != nil {
		report.Image = prepared.Compile.Image
	} else if prepared.Prepare != nil {
		report.Image = prepared.Prepare.Image
	}
	if err != nil {
		return report, err
	}
	if artifact == nil {
		report.Cleaned = prepared.Cleaned
		if prepared.Compile == nil {
			return report, ErrConfiguration
		}
		switch prepared.Compile.Outcome {
		case execution.NonzeroExit, execution.WallTimeout, execution.MemoryExceeded, execution.OutputExceeded:
			if prepared.Compile.Outcome == execution.MemoryExceeded && !prepared.Compile.OOMKilled {
				return report, ErrConfiguration
			}
			report.Verdict = "CE"
			report.CompileReason = "compile_" + string(prepared.Compile.Outcome)
			return report, nil
		case execution.Canceled:
			return report, context.Canceled
		default:
			return report, ErrConfiguration
		}
	}
	defer func() {
		closeErr := artifact.Close()
		report.Cleaned = artifact.Summary().Cleaned
		err = errors.Join(err, closeErr)
	}()
	report.Verdict = "AC"
	for _, test := range suite.Tests {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		phase, runErr := artifact.Run(ctx, test.Input, limits)
		if runErr != nil {
			return report, runErr
		}
		verdict, mapErr := RuntimeVerdict(phase)
		if mapErr != nil {
			return report, mapErr
		}
		row := TestResult{CaseID: test.ID, Order: test.Order, Verdict: verdict, Evidence: *evidence(phase)}
		if verdict == "AC" {
			match, checkErr := Compare(suite.Comparator, []byte(phase.Stdout), test.Expected)
			if checkErr != nil {
				return report, checkErr
			}
			row.Match = &match
			if !match {
				row.Verdict = "WA"
			}
		}
		report.Tests = append(report.Tests, row)
		if row.Verdict != "AC" {
			report.Verdict = row.Verdict
			break
		}
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	return report, nil
}

type Service struct {
	Repository Repository
	Bundles    bundles.Store
	Engine     execution.Preparer
}

func (s Service) Run(ctx context.Context, principal, id string) (Summary, error) {
	setup, setupCancel := context.WithTimeout(ctx, 10*time.Second)
	defer setupCancel()
	session, err := s.Repository.Acquire(setup, principal, id)
	if err != nil {
		return Summary{}, err
	}
	defer session.Close()
	work, err := session.Begin(setup)
	if err != nil {
		return Summary{}, err
	}
	if work.Existing != nil {
		summary := *work.Existing
		summary.Replayed = true
		return summary, nil
	}
	evaluation, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	suite, loadErr := s.Bundles.Load(work.Reference.Hash)
	var report Report
	if loadErr == nil {
		report, err = Evaluate(evaluation, s.Engine, work, suite)
	} else {
		err = loadErr
	}
	if err == nil {
		err = evaluation.Err()
	}
	if err != nil {
		code := "sandbox_infrastructure"
		if errors.Is(err, bundles.ErrIntegrity) {
			code = "bundle_verification_failed"
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = "evaluation_canceled"
		} else if errors.Is(err, ErrConfiguration) {
			code = "invalid_configuration"
		}
		persistence, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		summary, failErr := session.Fail(persistence, work, code)
		if failErr != nil {
			return summary, &Failure{Code: "failure_not_persisted", Cause: failErr}
		}
		return summary, &Failure{Code: code, Cause: err}
	}
	persistence, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	var summary Summary
	for attempt := 0; attempt < 3; attempt++ {
		summary, err = session.Complete(persistence, work, report)
		if err == nil || errors.Is(err, domain.ErrConflict) || errors.Is(err, ErrConfiguration) {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-timer.C:
		case <-persistence.Done():
			timer.Stop()
			return summary, &Failure{Code: "result_not_persisted", Cause: err}
		}
	}
	if err != nil {
		return summary, &Failure{Code: "result_not_persisted", Cause: err}
	}
	return summary, nil
}
