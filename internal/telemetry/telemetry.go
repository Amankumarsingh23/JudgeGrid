// Package telemetry observes work without becoming an execution dependency.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var current atomic.Pointer[Runtime]
var buckets = []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30, 60, 120, 300}

type Runtime struct {
	age            prometheus.Gauge
	stamp          prometheus.Gauge
	Registry       *prometheus.Registry
	operations     *prometheus.CounterVec
	ingestionBytes *prometheus.CounterVec
	duration       *prometheus.HistogramVec
	gauge          *prometheus.GaugeVec
	httpCount      *prometheus.CounterVec
	httpDuration   *prometheus.HistogramVec
	provider       *sdk.TracerProvider
	processor      *processor
	server         *http.Server
}

func Enabled() bool { return current.Load() != nil }
func normalize(value, allowed string) string {
	for _, v := range strings.Fields(allowed) {
		if value == v {
			return v
		}
	}
	return "other"
}
func operation(v string) string {
	if strings.HasPrefix(v, "analysis.") {
		return normalize(v, "analysis.total analysis.parse analysis.completed")
	}
	if strings.HasPrefix(v, "ingest.") {
		return normalize(v, "ingest.accepted ingest.completed ingest.failed ingest.retry ingest.fenced ingest.files ingest.total ingest.git ingest.storage ingest.db ingest.verify ingest.fetch_error ingest.storage_error")
	}
	return normalize(v, "http authorize create accepted rejudge.accepted rejudge.request terminal outbox.claim outbox.retry outbox.publish outbox.record redis.publish redis.process redis.duplicate scheduler.poll scheduler.admit lease.claim lease.renew lease.fail lease.recovery fence.reject bundle.fetch bundle.verify cache.restore cache.lookup cache.hit cache.miss cache.build cache.publish sandbox.prepare sandbox.workspace sandbox.stage sandbox.attached sandbox.compile sandbox.run sandbox.cleanup sandbox.timeout agent.execute judge.evaluate result.persist sse.connect sse.reconnect sse.replay sse.reset sse.write sse.slow sse.disconnect sse.delivery reconcile.scan reconcile.reap reconcile.preserve reconcile.error exporter.export exporter.drop backend.snapshot clock.skew accepted_admission eligible_admission outbox_delay accepted_terminal claimed_start")
}
func outcome(v string) string {
	if v == "discovered" || v == "included" || v == "excluded" {
		return v
	}
	return normalize(v, "ok error canceled timeout unavailable constraint fenced throttle capacity AC WA CE RE TLE MLE OLE FAILED QUEUED RUNNING COMPLETED PENDING CLAIMED PUBLISHED QUARANTINED ACTIVE EXPIRED renewed hit miss build cache_hit compile prepare run cleaned replay reset connected disconnected transient permanent")
}
func language(v string) string { return normalize(v, "cpp17 python3 go") }
func Count(op, result, lang string) {
	if r := current.Load(); r != nil {
		r.operations.WithLabelValues(operation(op), outcome(result), language(lang)).Inc()
	}
}

