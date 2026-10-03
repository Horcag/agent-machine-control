package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func desktopCLIHTTP(t *testing.T, handler http.HandlerFunc) *App {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	sd, err := statedir.Resolve(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.LoadOrCreate(sd.AuthDir()); err != nil {
		t.Fatal(err)
	}
	token, err := auth.ReadTokenFile(sd.AuthDir(), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("CLI did not use authenticated operator POST")
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	if err := daemon.WriteEndpointFile(sd.DaemonDir(), daemon.EndpointRecord{Endpoint: srv.URL, SchemaVersion: daemon.SchemaVersion, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	return NewApp(nil, WithStateDir(state))
}

func assertDesktopCLIRequest(t *testing.T, r *http.Request, route string, expected any) {
	t.Helper()
	var got, want any
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Error(err)
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Error(err)
	}
	if err := json.Unmarshal(encoded, &want); err != nil {
		t.Error(err)
	}
	if r.URL.Path != route || !reflect.DeepEqual(got, want) {
		t.Errorf("route=%s got=%v want=%v", r.URL.Path, got, want)
	}
}

func desktopCLIResponse(name string) any {
	switch name {
	case "enable":
		return struct {
			Grant   app.ConsoleLabGrant `json:"grant"`
			Receipt domain.Receipt      `json:"receipt"`
		}{Grant: app.ConsoleLabGrant{GrantID: "synthetic-receipt"}}
	case "status":
		return app.ConsoleLabGrantStatus{Grant: app.ConsoleLabGrant{GrantID: "synthetic-receipt"}, State: "active"}
	case "disable":
		return domain.Receipt{ReceiptID: "synthetic-receipt"}
	default:
		return app.DesktopActionResult{Receipt: &domain.Receipt{ReceiptID: "synthetic-receipt"}}
	}
}

func TestDesktopCLIRequestsAndFlags(t *testing.T) {
	deadline := "2026-10-02T20:00:00Z"
	request := domain.DesktopRequest{RequestID: "0123456789abcdef0123456789abcdef", Deadline: deadline, Action: "clipboard.set", Text: "synthetic text"}
	path := filepath.Join(t.TempDir(), "request.json")
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, route string
		args        []string
		want        any
	}{
		{"enable", "/v1/desktop/lab/issue", []string{"enable", "--target", "default", "--reason", "synthetic grant", "--idempotency-key", "issue-key", "--for-mcp", "--valid-for", "2m", "--acknowledge-external-effects"}, app.ConsoleLabGrantIssueRequest{Target: "default", Reason: "synthetic grant", IdempotencyKey: "issue-key", Beneficiary: "agent:mcp-local", ValidForMillis: 120000, AcknowledgeExternalEffects: true}},
		{"status", "/v1/desktop/lab/status", []string{"status", "own-grant"}, map[string]string{"grant_id": "own-grant"}},
		{"disable", "/v1/desktop/lab/revoke", []string{"disable", "own-grant", "--reason", "synthetic revoke", "--idempotency-key", "revoke-key", "--deadline", deadline}, app.ConsoleLabGrantRevokeRequest{GrantID: "own-grant", Reason: "synthetic revoke", IdempotencyKey: "revoke-key", Deadline: deadline}},
		{"action", "/v1/desktop/action", []string{"action", "--request-file", path, "--target", "default", "--reason", "synthetic action", "--idempotency-key", "action-key", "--lab-grant-id", "own-grant"}, app.DesktopActionRequest{Target: "default", Request: request, Reason: "synthetic action", IdempotencyKey: "action-key", LabGrantID: "own-grant"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a := desktopCLIHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				assertDesktopCLIRequest(t, r, tc.route, tc.want)
				if err := json.NewEncoder(w).Encode(desktopCLIResponse(tc.name)); err != nil {
					t.Error(err)
				}
			})
			var stdout, stderr bytes.Buffer
			if code := a.Run(append([]string{"desktop"}, tc.args...), &stdout, &stderr); code != ExitSuccess || calls != 1 || !strings.Contains(stdout.String(), "synthetic-receipt") {
				t.Fatalf("code=%d calls=%d stdout=%s stderr=%s", code, calls, stdout.String(), stderr.String())
			}
		})
	}
}

func TestDesktopCLIObserveRequest(t *testing.T) {
	a := desktopCLIHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var got app.DesktopActionRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if got.Request.Action != "uia.tree" || got.Request.Validate() != nil || got.Request.Deadline == "" || got.IdempotencyKey != "" {
			t.Errorf("observe request=%+v", got)
		}
		_, _ = w.Write([]byte(`{"response":{"success":true}}`))
	})
	var out bytes.Buffer
	if code := a.Run([]string{"desktop", "observe", "uia.tree"}, &out, &out); code != ExitSuccess {
		t.Fatalf("observe code=%d %s", code, out.String())
	}
}

