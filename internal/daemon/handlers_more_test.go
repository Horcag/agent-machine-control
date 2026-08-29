package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestServer_MoreHandlerBranches(t *testing.T) {
	srv, endpoint, opToken, agToken := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// 1. Health with agent token
	hReq, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/health", nil)
	hReq.Header.Set("Authorization", "Bearer "+agToken)
	hResp, err := http.DefaultClient.Do(hReq)
	if err != nil {
		t.Fatalf("health failed: %v", err)
	}
	defer hResp.Body.Close()
	if hResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for health with agent token, got %d", hResp.StatusCode)
	}

	// 2. Audit tail with limit query param
	auditReq, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/audit?limit=5", nil)
	auditReq.Header.Set("Authorization", "Bearer "+opToken)
	auditResp, err := http.DefaultClient.Do(auditReq)
	if err != nil {
		t.Fatalf("audit tail failed: %v", err)
	}
	defer auditResp.Body.Close()
	if auditResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for audit tail, got %d", auditResp.StatusCode)
	}

	// 3. Agent creates operation and queries own receipt
	agOpBody := daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "agent start op",
		IdempotencyKey: "key-agent-op-1",
	}
	agData, _ := json.Marshal(agOpBody)
	agReq, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(agData))
	agReq.Header.Set("Authorization", "Bearer "+agToken)
	agResp, err := http.DefaultClient.Do(agReq)
	if err != nil {
		t.Fatalf("agent create op failed: %v", err)
	}
	defer agResp.Body.Close()

	var agOpDTO daemon.OperationDTO
	_ = json.NewDecoder(agResp.Body).Decode(&agOpDTO)

	cl := client.New(endpoint, agToken)
	finalDTO, err := cl.WaitOperation(context.Background(), agOpDTO.OperationID, 0, 0)
	if err != nil {
		t.Fatalf("wait operation failed: %v", err)
	}
	if finalDTO.ReceiptID == "" {
		t.Fatalf("expected non-empty ReceiptID")
	}

	// Agent queries own receipt -> 200 OK
	ownRcptReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/receipts/%s", endpoint, finalDTO.ReceiptID), nil)
	ownRcptReq.Header.Set("Authorization", "Bearer "+agToken)
	ownRcptResp, err := http.DefaultClient.Do(ownRcptReq)
	if err != nil {
		t.Fatalf("agent query own receipt failed: %v", err)
	}
	defer ownRcptResp.Body.Close()
	if ownRcptResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for agent querying own receipt, got %d", ownRcptResp.StatusCode)
	}

	// 4. Agent lists receipts (tests actorFilter branch in handleListReceipts)
	agListReq, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/receipts?limit=5", nil)
	agListReq.Header.Set("Authorization", "Bearer "+agToken)
	agListResp, err := http.DefaultClient.Do(agListReq)
	if err != nil {
		t.Fatalf("agent list receipts failed: %v", err)
	}
	defer agListResp.Body.Close()
	if agListResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for agent listing receipts, got %d", agListResp.StatusCode)
	}
}

func TestServer_EventsQueryBranches(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	cl := client.New(endpoint, opToken)
	opDTO, err := cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "events query op",
		IdempotencyKey: "key-events-query-1",
	})
	if err != nil {
		t.Fatalf("CreateOperation failed: %v", err)
	}

	// 1. Events stream with invalid Last-Event-ID header (non-integer)
	badEvReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/operations/%s/events", endpoint, opDTO.OperationID), nil)
	badEvReq.Header.Set("Authorization", "Bearer "+opToken)
	badEvReq.Header.Set("Last-Event-ID", "not-a-number")
	badEvResp, err := http.DefaultClient.Do(badEvReq)
	if err != nil {
		t.Fatalf("events request failed: %v", err)
	}
	defer badEvResp.Body.Close()
	if badEvResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for events stream with bad Last-Event-ID, got %d", badEvResp.StatusCode)
	}

	// 2. Events stream with valid after_seq query param
	afterSeqReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/operations/%s/events?after_seq=1", endpoint, opDTO.OperationID), nil)
	afterSeqReq.Header.Set("Authorization", "Bearer "+opToken)
	afterSeqResp, err := http.DefaultClient.Do(afterSeqReq)
	if err != nil {
		t.Fatalf("after_seq events request failed: %v", err)
	}
	defer afterSeqResp.Body.Close()
	if afterSeqResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for events stream with after_seq, got %d", afterSeqResp.StatusCode)
	}

	// 3. Events stream with client context cancellation
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	cancelReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/operations/%s/events", endpoint, opDTO.OperationID), nil)
	cancelReq.Header.Set("Authorization", "Bearer "+opToken)
	cancelResp, err := http.DefaultClient.Do(cancelReq)
	if err == nil {
		defer cancelResp.Body.Close()
	}
}

