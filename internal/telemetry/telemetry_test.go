package telemetry

import (
	"context"
	"errors"
	"fmt"
	sdk "go.opentelemetry.io/otel/sdk/trace"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryExporter struct {
	mu    sync.Mutex
	spans []sdk.ReadOnlySpan
	block bool
}

func (m *memoryExporter) ExportSpans(ctx context.Context, s []sdk.ReadOnlySpan) error {
	if m.block {
		<-ctx.Done()
		return ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spans = append(m.spans, s...)
	return nil
}
func (m *memoryExporter) Shutdown(context.Context) error { return nil }
func setup(t *testing.T, e sdk.SpanExporter) *Runtime {
	t.Helper()
	r := New("test", e)
	restore := Install(r)
	t.Cleanup(func() { r.Close(); restore() })
	return r
}
func metrics(r *Runtime) string {
	w := httptest.NewRecorder()
	r.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	return w.Body.String()
}
func TestMetricValuesAndCardinality(t *testing.T) {
	r := setup(t, nil)
	Count("accepted", "ok", "cpp17")
	Count("accepted", "ok", "cpp17")
	for i := range 1000 {
		Count(fmt.Sprint("private-", i), fmt.Sprint("secret-", i), fmt.Sprint("user-", i))
	}
	s := metrics(r)
	if !strings.Contains(s, `language="cpp17",operation="accepted",outcome="ok",service="test"} 2`) || strings.Contains(s, "private-") || strings.Contains(s, "secret-") {
		t.Fatal(s)
	}
	rows, _ := r.Registry.Gather()
	found := false
	for _, row := range rows {
		if row.GetName() == "judgegrid_operations_total" {
			found = len(row.Metric) == 2
		}
	}
	if !found {
		t.Fatal("unbounded series", rows)
	}
}

func TestIngestionBytesAndLabelCardinality(t *testing.T) {
	r := setup(t, nil)
	IngestionBytes("fetched", 48)
	IngestionBytes("reused", 35)
	CountN("ingest.files", "included", 5)
	for range 1000 {
		IngestionBytes("private-repository-or-file", 1)
	}
	s := metrics(r)
	if strings.Contains(s, "private-repository") || !strings.Contains(s, `stage="fetched"} 48`) || !strings.Contains(s, `stage="reused"} 35`) {
		t.Fatal(s)
	}
	rows, _ := r.Registry.Gather()
	for _, row := range rows {
		if row.GetName() == "judgegrid_ingestion_bytes_total" && len(row.Metric) != 3 {
			t.Fatal("unbounded byte counter labels")
		}
	}
}
func TestDurableSpanLinksAndSafeAttributes(t *testing.T) {
	e := &memoryExporter{}
	r := setup(t, e)
	ctx, end := Begin(context.Background(), "create", "cpp17")
	parent := Parent(ctx)
	if !ValidParent(parent) {
		t.Fatal(parent)
	}
	end(nil)
	_, end = Link(context.Background(), "outbox.publish", "cpp17", parent)
	end(errors.New("source and password must not appear"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.processor.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.spans) != 2 || len(e.spans[1].Links()) != 1 || e.spans[1].Links()[0].SpanContext.TraceID() != e.spans[0].SpanContext().TraceID() {
		t.Fatal("lost causal link")
	}
	for _, s := range e.spans {
		if len(s.Events()) != 0 || strings.Contains(s.Status().Description, "password") {
			t.Fatal("untrusted error exported")
		}
	}
}
func TestExporterCapacityAndBoundedDrain(t *testing.T) {
	r := setup(t, &memoryExporter{block: true})
	start := time.Now()
	for range 1000 {
		_, end := Begin(context.Background(), "cache.lookup", "")
		end(nil)
	}
	if time.Since(start) > time.Second || len(r.processor.queue) > 128 {
		t.Fatal("export blocked critical path")
	}
	if !strings.Contains(metrics(r), `operation="exporter.drop",outcome="capacity"`) {
		t.Fatal("drops invisible")
	}
	start = time.Now()
	r.Close()
	if time.Since(start) > 2200*time.Millisecond {
		t.Fatal("unbounded shutdown")
	}
}
func TestUntrustedHTTPContextAndPrivateRoutes(t *testing.T) {
	r := setup(t, nil)
	h := HTTP(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if Parent(q.Context()) == q.Header.Get("traceparent") {
			t.Error("untrusted caller selected trace")
		}
		w.WriteHeader(429)
	}))
	q := httptest.NewRequest("POST", "/api/v1/submissions/private-uuid?token=secret", nil)
	q.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, q)
	s := metrics(r)
	if strings.Contains(s, "secret") || strings.Contains(s, "uuid") || !strings.Contains(s, `status_class="4xx"`) {
		t.Fatal(s)
	}
}
func TestMalformedParentAndDisabledNoop(t *testing.T) {
	for _, s := range []string{"", "00-" + strings.Repeat("0", 32) + "-" + strings.Repeat("1", 16) + "-01", "00-" + strings.Repeat("A", 32) + "-" + strings.Repeat("1", 16) + "-01", "bad\ncontext"} {
		if ValidParent(s) {
			t.Fatal(s)
		}
	}
	restore := Install(nil)
	defer restore()
	ctx, end := Begin(context.Background(), "cache.lookup", "")
	end(nil)
	if Parent(ctx) != "" {
		t.Fatal("disabled trace propagation")
	}
}
func TestCollectorAbsentAndMetricsBindFailure(t *testing.T) {
	old := current.Load()
	defer current.Store(old)
	t.Setenv("APP_ENV", "development")
	t.Setenv("TELEMETRY_ENABLED", "true")
	t.Setenv("TELEMETRY_ADDR", "127.0.0.1:0")
	t.Setenv("TELEMETRY_OTLP_URL", "http://127.0.0.1:1")
	r, e := Start("test")
	if e != nil {
		t.Fatal(e)
	}
	_, end := Begin(context.Background(), "cache.lookup", "")
	end(nil)
	r.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	t.Setenv("TELEMETRY_ADDR", occupied.Addr().String())
	withoutScrape, err := Start("test")
	if err != nil {
		t.Fatal("metrics listener outage stopped process", err)
	}
	_, finish := Begin(context.Background(), "cache.lookup", "")
	finish(nil)
	withoutScrape.Close()
	t.Setenv("TELEMETRY_ADDR", "invalid")
	if _, e = Start("test"); e == nil {
		t.Fatal("bad listener configuration accepted")
	}
}
func TestDurationUnitsAndBoundaries(t *testing.T) {
	r := setup(t, nil)
	Observe("sandbox.run", "ok", "python3", .25)
	Observe("sandbox.run", "ok", "python3", -1)
	s := metrics(r)
	if !strings.Contains(s, `judgegrid_duration_seconds_sum{language="python3",outcome="ok",phase="sandbox.run",service="test"} 0.25`) || !strings.Contains(s, `le="0.25"} 1`) {
		t.Fatal(s)
	}
}

func TestOTLPBaseURLUsesTraceSignalPath(t *testing.T) {
	old := current.Load()
	defer current.Store(old)
	paths := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		if r.URL.Path != "/v1/traces" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	defer server.Close()
	t.Setenv("APP_ENV", "development")
	t.Setenv("TELEMETRY_ENABLED", "true")
	t.Setenv("TELEMETRY_ADDR", "127.0.0.1:0")
	t.Setenv("TELEMETRY_OTLP_URL", server.URL)
	r, e := Start("test")
	if e != nil {
		t.Fatal(e)
	}
	_, end := Begin(context.Background(), "cache.lookup", "")
	end(nil)
	r.Close()
	select {
	case path := <-paths:
		if path != "/v1/traces" {
			t.Fatal(path)
		}
	case <-time.After(time.Second):
		t.Fatal("no real OTLP request")
	}
}
