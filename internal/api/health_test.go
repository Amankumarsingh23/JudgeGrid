package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealth(t *testing.T) {
	ok := func(context.Context) error { return nil }
	fail := func(context.Context) error { return errors.New("secret connection details") }
	for _, tc := range []struct {
		name, path string
		db, redis  Check
		status     int
		body       string
	}{
		{"liveness independent of dependencies", "/healthz", fail, fail, 200, "{\"status\":\"ok\"}\n"},
		{"ready", "/readyz", ok, ok, 200, "{\"postgres\":\"ok\",\"redis\":\"ok\"}\n"},
		{"postgres down", "/readyz", fail, ok, 503, "{\"postgres\":\"unavailable\",\"redis\":\"ok\"}\n"},
		{"redis down", "/readyz", ok, fail, 503, "{\"postgres\":\"ok\",\"redis\":\"unavailable\"}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			Handler(time.Second, tc.db, tc.redis).ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != tc.status || w.Body.String() != tc.body {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Content-Type") != "application/json" {
				t.Fatal("missing JSON content type")
			}
		})
	}
}

func TestReadinessDeadline(t *testing.T) {
	check := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	w := httptest.NewRecorder()
	Handler(10*time.Millisecond, check, check).ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestSlowDatabaseDoesNotHideHealthyRedis(t *testing.T) {
	slow := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	ok := func(context.Context) error { return nil }
	w := httptest.NewRecorder()
	Handler(50*time.Millisecond, slow, ok).ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 || w.Body.String() != "{\"postgres\":\"unavailable\",\"redis\":\"ok\"}\n" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
