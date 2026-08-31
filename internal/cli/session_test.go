package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func handleMockCLIGet(w http.ResponseWriter, r *http.Request, sessID string) bool {
	switch r.URL.Path {
	case "/v1/sessions":
		_ = json.NewEncoder(w).Encode(daemon.SessionListResponse{
			SchemaVersion: "1",
			Sessions: []daemon.SessionDTO{
				{SessionID: sessID, Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", State: "active", Cols: 80, Rows: 24},
			},
		})
		return true
	case "/v1/sessions/" + sessID:
		exitCode := 0
		_ = json.NewEncoder(w).Encode(daemon.SessionOpenResponse{
			SchemaVersion: "1",
			Session: daemon.SessionDTO{
				SessionID:    sessID,
				Target:       "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
				OwnerActor:   "agent:test",
				State:        "active",
				Cols:         80,
				Rows:         24,
				TermType:     "xterm-256color",
				BytesRead:    128,
				BytesWritten: 64,
				CreatedAt:    "2026-08-31T00:00:00Z",
				ExitCode:     &exitCode,
			},
		})
		return true
	case "/v1/sessions/" + sessID + "/read":
		_ = json.NewEncoder(w).Encode(daemon.SessionReadResponse{
			SchemaVersion: "1",
			SessionID:     sessID,
			Chunks:        []daemon.SessionChunkDTO{{Seq: 1, Data: "prompt> "}},
			NextSeq:       1,
		})
		return true
	default:
		return false
	}
}

func handleMockCLIPost(w http.ResponseWriter, r *http.Request, sessID string) bool {
	switch r.URL.Path {
	case "/v1/sessions":
		_ = json.NewEncoder(w).Encode(daemon.SessionOpenResponse{
			SchemaVersion: "1",
			Session: daemon.SessionDTO{
				SessionID: sessID,
				Target:    "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
				State:     "active",
			},
		})
		return true
	case "/v1/sessions/" + sessID + "/write":
		_ = json.NewEncoder(w).Encode(daemon.SessionWriteResponse{
			SchemaVersion: "1",
			BytesWritten:  4,
		})
		return true
	case "/v1/sessions/" + sessID + "/control":
		_ = json.NewEncoder(w).Encode(daemon.SessionControlResponse{
			SchemaVersion: "1",
			Status:        "sent",
		})
		return true
	case "/v1/sessions/" + sessID + "/wait":
		_ = json.NewEncoder(w).Encode(daemon.SessionWaitResponse{
			SchemaVersion: "1",
			SessionID:     sessID,
			Chunks:        []daemon.SessionChunkDTO{{Seq: 1, Data: "settled output"}},
			Matched:       true,
		})
		return true
	case "/v1/sessions/" + sessID + "/close":
		_ = json.NewEncoder(w).Encode(daemon.SessionCloseResponse{
			SchemaVersion: "1",
			Session: daemon.SessionDTO{
				SessionID: sessID,
				State:     "closed",
			},
		})
		return true
	default:
		return false
	}
}

func handleMockCLISessionRoute(w http.ResponseWriter, r *http.Request, sessID string) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && handleMockCLIGet(w, r, sessID) {
		return
	}
	if r.Method == http.MethodPost && handleMockCLIPost(w, r, sessID) {
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func setupTestCLIState(t *testing.T) (string, *httptest.Server) {
	tempDir := t.TempDir()
	sd, _ := statedir.Resolve(filepath.Join(tempDir, "state"))
	_ = sd.EnsureDirs()

	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleMockCLISessionRoute(w, r, sessID)
	}))

	// Write daemon endpoint file
	ep := daemon.EndpointRecord{
		SchemaVersion: daemon.SchemaVersion,
		Endpoint:      server.URL,
		PID:           os.Getpid(),
		RuntimeID:     "test-runtime",
		StartedAt:     time.Now().UTC(),
	}
	_ = daemon.WriteEndpointFile(sd.DaemonDir(), ep)

	createTestOperatorToken(t, sd.AuthDir())

	return sd.Root(), server
}

func TestCLISession_DirectModeRejection(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := cli.NewApp(nil)
	code := app.Run([]string{"--direct", "session", "list"}, &stdout, &stderr)
	if code != cli.ExitBackendUnavailable {
		t.Errorf("expected ExitBackendUnavailable (%d), got %d", cli.ExitBackendUnavailable, code)
	}
	if !strings.Contains(stderr.String(), "cannot run in --direct mode") {
		t.Errorf("expected explicit direct mode error message, got: %s", stderr.String())
	}
}

func TestCLISession_DaemonUnavailable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := cli.NewApp(nil, cli.WithStateDir(t.TempDir()))
	code := app.Run([]string{"session", "list"}, &stdout, &stderr)
	if code != cli.ExitBackendUnavailable {
		t.Errorf("expected ExitBackendUnavailable, got %d", code)
	}
}

