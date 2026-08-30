package mcpadapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func handleMockMCPMutations(w http.ResponseWriter, r *http.Request, sessID, machGUID string) bool {
	switch {
	case r.URL.Path == "/v1/sessions" && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(daemon.SessionOpenResponse{
			SchemaVersion: "1",
			Session:       daemon.SessionDTO{SessionID: sessID, Target: machGUID, State: "active"},
		})
		return true
	case r.URL.Path == "/v1/sessions/"+sessID+"/write" && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(daemon.SessionWriteResponse{SchemaVersion: "1", BytesWritten: 4})
		return true
	case r.URL.Path == "/v1/sessions/"+sessID+"/control" && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(daemon.SessionControlResponse{SchemaVersion: "1", Status: "sent"})
		return true
	case r.URL.Path == "/v1/sessions/"+sessID+"/close" && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(daemon.SessionCloseResponse{
			SchemaVersion: "1",
			Session:       daemon.SessionDTO{SessionID: sessID, State: "closed"},
		})
		return true
	default:
		return false
	}
}

func handleMockMCPReads(w http.ResponseWriter, r *http.Request, sessID, machGUID string) bool {
	switch {
	case r.URL.Path == "/v1/sessions" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(daemon.SessionListResponse{
			SchemaVersion: "1",
			Sessions:      []daemon.SessionDTO{{SessionID: sessID, Target: machGUID, State: "active"}},
		})
		return true
	case r.URL.Path == "/v1/sessions/"+sessID && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(daemon.SessionOpenResponse{
			SchemaVersion: "1",
			Session:       daemon.SessionDTO{SessionID: sessID, Target: machGUID, State: "active"},
		})
		return true
	case r.URL.Path == "/v1/sessions/"+sessID+"/read" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(daemon.SessionReadResponse{
			SchemaVersion: "1",
			SessionID:     sessID,
			Chunks:        []daemon.SessionChunkDTO{{Seq: 1, Data: "prompt> "}},
			NextSeq:       1,
		})
		return true
	case r.URL.Path == "/v1/sessions/"+sessID+"/wait" && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(daemon.SessionWaitResponse{
			SchemaVersion: "1",
			SessionID:     sessID,
			Chunks:        []daemon.SessionChunkDTO{{Seq: 1, Data: "settled"}},
			Matched:       true,
		})
		return true
	default:
		return false
	}
}

func handleMockMCPSessionRoute(w http.ResponseWriter, r *http.Request, sessID, machGUID string) {
	w.Header().Set("Content-Type", "application/json")
	if handleMockMCPMutations(w, r, sessID, machGUID) {
		return
	}
	if handleMockMCPReads(w, r, sessID, machGUID) {
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func setupMCPSessionTest(t *testing.T) (*Adapter, func()) {
	tempDir := t.TempDir()
	sd, _ := statedir.Resolve(tempDir)
	_ = sd.EnsureDirs()

	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	machGUID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleMockMCPSessionRoute(w, r, sessID, machGUID)
	}))

	ep := daemon.EndpointRecord{
		SchemaVersion: daemon.SchemaVersion,
		Endpoint:      server.URL,
		PID:           os.Getpid(),
		RuntimeID:     "test-runtime",
		StartedAt:     time.Now().UTC(),
	}
	_ = daemon.WriteEndpointFile(sd.DaemonDir(), ep)

	tokenStr := createTestAgentToken(t, tempDir)

	cl := client.New(server.URL, tokenStr)
	adapter := NewAdapter(tempDir)
	adapter.client = cl

	return adapter, func() {
		server.Close()
	}
}

func testMCPSessionsMutations(ctx context.Context, t *testing.T, adapter *Adapter, sessID, machGUID string) {
	resOpen, outOpen, err := adapter.SessionOpen(ctx, nil, SessionOpenInput{
		Target:         machGUID,
		Reason:         "open session for testing",
		IdempotencyKey: "idem-mcp-open-1",
		Timeout:        "30s",
	})
	if err != nil || resOpen != nil || outOpen.Session.SessionID != sessID {
		t.Fatalf("SessionOpen failed: err=%v, res=%v, out=%v", err, resOpen, outOpen)
	}

	resWrite, outWrite, err := adapter.SessionWrite(ctx, nil, SessionWriteInput{
		SessionID:      sessID,
		Data:           "dir\r\n",
		Reason:         "run directory listing",
		IdempotencyKey: "idem-mcp-write-1",
		Timeout:        "30s",
	})
	if err != nil || resWrite != nil || outWrite.BytesWritten != 4 {
		t.Fatalf("SessionWrite failed: err=%v, res=%v, out=%v", err, resWrite, outWrite)
	}

	resCtrl, outCtrl, err := adapter.SessionControl(ctx, nil, SessionControlInput{
		SessionID:      sessID,
		Key:            "ctrl-c",
		Reason:         "cancel command",
		IdempotencyKey: "idem-mcp-ctrl-1",
		Timeout:        "30s",
	})
	if err != nil || resCtrl != nil || outCtrl.Status != "sent" {
		t.Fatalf("SessionControl failed: err=%v, res=%v, out=%v", err, resCtrl, outCtrl)
	}

	resClose, outClose, err := adapter.SessionClose(ctx, nil, SessionCloseInput{
		SessionID:      sessID,
		Reason:         "finished work",
		IdempotencyKey: "idem-mcp-close-1",
		Timeout:        "30s",
	})
	if err != nil || resClose != nil || outClose.Session.State != "closed" {
		t.Fatalf("SessionClose failed: err=%v, res=%v, out=%v", err, resClose, outClose)
	}
}

