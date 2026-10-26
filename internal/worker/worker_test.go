package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	check := func(context.Context) error { calls++; return nil }
	done := make(chan error, 1)
	go func() { done <- Run(ctx, time.Second, check, check) }()
	cancel()
	select {
	case err := <-done:
		if err != nil || calls != 2 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestRunDependencyFailure(t *testing.T) {
	failure := errors.New("offline")
	ok := func(context.Context) error { return nil }
	fail := func(context.Context) error { return failure }
	for _, checks := range [][2]func(context.Context) error{{fail, ok}, {ok, fail}} {
		if err := Run(context.Background(), time.Second, checks[0], checks[1]); !errors.Is(err, failure) {
			t.Fatalf("got %v", err)
		}
	}
}
