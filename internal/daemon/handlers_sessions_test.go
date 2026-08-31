package daemon_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh/fakeserver"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	gossh "golang.org/x/crypto/ssh"
)

func setupTestDaemonWithSSH(t *testing.T) (*daemon.Server, string, string, *fakeserver.FakeSSHServer) {
	srv, endpoint, operatorToken, _, _, fakeSSH := setupTestDaemonWithSSHConfig(t, guestssh.SanitizerConfig{})
	return srv, endpoint, operatorToken, fakeSSH
}

func setupTestDaemonWithSSHConfig(t *testing.T, sanitizerConfig guestssh.SanitizerConfig) (*daemon.Server, string, string, string, string, *fakeserver.FakeSSHServer) {
	return setupTestDaemonWithSSHConfigAndContainment(t, sanitizerConfig, true)
}

func setupTestDaemonWithSSHConfigAndContainment(t *testing.T, sanitizerConfig guestssh.SanitizerConfig, contained bool) (*daemon.Server, string, string, string, string, *fakeserver.FakeSSHServer) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	seedDaemonTestTarget(t, stateRoot)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := gossh.NewSignerFromKey(priv)
	sshPub, _ := gossh.NewPublicKey(pub)

	fakeSSH, err := fakeserver.New(fakeserver.ModeEcho, sshPub)
	if err != nil {
		t.Fatalf("failed to create fake ssh server: %v", err)
	}

	chkID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"

	rollbackID := chkID
	if !contained {
		rollbackID = ""
	}
	kp := &guestssh.MockKeyProvider{
		Signer:          signer,
		PinnedKeySHA256: fakeSSH.HostKeyPin(),
		Endpoint:        fakeSSH.Addr(),
		User:            "testadmin",
		MachineConfig: &guestssh.MachineSSHConfig{
			Endpoint:                 fakeSSH.Addr(),
			User:                     "testadmin",
			DefaultKeyAlias:          "default",
			PinnedHostKeySHA256:      fakeSSH.HostKeyPin(),
			ExternalEffectsContained: contained,
			RollbackCheckpointID:     rollbackID,
		},
	}
	transport := guestssh.NewTransport(kp)

	cfg := daemon.Config{
		StateDir:               stateRoot,
		ListenAddr:             "127.0.0.1:0",
		Transport:              transport,
		KeyProvider:            kp,
		Backend:                &mockDaemonBackend{},
		SessionSanitizerConfig: sanitizerConfig,
	}

	srv, err := daemon.NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("srv.Start failed: %v", err)
	}

	sd, _ := statedir.Resolve(stateRoot)
	opToken, err := auth.ReadTokenFile(sd.AuthDir(), auth.TokenTypeOperator)
	if err != nil {
		t.Fatalf("failed to read operator token: %v", err)
	}
	agentToken, err := auth.ReadTokenFile(sd.AuthDir(), auth.TokenTypeAgentMCP)
	if err != nil {
		t.Fatalf("failed to read agent token: %v", err)
	}

	return srv, srv.Endpoint(), opToken, agentToken, stateRoot, fakeSSH
}

func doJSONReq(t *testing.T, method, url, token string, body any) (int, []byte) {
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, url, bodyReader)
	if err != nil {
		t.Fatalf("failed to construct request: %v", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBytes
}

func requireJSONOK(t *testing.T, status int, data []byte, action string) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("%s failed status %d: %s", action, status, string(data))
	}
}