func testMCPSessionsReads(ctx context.Context, t *testing.T, adapter *Adapter, sessID, machGUID string) {
	resRead, outRead, err := adapter.SessionRead(ctx, nil, SessionReadInput{
		SessionID: sessID,
		Timeout:   "5s",
	})
	if err != nil || resRead != nil || len(outRead.Chunks) == 0 {
		t.Fatalf("SessionRead failed: err=%v, res=%v, out=%v", err, resRead, outRead)
	}

	resWait, outWait, err := adapter.SessionWait(ctx, nil, SessionWaitInput{
		SessionID: sessID,
		SettleMs:  100,
		Timeout:   "5s",
	})
	if err != nil || resWait != nil || !outWait.Matched {
		t.Fatalf("SessionWait failed: err=%v, res=%v, out=%v", err, resWait, outWait)
	}

	resList, outList, err := adapter.SessionList(ctx, nil, SessionListInput{Machine: machGUID})
	if err != nil || resList != nil || len(outList.Sessions) == 0 {
		t.Fatalf("SessionList failed: err=%v, res=%v, out=%v", err, resList, outList)
	}

	resShow, outShow, err := adapter.SessionShow(ctx, nil, SessionShowInput{SessionID: sessID})
	if err != nil || resShow != nil || outShow.Session.SessionID != sessID {
		t.Fatalf("SessionShow failed: err=%v, res=%v, out=%v", err, resShow, outShow)
	}
}

func TestMCPSessions_AllTools(t *testing.T) {
	adapter, cleanup := setupMCPSessionTest(t)
	defer cleanup()
	ctx := context.Background()

	machGUID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"

	testMCPSessionsMutations(ctx, t, adapter, sessID, machGUID)
	testMCPSessionsReads(ctx, t, adapter, sessID, machGUID)
}

func TestMCPSessions_SubSecondTimeoutsReachClientAsMilliseconds(t *testing.T) {
	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	machGUID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	captured := make(map[string]map[string]any)
	server := newSubSecondSessionCaptureServer(t, captured, sessID, machGUID)
	defer server.Close()
	adapter := &Adapter{client: client.New(server.URL, strings.Repeat("a", 64))}
	ctx := context.Background()

	approvalID := "app-mcp-session-reference"
	deadline := "2026-08-30T10:00:00.123456789Z"
	if result, _, _ := adapter.SessionOpen(ctx, nil, SessionOpenInput{Target: machGUID, Reason: "open", IdempotencyKey: "open-key", Timeout: "250ms", ApprovalID: approvalID, Deadline: deadline}); result != nil {
		t.Fatalf("sub-second open failed: %v", result)
	}
	if result, _, _ := adapter.SessionWrite(ctx, nil, SessionWriteInput{SessionID: sessID, Data: "x", Reason: "write", IdempotencyKey: "write-key", Timeout: "250ms", ApprovalID: approvalID, Deadline: deadline}); result != nil {
		t.Fatalf("sub-second write failed: %v", result)
	}
	if result, _, _ := adapter.SessionControl(ctx, nil, SessionControlInput{SessionID: sessID, Key: "ctrl-c", Reason: "control", IdempotencyKey: "control-key", Timeout: "250ms", ApprovalID: approvalID, Deadline: deadline}); result != nil {
		t.Fatalf("sub-second control failed: %v", result)
	}
	if result, _, _ := adapter.SessionWait(ctx, nil, SessionWaitInput{SessionID: sessID, Timeout: "250ms"}); result != nil {
		t.Fatalf("sub-second wait failed: %v", result)
	}
	if result, _, _ := adapter.SessionClose(ctx, nil, SessionCloseInput{SessionID: sessID, Reason: "close", IdempotencyKey: "close-key", Timeout: "250ms", ApprovalID: approvalID, Deadline: deadline}); result != nil {
		t.Fatalf("sub-second close failed: %v", result)
	}

	assertSubSecondSessionRequests(t, captured, sessID, approvalID, deadline)
	if len(captured) != 5 {
		t.Fatalf("captured %d timeout-bearing requests, want 5", len(captured))
	}
}

