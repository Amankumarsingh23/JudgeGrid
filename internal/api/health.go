package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Check func(context.Context) error

func Handler(timeout time.Duration, database, redis Check) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		checks := map[string]string{}
		status := http.StatusOK
		for name, check := range map[string]Check{"postgres": database, "redis": redis} {
			checks[name] = "ok"
			if err := check(ctx); err != nil {
				checks[name] = "unavailable"
				status = http.StatusServiceUnavailable
			}
		}
		respond(w, status, checks)
	})
	return mux
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
