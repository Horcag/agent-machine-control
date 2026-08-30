package daemon

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

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
