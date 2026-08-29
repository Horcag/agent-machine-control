package daemon_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestServer_HealthAndStrictValidation(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// 1. GET /v1/health
	req, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Health request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for health, got %d", resp.StatusCode)
	}
	var health daemon.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health failed: %v", err)
	}
	if health.Status != "ok" || health.SchemaVersion != "1" {
		t.Errorf("unexpected health payload: %+v", health)
	}

	// 2. Reject unknown fields
	badJSON := `{"kind":"machine.stop","target":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","reason":"test","parameters":{"mode":"turn-off"},"idempotency_key":"key-bad","unknown_field":"forbidden"}`
	req, _ = http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader([]byte(badJSON)))
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for unknown field, got %d", resp.StatusCode)
	}

	// 3. Reject trailing JSON
	trailingJSON := `{"kind":"machine.stop","target":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","reason":"test","parameters":{"mode":"turn-off"},"idempotency_key":"key-trail"} trailing`
	req, _ = http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader([]byte(trailingJSON)))
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for trailing data, got %d", resp.StatusCode)
	}
}

func TestServer_OperationsLifecycleAndEvents(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// Create operation
	body := daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "start test machine",
		IdempotencyKey: "key-start-lifecycle",
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+opToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateOperation failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 202/200, got %d", resp.StatusCode)
	}

	var opDTO daemon.OperationDTO
	if err := json.NewDecoder(resp.Body).Decode(&opDTO); err != nil {
		t.Fatalf("decode opDTO failed: %v", err)
	}
	opID := opDTO.OperationID

	// Subscribe to events via SSE
	eventsReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/operations/%s/events", endpoint, opID), nil)
	eventsReq.Header.Set("Authorization", "Bearer "+opToken)

	eventsResp, err := http.DefaultClient.Do(eventsReq)
	if err != nil {
		t.Fatalf("SSE events request failed: %v", err)
	}
	defer eventsResp.Body.Close()

	if eventsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for events stream, got %d", eventsResp.StatusCode)
	}

	// Read SSE stream until terminal event
	if !readSSEUntilCompleted(t, eventsResp.Body) {
		t.Errorf("expected terminal completed event in SSE stream")
	}

	// Get operation details
	getReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/operations/%s", endpoint, opID), nil)
	getReq.Header.Set("Authorization", "Bearer "+opToken)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GetOperation failed: %v", err)
	}
	defer getResp.Body.Close()

	var finalDTO daemon.OperationDTO
	_ = json.NewDecoder(getResp.Body).Decode(&finalDTO)
	if finalDTO.State != "completed" {
		t.Errorf("expected completed state, got %s", finalDTO.State)
	}
	if finalDTO.ReceiptID == "" {
		t.Errorf("expected receipt ID to be present")
	}
}

func TestServer_AuditAndReceiptPermissions(t *testing.T) {
	srv, endpoint, opToken, agToken := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// 1. Audit tail forbidden for agent token
	auditReq, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/audit", nil)
	auditReq.Header.Set("Authorization", "Bearer "+agToken)
	resp, err := http.DefaultClient.Do(auditReq)
	if err != nil {
		t.Fatalf("Audit request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for agent audit query, got %d", resp.StatusCode)
	}

	// 2. Audit tail allowed for operator token
	auditReq, _ = http.NewRequest(http.MethodGet, endpoint+"/v1/audit", nil)
	auditReq.Header.Set("Authorization", "Bearer "+opToken)
	resp, err = http.DefaultClient.Do(auditReq)
	if err != nil {
		t.Fatalf("Audit request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for operator audit query, got %d", resp.StatusCode)
	}

	// 3. Stop daemon forbidden for agent token
	stopReq, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/daemon/stop", nil)
	stopReq.Header.Set("Authorization", "Bearer "+agToken)
	resp, err = http.DefaultClient.Do(stopReq)
	if err != nil {
		t.Fatalf("Stop request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for agent stop query, got %d", resp.StatusCode)
	}

	// 4. Stop daemon allowed for operator token
	stopReq, _ = http.NewRequest(http.MethodPost, endpoint+"/v1/daemon/stop", nil)
	stopReq.Header.Set("Authorization", "Bearer "+opToken)
	resp, err = http.DefaultClient.Do(stopReq)
	if err != nil {
		t.Fatalf("Stop request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for operator stop query, got %d", resp.StatusCode)
	}
}