func TestCLISession_HelpAndUsage(t *testing.T) {
	stateDir, server := setupTestCLIState(t)
	defer server.Close()

	app := cli.NewApp(nil, cli.WithStateDir(stateDir))
	var stdout, stderr bytes.Buffer

	// session bare
	code := app.Run([]string{"session"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for bare session, got %d", code)
	}

	// session --help
	stdout.Reset()
	code = app.Run([]string{"session", "--help"}, &stdout, &stderr)
	if code != cli.ExitSuccess || !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("expected help usage output")
	}

	stderr.Reset()
	code = app.Run([]string{"session", "close", "--help"}, &stdout, &stderr)
	if code != cli.ExitUsage || strings.Contains(stderr.String(), "force") {
		t.Errorf("close help still exposes removed force contract: code=%d stderr=%q", code, stderr.String())
	}

	// session unknown
	stderr.Reset()
	code = app.Run([]string{"session", "unknown"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage on unknown subcommand")
	}
}

func TestCLISession_SubcommandsAndJSON(t *testing.T) {
	stateDir, server := setupTestCLIState(t)
	defer server.Close()

	app := cli.NewApp(nil, cli.WithStateDir(stateDir))
	ctx := context.Background()

	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	machGUID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// 1. Open
	var stdout, stderr bytes.Buffer
	code := app.RunWithContext(ctx, []string{"session", "open", machGUID, "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session open failed: %d, stderr: %s", code, stderr.String())
	}

	// 2. Read
	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "read", sessID, "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session read failed: %d, stderr: %s", code, stderr.String())
	}

	// 3. Write
	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "write", sessID, "dir\n", "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session write failed: %d, stderr: %s", code, stderr.String())
	}

	// 4. Control
	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "control", sessID, "ctrl-c", "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session control failed: %d, stderr: %s", code, stderr.String())
	}

	// 5. Wait
	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "wait", sessID, "--settle-ms", "100", "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session wait failed: %d, stderr: %s", code, stderr.String())
	}

	// 6. List
	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "list", "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session list failed: %d, stderr: %s", code, stderr.String())
	}

	// 7. Show
	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "show", sessID, "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session show failed: %d, stderr: %s", code, stderr.String())
	}

	// 8. Close
	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "close", sessID, "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session close failed: %d, stderr: %s", code, stderr.String())
	}
}

func TestCLISession_HumanOutputAndFlags(t *testing.T) {
	stateDir, server := setupTestCLIState(t)
	defer server.Close()

	app := cli.NewApp(nil, cli.WithStateDir(stateDir))
	ctx := context.Background()

	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	machGUID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	var stdout, stderr bytes.Buffer
	code := app.RunWithContext(ctx, []string{"session", "open", machGUID, "--cols", "100", "--rows", "30", "--reason", "test human", "--idempotency-key", "idem-human-1"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session open human failed: %d, stderr: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "read", sessID, "--after-seq", "1", "--limit", "512"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session read human failed: %d, stderr: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "wait", sessID, "--regex", "prompt", "--after-seq", "1"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session wait human failed: %d, stderr: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "list", "--machine", machGUID}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session list human failed: %d, stderr: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "show", sessID}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session show human failed: %d, stderr: %s", code, stderr.String())
	}
	for _, want := range []string{"Owner Actor:       agent:test", "Dimensions:        80x24 (xterm-256color)", "Exit Code:         0"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("session show human output missing %q: %s", want, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "close", sessID, "--reason", "finished"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("session close human failed: %d, stderr: %s", code, stderr.String())
	}
}

func TestCLISessionCloseRejectsMissingApprovalFileBeforeRequest(t *testing.T) {
	stateDir, server := setupTestCLIState(t)
	defer server.Close()

	app := cli.NewApp(nil, cli.WithStateDir(stateDir))
	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	missingApproval := t.TempDir() + "/missing-approval.json"
	var stdout, stderr bytes.Buffer
	code := app.Run([]string{"session", "close", sessID, "--approval-file", missingApproval}, &stdout, &stderr)
	if code != cli.ExitDenied {
		t.Fatalf("missing close approval exit=%d, want %d; stderr=%s", code, cli.ExitDenied, stderr.String())
	}
	if !strings.Contains(stderr.String(), "invalid approval file") {
		t.Fatalf("missing close approval error=%q", stderr.String())
	}
}

func TestCLISession_Attach(t *testing.T) {
	stateDir, server := setupTestCLIState(t)
	defer server.Close()

	app := cli.NewApp(nil, cli.WithStateDir(stateDir))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"

	var stdout, stderr bytes.Buffer
	// Missing session ID
	code := app.RunWithContext(ctx, []string{"session", "attach"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage on missing attach session ID")
	}

	// Attach with short timeout
	stdout.Reset()
	stderr.Reset()
	code = app.RunWithContext(ctx, []string{"session", "attach", sessID}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Errorf("expected ExitSuccess on attach timeout, got %d", code)
	}
}

func TestCLISession_UsageAndDirectMode(t *testing.T) {
	stateDir, server := setupTestCLIState(t)
	defer server.Close()

	app := cli.NewApp(nil, cli.WithStateDir(stateDir))
	ctx := context.Background()

	var stdout, stderr bytes.Buffer

	// Direct mode rejection
	code := app.RunWithContext(ctx, []string{"--direct", "session", "open", "c4a523d4-6b99-4d62-a5e2-4752c0f20001"}, &stdout, &stderr)
	if code != cli.ExitBackendUnavailable {
		t.Errorf("expected ExitBackendUnavailable for --direct session, got %d", code)
	}

	// Missing subcommand
	code = app.RunWithContext(ctx, []string{"session"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage on empty session command, got %d", code)
	}

	// Help subcommand
	code = app.RunWithContext(ctx, []string{"session", "--help"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Errorf("expected ExitSuccess on session --help, got %d", code)
	}

	// Unknown subcommand
	code = app.RunWithContext(ctx, []string{"session", "invalid-subcommand"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage on unknown subcommand, got %d", code)
	}

	// Missing args for individual commands
	cmdMissingArgs := [][]string{
		{"session", "read"},
		{"session", "write"},
		{"session", "write", "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"},
		{"session", "control"},
		{"session", "control", "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"},
		{"session", "wait"},
		{"session", "show"},
		{"session", "close"},
	}

	for _, cmd := range cmdMissingArgs {
		stdout.Reset()
		stderr.Reset()
		code = app.RunWithContext(ctx, cmd, &stdout, &stderr)
		if code != cli.ExitUsage {
			t.Errorf("expected ExitUsage for %v, got %d", cmd, code)
		}
	}
}