func newSubSecondSessionCaptureServer(t *testing.T, captured map[string]map[string]any, sessID, machGUID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode %s body: %v", r.URL.Path, err)
			}
			captured[r.URL.Path] = body
		}
		handleMockMCPSessionRoute(w, r, sessID, machGUID)
	}))
}

func assertSubSecondSessionRequests(t *testing.T, captured map[string]map[string]any, sessID, approvalID, deadline string) {
	t.Helper()
	for path, body := range captured {
		if body["timeout_ms"] != float64(250) {
			t.Errorf("%s timeout_ms = %v, want 250", path, body["timeout_ms"])
		}
		if _, exists := body["timeout_seconds"]; exists {
			t.Errorf("%s sent conflicting timeout fields: %v", path, body)
		}
		if path == "/v1/sessions/"+sessID+"/wait" {
			continue
		}
		if body["approval_id"] != approvalID || body["deadline"] != deadline {
			t.Errorf("%s approval fields = %v", path, body)
		}
	}
}

func TestMCPSessions_ApprovalReferenceRequiresExactDeadlineBeforeClientResolution(t *testing.T) {
	adapter := NewAdapter(t.TempDir())
	result, _, err := adapter.SessionOpen(context.Background(), nil, SessionOpenInput{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "missing exact deadline",
		IdempotencyKey: "missing-approval-deadline", Timeout: "30s", ApprovalID: "app-mcp-session-reference",
	})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("missing deadline result=%+v err=%v", result, err)
	}
}

func TestMCPSessions_RejectInvalidApprovalIDBeforeClientResolution(t *testing.T) {
	adapter := NewAdapter(t.TempDir())
	result, _, err := adapter.SessionOpen(context.Background(), nil, SessionOpenInput{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "reject unsafe reference",
		IdempotencyKey: "invalid-approval-reference", Timeout: "30s", ApprovalID: "../outside",
	})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("invalid approval ID result=%+v err=%v", result, err)
	}
}

func testMCPValidationParamErrors(ctx context.Context, t *testing.T, adapter *Adapter, sessID string) {
	t.Helper()
	// Invalid target
	res, _, _ := adapter.SessionOpen(ctx, nil, SessionOpenInput{
		Target:         "bad-guid",
		Reason:         "test",
		IdempotencyKey: "k",
	})
	if res == nil || !res.IsError {
		t.Errorf("expected validation error for invalid target")
	}

	// Invalid terminal parameters are rejected before client/transport resolution.
	res, _, _ = adapter.SessionOpen(ctx, nil, SessionOpenInput{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "test",
		IdempotencyKey: "invalid-terminal",
		Term:           "not a terminal type",
	})
	if res == nil || !res.IsError {
		t.Errorf("expected validation error for invalid terminal type")
	}

	// Invalid session ID on read
	res, _, _ = adapter.SessionRead(ctx, nil, SessionReadInput{SessionID: "bad-id"})
	if res == nil || !res.IsError {
		t.Errorf("expected validation error for invalid session id")
	}

	// Empty data on write
	res, _, _ = adapter.SessionWrite(ctx, nil, SessionWriteInput{
		SessionID:      sessID,
		Data:           "",
		Reason:         "test",
		IdempotencyKey: "k",
	})
	if res == nil || !res.IsError {
		t.Errorf("expected validation error for empty write data")
	}

	// Invalid control key
	res, _, _ = adapter.SessionControl(ctx, nil, SessionControlInput{
		SessionID:      sessID,
		Key:            "not-a-key",
		Reason:         "test",
		IdempotencyKey: "k",
	})
	if res == nil || !res.IsError {
		t.Errorf("expected validation error for invalid control key")
	}

	// Empty reason on close
	res, _, _ = adapter.SessionClose(ctx, nil, SessionCloseInput{
		SessionID:      sessID,
		Reason:         "",
		IdempotencyKey: "k",
	})
	if res == nil || !res.IsError {
		t.Errorf("expected validation error for empty reason on close")
	}
}

