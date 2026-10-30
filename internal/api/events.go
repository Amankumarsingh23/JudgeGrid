package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/progress"
	"github.com/Amankumarsingh23/JudgeGrid/internal/submission"
	"github.com/Amankumarsingh23/JudgeGrid/internal/telemetry"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func eventCursor(id string, n int64) string { return id + ":" + strconv.FormatInt(n, 10) }
func parseCursor(value, id string) (int64, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || strings.ToLower(parts[0]) != id {
		return 0, domain.ErrInvalid
	}
	n, e := strconv.ParseInt(parts[1], 10, 64)
	if e != nil || n < 0 {
		return 0, domain.ErrInvalid
	}
	return n, nil
}
func sendSSE(w http.ResponseWriter, controller *http.ResponseController, id, kind string, data any) (err error) {
	start := time.Now()
	defer func() {
		telemetry.Observe("sse.write", telemetry.Class(err), "", time.Since(start).Seconds())
		if err != nil {
			telemetry.Count("sse.disconnect", telemetry.Class(err), "")
			if telemetry.Class(err) == "timeout" {
				telemetry.Count("sse.slow", "timeout", "")
			}
		}
	}()
	if e := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); e != nil {
		return e
	}
	encoded, e := json.Marshal(data)
	if e != nil {
		return e
	}
	if len(encoded) > 4096 {
		return fmt.Errorf("public event exceeds wire bound")
	}
	if _, e = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, kind, encoded); e != nil {
		return e
	}
	return controller.Flush()
}
func (h *submissionHandler) events(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	ctxObserve, finishObserve := telemetry.Begin(r.Context(), "sse.connect", "")
	r = r.WithContext(ctxObserve)
	defer finishObserve(nil)
	id := r.PathValue("id")
	if !submission.ValidID(id) {
		apiError(w, domain.ErrNotFound)
		return
	}
	store, ok := h.service.ProgressStore()
	if !ok {
		apiError(w, domain.ErrNotFound)
		return
	}
	check, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	snapshot, e := store.ProgressSnapshot(check, principal, id)
	cancel()
	if e != nil {
		apiError(w, e)
		return
	}
	id = snapshot.SubmissionID
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("cursor")
	}
	after := snapshot.Cursor
	resume := raw != ""
	if resume {
		telemetry.Count("sse.reconnect", "ok", "")
	}
	reset := false
	if resume {
		after, e = parseCursor(raw, id)
		if e != nil {
			apiError(w, e)
			return
		}
		if after > snapshot.Cursor {
			respond(w, 409, errorBody("invalid_event_cursor", "refresh the current submission state"))
			return
		}
		reset = after < snapshot.First-1 || snapshot.Cursor-after > progress.ReplayWindow
	}
	check, cancel = context.WithTimeout(r.Context(), 3*time.Second)
	lease, e := store.AcquireSSE(check, principal)
	cancel()
	if e != nil {
		apiError(w, e)
		return
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		store.ReleaseSSE(cleanup, principal, lease)
	}()
	controller := http.NewResponseController(w)
	if e = controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); e != nil {
		apiError(w, fmt.Errorf("stream transport unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Vary", "Cookie")
	if _, e = io.WriteString(w, ": connected\nretry: 1000\n\n"); e != nil {
		return
	}
	if controller.Flush() != nil {
		return
	}
	emit := func(event progress.Event) error {
		if event.Sequence != after+1 {
			return fmt.Errorf("journal gap")
		}
		if e := sendSSE(w, controller, eventCursor(id, event.Sequence), event.Kind, event.Data); e != nil {
			return e
		}
		telemetry.Since("sse.delivery", "", event.At)
		if resume {
			telemetry.Count("sse.replay", "replay", "")
		}
		after = event.Sequence
		return nil
	}
	if resume && !reset {
		for after < snapshot.Cursor {
			read, stop := context.WithTimeout(r.Context(), 3*time.Second)
			events, e := store.ProgressEvents(read, principal, id, after, snapshot.Cursor, progress.PageSize)
			stop()
			if e != nil || len(events) == 0 {
				return
			}
			for _, event := range events {
				if emit(event) != nil {
					return
				}
			}
		}
	}
	kind := "snapshot"
	if reset {
		kind = "reset"
		telemetry.Count("sse.reset", "reset", "")
	}
	if sendSSE(w, controller, eventCursor(id, snapshot.Cursor), kind, snapshot) != nil {
		return
	}
	after = snapshot.Cursor
	lifetime, stop := context.WithTimeout(r.Context(), 2*time.Minute)
	defer stop()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-lifetime.Done():
			return
		case <-heartbeat.C:
			check, cancel := context.WithTimeout(lifetime, 3*time.Second)
			e := store.RenewSSE(check, principal, lease)
			cancel()
			if e != nil {
				return
			}
			if controller.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
				return
			}
			if _, e = io.WriteString(w, ": heartbeat\n\n"); e != nil || controller.Flush() != nil {
				return
			}
		case <-poll.C:
			current, e := h.auth(r)
			if e != nil || current != principal {
				return
			}
			read, cancel := context.WithTimeout(lifetime, 3*time.Second)
			head, e := store.ProgressSnapshot(read, principal, id)
			cancel()
			if e != nil {
				return
			}
			if head.Cursor-after > progress.ReplayWindow || after < head.First-1 {
				telemetry.Count("sse.reset", "reset", "")
				sendSSE(w, controller, eventCursor(id, head.Cursor), "reset", head)
				return
			}
			read, cancel = context.WithTimeout(lifetime, 3*time.Second)
			events, e := store.ProgressEvents(read, principal, id, after, 0, progress.PageSize)
			cancel()
			if e != nil {
				return
			}
			for _, event := range events {
				if emit(event) != nil {
					return
				}
			}
		}
	}
}
