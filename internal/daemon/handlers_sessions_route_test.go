package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestSessionInvalidTerminalTypeMapsToCanonicalBadRequest(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).MapSessionErrorForTest(w, fmt.Errorf("canonical terminal validation: %w", domain.ErrInvalidTerminalType))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	var body ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Category != "invalid_argument" {
		t.Fatalf("error category = %v, want invalid_argument", body.Error.Category)
	}
}

func TestSessionTargetResolutionErrorsMapToSanitizedFailClosedResponses(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		status   int
		category string
	}{
		{name: "missing", err: target.ErrNoDefault, status: http.StatusConflict, category: "target_not_enrolled"},
		{name: "different", err: target.ErrDifferentTarget, status: http.StatusConflict, category: "target_mismatch"},
		{name: "reference miss", err: domain.ErrMachineReferenceMiss, status: http.StatusConflict, category: "target_mismatch"},
		{name: "stale", err: domain.ErrMachineReferenceStale, status: http.StatusConflict, category: "target_mismatch"},
		{name: "inventory unavailable", err: target.ErrInventoryRefresh, status: http.StatusServiceUnavailable, category: "target_unavailable"},
		{name: "host unavailable", err: domain.ErrMachineHostUnavailable, status: http.StatusServiceUnavailable, category: "target_unavailable"},
		{name: "access denied", err: domain.ErrMachineAccessDenied, status: http.StatusServiceUnavailable, category: "target_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			(&Server{}).MapSessionErrorForTest(recorder, fmt.Errorf("private provider detail: %w", test.err))
			var body ErrorEnvelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != test.status || body.Error.Category != test.category ||
				body.Error.Message == "private provider detail" {
				t.Fatalf("response=%d envelope=%+v", recorder.Code, body)
			}
		})
	}
}

func TestParseSessionSubrouteValidatesDecodedID(t *testing.T) {
	t.Parallel()

	validID := "sess-0123456789abcdef0123456789abcdef"
	tests := []struct {
		name         string
		path         string
		wantID       domain.SessionID
		wantSubparts []string
		wantErr      bool
	}{
		{name: "valid ID", path: "sessions/" + validID, wantID: domain.SessionID(validID)},
		{name: "valid action", path: "sessions/" + validID + "/read", wantID: domain.SessionID(validID), wantSubparts: []string{"read"}},
		{name: "extra route segments remain for not-found dispatch", path: "sessions/" + validID + "/read/extra", wantID: domain.SessionID(validID), wantSubparts: []string{"read", "extra"}},
		{name: "invalid canonical ID", path: "sessions/not-a-session/read", wantErr: true},
		{name: "dot dot", path: "sessions/../read", wantErr: true},
		{name: "decoded backslash", path: `sessions/..\synthetic/read`, wantErr: true},
		{name: "decoded slash", path: "sessions/../synthetic/read", wantErr: true},
		{name: "mixed separators", path: `sessions/..\synthetic/../read`, wantErr: true},
		{name: "double escaped traversal", path: `sessions/%2e%2e%5csynthetic/read`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertParsedSessionSubroute(t, tt.path, tt.wantID, tt.wantSubparts, tt.wantErr)
		})
	}
}

func assertParsedSessionSubroute(t *testing.T, path string, wantID domain.SessionID, wantSubparts []string, wantErr bool) {
	t.Helper()
	gotID, gotSubparts, err := parseSessionSubroute(path)
	if wantErr {
		if !errors.Is(err, domain.ErrInvalidSessionID) {
			t.Fatalf("parseSessionSubroute(%q) error = %v, want ErrInvalidSessionID", path, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("parseSessionSubroute(%q) error = %v", path, err)
	}
	if gotID != wantID {
		t.Fatalf("session ID = %q, want %q", gotID, wantID)
	}
	if len(gotSubparts) != len(wantSubparts) {
		t.Fatalf("subparts = %q, want %q", gotSubparts, wantSubparts)
	}
	for i := range gotSubparts {
		if gotSubparts[i] != wantSubparts[i] {
			t.Fatalf("subparts = %q, want %q", gotSubparts, wantSubparts)
		}
	}
}

func TestDispatchSessionsRejectsEscapedSeparatorsBeforeService(t *testing.T) {
	t.Parallel()

	validID := "sess-0123456789abcdef0123456789abcdef"
	for _, requestPath := range []string{
		"/v1/sessions/" + validID + "%2fread",
		"/v1/sessions/" + validID + "%2Fread",
		"/v1/sessions/%2e%2e%5csynthetic/read",
	} {
		t.Run(requestPath, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, requestPath, nil)
			recorder := httptest.NewRecorder()
			server := &Server{}

			path := req.URL.Path[len("/v1/"):]
			server.dispatchSessions(recorder, req, path)

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNotFound, recorder.Body.String())
			}
		})
	}
}
