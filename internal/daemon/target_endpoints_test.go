package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestDaemonTargetEndpointsOperatorApprovalStrictJSONAndCanonicalEvidence(t *testing.T) {
	srv, endpoint, operatorToken, agentToken := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()
	requireTargetEndpointGuards(t, endpoint, operatorToken, agentToken)
	requireAgentTargetShow(t, endpoint, agentToken)
	clearTargetThroughApproval(t, endpoint, operatorToken)
	enrollTargetThroughApproval(t, endpoint, operatorToken)
}

func requireTargetEndpointGuards(t *testing.T, endpoint, operatorToken, agentToken string) {
	t.Helper()
	status, _ := doJSONReq(t, http.MethodPost, endpoint+"/v1/target-approvals", agentToken, daemon.TargetApprovalIssueRequest{
		Kind: "clear", Reason: "agent must not clear authority", IdempotencyKey: "agent-target-clear", ValidForMillis: 60_000,
	})
	if status != http.StatusForbidden {
		t.Fatalf("agent target approval status = %d, want 403", status)
	}
	status, _ = doJSONReq(t, http.MethodPut, endpoint+"/v1/target", agentToken, daemon.TargetMutationRequest{})
	if status != http.StatusForbidden {
		t.Fatalf("agent target mutation status = %d, want 403", status)
	}
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/target-approvals", operatorToken, daemon.TargetApprovalIssueRequest{
		Kind: "unknown", Reason: "invalid target kind", IdempotencyKey: "invalid-target-kind", ValidForMillis: 60_000,
	})
	if status != http.StatusBadRequest {
		t.Fatalf("invalid target approval kind status = %d, want 400", status)
	}

	raw := []byte(`{"kind":"clear","kind":"enroll","reason":"duplicate field","idempotency_key":"duplicate-target","valid_for_ms":60000}`)
	req, err := http.NewRequest(http.MethodPost, endpoint+"/v1/target-approvals", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+operatorToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate target approval field status = %d, want 400", resp.StatusCode)
	}
	status, _ = doJSONReq(t, http.MethodPatch, endpoint+"/v1/target", operatorToken, nil)
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("target PATCH status = %d, want 405", status)
	}
}

func requireAgentTargetShow(t *testing.T, endpoint, agentToken string) {
	t.Helper()
	status, data := doJSONReq(t, http.MethodGet, endpoint+"/v1/target", agentToken, nil)
	if status != http.StatusOK {
		t.Fatalf("agent target show status = %d body=%s", status, data)
	}
	var shown daemon.TargetResponse
	if err := json.Unmarshal(data, &shown); err != nil || shown.Target.Locator != "local:"+daemonTestVMID {
		t.Fatalf("target show = %+v, %v", shown, err)
	}
}

func clearTargetThroughApproval(t *testing.T, endpoint, operatorToken string) {
	t.Helper()
	clearIssue := daemon.TargetApprovalIssueRequest{
		Kind: "clear", Reason: "clear synthetic target authority", IdempotencyKey: "target-http-clear", ValidForMillis: 60_000,
	}
	status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/target-approvals", operatorToken, clearIssue)
	if status != http.StatusOK {
		t.Fatalf("clear approval status = %d body=%s", status, data)
	}
	var clearGrant daemon.TargetApprovalIssueResponse
	if err := json.Unmarshal(data, &clearGrant); err != nil {
		t.Fatal(err)
	}
	deadline, err := time.Parse(time.RFC3339Nano, clearGrant.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	clearRequest := daemon.TargetMutationRequest{
		Reason: clearIssue.Reason, IdempotencyKey: clearIssue.IdempotencyKey,
		ApprovalID: clearGrant.ApprovalID, Deadline: deadline.Add(time.Nanosecond).Format(time.RFC3339Nano),
	}
	malformedDeadline := clearRequest
	malformedDeadline.Deadline = "not-a-deadline"
	status, _ = doJSONReq(t, http.MethodDelete, endpoint+"/v1/target", operatorToken, malformedDeadline)
	if status != http.StatusBadRequest {
		t.Fatalf("malformed clear deadline status = %d, want 400", status)
	}
	status, _ = doJSONReq(t, http.MethodDelete, endpoint+"/v1/target", operatorToken, clearRequest)
	if status != http.StatusForbidden {
		t.Fatalf("changed clear deadline status = %d, want 403", status)
	}
	clearRequest.Deadline = clearGrant.Deadline
	status, data = doJSONReq(t, http.MethodDelete, endpoint+"/v1/target", operatorToken, clearRequest)
	if status != http.StatusOK {
		t.Fatalf("clear target status = %d body=%s", status, data)
	}
	status, _ = doJSONReq(t, http.MethodGet, endpoint+"/v1/target", operatorToken, nil)
	if status != http.StatusConflict {
		t.Fatalf("show cleared target status = %d, want 409", status)
	}
}

func enrollTargetThroughApproval(t *testing.T, endpoint, operatorToken string) {
	t.Helper()
	enrollIssue := daemon.TargetApprovalIssueRequest{
		Kind: "enroll", Aliases: []string{"private-alias"}, Reason: "enroll synthetic target authority",
		IdempotencyKey: "target-http-enroll", ValidForMillis: 60_000,
	}
	status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/target-approvals", operatorToken, enrollIssue)
	if status != http.StatusOK {
		t.Fatalf("enroll approval status = %d body=%s", status, data)
	}
	var enrollGrant daemon.TargetApprovalIssueResponse
	if err := json.Unmarshal(data, &enrollGrant); err != nil {
		t.Fatal(err)
	}
	status, data = doJSONReq(t, http.MethodPut, endpoint+"/v1/target", operatorToken, daemon.TargetMutationRequest{
		Aliases: enrollIssue.Aliases, Reason: enrollIssue.Reason, IdempotencyKey: enrollIssue.IdempotencyKey,
		ApprovalID: enrollGrant.ApprovalID, Deadline: enrollGrant.Deadline,
	})
	if status != http.StatusOK {
		t.Fatalf("enroll target status = %d body=%s", status, data)
	}
	var enrolled daemon.TargetResponse
	if err := json.Unmarshal(data, &enrolled); err != nil || enrolled.Target.Locator != "local:"+daemonTestVMID || enrolled.Receipt == nil || enrolled.Receipt.Actor == "agent:mcp-local" {
		t.Fatalf("enroll response = %+v, %v", enrolled, err)
	}
	if strings.Contains(string(data), "private-alias") {
		t.Fatalf("target mutation response leaked alias plaintext: %s", data)
	}
}