func testMCPValidationTimeoutErrors(ctx context.Context, t *testing.T, adapter *Adapter, sessID string) {
	t.Helper()
	// Invalid timeout strings
	res, _, _ := adapter.SessionOpen(ctx, nil, SessionOpenInput{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "test",
		IdempotencyKey: "k",
		Timeout:        "invalid-duration",
	})
	if res == nil || !res.IsError {
		t.Errorf("expected error on invalid timeout in SessionOpen")
	}

	res, _, _ = adapter.SessionRead(ctx, nil, SessionReadInput{
		SessionID: sessID,
		Timeout:   "invalid-duration",
	})
	if res == nil || !res.IsError {
		t.Errorf("expected error on invalid timeout in SessionRead")
	}

	res, _, _ = adapter.SessionWait(ctx, nil, SessionWaitInput{
		SessionID: sessID,
		Timeout:   "invalid-duration",
	})
	if res == nil || !res.IsError {
		t.Errorf("expected error on invalid timeout in SessionWait")
	}
}

func TestMCPSessions_ValidationErrors(t *testing.T) {
	adapter, cleanup := setupMCPSessionTest(t)
	defer cleanup()
	ctx := context.Background()
	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"

	testMCPValidationParamErrors(ctx, t, adapter, sessID)
	testMCPValidationTimeoutErrors(ctx, t, adapter, sessID)
}

func TestMCPSessionOpenRejectsInvalidTerminalBeforeClientResolution(t *testing.T) {
	adapter := NewAdapter(t.TempDir())
	res, _, err := adapter.SessionOpen(context.Background(), nil, SessionOpenInput{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "reject invalid terminal before client resolution",
		IdempotencyKey: "invalid-terminal-before-client",
		Term:           "not a terminal type",
	})
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("invalid terminal result = %+v err %v, want MCP input error", res, err)
	}
}

func testMCPMutationsError(ctx context.Context, t *testing.T, adapter *Adapter, machGUID, sessID, desc string) {
	t.Helper()
	res, _, _ := adapter.SessionOpen(ctx, nil, SessionOpenInput{Target: machGUID, Reason: "r", IdempotencyKey: "k"})
	if res == nil || !res.IsError {
		t.Errorf("expected error for %s in SessionOpen", desc)
	}
	res, _, _ = adapter.SessionWrite(ctx, nil, SessionWriteInput{SessionID: sessID, Data: "a", Reason: "r", IdempotencyKey: "k"})
	if res == nil || !res.IsError {
		t.Errorf("expected error for %s in SessionWrite", desc)
	}
	res, _, _ = adapter.SessionControl(ctx, nil, SessionControlInput{SessionID: sessID, Key: "ctrl-c", Reason: "r", IdempotencyKey: "k"})
	if res == nil || !res.IsError {
		t.Errorf("expected error for %s in SessionControl", desc)
	}
	res, _, _ = adapter.SessionClose(ctx, nil, SessionCloseInput{SessionID: sessID, Reason: "r", IdempotencyKey: "k"})
	if res == nil || !res.IsError {
		t.Errorf("expected error for %s in SessionClose", desc)
	}
}

func testMCPReadsError(ctx context.Context, t *testing.T, adapter *Adapter, sessID, desc string) {
	t.Helper()
	res, _, _ := adapter.SessionRead(ctx, nil, SessionReadInput{SessionID: sessID})
	if res == nil || !res.IsError {
		t.Errorf("expected error for %s in SessionRead", desc)
	}
	res, _, _ = adapter.SessionWait(ctx, nil, SessionWaitInput{SessionID: sessID})
	if res == nil || !res.IsError {
		t.Errorf("expected error for %s in SessionWait", desc)
	}
	res, _, _ = adapter.SessionList(ctx, nil, SessionListInput{})
	if res == nil || !res.IsError {
		t.Errorf("expected error for %s in SessionList", desc)
	}
	res, _, _ = adapter.SessionShow(ctx, nil, SessionShowInput{SessionID: sessID})
	if res == nil || !res.IsError {
		t.Errorf("expected error for %s in SessionShow", desc)
	}
}

func TestMCPSessions_ClientFailures(t *testing.T) {
	ctx := context.Background()
	machGUID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"

	// Unconfigured client
	adapterUnconfigured := NewAdapter(t.TempDir())
	testMCPMutationsError(ctx, t, adapterUnconfigured, machGUID, sessID, "unconfigured client")
	testMCPReadsError(ctx, t, adapterUnconfigured, sessID, "unconfigured client")

	// Server returns 500 error
	failingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"category": "internal_error", "message": "server failed"}})
	}))
	defer failingServer.Close()

	clFailing := client.New(failingServer.URL, "token")
	adapterFailing := NewAdapter(t.TempDir())
	adapterFailing.client = clFailing

	testMCPMutationsError(ctx, t, adapterFailing, machGUID, sessID, "server 500 error")
	testMCPReadsError(ctx, t, adapterFailing, sessID, "server 500 error")
}