func TestServer_OperationsListAndCancel_AndReceipts(t *testing.T) {
	srv, endpoint, opToken, agToken := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// 1. Create operation
	body := daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "receipt and list test",
		IdempotencyKey: "key-list-test-1",
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+opToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateOperation failed: %v", err)
	}
	defer resp.Body.Close()

	var opDTO daemon.OperationDTO
	_ = json.NewDecoder(resp.Body).Decode(&opDTO)

	// 2. GET /v1/operations
	listReq, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/operations?state=pending&limit=10", nil)
	listReq.Header.Set("Authorization", "Bearer "+opToken)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("List operations failed: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for list operations, got %d", listResp.StatusCode)
	}

	// 3. GET /v1/receipts (list receipts)
	rcptListReq, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/receipts", nil)
	rcptListReq.Header.Set("Authorization", "Bearer "+opToken)
	rcptListResp, err := http.DefaultClient.Do(rcptListReq)
	if err != nil {
		t.Fatalf("List receipts failed: %v", err)
	}
	defer rcptListResp.Body.Close()
	if rcptListResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for list receipts, got %d", rcptListResp.StatusCode)
	}

	// 4. GET /v1/receipts/{non-existent} -> 404
	badRcptReq, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/receipts/rcpt-00000000000000000000000000000000", nil)
	badRcptReq.Header.Set("Authorization", "Bearer "+opToken)
	badRcptResp, err := http.DefaultClient.Do(badRcptReq)
	if err != nil {
		t.Fatalf("Get bad receipt failed: %v", err)
	}
	defer badRcptResp.Body.Close()
	if badRcptResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 Not Found for non-existent receipt, got %d", badRcptResp.StatusCode)
	}

	// 5. POST /v1/operations/{id}/cancel
	cancelBody := daemon.CancelOperationRequest{Reason: "test cancel handler"}
	cData, _ := json.Marshal(cancelBody)
	cancelReq, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v1/operations/%s/cancel", endpoint, opDTO.OperationID), bytes.NewReader(cData))
	cancelReq.Header.Set("Authorization", "Bearer "+opToken)
	cancelResp, err := http.DefaultClient.Do(cancelReq)
	if err != nil {
		t.Fatalf("Cancel request failed: %v", err)
	}
	defer cancelResp.Body.Close()

	// 6. Cross-actor cancel (agent trying to cancel operator's operation -> 404)
	agentCancelReq, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v1/operations/%s/cancel", endpoint, opDTO.OperationID), bytes.NewReader(cData))
	agentCancelReq.Header.Set("Authorization", "Bearer "+agToken)
	agentCancelResp, err := http.DefaultClient.Do(agentCancelReq)
	if err != nil {
		t.Fatalf("Agent cancel request failed: %v", err)
	}
	defer agentCancelResp.Body.Close()
	if agentCancelResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for cross-actor cancel, got %d", agentCancelResp.StatusCode)
	}
}

func TestServer_HandlerEdgeCases(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// 1. Unknown endpoint -> 404
	req, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/unknown-endpoint", nil)
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown endpoint, got %d", resp.StatusCode)
	}

	// 2. Method not allowed for /v1/operations/{id}
	req, _ = http.NewRequest(http.MethodPut, endpoint+"/v1/operations/op-123", nil)
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", resp.StatusCode)
	}

	// 3. Bad operation path /v1/operations/op-123/extra/parts -> 404
	req, _ = http.NewRequest(http.MethodGet, endpoint+"/v1/operations/op-123/extra/parts", nil)
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for bad operation subpath, got %d", resp.StatusCode)
	}
}

func TestServer_MoreOperationsAndAuthPaths(t *testing.T) {
	srv, endpoint, opToken, agToken := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// 1. Create turn-off machine stop operation
	body := daemon.CreateOperationRequest{
		Kind:           "machine.stop",
		Target:         targetID,
		Reason:         "turn off test",
		IdempotencyKey: "key-turnoff-1",
		Parameters:     map[string]any{"mode": "turn-off"},
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// 2. Cross-actor operation query (agent querying operator's operation -> 404)
	var opDTO daemon.OperationDTO
	_ = json.NewDecoder(resp.Body).Decode(&opDTO)

	getReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/operations/%s", endpoint, opDTO.OperationID), nil)
	getReq.Header.Set("Authorization", "Bearer "+agToken)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("agent get operation failed: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for agent querying operator op, got %d", getResp.StatusCode)
	}

	// 3. Cross-actor events subscribe -> 404
	evReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/operations/%s/events", endpoint, opDTO.OperationID), nil)
	evReq.Header.Set("Authorization", "Bearer "+agToken)
	evResp, err := http.DefaultClient.Do(evReq)
	if err != nil {
		t.Fatalf("agent events stream failed: %v", err)
	}
	defer evResp.Body.Close()
	if evResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for agent subscribing to operator op, got %d", evResp.StatusCode)
	}
}

