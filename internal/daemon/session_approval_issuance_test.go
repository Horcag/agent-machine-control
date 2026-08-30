package daemon_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

func issueSessionApprovalHTTP(t *testing.T, endpoint, token string, req daemon.SessionApprovalIssueRequest) daemon.SessionApprovalIssueResponse {
	t.Helper()
	status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/session-approvals", token, req)
	if status != http.StatusOK {
		t.Fatalf("issue %s approval status=%d body=%s", req.Kind, status, data)
	}
	var response daemon.SessionApprovalIssueResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("decode issuance response: %v", err)
	}
	if response.ApprovalID == "" || response.Deadline == "" || response.Receipt == nil {
		t.Fatalf("incomplete issuance response: %+v", response)
	}
	return response
}

func TestDaemonSessionApprovalIssuanceOperatorOnlyAndAllMutationKinds(t *testing.T) {
	srv, endpoint, operatorToken, agentToken, stateRoot, fakeSSH := setupTestDaemonWithSSHConfigAndContainment(t, guestssh.SanitizerConfig{}, false)
	defer func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown daemon: %v", err)
		}
	}()
	defer fakeSSH.Close()

	target := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	requireOperatorOnlySessionApprovalIssuance(t, endpoint, operatorToken, agentToken, target)
	opened := openApprovedAgentSessionHTTP(t, endpoint, operatorToken, agentToken, target)
	writeData := "PRIVATE_SESSION_WRITE_7e4c\r\n"
	exerciseApprovedSessionWriteHTTP(t, endpoint, operatorToken, agentToken, opened.Session.SessionID, writeData)
	exerciseApprovedSessionControlAndCloseHTTP(t, endpoint, operatorToken, agentToken, opened.Session.SessionID)
	requireNoSessionWriteInApprovalEvidence(t, stateRoot, writeData)
}

func requireOperatorOnlySessionApprovalIssuance(t *testing.T, endpoint, operatorToken, agentToken, target string) {
	t.Helper()
	openIssue := daemon.SessionApprovalIssueRequest{
		Kind: "session.open", Target: target, Reason: "approve exact MCP open",
		IdempotencyKey: "approval-http-open", ValidForMillis: 60_000, Cols: 80, Rows: 24, Term: "xterm-256color",
	}
	status, _ := doJSONReq(t, http.MethodPost, endpoint+"/v1/session-approvals", agentToken, openIssue)
	if status != http.StatusForbidden {
		t.Fatalf("agent issuance status=%d, want 403", status)
	}
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/session-approvals", operatorToken, map[string]any{
		"kind": "session.open", "target": target, "reason": "forged authority", "idempotency_key": "approval-http-forged",
		"valid_for_ms": 60_000, "beneficiary": "agent:other", "authorized_class": "destructive_privileged",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("forged issuance fields status=%d, want 400", status)
	}
}

func openApprovedAgentSessionHTTP(t *testing.T, endpoint, operatorToken, agentToken, target string) daemon.SessionOpenResponse {
	t.Helper()
	openIssue := daemon.SessionApprovalIssueRequest{
		Kind: "session.open", Target: target, Reason: "approve exact MCP open",
		IdempotencyKey: "approval-http-open", ValidForMillis: 60_000, Cols: 80, Rows: 24, Term: "xterm-256color",
	}
	openGrant := issueSessionApprovalHTTP(t, endpoint, operatorToken, openIssue)
	openGrantRetry := issueSessionApprovalHTTP(t, endpoint, operatorToken, openIssue)
	if openGrantRetry.ApprovalID != openGrant.ApprovalID || openGrantRetry.Deadline != openGrant.Deadline || openGrantRetry.Receipt.ReceiptID != openGrant.Receipt.ReceiptID {
		t.Fatalf("issuance retry changed immutable grant: first=%+v retry=%+v", openGrant, openGrantRetry)
	}
	status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", agentToken, daemon.SessionOpenRequest{
		Target: target, Reason: openIssue.Reason, IdempotencyKey: openIssue.IdempotencyKey,
		Cols: 80, Rows: 24, Term: "xterm-256color", ApprovalID: openGrant.ApprovalID, Deadline: openGrant.Deadline,
	})
	if status != http.StatusOK {
		t.Fatalf("approved agent open status=%d body=%s", status, data)
	}
	var opened daemon.SessionOpenResponse
	if err := json.Unmarshal(data, &opened); err != nil {
		t.Fatal(err)
	}
	return opened
}