func TestDaemonSessions_EndToEnd(t *testing.T) {
	srv, endpoint, token, fakeSSH := setupTestDaemonWithSSH(t)
	defer func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown daemon: %v", err)
		}
	}()
	defer fakeSSH.Close()

	// 1. Open Session
	openReq := daemon.SessionOpenRequest{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "e2e test",
		IdempotencyKey: "idem-e2e-1",
		Cols:           80,
		Rows:           24,
		Term:           "xterm-256color",
	}
	status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, openReq)
	requireJSONOK(t, status, data, "Open")

	var openResp daemon.SessionOpenResponse
	if err := json.Unmarshal(data, &openResp); err != nil {
		t.Fatalf("unmarshal Open response failed: %v", err)
	}
	sessID := openResp.Session.SessionID
	if sessID == "" {
		t.Fatal("expected non-empty session ID")
	}

	// 2. Wait for the fake transport's initial prompt instead of relying on scheduler timing.
	status, data = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/wait", endpoint, sessID), token, daemon.SessionWaitRequest{
		Regex: "PS C:", TimeoutMillis: 1000,
	})
	requireJSONOK(t, status, data, "Wait for initial prompt")

	// 3. Read initial prompt.
	status, data = doJSONReq(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/read?after_seq=0", endpoint, sessID), token, nil)
	requireJSONOK(t, status, data, "Read")
	var readResp daemon.SessionReadResponse
	_ = json.Unmarshal(data, &readResp)
	if len(readResp.Chunks) == 0 {
		t.Errorf("expected initial prompt chunks")
	}

	// 4. Write data
	writeReq := daemon.SessionWriteRequest{Data: "dir\r\n", Reason: "list files", IdempotencyKey: "key-w-1"}
	status, data = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/write", endpoint, sessID), token, writeReq)
	requireJSONOK(t, status, data, "Write")

	// 5. Control key
	ctrlReq := daemon.SessionControlRequest{Key: "ctrl-c", Reason: "cancel command", IdempotencyKey: "key-c-1"}
	status, data = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/control", endpoint, sessID), token, ctrlReq)
	requireJSONOK(t, status, data, "Control")

	// 6. Wait settle with a test-owned deadline.
	waitReq := daemon.SessionWaitRequest{SettleMs: 100, AfterSeq: readResp.NextSeq, TimeoutMillis: 1000}
	status, data = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/wait", endpoint, sessID), token, waitReq)
	requireJSONOK(t, status, data, "Wait")

	// 7. List and Get
	status, data = doJSONReq(t, http.MethodGet, endpoint+"/v1/sessions", token, nil)
	requireJSONOK(t, status, data, "List")
	var listResp daemon.SessionListResponse
	_ = json.Unmarshal(data, &listResp)
	if len(listResp.Sessions) != 1 {
		t.Errorf("expected 1 session in list, got %d", len(listResp.Sessions))
	}

	status, data = doJSONReq(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s", endpoint, sessID), token, nil)
	requireJSONOK(t, status, data, "Get")

	// 8. Close Session
	closeReq := daemon.SessionCloseRequest{Reason: "finished e2e test", IdempotencyKey: "key-close-1"}
	status, data = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/close", endpoint, sessID), token, closeReq)
	requireJSONOK(t, status, data, "Close")
	var closeResp daemon.SessionCloseResponse
	_ = json.Unmarshal(data, &closeResp)
	if closeResp.Session.State != "closed" {
		t.Errorf("expected closed state, got %s", closeResp.Session.State)
	}
}

func TestDaemonSessions_EncodedRoutesFailClosed(t *testing.T) {
	srv, endpoint, token, fakeSSH := setupTestDaemonWithSSH(t)
	defer func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown daemon: %v", err)
		}
	}()
	defer fakeSSH.Close()

	status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "encoded route containment",
		IdempotencyKey: "encoded-route-open",
	})
	if status != http.StatusOK {
		t.Fatalf("open status = %d, body = %s", status, data)
	}
	var opened daemon.SessionOpenResponse
	if err := json.Unmarshal(data, &opened); err != nil {
		t.Fatal(err)
	}
	validID := opened.Session.SessionID

	tests := []struct {
		name string
		path string
	}{
		{name: "invalid canonical ID", path: "/v1/sessions/not-a-session"},
		{name: "dot dot", path: "/v1/sessions/%2e%2e"},
		{name: "encoded backslash", path: "/v1/sessions/..%5coutside"},
		{name: "encoded slash", path: "/v1/sessions/" + validID + "%2fread"},
		{name: "mixed separators", path: "/v1/sessions/..%5coutside%2fread"},
		{name: "double escape", path: "/v1/sessions/%252e%252e%255coutside"},
		{name: "extra route segments", path: "/v1/sessions/" + validID + "/read/extra"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := doJSONReq(t, http.MethodGet, endpoint+tt.path, token, nil)
			if status != http.StatusNotFound {
				t.Fatalf("status = %d, want %d; body=%s", status, http.StatusNotFound, body)
			}
			if bytes.Contains(body, []byte("outside")) || bytes.Contains(body, []byte(validID)) {
				t.Fatalf("error body disclosed route input: %s", body)
			}
		})
	}

	status, data = doJSONReq(t, http.MethodGet, endpoint+"/v1/sessions/"+validID, token, nil)
	if status != http.StatusOK {
		t.Fatalf("valid session route status = %d, want %d; body=%s", status, http.StatusOK, data)
	}
}

