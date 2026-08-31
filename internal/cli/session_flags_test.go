package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type capturedSessionRequests struct {
	open       daemon.SessionOpenRequest
	readQuery  url.Values
	write      daemon.SessionWriteRequest
	control    daemon.SessionControlRequest
	wait       daemon.SessionWaitRequest
	listQuery  url.Values
	close      daemon.SessionCloseRequest
	showCalled bool
	approvals  []daemon.SessionApprovalIssueRequest
}

func newCapturedSessionHandler(t *testing.T, captured *capturedSessionRequests, sessionID string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		decodeCapturedRequest(t, r, &captured.open)
		writeCapturedResponse(t, w, daemon.SessionOpenResponse{
			SchemaVersion: "1",
			Session: daemon.SessionDTO{
				SessionID: sessionID,
				Target:    captured.open.Target,
				State:     "active",
			},
		})
	})
	mux.HandleFunc("POST /v1/session-approvals", func(w http.ResponseWriter, r *http.Request) {
		var request daemon.SessionApprovalIssueRequest
		decodeCapturedRequest(t, r, &request)
		captured.approvals = append(captured.approvals, request)
		writeCapturedResponse(t, w, daemon.SessionApprovalIssueResponse{
			SchemaVersion: "1", ApprovalID: "app-session-0123456789abcdef0123456789abcdef",
			Deadline: "2026-08-30T10:01:00Z", ExpiresAt: "2026-08-30T10:01:00Z",
			Operation: daemon.SessionApprovalOperationDTO{
				Kind: request.Kind, Target: request.Target, Reason: request.Reason,
				IdempotencyKey: request.IdempotencyKey, Parameters: map[string]any{},
			},
		})
	})
	mux.HandleFunc("GET /v1/sessions/"+sessionID+"/read", func(w http.ResponseWriter, r *http.Request) {
		captured.readQuery = r.URL.Query()
		writeCapturedResponse(t, w, daemon.SessionReadResponse{
			SchemaVersion: "1",
			SessionID:     sessionID,
			Chunks:        []daemon.SessionChunkDTO{},
		})
	})
	mux.HandleFunc("POST /v1/sessions/"+sessionID+"/write", func(w http.ResponseWriter, r *http.Request) {
		decodeCapturedRequest(t, r, &captured.write)
		writeCapturedResponse(t, w, daemon.SessionWriteResponse{
			SchemaVersion: "1",
			BytesWritten:  len(captured.write.Data),
		})
	})
	mux.HandleFunc("POST /v1/sessions/"+sessionID+"/control", func(w http.ResponseWriter, r *http.Request) {
		decodeCapturedRequest(t, r, &captured.control)
		writeCapturedResponse(t, w, daemon.SessionControlResponse{SchemaVersion: "1", Status: "sent"})
	})
	mux.HandleFunc("POST /v1/sessions/"+sessionID+"/wait", func(w http.ResponseWriter, r *http.Request) {
		decodeCapturedRequest(t, r, &captured.wait)
		writeCapturedResponse(t, w, daemon.SessionWaitResponse{SchemaVersion: "1", SessionID: sessionID, Matched: true})
	})
	mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		captured.listQuery = r.URL.Query()
		writeCapturedResponse(t, w, daemon.SessionListResponse{SchemaVersion: "1", Sessions: []daemon.SessionDTO{}})
	})
	mux.HandleFunc("GET /v1/sessions/"+sessionID, func(w http.ResponseWriter, _ *http.Request) {
		captured.showCalled = true
		writeCapturedResponse(t, w, daemon.SessionOpenResponse{
			SchemaVersion: "1",
			Session:       daemon.SessionDTO{SessionID: sessionID, State: "active"},
		})
	})
	mux.HandleFunc("POST /v1/sessions/"+sessionID+"/close", func(w http.ResponseWriter, r *http.Request) {
		decodeCapturedRequest(t, r, &captured.close)
		writeCapturedResponse(t, w, daemon.SessionCloseResponse{
			SchemaVersion: "1",
			Session:       daemon.SessionDTO{SessionID: sessionID, State: "closed"},
		})
	})
	return mux
}

type sessionApprovalPrompter struct {
	confirm bool
	calls   int
}

func (p *sessionApprovalPrompter) PromptConfirmation(_ string) bool {
	p.calls++
	return p.confirm
}

