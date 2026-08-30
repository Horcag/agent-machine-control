package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