func TestDaemonSessions_SubSecondTimeoutsReachAppAndTransport(t *testing.T) {
	dir := missingDaemonStateRoot(t)
	seedDaemonTestTarget(t, dir)
	target := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	checkpoint := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	transport := &deadlineCaptureTransport{remaining: make(map[string]time.Duration)}
	keyProvider := &guestssh.MockKeyProvider{MachineConfig: &guestssh.MachineSSHConfig{
		Endpoint: "192.0.2.20:22", User: "synthetic", DefaultKeyAlias: "default",
		PinnedHostKeySHA256: "c3ludGhldGlj", ExternalEffectsContained: true, RollbackCheckpointID: checkpoint,
	}}
	srv, err := daemon.NewServer(daemon.Config{
		StateDir: dir, ListenAddr: "127.0.0.1:0", Backend: &mockDaemonBackend{}, Transport: transport, KeyProvider: keyProvider,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()
	token, err := auth.ReadTokenFile(filepath.Join(dir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}

	status, data := doJSONReq(t, http.MethodPost, srv.Endpoint()+"/v1/sessions", token, daemon.SessionOpenRequest{
		Target: target, Reason: "sub-second open", IdempotencyKey: "subsecond-open", TimeoutMillis: 250,
	})
	if !assertSubSecondTransportOutcome(t, transport, "open", status) {
		return
	}
	var opened daemon.SessionOpenResponse
	if err := json.Unmarshal(data, &opened); err != nil {
		t.Fatal(err)
	}
	sessionPath := srv.Endpoint() + "/v1/sessions/" + opened.Session.SessionID

	status, _ = doJSONReq(t, http.MethodPost, sessionPath+"/write", token, daemon.SessionWriteRequest{
		Data: "x", Reason: "sub-second write", IdempotencyKey: "subsecond-write", TimeoutMillis: 250,
	})
	assertSubSecondTransportOutcome(t, transport, "write", status)
	status, _ = doJSONReq(t, http.MethodPost, sessionPath+"/control", token, daemon.SessionControlRequest{
		Key: "ctrl-c", Reason: "sub-second control", IdempotencyKey: "subsecond-control", TimeoutMillis: 250,
	})
	assertSubSecondTransportOutcome(t, transport, "control", status)

	waitStarted := time.Now()
	status, _ = doJSONReq(t, http.MethodPost, sessionPath+"/wait", token, daemon.SessionWaitRequest{
		Regex: "never-matches", TimeoutMillis: 40,
	})
	if status != http.StatusGatewayTimeout {
		t.Fatalf("sub-second wait status=%d, want 504", status)
	}
	if elapsed := time.Since(waitStarted); elapsed > 300*time.Millisecond {
		t.Fatalf("40ms daemon wait lasted %v", elapsed)
	}

	status, _ = doJSONReq(t, http.MethodPost, sessionPath+"/close", token, daemon.SessionCloseRequest{
		Reason: "sub-second close", IdempotencyKey: "subsecond-close", TimeoutMillis: 250,
	})
	assertSubSecondTransportOutcome(t, transport, "close", status)
}

func TestDaemonSessions_ErrorBranches(t *testing.T) {
	srv, endpoint, token, fakeSSH := setupTestDaemonWithSSH(t)
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()
	defer fakeSSH.Close()

	sessID := "sess-0123456789abcdef0123456789abcdef"

	// 1. Invalid JSON body on open
	status, _ := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, "invalid json")
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on malformed open body, got %d", status)
	}

	// 2. Invalid target GUID on open
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{Target: "not-a-guid", Reason: "valid reason", IdempotencyKey: "k"})
	if status != http.StatusConflict {
		t.Errorf("expected 409 on target mismatch, got %d", status)
	}

	// 3. Read invalid session ID
	status, _ = doJSONReq(t, http.MethodGet, endpoint+"/v1/sessions/not-a-session/read", token, nil)
	if status != http.StatusNotFound {
		t.Errorf("expected 404 on not-a-session, got %d", status)
	}

	// 4. Read non-existent session ID
	status, _ = doJSONReq(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/read", endpoint, sessID), token, nil)
	if status != http.StatusNotFound {
		t.Errorf("expected 404 on non-existent session, got %d", status)
	}

	// 5. Write invalid session ID
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions/bad-id/write", token, daemon.SessionWriteRequest{Data: "dir\n", Reason: "test", IdempotencyKey: "k"})
	if status != http.StatusNotFound {
		t.Errorf("expected 404 on bad-id write, got %d", status)
	}

	// 6. Control invalid key
	status, _ = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/control", endpoint, sessID), token, daemon.SessionControlRequest{Key: "invalid-key", Reason: "test", IdempotencyKey: "k"})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on invalid control key, got %d", status)
	}

	// 7. Wait negative settle
	status, _ = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/wait", endpoint, sessID), token, daemon.SessionWaitRequest{SettleMs: -1})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on negative settle_ms, got %d", status)
	}

	// 8. Close missing body
	status, _ = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/close", endpoint, sessID), token, nil)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on close missing body, got %d", status)
	}

	// 9. Method not allowed on /v1/sessions
	status, _ = doJSONReq(t, http.MethodDelete, endpoint+"/v1/sessions", token, nil)
	if status != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 on delete /v1/sessions, got %d", status)
	}

	// 10. List filter by invalid machine
	status, _ = doJSONReq(t, http.MethodGet, endpoint+"/v1/sessions?machine=invalid", token, nil)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on invalid machine query param, got %d", status)
	}
}