func TestCLISessionApproveConfirmsAndTranslatesAllOperationKinds(t *testing.T) {
	var captured capturedSessionRequests
	statePath, sessionID := setupCapturedSessionCLI(t, &captured)
	prompter := &sessionApprovalPrompter{confirm: true}
	application := cli.NewApp(nil, cli.WithStateDir(statePath), cli.WithPrompter(prompter))
	machineID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	writeData := "CLI_PRIVATE_WRITE_42"

	commands := [][]string{
		{"session", "approve", "open", machineID, "--reason", "approve open", "--idempotency-key", "cli-approve-open", "--valid-for", "45s", "--json"},
		{"session", "approve", "write", sessionID, "--data", writeData, "--reason", "approve write", "--idempotency-key", "cli-approve-write", "--valid-for", "45s", "--json"},
		{"session", "approve", "control", sessionID, "ctrl-c", "--reason", "approve control", "--idempotency-key", "cli-approve-control", "--valid-for", "45s", "--json"},
		{"session", "approve", "close", sessionID, "--reason", "approve close", "--idempotency-key", "cli-approve-close", "--valid-for", "45s", "--json"},
	}
	for _, command := range commands {
		var stdout, stderr bytes.Buffer
		if code := application.Run(command, &stdout, &stderr); code != cli.ExitSuccess {
			t.Fatalf("command %v exit=%d stderr=%s", command, code, stderr.String())
		}
		if bytes.Contains(stdout.Bytes(), []byte(writeData)) {
			t.Fatalf("command %v leaked write plaintext", command)
		}
	}
	if prompter.calls != 4 || len(captured.approvals) != 4 {
		t.Fatalf("prompts=%d approvals=%d", prompter.calls, len(captured.approvals))
	}
	if captured.approvals[0].Kind != "session.open" || captured.approvals[0].Target != machineID || captured.approvals[0].ValidForMillis != 45_000 {
		t.Fatalf("open approval request=%+v", captured.approvals[0])
	}
	if captured.approvals[1].Kind != "session.write" || captured.approvals[1].SessionID != sessionID || captured.approvals[1].Data != writeData {
		t.Fatalf("write approval request=%+v", captured.approvals[1])
	}
	if captured.approvals[2].Key != "ctrl-c" || captured.approvals[3].Kind != "session.close" || captured.approvals[3].SessionID != sessionID {
		t.Fatalf("control/close requests=%+v %+v", captured.approvals[2], captured.approvals[3])
	}
}

func TestCLISessionApproveDeclinedConfirmationSendsNothing(t *testing.T) {
	var captured capturedSessionRequests
	statePath, _ := setupCapturedSessionCLI(t, &captured)
	prompter := &sessionApprovalPrompter{confirm: false}
	application := cli.NewApp(nil, cli.WithStateDir(statePath), cli.WithPrompter(prompter))
	var stdout, stderr bytes.Buffer
	code := application.Run([]string{
		"session", "approve", "open", "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		"--reason", "decline approval", "--idempotency-key", "cli-approve-declined", "--valid-for", "30s", "--json",
	}, &stdout, &stderr)
	if code != cli.ExitDenied || prompter.calls != 1 || len(captured.approvals) != 0 {
		t.Fatalf("exit=%d prompts=%d approvals=%d", code, prompter.calls, len(captured.approvals))
	}
}

func TestCLISessionApproveHelpAndInvalidRequestsStayLocal(t *testing.T) {
	var captured capturedSessionRequests
	statePath, sessionID := setupCapturedSessionCLI(t, &captured)
	prompter := &sessionApprovalPrompter{confirm: true}
	application := cli.NewApp(nil, cli.WithStateDir(statePath), cli.WithPrompter(prompter))
	machineID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	var stdout, stderr bytes.Buffer
	if code := application.Run([]string{"session", "approve", "--help"}, &stdout, &stderr); code != cli.ExitSuccess {
		t.Fatalf("help exit=%d stderr=%s", code, stderr.String())
	}
	invalid := [][]string{
		{"session", "approve", "open", machineID, "--idempotency-key", "missing-reason", "--valid-for", "30s"},
		{"session", "approve", "open", machineID, "--reason", "missing key", "--valid-for", "30s"},
		{"session", "approve", "open", machineID, "--reason", "bad duration", "--idempotency-key", "bad-duration", "--valid-for", "500ms"},
		{"session", "approve", "write", sessionID, "--reason", "missing data", "--idempotency-key", "missing-data", "--valid-for", "30s"},
		{"session", "approve", "control", sessionID, "--reason", "missing control", "--idempotency-key", "missing-control", "--valid-for", "30s"},
		{"session", "approve", "close", sessionID, "--force", "--reason", "removed force", "--idempotency-key", "removed-force", "--valid-for", "30s"},
		{"session", "approve", "unknown", machineID, "--reason", "unknown action", "--idempotency-key", "unknown-action", "--valid-for", "30s"},
	}
	for _, args := range invalid {
		stdout.Reset()
		stderr.Reset()
		if code := application.Run(args, &stdout, &stderr); code != cli.ExitUsage {
			t.Fatalf("args=%v exit=%d stderr=%s", args, code, stderr.String())
		}
	}
	if prompter.calls != 0 || len(captured.approvals) != 0 {
		t.Fatalf("invalid requests prompted=%d sent=%d", prompter.calls, len(captured.approvals))
	}
}

