package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func handleMockGet(w http.ResponseWriter, r *http.Request, sessID string) bool {
	switch r.URL.Path {
	case "/v1/sessions":
		_ = json.NewEncoder(w).Encode(daemon.SessionListResponse{
			SchemaVersion: "1",
			Sessions: []daemon.SessionDTO{
				{SessionID: sessID, State: "active"},
			},
		})
		return true
	case "/v1/sessions/" + sessID:
		_ = json.NewEncoder(w).Encode(daemon.SessionOpenResponse{
			SchemaVersion: "1",
			Session: daemon.SessionDTO{
				SessionID: sessID,
				State:     "active",
			},
		})
		return true
	case "/v1/sessions/" + sessID + "/read":
		_ = json.NewEncoder(w).Encode(daemon.SessionReadResponse{
			SchemaVersion: "1",
			SessionID:     sessID,
			Chunks: []daemon.SessionChunkDTO{
				{Seq: 1, Data: "prompt> "},
			},
			NextSeq: 1,
		})
		return true
	default:
		return false
	}
}

func handleMockPost(w http.ResponseWriter, r *http.Request, sessID string) bool {
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
			BytesWritten:  5,
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

func handleMockSessionRoute(w http.ResponseWriter, r *http.Request, sessID string) {
	if r.Method == http.MethodGet && handleMockGet(w, r, sessID) {
		return
	}
	if r.Method == http.MethodPost && handleMockPost(w, r, sessID) {
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func setupClientMockServer(sessID string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		handleMockSessionRoute(w, r, sessID)
	}))
}

func testClientOpenReadWrite(ctx context.Context, t *testing.T, cl *client.Client, sessID string) {
	openResp, err := cl.OpenSession(ctx, daemon.SessionOpenRequest{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "test",
		IdempotencyKey: "idem-1",
	})
	if err != nil || openResp.Session.SessionID != sessID {
		t.Fatalf("OpenSession failed: %v", err)
	}

	readResp, err := cl.ReadSession(ctx, sessID, 0, 1024, 1*time.Second)
	if err != nil || len(readResp.Chunks) != 1 {
		t.Fatalf("ReadSession failed: %v", err)
	}

	readResp2, err := cl.ReadSession(ctx, sessID, 1, 512, 1*time.Second)
	if err != nil || len(readResp2.Chunks) != 1 {
		t.Fatalf("ReadSession 2 failed: %v", err)
	}

	writeResp, err := cl.WriteSession(ctx, sessID, "dir\r\n", "list", "key-w")
	if err != nil || writeResp.BytesWritten != 5 {
		t.Fatalf("WriteSession failed: %v", err)
	}
}

func testClientControlWaitAndClose(ctx context.Context, t *testing.T, cl *client.Client, sessID string) {
	ctrlResp, err := cl.SendControlKey(ctx, sessID, domain.ControlKeyCtrlC, "cancel", "key-c")
	if err != nil || ctrlResp.Status != "sent" {
		t.Fatalf("SendControlKey failed: %v", err)
	}

	waitResp, err := cl.WaitSession(ctx, sessID, daemon.SessionWaitRequest{SettleMs: 100, Regex: "prompt", TimeoutSeconds: 5})
	if err != nil || !waitResp.Matched {
		t.Fatalf("WaitSession failed: %v", err)
	}

	list, err := cl.ListSessions(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSessions failed: %v", err)
	}

	getDto, err := cl.GetSession(ctx, sessID)
	if err != nil || getDto.SessionID != sessID {
		t.Fatalf("GetSession failed: %v", err)
	}

	closeResp, err := cl.CloseSession(ctx, sessID, "done", "key-close", false)
	if err != nil || closeResp.Session.State != "closed" {
		t.Fatalf("CloseSession failed: %v", err)
	}
}

func TestClient_SessionsMethods(t *testing.T) {
	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	server := setupClientMockServer(sessID)
	defer server.Close()

	cl := client.New(server.URL, "test-token")
	ctx := context.Background()

	testClientOpenReadWrite(ctx, t, cl, sessID)
	testClientControlWaitAndClose(ctx, t, cl, sessID)
}

