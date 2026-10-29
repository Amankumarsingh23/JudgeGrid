package execution

import (
	"context"
	"strings"
	"time"
)

type phaseObserver interface {
	ObserveSandboxPhase(context.Context, Provenance, string) error
}
type observationKey struct{}
type observationCallback func(context.Context) error
type phaseObservation struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

func startObservation(parent context.Context, name string, limit time.Duration) *phaseObservation {
	callback, ok := parent.Value(observationKey{}).(observationCallback)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, min(limit+2*time.Second, 32*time.Second))
	o := &phaseObservation{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(o.done)
		for {
			state, e := containerState(ctx, name)
			if e != nil {
				o.err = e
				return
			}
			if state.StartedAt != "" && !strings.HasPrefix(state.StartedAt, "0001-") {
				o.err = callback(ctx)
				return
			}
			select {
			case <-ctx.Done():
				o.err = ctx.Err()
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	return o
}
func (o *phaseObservation) Close() {
	if o != nil {
		o.cancel()
		<-o.done
	}
}
func (o *phaseObservation) Wait() error {
	if o == nil {
		return nil
	}
	<-o.done
	return o.err
}