func TestServer_AuditAndStopAuthPermissions(t *testing.T) {
	srv, endpoint, _, agToken := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// 1. Agent calling GET /v1/audit -> 403 Forbidden
	req, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/audit", nil)
	req.Header.Set("Authorization", "Bearer "+agToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for agent audit read, got %d", resp.StatusCode)
	}

	// 2. Agent calling POST /v1/daemon/stop -> 403 Forbidden
	stopReq, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/daemon/stop", nil)
	stopReq.Header.Set("Authorization", "Bearer "+agToken)
	stopResp, err := http.DefaultClient.Do(stopReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer stopResp.Body.Close()
	if stopResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for agent daemon stop, got %d", stopResp.StatusCode)
	}
}

func TestServer_RequestValidationErrors(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// 1. Unknown fields in body -> 400
	req, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader([]byte(`{"kind":"machine.stop","target":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","unknown_field":true}`)))
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown fields, got %d", resp.StatusCode)
	}

	// 2. Trailing data in body -> 400
	req, _ = http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader([]byte(`{"kind":"machine.stop","target":"c4a523d4-6b99-4d62-a5e2-4752c0f20001"} extra`)))
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for trailing data, got %d", resp.StatusCode)
	}
}

func TestServer_PIDWait_AndReceiptDetails(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// PID check
	if srv.PID() <= 0 {
		t.Errorf("expected valid PID, got %d", srv.PID())
	}

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// 1. Submit machine start operation
	body := daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "receipt details test",
		IdempotencyKey: "test-rcpt-details",
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var opDTO daemon.OperationDTO
	_ = json.NewDecoder(resp.Body).Decode(&opDTO)

	// Wait for operation to complete
	cl := client.New(srv.Endpoint(), opToken)
	finalDTO, err := cl.WaitOperation(context.Background(), opDTO.OperationID, 0, 0)
	if err != nil {
		t.Fatalf("WaitOperation failed: %v", err)
	}
	if finalDTO.ReceiptID != "" {
		// GET /v1/receipts/{receipt_id}
		rcptReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/receipts/%s", endpoint, finalDTO.ReceiptID), nil)
		rcptReq.Header.Set("Authorization", "Bearer "+opToken)
		rcptResp, err := http.DefaultClient.Do(rcptReq)
		if err != nil {
			t.Fatalf("get receipt failed: %v", err)
		}
		defer rcptResp.Body.Close()
		if rcptResp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK for valid receipt, got %d", rcptResp.StatusCode)
		}
	}

	// Cancel on terminal -> 409 Conflict
	cancelBody, _ := json.Marshal(daemon.CancelOperationRequest{Reason: "cancel on terminal"})
	cReq, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v1/operations/%s/cancel", endpoint, opDTO.OperationID), bytes.NewReader(cancelBody))
	cReq.Header.Set("Authorization", "Bearer "+opToken)
	cResp, err := http.DefaultClient.Do(cReq)
	if err != nil {
		t.Fatalf("cancel terminal failed: %v", err)
	}
	defer cResp.Body.Close()
	if cResp.StatusCode != http.StatusConflict && cResp.StatusCode != http.StatusOK {
		t.Errorf("expected 409 Conflict for terminal cancel, got %d", cResp.StatusCode)
	}

	// Cancel non-existent -> 404
	badCReq, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations/op-00000000000000000000000000000000/cancel", bytes.NewReader(cancelBody))
	badCReq.Header.Set("Authorization", "Bearer "+opToken)
	badCResp, err := http.DefaultClient.Do(badCReq)
	if err != nil {
		t.Fatalf("cancel bad op failed: %v", err)
	}
	defer badCResp.Body.Close()
	if badCResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for cancel nonexistent, got %d", badCResp.StatusCode)
	}

	// GET /v1/operations/{id}/events with Last-Event-ID
	evReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/operations/%s/events", endpoint, opDTO.OperationID), nil)
	evReq.Header.Set("Authorization", "Bearer "+opToken)
	evReq.Header.Set("Last-Event-ID", "1")
	evResp, err := http.DefaultClient.Do(evReq)
	if err != nil {
		t.Fatalf("events request with Last-Event-ID failed: %v", err)
	}
	defer evResp.Body.Close()
	if evResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for events stream, got %d", evResp.StatusCode)
	}
}

