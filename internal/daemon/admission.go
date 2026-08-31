package daemon

import (
	"context"
	"net/http"
	"strings"
)

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			writeError(w, http.StatusForbidden, "forbidden", "browser Origin headers are forbidden")
			return
		}

		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid authorization header")
			return
		}
		token := strings.TrimSpace(authHeader[7:])
		caller, _, ok := s.authStore.Authenticate(token)
		if !ok || caller == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid bearer token")
			return
		}

		mutationRequest := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		if s.admissionClosed.Load() && mutationRequest {
			writeError(w, http.StatusServiceUnavailable, "shutting_down", "mutation admission is closed")
			return
		}
		if mutationRequest && s.afterEarlyMutationAdmissionCheck != nil {
			s.afterEarlyMutationAdmissionCheck()
		}

		select {
		case s.semaphore <- struct{}{}:
			defer func() { <-s.semaphore }()
		case <-r.Context().Done():
			writeError(w, http.StatusRequestTimeout, "request_cancelled", "request was cancelled while waiting for server capacity")
			return
		}

		// This is the admission linearization point for mutations queued across cutover.
		if s.admissionClosed.Load() && mutationRequest {
			writeError(w, http.StatusServiceUnavailable, "shutting_down", "mutation admission is closed")
			return
		}

		ctx := context.WithValue(r.Context(), callerContextKey, *caller)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