func TestClient_SessionApprovalIssuanceAndExactDeadlineWireFields(t *testing.T) {
	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	deadline := time.Date(2026, 8, 30, 10, 1, 2, 345, time.UTC)
	var issued daemon.SessionApprovalIssueRequest
	var written daemon.SessionWriteRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/session-approvals":
			if err := json.NewDecoder(r.Body).Decode(&issued); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(daemon.SessionApprovalIssueResponse{
				SchemaVersion: "1", ApprovalID: "app-session-0123456789abcdef0123456789abcdef",
				Deadline: deadline.Format(time.RFC3339Nano), ExpiresAt: deadline.Format(time.RFC3339Nano),
				Operation: daemon.SessionApprovalOperationDTO{Kind: issued.Kind, Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Parameters: map[string]any{}},
			})
		case "/v1/sessions/" + sessID + "/write":
			if err := json.NewDecoder(r.Body).Decode(&written); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(daemon.SessionWriteResponse{SchemaVersion: "1", BytesWritten: len(written.Data)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cl := client.New(server.URL, "test-token")
	grant, err := cl.IssueSessionApproval(context.Background(), daemon.SessionApprovalIssueRequest{
		Kind: "session.write", SessionID: sessID, Data: "exact data", Reason: "approve exact write",
		IdempotencyKey: "client-approval-issue", ValidForMillis: 30_000,
	})
	if err != nil || grant.ApprovalID == "" || issued.Kind != "session.write" {
		t.Fatalf("issue grant=%+v request=%+v err=%v", grant, issued, err)
	}
	if _, err := cl.WriteSessionWithApprovalReference(context.Background(), sessID, "exact data", "approve exact write", "client-approval-issue", time.Second, grant.ApprovalID, deadline); err != nil {
		t.Fatalf("write with approval reference: %v", err)
	}
	if written.ApprovalID != grant.ApprovalID || written.Deadline != deadline.Format(time.RFC3339Nano) {
		t.Fatalf("approval wire fields=%+v", written)
	}
}

func TestClient_SessionErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not_found"})
	}))
	defer server.Close()

	cl := client.New(server.URL, "test-token")
	ctx := context.Background()

	if _, err := cl.GetSession(ctx, "nonexistent"); err == nil {
		t.Errorf("expected error on 404 GetSession")
	}
	if _, err := cl.ReadSession(ctx, "nonexistent", 0, 1024, time.Second); err == nil {
		t.Errorf("expected error on 404 ReadSession")
	}
	if _, err := cl.WriteSession(ctx, "nonexistent", "data", "reason", "key"); err == nil {
		t.Errorf("expected error on 404 WriteSession")
	}
	if _, err := cl.SendControlKey(ctx, "nonexistent", domain.ControlKeyCtrlC, "reason", "key"); err == nil {
		t.Errorf("expected error on 404 SendControlKey")
	}
	if _, err := cl.CloseSession(ctx, "nonexistent", "reason", "key", false); err == nil {
		t.Errorf("expected error on 404 CloseSession")
	}
}

func TestClient_SessionInvalidArguments(t *testing.T) {
	cl := client.New("http://127.0.0.1:9999", "test-token")
	ctx := context.Background()

	badID := "not-a-valid-session-id"
	if _, err := cl.GetSession(ctx, badID); err == nil {
		t.Errorf("expected error on invalid session ID in GetSession")
	}
	if _, err := cl.ReadSession(ctx, badID, 0, 1024, 0); err == nil {
		t.Errorf("expected error on invalid session ID in ReadSession")
	}
	if _, err := cl.WriteSession(ctx, badID, "data", "reason", "key"); err == nil {
		t.Errorf("expected error on invalid session ID in WriteSession")
	}
	if _, err := cl.SendControlKey(ctx, badID, domain.ControlKeyCtrlC, "reason", "key"); err == nil {
		t.Errorf("expected error on invalid session ID in SendControlKey")
	}
	if _, err := cl.WaitSession(ctx, badID, daemon.SessionWaitRequest{}); err == nil {
		t.Errorf("expected error on invalid session ID in WaitSession")
	}
	if _, err := cl.CloseSession(ctx, badID, "reason", "key", false); err == nil {
		t.Errorf("expected error on invalid session ID in CloseSession")
	}
}