func TestDesktopCLIRejectsInvalidFiles(t *testing.T) {
	a := desktopCLIHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid request reached daemon") })
	valid := `{"request_id":"0123456789abcdef0123456789abcdef","deadline":"2026-10-02T20:00:00Z","action":"windows"}`
	for _, data := range []string{`{`, valid + ` {}`, strings.Replace(valid, `"action":"windows"`, `"action":"windows","actor":"operator:forged"`, 1), `null`, strings.Replace(valid, "windows", "window.resize", 1), valid[:len(valid)-1] + `,"text":"` + strings.Repeat("x", 128*1024) + `"}`} {
		path := filepath.Join(t.TempDir(), "request.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if code := a.Run([]string{"desktop", "action", "--request-file", path}, &out, &out); code != ExitUsage || !strings.Contains(out.String(), "invalid") {
			t.Fatalf("code=%d output=%s", code, out.String())
		}
	}
}

func TestDesktopCLIRejectsInvalidUsage(t *testing.T) {
	a := desktopCLIHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid request reached daemon") })
	for _, args := range [][]string{{"desktop"}, {"--direct", "desktop", "observe"}, {"desktop", "observe", "--unknown"}, {"desktop", "observe", "one", "two"}, {"desktop", "action"}, {"desktop", "action", "extra"}, {"desktop", "enable", "extra"}, {"desktop", "unknown"}} {
		var out bytes.Buffer
		if code := a.Run(args, &out, &out); code != ExitUsage {
			t.Fatalf("args=%v code=%d output=%s", args, code, out.String())
		}
		if len(args) > 0 && args[0] == "--direct" && !strings.Contains(out.String(), "console --direct remains available") {
			t.Fatal("native recovery guidance missing")
		}
	}
}

type recordCLIStub struct {
	consoleStub
	request   app.ConsoleRecordRequest
	recording app.ConsoleRecording
}

func (s *recordCLIStub) Record(_ context.Context, actor domain.ActorContext, req app.ConsoleRecordRequest) (app.ConsoleRecording, error) {
	s.calls++
	s.actor = actor
	s.request = req
	return s.recording, s.err
}

func TestConsoleRecordCLIProtectedArtifactAndCleanup(t *testing.T) {
	data := []byte("GIF89a synthetic recording")
	svc := &recordCLIStub{recording: app.ConsoleRecording{MIMEType: "image/gif", Data: data, SHA256: "synthetic-digest", Width: 8, Height: 4, ObservedAt: []time.Time{time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)}}}
	a := NewApp(nil, WithConsoleService(svc))
	path := filepath.Join(privateConsoleTestDir(t), "record.gif")
	args := []string{"--direct", "console", "record", "default", "--output", path, "--width", "8", "--height", "4", "--frames", "2", "--interval-ms", "100"}
	var out bytes.Buffer
	if code := a.Run(args, &out, &out); code != ExitSuccess {
		t.Fatalf("code=%d %s", code, out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) || svc.request != (app.ConsoleRecordRequest{Target: "default", Width: 8, Height: 4, Frames: 2, IntervalMillis: 100}) {
		t.Fatalf("artifact=%q request=%+v err=%v", got, svc.request, err)
	}
	if err := target.NewPrivatePathSecurity().ValidateFile(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte(`"data"`)) || !bytes.Contains(out.Bytes(), []byte("synthetic-digest")) {
		t.Fatalf("metadata=%s", out.String())
	}
	if code := a.Run(args, &out, &out); code != ExitConflict || svc.calls != 1 {
		t.Fatal("record overwrote existing artifact")
	}
	svc.err = errors.New("synthetic capture failure")
	failed := filepath.Join(privateConsoleTestDir(t), "failed.gif")
	if code := a.Run([]string{"--direct", "console", "record", "--output", failed}, &out, &out); code == ExitSuccess {
		t.Fatal("capture failure succeeded")
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatalf("failed artifact remains: %v", err)
	}
	unsupported := NewApp(nil, WithConsoleService(&consoleStub{}))
	if code := unsupported.Run([]string{"--direct", "console", "record", "--output", failed}, &out, &out); code != ExitBackendUnavailable {
		t.Fatalf("unsupported code=%d", code)
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatalf("unsupported artifact remains: %v", err)
	}
}

func TestClipboardUncertaintyCLIMessage(t *testing.T) {
	var stderr bytes.Buffer
	code := mapClientError(domain.ErrClipboardUncertain, &stderr, "desktop")
	if code == ExitSuccess || !strings.Contains(stderr.String(), "reconcile before any retry or restoration") {
		t.Fatal(code, stderr.String())
	}
}
