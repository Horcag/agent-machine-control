package daemon

import (
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/events"
)

func (s *Server) handleGlobalEvents(w http.ResponseWriter, r *http.Request) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	if !caller.CallerPermissions.Has("audit:read") {
		writeError(w, http.StatusForbidden, "forbidden", "global event stream requires operator authority")
		return
	}

	if s.eventHub == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "event hub unavailable")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "streaming unsupported")
		return
	}

	ch, unsub := s.eventHub.SubscribeGlobal(r.Context())
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := events.FormatSSE(ev)
			if err != nil {
				return
			}
			if _, err := w.Write(data); err != nil {
				return
			}
			flusher.Flush()
			if ev.EventType == "overflow" {
				return
			}
		}
	}
}