func captureSessionTimeouts(t *testing.T, sessID string, captured map[string]map[string]any, mu *sync.Mutex) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode %s body: %v", r.URL.Path, err)
			}
			mu.Lock()
			captured[r.URL.Path] = body
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/sessions":
			_ = json.NewEncoder(w).Encode(daemon.SessionOpenResponse{Session: daemon.SessionDTO{SessionID: sessID}})
		case "/v1/sessions/" + sessID + "/write":
			_ = json.NewEncoder(w).Encode(daemon.SessionWriteResponse{BytesWritten: 1})
		case "/v1/sessions/" + sessID + "/control":
			_ = json.NewEncoder(w).Encode(daemon.SessionControlResponse{Status: "sent"})
		case "/v1/sessions/" + sessID + "/wait":
			_ = json.NewEncoder(w).Encode(daemon.SessionWaitResponse{SessionID: sessID})
		case "/v1/sessions/" + sessID + "/close":
			_ = json.NewEncoder(w).Encode(daemon.SessionCloseResponse{Session: daemon.SessionDTO{SessionID: sessID, State: "closed"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestClient_SubSecondTimeoutsUseMillisecondWireField(t *testing.T) {
	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	var mu sync.Mutex
	captured := make(map[string]map[string]any)
	server := httptest.NewServer(captureSessionTimeouts(t, sessID, captured, &mu))
	defer server.Close()

	cl := client.New(server.URL, "test-token")
	ctx := context.Background()
	calls := []func() error{
		func() error {
			_, err := cl.OpenSession(ctx, daemon.SessionOpenRequest{Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "open", IdempotencyKey: "open-key", TimeoutMillis: 250})
			return err
		},
		func() error {
			_, err := cl.WriteSessionWithTimeout(ctx, sessID, "x", "write", "write-key", 250*time.Millisecond)
			return err
		},
		func() error {
			_, err := cl.SendControlKeyWithTimeout(ctx, sessID, domain.ControlKeyCtrlC, "control", "control-key", 250*time.Millisecond)
			return err
		},
		func() error {
			_, err := cl.WaitSession(ctx, sessID, daemon.SessionWaitRequest{TimeoutMillis: 250})
			return err
		},
		func() error {
			_, err := cl.CloseSessionWithTimeout(ctx, sessID, "close", "close-key", false, 250*time.Millisecond)
			return err
		},
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("sub-second session call %d failed: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"/v1/sessions", "/v1/sessions/" + sessID + "/write", "/v1/sessions/" + sessID + "/control", "/v1/sessions/" + sessID + "/wait", "/v1/sessions/" + sessID + "/close"} {
		body := captured[path]
		if body["timeout_ms"] != float64(250) {
			t.Errorf("%s timeout_ms = %v, want 250", path, body["timeout_ms"])
		}
		if _, exists := body["timeout_seconds"]; exists {
			t.Errorf("%s sent conflicting timeout_seconds: %v", path, body)
		}
	}
}

func TestClient_ApprovalIDsReachAllSessionMutationRequests(t *testing.T) {
	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
	approvalID := "app-client-session-reference"
	var mu sync.Mutex
	captured := make(map[string]map[string]any)
	server := httptest.NewServer(captureSessionTimeouts(t, sessID, captured, &mu))
	defer server.Close()

	cl := client.New(server.URL, "test-token")
	ctx := context.Background()
	if _, err := cl.OpenSession(ctx, daemon.SessionOpenRequest{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "open", IdempotencyKey: "open-approval-id",
		TimeoutSeconds: 30, ApprovalID: approvalID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.WriteSessionWithApprovalID(ctx, sessID, "x", "write", "write-approval-id", 30*time.Second, approvalID); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.SendControlKeyWithApprovalID(ctx, sessID, domain.ControlKeyCtrlC, "control", "control-approval-id", 30*time.Second, approvalID); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.CloseSessionWithApprovalID(ctx, sessID, "close", "close-approval-id", true, 30*time.Second, approvalID); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{
		"/v1/sessions",
		"/v1/sessions/" + sessID + "/write",
		"/v1/sessions/" + sessID + "/control",
		"/v1/sessions/" + sessID + "/close",
	} {
		body := captured[path]
		if body["approval_id"] != approvalID {
			t.Errorf("%s approval_id = %v", path, body["approval_id"])
		}
		if _, ok := body["approval"]; ok {
			t.Errorf("%s leaked raw approval object: %v", path, body)
		}
	}
}

func TestClient_ReadSessionUsesExactSubSecondRequestContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	cl := client.New(server.URL, "test-token")
	started := time.Now()
	_, err := cl.ReadSession(context.Background(), "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4", 0, 1024, 40*time.Millisecond)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("sub-second read unexpectedly succeeded")
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("40ms read context lasted %v", elapsed)
	}
}
