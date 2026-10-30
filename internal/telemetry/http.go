package telemetry

import (
	"context"
	"fmt"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"net/http"
	"strings"
	"time"
)

func Route(path string) string {
	switch path {
	case "/healthz", "/readyz":
		return "health"
	case "/api/v1/session":
		return "session"
	case "/api/v1/submissions":
		return "submissions"
	case "/api/v1/problems":
		return "problems"
	}
	if strings.HasPrefix(path, "/api/v1/submissions/") {
		if strings.HasSuffix(path, "/events") {
			return "events"
		}
		if strings.HasSuffix(path, "/evaluations") {
			return "history"
		}
		return "submission"
	}
	if strings.HasPrefix(path, "/api/v1/problems/") {
		return "problem"
	}
	return "other"
}

type response struct {
	http.ResponseWriter
	status int
}

func (w *response) WriteHeader(n int) {
	if w.status == 0 {
		w.status = n
	}
	w.ResponseWriter.WriteHeader(n)
}
func (w *response) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(b)
}
func (w *response) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r := current.Load()
		if r == nil {
			next.ServeHTTP(w, q)
			return
		}
		start := time.Now()
		route := Route(q.URL.Path)
		method := normalize(q.Method, "GET POST DELETE")
		ctx := trace.ContextWithSpanContext(q.Context(), trace.SpanContext{})
		ctx, end := Begin(ctx, "http", "")
		out := &response{ResponseWriter: w}
		defer func() {
			n := out.status
			if n == 0 {
				n = 200
			}
			status := fmt.Sprintf("%dxx", n/100)
			r.httpCount.WithLabelValues(route, method, status).Inc()
			r.httpDuration.WithLabelValues(route, method, status).Observe(time.Since(start).Seconds())
			trace.SpanFromContext(ctx).SetAttributes(attribute.String("http.route", route), attribute.String("http.request.method", method), attribute.Int("http.response.status_code", n))
			if n >= 500 {
				trace.SpanFromContext(ctx).SetStatus(codes.Error, "HTTP server error")
			}
			if n == 429 {
				Count("http", "throttle", "")
			}
			end(nil)
		}()
		next.ServeHTTP(out, q.WithContext(context.WithValue(ctx, requestStartKey{}, start)))
	})
}

type requestStartKey struct{}