func TestDaemonSessions_ValidationAndMutationBranches(t *testing.T) {
	srv, endpoint, token, fakeSSH := setupTestDaemonWithSSH(t)
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()
	defer fakeSSH.Close()

	sessID := "sess-0123456789abcdef0123456789abcdef"
	machGUID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// 1. Control missing reason or key
	status, _ := doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/control", endpoint, sessID), token, daemon.SessionControlRequest{Key: "ctrl-c"})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on control missing reason/key, got %d", status)
	}

	// 2. Wait negative timeout or large regex
	status, _ = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/wait", endpoint, sessID), token, daemon.SessionWaitRequest{TimeoutSeconds: -1})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on negative timeout_seconds, got %d", status)
	}
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
		Target: machGUID, Reason: "conflicting timeout", IdempotencyKey: "timeout-conflict", TimeoutSeconds: 1, TimeoutMillis: 250,
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on conflicting timeout fields, got %d", status)
	}
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
		Target: machGUID, Reason: "overflow timeout", IdempotencyKey: "timeout-overflow", TimeoutMillis: int64((time.Duration(1<<63-1))/time.Millisecond) + 1,
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on overflowing timeout_ms, got %d", status)
	}

	// 3. Close missing reason
	status, _ = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/close", endpoint, sessID), token, daemon.SessionCloseRequest{IdempotencyKey: "k"})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on close missing reason, got %d", status)
	}

	// 4. Read invalid after_seq / limit_bytes
	status, _ = doJSONReq(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/read?after_seq=bad", endpoint, sessID), token, nil)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on invalid after_seq, got %d", status)
	}
	status, _ = doJSONReq(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/read?limit_bytes=bad", endpoint, sessID), token, nil)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on invalid limit_bytes, got %d", status)
	}
}

