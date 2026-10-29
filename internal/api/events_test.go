package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestEventCursorIsSubmissionScoped(t *testing.T) {
	id := "550e8400-e29b-41d4-a716-446655440000"
	for _, bad := range []string{"", "42", id + ":-1", id + ":9223372036854775808", id + ":1:2", "650e8400-e29b-41d4-a716-446655440000:1"} {
		if _, e := parseCursor(bad, id); e == nil {
			t.Fatal("accepted invalid resume cursor", bad)
		}
	}
	if n, e := parseCursor(id+":12", id); e != nil || n != 12 {
		t.Fatal(n, e)
	}
}
func TestLocalWebRejectsNonLoopbackOrigins(t *testing.T) {
	for _, bad := range []string{"https://example.com", "http://localhost.evil", "http://localhost/", "http://user@localhost", "http://localhost?x=y", "ftp://localhost"} {
		if _, e := NewLocalWeb(nil, bad); e == nil {
			t.Fatal("unsafe local origin", bad)
		}
	}
	for _, good := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "https://[::1]:8080"} {
		if _, e := NewLocalWeb(nil, good); e != nil {
			t.Fatal(e)
		}
	}
}

type stalledEventWriter struct {
	header   http.Header
	deadline time.Time
}

func (s *stalledEventWriter) Header() http.Header                { return s.header }
func (s *stalledEventWriter) WriteHeader(int)                    {}
func (s *stalledEventWriter) SetWriteDeadline(t time.Time) error { s.deadline = t; return nil }
func (s *stalledEventWriter) Write([]byte) (int, error) {
	time.Sleep(time.Until(s.deadline))
	return 0, os.ErrDeadlineExceeded
}
func (s *stalledEventWriter) FlushError() error { return nil }
func TestSSEStalledWriteHasFiniteDeadline(t *testing.T) {
	w := &stalledEventWriter{header: make(http.Header)}
	start := time.Now()
	e := sendSSE(w, http.NewResponseController(w), "id:1", "running", map[string]string{"status": "RUNNING"})
	if e == nil || time.Since(start) > 6*time.Second || time.Since(start) < 4*time.Second {
		t.Fatal("stalled writer did not terminate within configured bound", e, time.Since(start))
	}
}
func TestLocalWebHostCannotBeForged(t *testing.T) {
	w, _ := NewLocalWeb(nil, "http://localhost:8080")
	r := httptest.NewRequest("GET", "http://evil.invalid/", nil)
	out := httptest.NewRecorder()
	w.Wrap(http.NotFoundHandler()).ServeHTTP(out, r)
	if out.Code != 404 {
		t.Fatal(out.Code)
	}
}