func TestServer_WaitAndListenError(t *testing.T) {
	dir := t.TempDir()
	srv, err := daemon.NewServer(daemon.Config{
		StateDir:   dir,
		ListenAddr: "127.0.0.1:0",
		Backend:    &mockDaemonBackend{},
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	// TriggerShutdown and Wait
	go func() {
		time.Sleep(20 * time.Millisecond)
		srv.TriggerShutdown()
	}()
	srv.Wait()

	// Invalid listen address
	badSrv, err := daemon.NewServer(daemon.Config{
		StateDir:   t.TempDir(),
		ListenAddr: "999.999.999.999:80",
		Backend:    &mockDaemonBackend{},
	})
	if err == nil {
		_ = badSrv.Start()
	}
}

func TestServer_CheckpointOperations(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// 1. Submit checkpoint.create operation
	chkCreateBody := daemon.CreateOperationRequest{
		Kind:           "checkpoint.create",
		Target:         targetID,
		Reason:         "chk create test",
		IdempotencyKey: "key-chk-create-1",
		Parameters:     map[string]any{"name": "snap-test"},
	}
	data, _ := json.Marshal(chkCreateBody)
	req, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer resp.Body.Close()

	// 2. Submit checkpoint.restore operation
	chkRestoreBody := daemon.CreateOperationRequest{
		Kind:           "checkpoint.restore",
		Target:         targetID,
		Reason:         "chk restore test",
		IdempotencyKey: "key-chk-restore-1",
		Parameters:     map[string]any{"checkpoint_id": "e4a523d4-6b99-4d62-a5e2-4752c0f20001"},
	}
	data, _ = json.Marshal(chkRestoreBody)
	req, _ = http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	defer resp2.Body.Close()
}

func TestServer_MethodNotAllowedAndInvalidIDs(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	testCases := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodPost, "/v1/audit", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/receipts", http.StatusMethodNotAllowed},
		{http.MethodGet, "/v1/receipts/bad-id", http.StatusBadRequest},
		{http.MethodGet, "/v1/receipts/rcpt-00000000000000000000000000000000", http.StatusNotFound},
		{http.MethodGet, "/v1/operations/bad-id", http.StatusBadRequest},
		{http.MethodPost, "/v1/operations/bad-id/cancel", http.StatusBadRequest},
		{http.MethodDelete, "/v1/operations/op-00000000000000000000000000000001", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/v1/receipts/rcpt-00000000000000000000000000000001", http.StatusMethodNotAllowed},
		{http.MethodGet, "/v1/operations/op-00000000000000000000000000000001/unknown", http.StatusNotFound},
		{http.MethodPut, "/v1/operations", http.StatusMethodNotAllowed},
	}

	for _, tc := range testCases {
		req, _ := http.NewRequest(tc.method, endpoint+tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+opToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s failed: %v", tc.method, tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Errorf("%s %s: expected status %d, got %d", tc.method, tc.path, tc.status, resp.StatusCode)
		}
	}
}

func TestServer_OperationRetryAndConflict(t *testing.T) {
	srv, endpoint, opToken, agToken := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// 1. Operation request with trailing data -> 400
	trailingReq, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", strings.NewReader(`{"kind":"machine.start","target":"c4a523d4-6b99-4d62-a5e2-4752c0f20001"} extra`))
	trailingReq.Header.Set("Authorization", "Bearer "+opToken)
	trailingReq.Header.Set("Content-Type", "application/json")
	trailingResp, err := http.DefaultClient.Do(trailingReq)
	if err != nil {
		t.Fatalf("trailing req failed: %v", err)
	}
	defer trailingResp.Body.Close()
	if trailingResp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for trailing data in op body, got %d", trailingResp.StatusCode)
	}

	// 2. Existing operation retry with same key returns 200 OK
	explicitDeadline := time.Date(2026, 8, 29, 12, 30, 0, 0, time.UTC)
	sameKeyBody := daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "same key op",
		IdempotencyKey: "key-same-op-1",
		Deadline:       &explicitDeadline,
	}
	sameKeyData, _ := json.Marshal(sameKeyBody)
	reqA, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(sameKeyData))
	reqA.Header.Set("Authorization", "Bearer "+opToken)
	reqA.Header.Set("Content-Type", "application/json")
	respA, _ := http.DefaultClient.Do(reqA)
	if respA != nil {
		_ = respA.Body.Close()
	}

	reqB, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(sameKeyData))
	reqB.Header.Set("Authorization", "Bearer "+opToken)
	reqB.Header.Set("Content-Type", "application/json")
	respB, err := http.DefaultClient.Do(reqB)
	if err != nil {
		t.Fatalf("reqB failed: %v", err)
	}
	defer respB.Body.Close()
	if respB.StatusCode != http.StatusOK && respB.StatusCode != http.StatusAccepted {
		t.Errorf("expected 200/202 for identical retry, got %d", respB.StatusCode)
	}

	// 3. Cross-actor collision on same idempotency key -> 409 Conflict
	reqC, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(sameKeyData))
	reqC.Header.Set("Authorization", "Bearer "+agToken)
	reqC.Header.Set("Content-Type", "application/json")
	respC, err := http.DefaultClient.Do(reqC)
	if err != nil {
		t.Fatalf("reqC failed: %v", err)
	}
	defer respC.Body.Close()
	if respC.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 Conflict for cross-actor idempotency collision, got %d", respC.StatusCode)
	}
}