func TestDaemonSessions_MapSessionErrorUnit(t *testing.T) {
	srv, _, _, fakeSSH := setupTestDaemonWithSSH(t)
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()
	defer fakeSSH.Close()

	testCases := []struct {
		err        error
		wantStatus int
	}{
		{err: &app.PolicyDeniedError{Reason: "rule", Message: "denied"}, wantStatus: http.StatusForbidden},
		{err: domain.ErrSessionNotFound, wantStatus: http.StatusNotFound},
		{err: domain.ErrSessionAccessDenied, wantStatus: http.StatusForbidden},
		{err: domain.ErrSessionClosed, wantStatus: http.StatusConflict},
		{err: domain.ErrSessionConflict, wantStatus: http.StatusConflict},
		{err: domain.ErrSessionWaitTimeout, wantStatus: http.StatusGatewayTimeout},
		{err: domain.ErrHostKeyMismatch, wantStatus: http.StatusBadGateway},
		{err: domain.ErrMissingHostKeyPin, wantStatus: http.StatusBadGateway},
		{err: domain.ErrNonCanonicalParameter, wantStatus: http.StatusBadRequest},
		{err: domain.ErrInvalidTerminalType, wantStatus: http.StatusBadRequest},
		{err: domain.ErrInvalidApprovalRecord, wantStatus: http.StatusBadRequest},
		{err: errors.New("unknown error"), wantStatus: http.StatusInternalServerError},
	}

	for _, tc := range testCases {
		w := httptest.NewRecorder()
		srv.MapSessionErrorForTest(w, tc.err)
		if w.Code != tc.wantStatus {
			t.Errorf("for err %v, got HTTP %d, want %d", tc.err, w.Code, tc.wantStatus)
		}
	}
}