func decodeCapturedRequest(t *testing.T, request *http.Request, target any) {
	t.Helper()
	if err := json.NewDecoder(request.Body).Decode(target); err != nil {
		t.Errorf("decode request: %v", err)
	}
}

func writeCapturedResponse(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func setupCapturedSessionCLI(t *testing.T, captured *capturedSessionRequests) (string, string) {
	t.Helper()

	statePath := t.TempDir()
	sd, err := statedir.Resolve(statePath)
	if err != nil {
		t.Fatalf("resolve state dir: %v", err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatalf("ensure state dir: %v", err)
	}

	sessionID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	server := httptest.NewServer(newCapturedSessionHandler(t, captured, sessionID))
	t.Cleanup(server.Close)

	record := daemon.EndpointRecord{
		SchemaVersion: daemon.SchemaVersion,
		Endpoint:      server.URL,
		PID:           os.Getpid(),
		RuntimeID:     "session-flags-test",
		StartedAt:     time.Now().UTC(),
	}
	if err := daemon.WriteEndpointFile(sd.DaemonDir(), record); err != nil {
		t.Fatalf("write endpoint: %v", err)
	}
	createTestOperatorToken(t, sd.AuthDir())

	return statePath, sessionID
}

func runSessionJSONCommand(t *testing.T, app *cli.App, args ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := app.Run(args, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("command %q returned %d: %s", args, code, stderr.String())
	}
	var value any
	if err := json.Unmarshal(stdout.Bytes(), &value); err != nil {
		t.Fatalf("command %q did not emit JSON: %q (%v)", args, stdout.String(), err)
	}
}

func TestCLISessionPositionalFirstFlagsReachDaemon(t *testing.T) {
	var captured capturedSessionRequests
	statePath, sessionID := setupCapturedSessionCLI(t, &captured)
	app := cli.NewApp(nil, cli.WithStateDir(statePath))
	machineID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	runSessionJSONCommand(t, app,
		"session", "open", machineID,
		"--reason", "positional open",
		"--idempotency-key", "open-positional-1",
		"--cols", "101",
		"--rows", "37",
		"--term", "xterm",
		"--json",
	)
	runSessionJSONCommand(t, app,
		"session", "read", sessionID,
		"--after-seq", "7",
		"--limit", "321",
		"--json",
	)
	runSessionJSONCommand(t, app,
		"session", "write", sessionID,
		"--data", "echo 42\r\n",
		"--reason", "positional write",
		"--idempotency-key", "write-positional-1",
		"--json",
	)
	runSessionJSONCommand(t, app,
		"session", "control", sessionID, "ctrl-c",
		"--reason", "positional control",
		"--idempotency-key", "control-positional-1",
		"--json",
	)
	runSessionJSONCommand(t, app,
		"session", "wait", sessionID,
		"--settle-ms", "250",
		"--regex", "done",
		"--after-seq", "8",
		"--timeout", "17",
		"--json",
	)
	runSessionJSONCommand(t, app,
		"session", "list",
		"--machine", machineID,
		"--json",
	)
	runSessionJSONCommand(t, app, "session", "show", sessionID, "--json")
	runSessionJSONCommand(t, app,
		"session", "close", sessionID,
		"--reason", "positional close",
		"--idempotency-key", "close-positional-1",
		"--json",
	)

	assertCapturedOpenAndRead(t, captured, machineID)
	assertCapturedWriteAndControl(t, captured)
	assertCapturedWaitListShowAndClose(t, captured, machineID)
}

func TestCLISessionOpenRejectsNonCanonicalTerminalInputsBeforeRequest(t *testing.T) {
	var captured capturedSessionRequests
	statePath, _ := setupCapturedSessionCLI(t, &captured)
	application := cli.NewApp(nil, cli.WithStateDir(statePath))
	machineID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	for _, args := range [][]string{
		{"session", "open", machineID, "--cols", "65536"},
		{"session", "open", "--rows", "65536", machineID},
		{"session", "open", machineID, "--term", "xterm 256"},
	} {
		var stdout, stderr bytes.Buffer
		if code := application.Run(args, &stdout, &stderr); code != cli.ExitUsage {
			t.Fatalf("args %v exit = %d, want usage; stderr=%s", args, code, stderr.String())
		}
	}
	if captured.open.Target != "" {
		t.Fatalf("invalid terminal request reached daemon: %+v", captured.open)
	}
}

func TestCLISessionOpenAllowsOmittedEnrolledTarget(t *testing.T) {
	var captured capturedSessionRequests
	statePath, _ := setupCapturedSessionCLI(t, &captured)
	application := cli.NewApp(nil, cli.WithStateDir(statePath))
	var stdout, stderr bytes.Buffer
	if code := application.Run([]string{
		"session", "open", "--reason", "open enrolled default", "--idempotency-key", "open-default", "--json",
	}, &stdout, &stderr); code != cli.ExitSuccess {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if captured.open.Target != "" {
		t.Fatalf("omitted target request = %+v", captured.open)
	}
}

func TestCLISessionCloseRejectsRemovedForceFlagBeforeRequest(t *testing.T) {
	var captured capturedSessionRequests
	statePath, sessionID := setupCapturedSessionCLI(t, &captured)
	application := cli.NewApp(nil, cli.WithStateDir(statePath))
	var stdout, stderr bytes.Buffer
	code := application.Run([]string{
		"session", "close", sessionID, "--force",
		"--reason", "removed force", "--idempotency-key", "removed-close-force",
	}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Fatalf("removed close force exit=%d stderr=%s", code, stderr.String())
	}
	if captured.close.IdempotencyKey != "" {
		t.Fatalf("removed close force reached daemon: %+v", captured.close)
	}
}

func assertCapturedOpenAndRead(t *testing.T, captured capturedSessionRequests, machineID string) {
	t.Helper()
	if captured.open.Target != machineID || captured.open.Reason != "positional open" || captured.open.IdempotencyKey != "open-positional-1" {
		t.Fatalf("open request lost positional-first flags: %+v", captured.open)
	}
	if captured.open.Cols != 101 || captured.open.Rows != 37 || captured.open.Term != "xterm" {
		t.Fatalf("open terminal flags = %d x %d %q", captured.open.Cols, captured.open.Rows, captured.open.Term)
	}
	if captured.readQuery.Get("after_seq") != "7" || captured.readQuery.Get("limit_bytes") != "321" {
		t.Fatalf("read query = %v", captured.readQuery)
	}
}

func assertCapturedWriteAndControl(t *testing.T, captured capturedSessionRequests) {
	t.Helper()
	if captured.write.Data != "echo 42\r\n" || captured.write.Reason != "positional write" || captured.write.IdempotencyKey != "write-positional-1" {
		t.Fatalf("write request lost flags: %+v", captured.write)
	}
	if captured.control.Key != "ctrl-c" || captured.control.Reason != "positional control" || captured.control.IdempotencyKey != "control-positional-1" {
		t.Fatalf("control request lost flags: %+v", captured.control)
	}
}

func assertCapturedWaitListShowAndClose(t *testing.T, captured capturedSessionRequests, machineID string) {
	t.Helper()
	if captured.wait.SettleMs != 250 || captured.wait.Regex != "done" || captured.wait.AfterSeq != 8 || captured.wait.TimeoutSeconds != 17 {
		t.Fatalf("wait request lost flags: %+v", captured.wait)
	}
	if captured.listQuery.Get("machine") != machineID {
		t.Fatalf("list query = %v", captured.listQuery)
	}
	if !captured.showCalled {
		t.Fatal("show route was not called")
	}
	if captured.close.Reason != "positional close" || captured.close.IdempotencyKey != "close-positional-1" {
		t.Fatalf("close request lost flags: %+v", captured.close)
	}
}
