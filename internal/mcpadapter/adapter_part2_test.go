package mcpadapter

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMutationAndDurableToolsOperations(t *testing.T) {
	ctx := t.Context()
	var lastReqPath string
	server := setupMockDaemon(t, &lastReqPath)

	cl := client.New(server.URL, "mock-agent-mcp-token")
	a := &Adapter{client: cl}

	_, opRes, err := a.OperationShow(ctx, nil, OperationShowInput{OperationID: "op-12345678901234567890123456789012"})
	if err != nil {
		t.Fatalf("OperationShow error: %v", err)
	}
	if opRes.Operation.OperationID != "op-12345678901234567890123456789012" {
		t.Errorf("Unexpected operation result: %+v", opRes)
	}

	_, _, err = a.OperationWait(ctx, nil, OperationWaitInput{OperationID: "op-12345678901234567890123456789012", Timeout: "10s"})
	if err != nil {
		t.Fatalf("OperationWait error: %v", err)
	}

	_, rcptRes, err := a.ReceiptShow(ctx, nil, ReceiptShowInput{ReceiptID: "rcpt-12345678901234567890123456789012"})
	if err != nil {
		t.Fatalf("ReceiptShow error: %v", err)
	}
	if rcptRes.Receipt.ReceiptID != "rcpt-12345678901234567890123456789012" {
		t.Errorf("Unexpected receipt result: %+v", rcptRes)
	}
}