func TestDaemonSessions_GetSessionSuccess(t *testing.T) {
	srv, endpoint, token, fakeSSH := setupTestDaemonWithSSH(t)
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()
	defer fakeSSH.Close()

	machGUID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// Open
	status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
		Target:         machGUID,
		Reason:         "open test",
		IdempotencyKey: "idem-get-test",
	})
	if status != http.StatusOK {
		t.Fatalf("Open failed: %d, body: %s", status, body)
	}

	var openResp daemon.SessionOpenResponse
	_ = json.Unmarshal([]byte(body), &openResp)
	sessID := openResp.Session.SessionID

	// Get
	status, getBody := doJSONReq(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s", endpoint, sessID), token, nil)
	if status != http.StatusOK {
		t.Fatalf("Get session failed: %d, body: %s", status, getBody)
	}
	var getResp daemon.SessionOpenResponse
	_ = json.Unmarshal([]byte(getBody), &getResp)
	if getResp.Session.SessionID != sessID {
		t.Errorf("expected session ID %s, got %s", sessID, getResp.Session.SessionID)
	}

	// Method not allowed on /v1/sessions/{id}
	status, _ = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s", endpoint, sessID), token, nil)
	if status != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 on POST /v1/sessions/{id}, got %d", status)
	}

	// Not found on /v1/sessions/{id}/unknown
	status, _ = doJSONReq(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/unknown", endpoint, sessID), token, nil)
	if status != http.StatusNotFound {
		t.Errorf("expected 404 on /v1/sessions/{id}/unknown, got %d", status)
	}

	// Not found on /v1/sessions/{id}/read/extra
	status, _ = doJSONReq(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/read/extra", endpoint, sessID), token, nil)
	if status != http.StatusNotFound {
		t.Errorf("expected 404 on /v1/sessions/{id}/read/extra, got %d", status)
	}

	// List with machine filter
	status, listBody := doJSONReq(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions?machine=%s", endpoint, machGUID), token, nil)
	if status != http.StatusOK {
		t.Fatalf("List failed: %d, body: %s", status, listBody)
	}
	var listResp daemon.SessionListResponse
	_ = json.Unmarshal([]byte(listBody), &listResp)
	if len(listResp.Sessions) != 1 {
		t.Errorf("expected 1 session from filtered list, got %d", len(listResp.Sessions))
	}
}

func TestDaemonSessions_MalformedInputs(t *testing.T) {
	srv, endpoint, token, fakeSSH := setupTestDaemonWithSSH(t)
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()
	defer fakeSSH.Close()

	machGUID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// Open
	status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
		Target:         machGUID,
		Reason:         "open test",
		IdempotencyKey: "idem-malformed-test",
	})
	if status != http.StatusOK {
		t.Fatalf("Open failed: %d, body: %s", status, body)
	}

	var openResp daemon.SessionOpenResponse
	_ = json.Unmarshal([]byte(body), &openResp)
	sessID := openResp.Session.SessionID

	// Bad after_seq
	status, _ = doJSONReq(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/read?after_seq=bad", endpoint, sessID), token, nil)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on bad after_seq, got %d", status)
	}

	// Bad limit_bytes
	status, _ = doJSONReq(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/read?limit_bytes=bad", endpoint, sessID), token, nil)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on bad limit_bytes, got %d", status)
	}

	// Malformed wait request
	status, _ = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/wait", endpoint, sessID), token, map[string]any{"settle_ms": -1})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on negative settle_ms, got %d", status)
	}

	// Open missing reason or invalid idempotency key
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
		Target:         machGUID,
		Reason:         "", // empty reason
		IdempotencyKey: "k",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on empty open reason, got %d", status)
	}
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
		Target:         machGUID,
		Reason:         "valid reason",
		IdempotencyKey: "", // empty idempotency key
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on empty open idempotency key, got %d", status)
	}

	// Write missing reason or empty idempotency key
	status, _ = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/write", endpoint, sessID), token, daemon.SessionWriteRequest{
		Data:           "dir\n",
		Reason:         "",
		IdempotencyKey: "k",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on empty write reason, got %d", status)
	}
	status, _ = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/write", endpoint, sessID), token, daemon.SessionWriteRequest{
		Data:           "dir\n",
		Reason:         "valid",
		IdempotencyKey: "",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on empty write idempotency key, got %d", status)
	}

	// Control empty idempotency key
	status, _ = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/control", endpoint, sessID), token, daemon.SessionControlRequest{
		Key:            "ctrl-c",
		Reason:         "valid",
		IdempotencyKey: "",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on empty control idempotency key, got %d", status)
	}

	// Close empty idempotency key
	status, _ = doJSONReq(t, http.MethodPost, fmt.Sprintf("%s/v1/sessions/%s/close", endpoint, sessID), token, daemon.SessionCloseRequest{
		Reason:         "valid",
		IdempotencyKey: "",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400 on empty close idempotency key, got %d", status)
	}
}

func TestDaemonSessions_UnauthenticatedRejection(t *testing.T) {
	srv, endpoint, _, fakeSSH := setupTestDaemonWithSSH(t)
	defer func() {
		_ = srv.Shutdown(context.Background())
	}()
	defer fakeSSH.Close()

	// Missing token -> 401 Unauthorized
	status, _ := doJSONReq(t, http.MethodGet, endpoint+"/v1/sessions", "", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("expected 401 on unauthenticated list, got %d", status)
	}

	// Invalid token -> 401 Unauthorized
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", "invalid-token", daemon.SessionOpenRequest{})
	if status != http.StatusUnauthorized {
		t.Errorf("expected 401 on invalid token open, got %d", status)
	}
}
