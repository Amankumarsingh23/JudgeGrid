package telemetry

import (
	"context"
	sdk "go.opentelemetry.io/otel/sdk/trace"
	"sync"
	"sync/atomic"
	"time"
)

// A nonblocking 128-span queue, 16-span batches and 500ms exports bound memory
// and exporter work. No caller waits for the network in OnEnd.
type processor struct {
	queue    chan sdk.ReadOnlySpan
	exporter sdk.SpanExporter
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
	closed   atomic.Bool
	runtime  *Runtime
	flush    chan chan struct{}
}

func newProcessor(e sdk.SpanExporter, r *Runtime) *processor {
	p := &processor{queue: make(chan sdk.ReadOnlySpan, 128), exporter: e, stop: make(chan struct{}), done: make(chan struct{}), runtime: r, flush: make(chan chan struct{})}
	go p.run()
	return p
}
func (p *processor) OnStart(context.Context, sdk.ReadWriteSpan) {}
func (p *processor) OnEnd(s sdk.ReadOnlySpan) {
	if p.closed.Load() {
		return
	}
	select {
	case p.queue <- s:
	default:
		p.runtime.operations.WithLabelValues("exporter.drop", "capacity", "other").Inc()
	}
}
func (p *processor) batch() {
	rows := make([]sdk.ReadOnlySpan, 0, 16)
	for len(rows) < 16 {
		select {
		case s := <-p.queue:
			rows = append(rows, s)
		default:
			goto export
		}
	}
export:
	if len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	e := p.exporter.ExportSpans(ctx, rows)
	cancel()
	p.runtime.operations.WithLabelValues("exporter.export", Class(e), "other").Add(float64(len(rows)))
}
func (p *processor) run() {
	defer close(p.done)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			p.batch()
		case ack := <-p.flush:
			for n := 0; n < 8 && len(p.queue) > 0; n++ {
				p.batch()
			}
			close(ack)
		case <-p.stop:
			deadline := time.Now().Add(time.Second)
			for len(p.queue) > 0 && time.Now().Before(deadline) {
				p.batch()
			}
			p.runtime.operations.WithLabelValues("exporter.drop", "canceled", "other").Add(float64(len(p.queue)))
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			_ = p.exporter.Shutdown(ctx)
			cancel()
			return
		}
	}
}
func (p *processor) Shutdown(ctx context.Context) error {
	p.closed.Store(true)
	p.once.Do(func() { close(p.stop) })
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *processor) ForceFlush(ctx context.Context) error {
	ack := make(chan struct{})
	select {
	case p.flush <- ack:
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