// CountN has the same fixed label policy as Count; callers cannot add label names.
func CountN(op, result string, n float64) {
	if r := current.Load(); r != nil && n >= 0 {
		r.operations.WithLabelValues(operation(op), outcome(result), "other").Add(n)
	}
}
func IngestionBytes(kind string, n float64) {
	if r := current.Load(); r != nil && n >= 0 {
		r.ingestionBytes.WithLabelValues(normalize(kind, "processed fetched reused")).Add(n)
	}
}
func Observe(op, result, lang string, seconds float64) {
	if r := current.Load(); r != nil && seconds >= 0 {
		r.duration.WithLabelValues(operation(op), outcome(result), language(lang)).Observe(seconds)
	}
}
func Gauge(name, state string, value float64) {
	if r := current.Load(); r != nil {
		if name == "outbox_age" {
			r.age.Set(value)
			return
		}
		if name == "snapshot_age" {
			r.stamp.Set(value)
			return
		}
		r.gauge.WithLabelValues(normalize(name, "scheduler outbox leases sse snapshot_age snapshot_ok snapshot_capped outbox_age streams"), outcome(state)).Set(value)
	}
}
func Since(op, lang string, stamp time.Time) {
	if !Enabled() || stamp.IsZero() {
		return
	}
	d := time.Since(stamp)
	if d < 0 {
		Count("clock.skew", "error", lang)
		return
	}
	Observe(op, "ok", lang, d.Seconds())
}
func Class(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return "timeout"
	}
	var p *pgconn.PgError
	if errors.As(err, &p) {
		switch p.Code {
		case "PZ429":
			return "throttle"
		case "PZ503":
			return "capacity"
		case "23514":
			return "constraint"
		}
	}
	if err.Error() == "execution busy, expired or uncertain" || err.Error() == "outbox claim expired or replaced" {
		return "fenced"
	}
	return "error"
}
func ValidParent(s string) bool {
	if len(s) != 55 || s[:3] != "00-" || s[35] != '-' || s[52] != '-' || (s[53:] != "00" && s[53:] != "01") {
		return false
	}
	_, e := trace.TraceIDFromHex(s[3:35])
	_, f := trace.SpanIDFromHex(s[36:52])
	return e == nil && f == nil && s == strings.ToLower(s)
}
func Parent(ctx context.Context) string {
	if !Enabled() {
		return ""
	}
	s := trace.SpanContextFromContext(ctx)
	if !s.IsValid() {
		return ""
	}
	flag := "00"
	if s.IsSampled() {
		flag = "01"
	}
	return "00-" + s.TraceID().String() + "-" + s.SpanID().String() + "-" + flag
}
func linked(parent string) []trace.Link {
	if !ValidParent(parent) {
		return nil
	}
	tid, _ := trace.TraceIDFromHex(parent[3:35])
	sid, _ := trace.SpanIDFromHex(parent[36:52])
	flags := trace.TraceFlags(0)
	if parent[53:] == "01" {
		flags = trace.FlagsSampled
	}
	return []trace.Link{{SpanContext: trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: flags, Remote: true})}}
}
func Begin(ctx context.Context, op, lang string) (context.Context, func(error)) {
	return begin(ctx, op, lang, "")
}
func Link(ctx context.Context, op, lang, parent string) (context.Context, func(error)) {
	return begin(ctx, op, lang, parent)
}
func begin(ctx context.Context, op, lang, parent string) (context.Context, func(error)) {
	r := current.Load()
	if r == nil {
		return ctx, func(error) {}
	}
	op = operation(op)
	lang = language(lang)
	start := time.Now()
	options := []trace.SpanStartOption{trace.WithAttributes(attribute.String("judgegrid.phase", op), attribute.String("judgegrid.language", lang))}
	if parent != "" {
		options = append(options, trace.WithNewRoot(), trace.WithLinks(linked(parent)...))
	}
	if op == "http" {
		options = append(options, trace.WithSpanKind(trace.SpanKindServer))
	}
	if op == "outbox.publish" {
		options = append(options, trace.WithSpanKind(trace.SpanKindProducer))
	}
	if op == "redis.process" || op == "agent.execute" {
		options = append(options, trace.WithSpanKind(trace.SpanKindConsumer))
	}
	ctx, span := r.provider.Tracer("judgegrid").Start(ctx, op, options...)
	return ctx, func(err error) {
		result := Class(err)
		Count(op, result, lang)
		Observe(op, result, lang, time.Since(start).Seconds())
		span.SetAttributes(attribute.String("judgegrid.outcome", result))
		if err != nil {
			span.SetStatus(codes.Error, "operation failed")
		}
		s := span.SpanContext()
		level := slog.LevelDebug
		if op == "create" || op == "agent.execute" || op == "outbox.publish" || op == "result.persist" {
			level = slog.LevelInfo
		}
		slog.LogAttrs(ctx, level, "observed operation", slog.String("phase", op), slog.String("outcome", result), slog.String("trace_id", s.TraceID().String()), slog.String("span_id", s.SpanID().String()))
		span.End()
	}
}
func New(service string, exporter sdk.SpanExporter) *Runtime {
	reg := prometheus.NewRegistry()
	labels := prometheus.Labels{"service": normalize(service, "api outbox-relay judge-agent worker reconciler repository-ingest test")}
	r := &Runtime{Registry: reg, operations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "judgegrid_operations_total", Help: "Physical operation observations; accepted/terminal are committed logical outcomes.", ConstLabels: labels}, []string{"operation", "outcome", "language"}), duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "judgegrid_duration_seconds", Help: "Defined phase duration in seconds; asynchronous timestamp estimates may include commit and clock skew.", Buckets: buckets, ConstLabels: labels}, []string{"phase", "outcome", "language"}), gauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "judgegrid_state", Help: "Cached bounded DB snapshot; capped=1 indicates a 10001-row lower-bound sample.", ConstLabels: labels}, []string{"kind", "state"}), httpCount: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "judgegrid_http_requests_total", Help: "HTTP responses by bounded route/method/status class.", ConstLabels: labels}, []string{"route", "method", "status_class"}), httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "judgegrid_http_duration_seconds", Help: "HTTP handler duration including stream lifetime for events.", Buckets: buckets, ConstLabels: labels}, []string{"route", "method", "status_class"})}
	r.age = prometheus.NewGauge(prometheus.GaugeOpts{Name: "judgegrid_outbox_oldest_pending_seconds", Help: "Oldest sampled pending/claimed intent age in seconds.", ConstLabels: labels})
	r.stamp = prometheus.NewGauge(prometheus.GaugeOpts{Name: "judgegrid_snapshot_updated_timestamp_seconds", Help: "Unix timestamp of the last successful bounded database inventory.", ConstLabels: labels})
	r.ingestionBytes = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "judgegrid_ingestion_bytes_total", Help: "Bounded source-byte observations by processing stage; no repository identity labels.", ConstLabels: labels}, []string{"stage"})
	reg.MustRegister(r.operations, r.duration, r.gauge, r.httpCount, r.httpDuration, r.age, r.stamp, r.ingestionBytes)
	options := []sdk.TracerProviderOption{sdk.WithResource(resource.NewSchemaless(attribute.String("service.name", labels["service"]), attribute.String("service.version", "session11"))), sdk.WithSampler(sdk.AlwaysSample()), sdk.WithRawSpanLimits(sdk.SpanLimits{AttributeValueLengthLimit: 128, AttributeCountLimit: 16, EventCountLimit: 0, LinkCountLimit: 4, AttributePerLinkCountLimit: 0})}
	if exporter != nil {
		r.processor = newProcessor(exporter, r)
		options = append(options, sdk.WithSpanProcessor(r.processor))
	}
	r.provider = sdk.NewTracerProvider(options...)
	return r
}

