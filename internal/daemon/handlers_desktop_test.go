package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image/gif"
	"io"
	"net/http"

	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func TestDesktopDaemonStrictRequestBoundary(t *testing.T) {
	backend := &consoleDaemonBackend{mockDaemonBackend: &mockDaemonBackend{}}
	endpoint, _, agent := setupConsoleDaemon(t, backend)
	for _, path := range []string{"desktop/action", "desktop/lab/issue", "desktop/lab/status", "desktop/lab/active", "desktop/lab/revoke", "console/record"} {
		for _, body := range []string{`{"actor":"operator:forged"}`, `{"unknown":true}`, `{} {}`, `null`, `[]`, `{`, `{"target":"` + strings.Repeat("x", 128*1024) + `"}`} {
			t.Run(path+"/"+body[:min(len(body), 24)], func(t *testing.T) {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/v1/"+path, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+agent)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != http.StatusBadRequest || !bytes.Contains(data, []byte("invalid_argument")) {
					t.Fatalf("status=%d body=%s", resp.StatusCode, data)
				}
			})
		}
		status, _ := doJSONReq(t, http.MethodGet, endpoint+"/v1/"+path, agent, nil)
		if status != http.StatusMethodNotAllowed {
			t.Fatalf("%s GET status=%d", path, status)
		}
		status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/"+path, "", map[string]any{})
		if status != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated status=%d", path, status)
		}
	}
	status, _ := doJSONReq(t, http.MethodPost, endpoint+"/v1/desktop/unknown", agent, map[string]any{})
	if status != http.StatusNotFound {
		t.Fatalf("unknown status=%d", status)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.inputs) != 0 {
		t.Fatal("invalid envelope reached input provider")
	}
}

func TestDesktopDaemonUnavailableRoutes(t *testing.T) {
	srv, endpoint, _, agent := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()
	for _, path := range []string{"desktop/action", "desktop/lab/issue", "desktop/lab/status", "desktop/lab/active", "desktop/lab/revoke", "console/record"} {
		status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/"+path, agent, map[string]any{})
		if status != http.StatusNotImplemented || !bytes.Contains(body, []byte("capability_unavailable")) {
			t.Fatalf("%s status=%d body=%s", path, status, body)
		}
	}
}

func TestDesktopDaemonLabRejectsMissingAuthority(t *testing.T) {
	endpoint, operator, agent := setupConsoleDaemon(t, &consoleDaemonBackend{mockDaemonBackend: &mockDaemonBackend{}})
	for _, token := range []string{operator, agent} {
		for _, path := range []string{"desktop/lab/issue", "desktop/lab/status", "desktop/lab/revoke", "desktop/action"} {
			status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/"+path, token, map[string]any{})
			if status != http.StatusBadRequest || !bytes.Contains(body, []byte("console_failed")) {
				t.Fatalf("%s status=%d body=%s", path, status, body)
			}
		}
		status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/desktop/lab/active", token, map[string]string{"target": "default"})
		if status != http.StatusOK || !bytes.Contains(body, []byte(`"state":"disabled"`)) {
			t.Fatalf("active status=%d body=%s", status, body)
		}
	}
}

func TestDesktopDaemonRecordGIF(t *testing.T) {
	endpoint, _, agent := setupConsoleDaemon(t, &consoleDaemonBackend{mockDaemonBackend: &mockDaemonBackend{}})
	status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/console/record", agent, app.ConsoleRecordRequest{Width: 8, Height: 4, Frames: 2, IntervalMillis: 100})
	var got app.ConsoleRecording
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	animation, err := gif.DecodeAll(bytes.NewReader(got.Data))
	if status != http.StatusOK || err != nil || len(animation.Image) != 2 || len(got.ObservedAt) != 2 || got.MIMEType != "image/gif" || got.SHA256 == "" {
		t.Fatalf("status=%d recording=%+v err=%v", status, got, err)
	}
}