func TestServer_GetOperationSuccess(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	body := daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "get op test",
		IdempotencyKey: "key-get-op-1",
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+opToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer resp.Body.Close()

	var opDTO daemon.OperationDTO
	_ = json.NewDecoder(resp.Body).Decode(&opDTO)

	// GET /v1/operations/{id}
	getReq, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/operations/%s", endpoint, opDTO.OperationID), nil)
	getReq.Header.Set("Authorization", "Bearer "+opToken)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("get op failed: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for get op, got %d", getResp.StatusCode)
	}
}

func readSSEUntilCompleted(t *testing.T, r io.Reader) bool {
	t.Helper()
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("read SSE failed: %v", err)
		}
		if dataPayload, ok := strings.CutPrefix(line, "data: "); ok {
			if strings.Contains(dataPayload, `"completed"`) {
				return true
			}
		}
	}
	return false
}

func TestDaemon_IdempotentDenialWithRegeneratedDeadline(t *testing.T) {
	srv, endpoint, adminToken, opToken := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	cl := client.New(endpoint, opToken)
	ctx := context.Background()

	deadline1 := time.Now().Add(10 * time.Minute)
	req1 := daemon.CreateOperationRequest{
		Kind:           "machine.stop",
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "test",
		Parameters:     map[string]any{"mode": "turn-off"},
		IdempotencyKey: "key-regen-deadline",
		Deadline:       &deadline1,
	}

	op1, err := cl.CreateOperation(ctx, req1)
	if err != nil {
		t.Fatalf("req1 failed: %v", err)
	}

	op1Final, err := cl.WaitOperation(ctx, op1.OperationID, 5*time.Second, 0)
	if err != nil {
		t.Fatalf("wait req1 failed: %v", err)
	}

	if op1Final.ErrorCategory != "approval_required" {
		t.Fatalf("expected approval_required denial, got %v %v", op1Final.ErrorCategory, op1Final.ErrorMessage)
	}

	deadline2 := time.Now().Add(20 * time.Minute)
	req2 := daemon.CreateOperationRequest{
		Kind:           "machine.stop",
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "test",
		Parameters:     map[string]any{"mode": "turn-off"},
		IdempotencyKey: "key-regen-deadline",
		Deadline:       &deadline2,
	}

	op2, err := cl.CreateOperation(ctx, req2)
	if err != nil {
		t.Fatalf("req2 failed: %v", err)
	}

	if op2.OperationID != op1Final.OperationID {
		t.Fatalf("expected same operation ID, got %s vs %s", op1Final.OperationID, op2.OperationID)
	}
	if op2.ReceiptID != op1Final.ReceiptID {
		t.Fatalf("expected same receipt ID, got %s vs %s", op1Final.ReceiptID, op2.ReceiptID)
	}
	if op2.ErrorCategory != op1Final.ErrorCategory {
		t.Fatalf("expected same error category, got %s vs %s", op1Final.ErrorCategory, op2.ErrorCategory)
	}

	// Verify exact audit events (1 denial total for this key)
	req3, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/audit", nil)
	req3.Header.Set("Authorization", "Bearer "+adminToken)
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("audit req failed: %v", err)
	}
	defer resp3.Body.Close()
	var auditResp daemon.AuditListResponse
	if err := json.NewDecoder(resp3.Body).Decode(&auditResp); err != nil {
		t.Fatalf("failed to decode audit response: %v", err)
	}

	denials := countDenials(auditResp.Events, op1Final.ReceiptID, op1Final.ErrorCategory, op1Final.ErrorMessage)
	if denials != 1 {
		t.Fatalf("expected exactly 1 denial audit event for receipt ID %s, got %d, events: %+v", op1Final.ReceiptID, denials, auditResp.Events)
	}
}

func countDenials(events []audit.Event, receiptID, category, message string) int {
	denials := 0
	for _, e := range events {
		if e.ReceiptID == receiptID && string(e.OutcomeStatus) == "denied" {
			if e.ErrorCategory == category && e.ErrorMessage == message {
				denials++
			}
		}
	}
	return denials
}
