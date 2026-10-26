package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestGuestTrailerFailuresPreserveCategoriesWithoutReplayOrSecrets(t *testing.T) {
	for _, tc := range []struct {
		category string
		cause    error
	}{
		{"invalid_argument", ErrInvalidArgument}, {"approval_required", ErrDenied},
		{"guest_workspace_unprovisioned", app.ErrGuestWorkspaceUnprovisioned},
		{"guest_authority", app.ErrGuestAuthority}, {"guest_not_prepared", app.ErrGuestNotPrepared},
		{"guest_ownership", app.ErrGuestOwnership}, {"conflict", ErrConflict},
		{"timeout", ErrTimeout}, {"", app.ErrGuestUncertain}, {"private-unknown-category", app.ErrGuestUncertain},
	} {
		t.Run(tc.category, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Trailer", daemon.GuestResultTrailer)
				final, _ := json.Marshal(daemon.GuestFinal{Category: tc.category, Error: "private-secret-path-token"})
				w.Header().Set(daemon.GuestResultTrailer, base64.RawStdEncoding.EncodeToString(final))
			}))
			defer server.Close()
			_, err := New(server.URL, "synthetic").GuestOperation(t.Context(), app.GuestOperationParams{Operation: "prepare", Deadline: time.Now().Add(time.Minute)}, nil, nil)
			if !errors.Is(err, tc.cause) || calls != 1 || strings.Contains(err.Error(), "private") {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if tc.cause != app.ErrGuestUncertain && errors.Is(err, app.ErrGuestUncertain) {
				t.Fatal("known failure became uncertain")
			}
		})
	}
}

func TestGuestMissingTrailerRemainsUncertain(t *testing.T) {
	cl, calls := guestFixture(t, nil, "missing")
	_, err := cl.GuestOperation(t.Context(), app.GuestOperationParams{Operation: "prepare", Deadline: time.Now().Add(time.Minute)}, nil, nil)
	if !errors.Is(err, app.ErrGuestUncertain) || !errors.Is(err, ErrMalformedResponse) || *calls != 1 {
		t.Fatalf("calls=%d err=%v", *calls, err)
	}
}

func TestGuestLegacyHTTPEnvelopeUsesExistingErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		status   int
		category string
		cause    error
	}{
		{400, "invalid_argument", ErrInvalidArgument}, {403, "approval_required", ErrDenied},
	} {
		payload, _ := json.Marshal(daemon.ErrorEnvelope{SchemaVersion: daemon.SchemaVersion, Error: daemon.ErrorField{Category: tc.category, Message: "guest operation rejected before dispatch; inspect its category"}})
		response := &http.Response{StatusCode: tc.status, Body: io.NopCloser(bytes.NewReader(payload))}
		err := mapHTTPError(response)
		api, ok := errors.AsType[*APIError](err)
		if !errors.Is(err, tc.cause) || !ok || api.Category != tc.category || errors.Is(err, app.ErrGuestUncertain) {
			t.Fatalf("legacy error=%v", err)
		}
	}
}
