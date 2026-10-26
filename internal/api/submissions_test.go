package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/submission"
)

type stubRepository struct {
	err       error
	principal string
	replay    bool
	calls     int
}

func (s *stubRepository) Create(_ context.Context, p, k string, r domain.CreateRequest) (domain.Submission, bool, error) {
	s.calls++
	s.principal = p
	return domain.Submission{ID: "550e8400-e29b-41d4-a716-446655440000", Status: domain.Queued}, s.replay, s.err
}
func (s *stubRepository) Get(_ context.Context, p, id string) (domain.Submission, error) {
	s.principal = p
	return domain.Submission{}, s.err
}
func (s *stubRepository) List(_ context.Context, p string, n int, c *domain.Cursor) ([]domain.Submission, error) {
	s.principal = p
	return nil, s.err
}
func (s *stubRepository) Problem(context.Context, string) (domain.Problem, error) {
	return domain.Problem{}, s.err
}

func TestSubmissionHTTP(t *testing.T) {
	valid := `{"problem_id":1,"language":"cpp17","source_code":"int main(){}"}`
	for _, tc := range []struct {
		name, body, key  string
		want             int
		err              error
		replay, disabled bool
	}{
		{name: "new", body: valid, key: "one", want: 201},
		{name: "replay", body: valid, key: "one", want: 200, replay: true},
		{name: "missing key", body: valid, want: 400},
		{name: "empty", body: `{}`, key: "one", want: 400},
		{name: "malformed", body: `{`, key: "one", want: 400},
		{name: "unknown field", body: `{"extra":1}`, key: "one", want: 400},
		{name: "trailing JSON", body: valid + ` {}`, key: "one", want: 400},
		{name: "trailing garbage", body: valid + ` x`, key: "one", want: 400},
		{name: "body limit", body: strings.Repeat(" ", domain.MaxBodyBytes+1), key: "one", want: 413},
		{name: "source limit", body: `{"problem_id":1,"language":"go","source_code":"` + strings.Repeat("x", domain.MaxSourceBytes+1) + `"}`, key: "one", want: 413},
		{name: "not found", body: valid, key: "one", want: 404, err: domain.ErrNotFound},
		{name: "conflict", body: valid, key: "one", want: 409, err: domain.ErrConflict},
		{name: "database failure", body: valid, key: "one", want: 503, err: errors.New("secret password")},
		{name: "auth disabled ignores user header", body: valid, key: "one", want: 401, disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubRepository{err: tc.err, replay: tc.replay}
			h := Routes(time.Second, func(context.Context) error { return nil }, func(context.Context) error { return nil }, submission.New(repo, time.Second), DevelopmentPrincipal(!tc.disabled, "local-user"))
			req := httptest.NewRequest("POST", "/api/v1/submissions", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", tc.key)
			req.Header.Set("X-User-ID", "attacker")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
			if !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "secret password") {
				t.Fatal("unsafe response")
			}
			if repo.calls > 0 && repo.principal != "local-user" {
				t.Fatal("untrusted principal")
			}
			if tc.replay && w.Header().Get("Idempotency-Replayed") != "true" {
				t.Fatal("missing replay header")
			}
		})
	}
}

func TestReadRoutes(t *testing.T) {
	for _, tc := range []struct {
		path string
		err  error
		want int
	}{
		{"/api/v1/submissions", nil, 200}, {"/api/v1/submissions?limit=101", nil, 400},
		{"/api/v1/submissions?limit=no", nil, 400}, {"/api/v1/submissions?cursor=bad", nil, 400},
		{"/api/v1/submissions/not-uuid", nil, 404},
		{"/api/v1/submissions/550e8400-e29b-41d4-a716-446655440000", domain.ErrNotFound, 404},
		{"/api/v1/problems/two-sum", nil, 200},
	} {
		t.Run(tc.path, func(t *testing.T) {
			repo := &stubRepository{err: tc.err}
			h := Routes(time.Second, nil, nil, submission.New(repo, time.Second), DevelopmentPrincipal(true, "owner"))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestContentType(t *testing.T) {
	h := Routes(time.Second, nil, nil, submission.New(&stubRepository{}, time.Second), DevelopmentPrincipal(true, "owner"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader(`{}`)))
	if w.Code != 415 {
		t.Fatal(w.Code)
	}
}