// Install is used once at process startup; tests restore the previous runtime.
func Install(r *Runtime) func() { old := current.Swap(r); return func() { current.Store(old) } }
func (r *Runtime) Handler() http.Handler {
	return promhttp.HandlerFor(r.Registry, promhttp.HandlerOpts{Timeout: time.Second, MaxRequestsInFlight: 2})
}
func (r *Runtime) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if r.server != nil {
		_ = r.server.Shutdown(ctx)
	}
	_ = r.provider.Shutdown(ctx)
}
func Start(service string) (*Runtime, error) {
	if os.Getenv("TELEMETRY_ENABLED") != "true" {
		return nil, nil
	}
	if os.Getenv("APP_ENV") != "development" {
		return nil, fmt.Errorf("telemetry requires explicit local development mode")
	}
	private := os.Getenv("TELEMETRY_PRIVATE_NETWORK") == "true"
	addr := os.Getenv("TELEMETRY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:9091"
	}
	host, _, e := net.SplitHostPort(addr)
	if e != nil || (!private && host != "127.0.0.1" && host != "::1" && host != "localhost") {
		return nil, fmt.Errorf("metrics must bind loopback or explicitly private container network")
	}
	endpoint := os.Getenv("TELEMETRY_OTLP_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:14318"
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || (!private && u.Scheme == "http" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		return nil, fmt.Errorf("invalid private OTLP endpoint")
	}
	// WithEndpointURL in the pinned SDK treats an empty path as root, rather
	// than adding the signal path. Our documented configuration is a base URL.
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1/traces"
	}
	endpoint = u.String()
	exporter, e := otlptracehttp.New(context.Background(), otlptracehttp.WithEndpointURL(endpoint), otlptracehttp.WithTimeout(500*time.Millisecond), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
	if e != nil {
		return nil, fmt.Errorf("telemetry exporter configuration rejected")
	}
	r := New(service, exporter)
	listener, e := net.Listen("tcp", addr)
	if e != nil {
		Install(r)
		Count("backend.snapshot", "unavailable", "")
		return r, nil
	}
	Install(r)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) { Count("exporter.export", "error", "") }))
	r.server = &http.Server{Handler: r.Handler(), ReadHeaderTimeout: time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	go func() { _ = r.server.Serve(listener) }()
	return r, nil
}
