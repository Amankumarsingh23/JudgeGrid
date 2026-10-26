package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/submission"
)

// PrincipalResolver must be wired from trusted authentication, never a client-supplied user header.
type PrincipalResolver func(*http.Request) (string, error)

func DevelopmentPrincipal(enabled bool, principal string) PrincipalResolver {
	return func(*http.Request) (string, error) {
		if !enabled || principal == "" {
			return "", domain.ErrUnauthorized
		}
		return principal, nil
	}
}

func Routes(timeout time.Duration, database, redis Check, service *submission.Service, auth PrincipalResolver) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", Handler(timeout, database, redis))
	mux.Handle("GET /readyz", Handler(timeout, database, redis))
	h := &submissionHandler{service: service, auth: auth}
	mux.HandleFunc("POST /api/v1/submissions", h.create)
	mux.HandleFunc("GET /api/v1/submissions/{id}", h.get)
	mux.HandleFunc("GET /api/v1/submissions", h.list)
	mux.HandleFunc("GET /api/v1/problems/{slug}", func(w http.ResponseWriter, r *http.Request) {
		p, err := service.Problem(r.Context(), r.PathValue("slug"))
		if err != nil {
			apiError(w, err)
			return
		}
		respond(w, 200, p)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 404, errorBody("not_found", "resource not found"))
	})
	return mux
}

type submissionHandler struct {
	service *submission.Service
	auth    PrincipalResolver
}

func (h *submissionHandler) principal(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, err := h.auth(r)
	if err != nil {
		apiError(w, err)
		return "", false
	}
	return p, true
}
func (h *submissionHandler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		respond(w, 415, errorBody("unsupported_media_type", "Content-Type must be application/json"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, domain.MaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req domain.CreateRequest
	if err = decoder.Decode(&req); err != nil {
		decodeError(w, err)
		return
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = domain.ErrInvalid
		}
		decodeError(w, err)
		return
	}
	s, replay, err := h.service.Create(r.Context(), p, r.Header.Get("Idempotency-Key"), req)
	if err != nil {
		apiError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/submissions/"+s.ID)
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
		w.Header().Set("Idempotency-Replayed", "true")
	}
	respond(w, status, s)
}
func (h *submissionHandler) get(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	s, err := h.service.Get(r.Context(), p, r.PathValue("id"))
	if err != nil {
		apiError(w, err)
		return
	}
	respond(w, 200, s)
}
func (h *submissionHandler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := h.principal(w, r)
	if !ok {
		return
	}
	limit := 20
	if value, exists := r.URL.Query()["limit"]; exists {
		var err error
		limit, err = strconv.Atoi(value[0])
		if err != nil {
			apiError(w, domain.ErrInvalid)
			return
		}
	}
	page, err := h.service.List(r.Context(), p, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		apiError(w, err)
		return
	}
	respond(w, 200, page)
}
func decodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		apiError(w, domain.ErrTooLarge)
	} else {
		apiError(w, domain.ErrInvalid)
	}
}
func errorBody(code, message string) any {
	return map[string]any{"error": map[string]string{"code": code, "message": message}}
}
func apiError(w http.ResponseWriter, err error) {
	status, code, message := 503, "unavailable", "service temporarily unavailable"
	switch {
	case errors.Is(err, domain.ErrInvalid):
		status, code, message = 400, "invalid_request", "invalid request"
	case errors.Is(err, domain.ErrTooLarge):
		status, code, message = 413, "payload_too_large", "request or source code exceeds limit"
	case errors.Is(err, domain.ErrNotFound):
		status, code, message = 404, "not_found", "resource not found"
	case errors.Is(err, domain.ErrConflict):
		status, code, message = 409, "conflict", "request conflicts with existing state or idempotency key"
	case errors.Is(err, domain.ErrUnauthorized):
		status, code, message = 401, "unauthorized", "authentication required"
	default:
		slog.Error("submission operation failed", "error", err)
	}
	respond(w, status, errorBody(code, message))
}