func TestHTTPTransportAndAuth(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mcpadapter-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	authDir := filepath.Join(tempDir, "auth")
	if err := os.MkdirAll(authDir, 0700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	// Populate the mock token file
	agentToken := strings.Repeat("a", 64)
	tokenPath := filepath.Join(authDir, "agent-mcp.token")
	if err := os.WriteFile(tokenPath, []byte(agentToken+"\n"), 0600); err != nil {
		t.Fatalf("failed to write agent token: %v", err)
	}

	// Loopback validation check
	if err := validateLoopbackAddress("127.0.0.1:0"); err != nil {
		t.Errorf("Expected 127.0.0.1:0 to be a valid loopback address: %v", err)
	}
	if err := validateLoopbackAddress("8.8.8.8:80"); err == nil {
		t.Errorf("Expected 8.8.8.8:80 to be rejected as loopback")
	}

	// Start streamable HTTP server
	// We bind to a loopback address using listener to find a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		Run(tempDir, addr, io.Discard, io.Discard)
	}()

	// Wait for server to boot
	time.Sleep(200 * time.Millisecond)

	// Test 1: Unauthenticated request
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/sse", nil)
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected HTTP 401 for unauthenticated request, got %d", resp.StatusCode)
		}
	}

	// Test 2: Request with Origin header (forbidden)
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/sse", nil)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+agentToken)
	req.Header.Set("Origin", "http://malicious-site.com")
	resp, err = http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Expected HTTP 403 for request with Origin header, got %d", resp.StatusCode)
		}
	}

	// Test 3: Request with valid token
	body := `{"jsonrpc":"2.0","method":"initialize","id":1,"params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/", strings.NewReader(body))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+agentToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed request with valid token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected HTTP 200 for valid token, got %d", resp.StatusCode)
	}

	// Test 4: Run HTTP with a directory that exists but has no auth token file
	emptyDir := t.TempDir()
	if code := Run(emptyDir, "127.0.0.1:0", io.Discard, io.Discard); code != 2 {
		t.Errorf("Expected exit code 2 when token file is missing, got %d", code)
	}
}

func TestMutationParamsValidation(t *testing.T) {
	if err := validateMutationParams("bad-id", "reason", "key"); err == nil {
		t.Error("expected error for invalid target ID")
	}
	if err := validateMutationParams("c4a523d4-6b99-4d62-a5e2-4752c0f20001", "", "key"); err == nil {
		t.Error("expected error for empty reason")
	}
	if err := validateMutationParams("c4a523d4-6b99-4d62-a5e2-4752c0f20001", strings.Repeat("a", 1025), "key"); err == nil {
		t.Error("expected error for reason too long")
	}
	if err := validateMutationParams("c4a523d4-6b99-4d62-a5e2-4752c0f20001", "reason", ""); err == nil {
		t.Error("expected error for empty idempotency key")
	}
	if err := validateMutationParams("c4a523d4-6b99-4d62-a5e2-4752c0f20001", "reason", strings.Repeat("k", 257)); err == nil {
		t.Error("expected error for idempotency key too long")
	}
}

func TestParseTimeoutValues(t *testing.T) {
	if d, err := parseTimeout("", false); err != nil || d != 5*time.Minute {
		t.Errorf("expected 5m for empty timeout, got %v (err: %v)", d, err)
	}
	if _, err := parseTimeout("", true); err == nil {
		t.Error("expected error for empty timeout when required")
	}
	if _, err := parseTimeout("bad-duration", false); err == nil {
		t.Error("expected error for invalid duration format")
	}
	if _, err := parseTimeout("-5s", false); err == nil {
		t.Error("expected error for negative duration")
	}
	if _, err := parseTimeout("2h", false); err == nil {
		t.Error("expected error for duration exceeding 1h")
	}
}

func TestDTOConversions(t *testing.T) {
	mObs := domain.MachineObservation{
		ID:           "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Name:         "VM-1",
		State:        domain.MachineStateRunning,
		RawState:     "Running",
		ObservedAt:   time.Unix(1000000, 0).UTC(),
		Capabilities: domain.ReadOnlyMachineCapabilities(),
		NetworkAdapters: []domain.NetworkAdapterSummary{
			{
				Name:        "Adapter-A",
				SwitchName:  "Switch-1",
				MACAddress:  "00-15-5D-00-00-01",
				IPAddresses: []string{"192.168.1.2", "192.168.1.1"},
				Status:      "Ok",
			},
		},
	}
	dto := convertToMachineDTO(mObs)
	if dto.Name != "VM-1" || len(dto.NetworkAdapters) != 1 {
		t.Errorf("conversion failed: %+v", dto)
	}
	if dto.NetworkAdapters[0].IPAddresses[0] != "192.168.1.1" {
		t.Errorf("expected IP addresses to be sorted, got %+v", dto.NetworkAdapters[0].IPAddresses)
	}

	cObs := domain.CheckpointObservation{
		ID:              "c4a523d4-6b99-4d62-a5e2-4752c0f20002",
		Name:            "Check-1",
		VMID:            "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		CreatedAt:       time.Unix(900000, 0).UTC(),
		ObservedAt:      time.Unix(1000000, 0).UTC(),
		ObservationType: domain.ObservationObserved,
	}
	cDto := convertToCheckpointDTO(cObs)
	if cDto.Name != "Check-1" {
		t.Errorf("checkpoint conversion failed: %+v", cDto)
	}
}

//nolint:gocognit,cyclop,maintidx
func TestHandlersErrorPaths(t *testing.T) {
	ctx := t.Context()
	mockObs := &MockObserver{
		doctorErr:  errors.New("doctor failed"),
		listErr:    errors.New("list failed"),
		inspectErr: errors.New("inspect failed"),
		chkListErr: errors.New("chklist failed"),
	}
	a := &Adapter{
		discoveryService: app.NewDiscoveryService(mockObs),
		recoveryService:  app.NewRecoveryService(mockObs, nil, nil, nil, nil),
	}

	// 1. Discovery/Recovery Errors
	if res, _, err := a.Doctor(ctx, nil, DoctorInput{}); err != nil || res == nil || !res.IsError {
		t.Error("expected tool error for Doctor")
	}
	if res, _, err := a.MachineList(ctx, nil, MachineListInput{}); err != nil || res == nil || !res.IsError {
		t.Error("expected tool error for MachineList")
	}
	if res, _, err := a.MachineInspect(ctx, nil, MachineInspectInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001"}); err != nil || res == nil || !res.IsError {
		t.Error("expected tool error for MachineInspect")
	}
	if res, _, err := a.CheckpointList(ctx, nil, CheckpointListInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001"}); err != nil || res == nil || !res.IsError {
		t.Error("expected tool error for CheckpointList")
	}

	// 2. Input validation failures on handlers directly
	if res, _, err := a.MachineStart(ctx, nil, MachineStartInput{}); err != nil || res == nil || !res.IsError {
		t.Error("expected validation tool error for MachineStart")
	}
	if res, _, err := a.MachineStart(ctx, nil, MachineStartInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Timeout: "bad"}); err != nil || res == nil || !res.IsError {
		t.Error("expected timeout tool error for MachineStart")
	}
	if res, _, err := a.MachineStart(ctx, nil, MachineStartInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Timeout: ""}); err != nil || res == nil || !res.IsError {
		t.Error("expected empty timeout tool error for MachineStart")
	}

	if res, _, err := a.MachineStop(ctx, nil, MachineStopInput{}); err != nil || res == nil || !res.IsError {
		t.Error("expected validation tool error for MachineStop")
	}
	if res, _, err := a.MachineStop(ctx, nil, MachineStopInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Mode: "invalid-mode", Timeout: "30s"}); err != nil || res == nil || !res.IsError {
		t.Error("expected mode tool error for MachineStop")
	}
	if res, _, err := a.MachineStop(ctx, nil, MachineStopInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Mode: "shutdown", Timeout: "bad"}); err != nil || res == nil || !res.IsError {
		t.Error("expected timeout tool error for MachineStop")
	}
	if res, _, err := a.MachineStop(ctx, nil, MachineStopInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Mode: "shutdown", Timeout: ""}); err != nil || res == nil || !res.IsError {
		t.Error("expected empty timeout tool error for MachineStop")
	}

	if res, _, err := a.CheckpointCreate(ctx, nil, CheckpointCreateInput{}); err != nil || res == nil || !res.IsError {
		t.Error("expected validation tool error for CheckpointCreate")
	}
	if res, _, err := a.CheckpointCreate(ctx, nil, CheckpointCreateInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Name: "", Timeout: "30s"}); err != nil || res == nil || !res.IsError {
		t.Error("expected name tool error for CheckpointCreate")
	}
	if res, _, err := a.CheckpointCreate(ctx, nil, CheckpointCreateInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Name: strings.Repeat("n", 257), Timeout: "30s"}); err != nil || res == nil || !res.IsError {
		t.Error("expected name too long tool error for CheckpointCreate")
	}
	if res, _, err := a.CheckpointCreate(ctx, nil, CheckpointCreateInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Name: "name", Timeout: "bad"}); err != nil || res == nil || !res.IsError {
		t.Error("expected timeout tool error for CheckpointCreate")
	}
	if res, _, err := a.CheckpointCreate(ctx, nil, CheckpointCreateInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Name: "name", Timeout: ""}); err != nil || res == nil || !res.IsError {
		t.Error("expected empty timeout tool error for CheckpointCreate")
	}

	if res, _, err := a.CheckpointRestore(ctx, nil, CheckpointRestoreInput{}); err != nil || res == nil || !res.IsError {
		t.Error("expected validation tool error for CheckpointRestore")
	}
	if res, _, err := a.CheckpointRestore(ctx, nil, CheckpointRestoreInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", CheckpointID: "bad", Timeout: "30s"}); err != nil || res == nil || !res.IsError {
		t.Error("expected checkpoint ID tool error for CheckpointRestore")
	}
	if res, _, err := a.CheckpointRestore(ctx, nil, CheckpointRestoreInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", CheckpointID: "c4a523d4-6b99-4d62-a5e2-4752c0f20002", Timeout: "bad"}); err != nil || res == nil || !res.IsError {
		t.Error("expected timeout tool error for CheckpointRestore")
	}
	if res, _, err := a.CheckpointRestore(ctx, nil, CheckpointRestoreInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", CheckpointID: "c4a523d4-6b99-4d62-a5e2-4752c0f20002", Timeout: ""}); err != nil || res == nil || !res.IsError {
		t.Error("expected empty timeout tool error for CheckpointRestore")
	}

	if res, _, err := a.OperationShow(ctx, nil, OperationShowInput{OperationID: "bad"}); err != nil || res == nil || !res.IsError {
		t.Error("expected tool error for OperationShow")
	}
	if res, _, err := a.OperationWait(ctx, nil, OperationWaitInput{OperationID: "bad"}); err != nil || res == nil || !res.IsError {
		t.Error("expected ID tool error for OperationWait")
	}
	if res, _, err := a.OperationWait(ctx, nil, OperationWaitInput{OperationID: "op-12345678901234567890123456789012", Timeout: "bad"}); err != nil || res == nil || !res.IsError {
		t.Error("expected timeout tool error for OperationWait")
	}
	if res, _, err := a.OperationWait(ctx, nil, OperationWaitInput{OperationID: "op-12345678901234567890123456789012", AfterSeq: "bad"}); err != nil || res == nil || !res.IsError {
		t.Error("expected afterSeq tool error for OperationWait")
	}
	if res, _, err := a.ReceiptShow(ctx, nil, ReceiptShowInput{ReceiptID: "bad"}); err != nil || res == nil || !res.IsError {
		t.Error("expected tool error for ReceiptShow")
	}
}

func TestSanitizedMCPToolError(t *testing.T) {
	errSecret := errors.New("secret message containing /home/user/private/key.pem and sensitive info: secret-value")
	res := mcpToolError(errSecret)
	if !res.IsError {
		t.Fatal("expected IsError to be true")
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	if strings.Contains(txt, "secret-value") {
		t.Error("secret was leaked in error message")
	}
	if strings.Contains(txt, "/home/user") {
		t.Error("host path was leaked in error message")
	}
	if len(txt) > 200 {
		t.Errorf("error message exceeds 200 characters: %d", len(txt))
	}
	if txt != "an internal daemon error occurred" {
		t.Errorf("expected generic error message, got %q", txt)
	}

	// Test category mapping: token -> authentication failed
	resAuth := mcpToolError(errors.New("failed because token is bad"))
	txtAuth := resAuth.Content[0].(*mcp.TextContent).Text
	if txtAuth != "authentication failed" {
		t.Errorf("expected 'authentication failed', got %q", txtAuth)
	}
}

//nolint:cyclop
func TestHandlersClientErrors(t *testing.T) {
	ctx := t.Context()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cl := client.New(server.URL, "token")
	a := &Adapter{client: cl}

	// Stdio/client error cases where handlers return mcpToolError
	if res, _, _ := a.MachineStart(ctx, nil, MachineStartInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Timeout: "30s"}); res == nil || !res.IsError {
		t.Error("expected tool error for MachineStart with failing client")
	}
	if res, _, _ := a.MachineStop(ctx, nil, MachineStopInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Mode: "shutdown", Timeout: "30s"}); res == nil || !res.IsError {
		t.Error("expected tool error for MachineStop with failing client")
	}
	if res, _, _ := a.CheckpointCreate(ctx, nil, CheckpointCreateInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", Name: "name", Timeout: "30s"}); res == nil || !res.IsError {
		t.Error("expected tool error for CheckpointCreate with failing client")
	}
	if res, _, _ := a.CheckpointRestore(ctx, nil, CheckpointRestoreInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reason", IdempotencyKey: "key", CheckpointID: "c4a523d4-6b99-4d62-a5e2-4752c0f20002", Timeout: "30s"}); res == nil || !res.IsError {
		t.Error("expected tool error for CheckpointRestore with failing client")
	}
	if res, _, _ := a.OperationList(ctx, nil, OperationListInput{}); res == nil || !res.IsError {
		t.Error("expected tool error for OperationList with failing client")
	}
	if res, _, _ := a.OperationShow(ctx, nil, OperationShowInput{OperationID: "op-12345678901234567890123456789012"}); res == nil || !res.IsError {
		t.Error("expected tool error for OperationShow with failing client")
	}
	if res, _, _ := a.OperationWait(ctx, nil, OperationWaitInput{OperationID: "op-12345678901234567890123456789012"}); res == nil || !res.IsError {
		t.Error("expected tool error for OperationWait with failing client")
	}
	if res, _, _ := a.ReceiptShow(ctx, nil, ReceiptShowInput{ReceiptID: "rcpt-12345678901234567890123456789012"}); res == nil || !res.IsError {
		t.Error("expected tool error for ReceiptShow with failing client")
	}
}

func TestRunErrors(t *testing.T) {
	// Test validateLoopbackAddress errors
	if err := validateLoopbackAddress("bad-address"); err == nil {
		t.Error("expected error for malformed address")
	}
	if err := validateLoopbackAddress("not-an-ip:80"); err == nil {
		t.Error("expected error for non-IP host")
	}

	// Test Run invalid address
	if code := Run("", "8.8.8.8:80", io.Discard, io.Discard); code != 2 {
		t.Errorf("expected exit code 2 for external listen address, got %d", code)
	}

	// Test Run invalid state-dir
	if code := Run("/non-existent-dir-12345", "127.0.0.1:0", io.Discard, io.Discard); code != 2 {
		t.Errorf("expected exit code 2 for invalid state-dir, got %d", code)
	}

	// Test Run busy or invalid address bind failure
	if code := Run("", "127.0.0.1:99999", io.Discard, io.Discard); code != 2 {
		t.Errorf("expected exit code 2 for invalid port bind, got %d", code)
	}
}

func TestAdapterInternalGetters(t *testing.T) {
	a := NewAdapter("/non-existent-dir")
	if ds := a.getDiscoveryService(); ds == nil {
		t.Error("expected non-nil discovery service")
	}
	if rs := a.getRecoveryService(); rs == nil {
		t.Error("expected non-nil recovery service")
	}
	if cl, err := a.getClient(); err == nil || cl != nil {
		t.Error("expected error getting client for non-existent-dir")
	}
}

func TestStdioHandshake(t *testing.T) {
	oldStdin := os.Stdin
	oldStdout := os.Stdout
	defer func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
	}()

	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdin = rIn
	os.Stdout = wOut

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	a := NewAdapter("")
	server := a.BuildServer()

	done := make(chan struct{})
	go func() {
		defer close(done)
		sigChan := make(chan os.Signal, 1)
		runStdio(ctx, server, sigChan, cancel, io.Discard)
	}()

	// Simulating EOF on stdin. StdioTransport should stop and runStdio should exit.
	wIn.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runStdio did not exit on stdin EOF")
	}

	rOut.Close()
	wOut.Close()
	rIn.Close()
}