func exerciseApprovedSessionWriteHTTP(t *testing.T, endpoint, operatorToken, agentToken, sessionID, writeData string) {
	t.Helper()
	writeIssue := daemon.SessionApprovalIssueRequest{
		Kind: "session.write", SessionID: sessionID, Data: writeData,
		Reason: "approve exact MCP write", IdempotencyKey: "approval-http-write", ValidForMillis: 60_000,
	}
	writeGrant := issueSessionApprovalHTTP(t, endpoint, operatorToken, writeIssue)
	grantJSON, _ := json.Marshal(writeGrant)
	if strings.Contains(string(grantJSON), writeData) {
		t.Fatal("issuance response leaked session write plaintext")
	}
	deadline, err := time.Parse(time.RFC3339Nano, writeGrant.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	changedDeadline := deadline.Add(time.Nanosecond).UTC().Format(time.RFC3339Nano)
	status, _ := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions/"+sessionID+"/write", agentToken, daemon.SessionWriteRequest{
		Data: writeData, Reason: writeIssue.Reason, IdempotencyKey: writeIssue.IdempotencyKey,
		ApprovalID: writeGrant.ApprovalID, Deadline: changedDeadline,
	})
	if status != http.StatusForbidden {
		t.Fatalf("changed approval deadline status=%d, want 403", status)
	}

	writeIssue.IdempotencyKey = "approval-http-write-success"
	writeGrant = issueSessionApprovalHTTP(t, endpoint, operatorToken, writeIssue)
	status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions/"+sessionID+"/write", agentToken, daemon.SessionWriteRequest{
		Data: writeData, Reason: writeIssue.Reason, IdempotencyKey: writeIssue.IdempotencyKey,
		ApprovalID: writeGrant.ApprovalID, Deadline: writeGrant.Deadline,
	})
	if status != http.StatusOK {
		t.Fatalf("approved write status=%d body=%s", status, data)
	}
}

func exerciseApprovedSessionControlAndCloseHTTP(t *testing.T, endpoint, operatorToken, agentToken, sessionID string) {
	t.Helper()
	controlIssue := daemon.SessionApprovalIssueRequest{
		Kind: "session.control", SessionID: sessionID, Key: "ctrl-c",
		Reason: "approve exact MCP control", IdempotencyKey: "approval-http-control", ValidForMillis: 60_000,
	}
	controlGrant := issueSessionApprovalHTTP(t, endpoint, operatorToken, controlIssue)
	status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions/"+sessionID+"/control", agentToken, daemon.SessionControlRequest{
		Key: controlIssue.Key, Reason: controlIssue.Reason, IdempotencyKey: controlIssue.IdempotencyKey,
		ApprovalID: controlGrant.ApprovalID, Deadline: controlGrant.Deadline,
	})
	if status != http.StatusOK {
		t.Fatalf("approved control status=%d body=%s", status, data)
	}

	closeIssue := daemon.SessionApprovalIssueRequest{
		Kind: "session.close", SessionID: sessionID, Force: true,
		Reason: "approve exact MCP close", IdempotencyKey: "approval-http-close", ValidForMillis: 60_000,
	}
	closeGrant := issueSessionApprovalHTTP(t, endpoint, operatorToken, closeIssue)
	status, data = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions/"+sessionID+"/close", agentToken, daemon.SessionCloseRequest{
		Reason: closeIssue.Reason, IdempotencyKey: closeIssue.IdempotencyKey, Force: true,
		ApprovalID: closeGrant.ApprovalID, Deadline: closeGrant.Deadline,
	})
	if status != http.StatusOK {
		t.Fatalf("approved close status=%d body=%s", status, data)
	}
}

func requireNoSessionWriteInApprovalEvidence(t *testing.T, stateRoot, writeData string) {
	t.Helper()
	for _, relative := range []string{"approvals", "audit", "receipts"} {
		requireRootExcludesText(t, filepath.Join(stateRoot, relative), writeData)
	}
}

func requireRootExcludesText(t *testing.T, rootPath, forbiddenText string) {
	t.Helper()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open evidence root %s: %v", rootPath, err)
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		t.Fatalf("read evidence root %s: %v", rootPath, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		contents, err := root.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read evidence file %s: %v", entry.Name(), err)
		}
		if strings.Contains(string(contents), forbiddenText) {
			t.Errorf("%s leaked session write plaintext", rootPath)
		}
	}
}
