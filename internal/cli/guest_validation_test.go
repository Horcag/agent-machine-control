package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestGuestCLIInvalidShapesRejectBeforeDiscovery(t *testing.T) {
	for _, args := range [][]string{
		{"prepare", "--max-output", "64"}, {"put", "--source", "private-missing-path", "--max-output", "64"},
		{"get", "--destination", "private-output", "--max-output", "64"},
		{"exec", "--source", "private-missing-path", "--destination", "private-output", "--max-output", "16777217"},
		{"remove-owned"}, {"prepare", "--source", "private-missing-path"},
	} {
		args = append(args, "--deadline", time.Now().Add(time.Minute).Format(time.RFC3339Nano))
		var out, stderr bytes.Buffer
		code := runGuest(t.Context(), nil, false, "missing-state", args, &out, &stderr)
		if code != ExitUsage || !strings.Contains(stderr.String(), "invalid guest") || strings.Contains(stderr.String(), "private-") || out.Len() != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
	}
}

func TestGuestCLITypedFailureExitAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		category string
		exit     int
	}{
		{"invalid_argument", ExitUsage}, {"approval_required", ExitDenied}, {"guest_workspace_unprovisioned", ExitDenied},
		{"guest_authority", ExitDenied}, {"guest_not_prepared", ExitConflict}, {"guest_ownership", ExitConflict},
		{"guest_uncertain", ExitBackendUnavailable},
	} {
		t.Run(tc.category, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if _, _, err := daemon.DecodeGuestMetadata(r.Body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Trailer", daemon.GuestResultTrailer)
				data, _ := json.Marshal(daemon.GuestFinal{Category: tc.category, Error: "private-secret-path-token"})
				w.Header().Set(daemon.GuestResultTrailer, base64.RawStdEncoding.EncodeToString(data))
			}))
			defer server.Close()
			state := guestCLIState(t, server.URL)
			var out, stderr bytes.Buffer
			args := []string{"prepare", "--deadline", time.Now().Add(time.Minute).Format(time.RFC3339Nano)}
			code := runGuest(t.Context(), nil, false, state, args, &out, &stderr)
			if code != tc.exit || calls != 1 || out.Len() != 0 || strings.Contains(stderr.String(), "private") {
				t.Fatalf("code=%d calls=%d stderr=%s", code, calls, stderr.String())
			}
		})
	}
}
