package daemon

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
	"github.com/Horcag/agent-machine-control/internal/operations"
)

func TestServer_HandlersDirectNoCallerContext(t *testing.T) {
	srv := &Server{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)

	// 1. handleHealth without caller context -> 401
	srv.handleHealth(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// 2. handleGetAudit without caller context -> 401
	w = httptest.NewRecorder()
	srv.handleGetAudit(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// 3. handleGetReceipt without caller context -> 401
	w = httptest.NewRecorder()
	srv.handleGetReceipt(w, r, "rcpt-1")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// 4. handleStopDaemon without caller context -> 401
	w = httptest.NewRecorder()
	srv.handleStopDaemon(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// 5. handleListReceipts without caller context -> 401
	w = httptest.NewRecorder()
	srv.handleListReceipts(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// 6. handleCreateOperation without caller context -> 401
	w = httptest.NewRecorder()
	srv.handleCreateOperation(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// 7. handleListOperations without caller context -> 401
	w = httptest.NewRecorder()
	srv.handleListOperations(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// 8. handleGetOperation without caller context -> 401
	w = httptest.NewRecorder()
	srv.handleGetOperation(w, r, "op-1")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// 9. handleCancelOperation without caller context -> 401
	w = httptest.NewRecorder()
	srv.handleCancelOperation(w, r, "op-1")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// 10. handleOperationEvents without caller context -> 401
	w = httptest.NewRecorder()
	srv.handleOperationEvents(w, r, "op-1")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	// 11. handleGlobalEvents without caller context -> 401
	w = httptest.NewRecorder()
	srv.handleGlobalEvents(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

type nonFlusherWriter struct {
	http.ResponseWriter
}

func TestServer_EventsNilHubAndNonFlusher(t *testing.T) {
	srv := &Server{eventHub: nil}
	act, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("audit:read", "machine:read"), domain.NewScopeSet("audit:read", "machine:read"))

	// 1. handleGlobalEvents with nil eventHub -> 500
	r := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	r = r.WithContext(context.WithValue(r.Context(), callerContextKey, act))
	w := httptest.NewRecorder()
	srv.handleGlobalEvents(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for nil eventHub on global events, got %d", w.Code)
	}

	// 2. handleGlobalEvents with non-flusher writer -> 500
	srv.eventHub = events.NewHub(t.TempDir())
	nonFlusher := &nonFlusherWriter{ResponseWriter: httptest.NewRecorder()}
	srv.handleGlobalEvents(nonFlusher, r)
	// ResponseWriter in nonFlusherWriter returns 500
	if rec, ok := nonFlusher.ResponseWriter.(*httptest.ResponseRecorder); ok && rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for non-flusher on global events, got %d", rec.Code)
	}

	// 3. handleOperationEvents with nil eventHub -> 500
	opMgr := operations.NewManager(t.TempDir(), nil, nil, nil, nil)
	srvNil := &Server{eventHub: nil, opMgr: opMgr}
	w = httptest.NewRecorder()
	srvNil.handleOperationEvents(w, r, "op-00000000000000000000000000000001")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing op, got %d", w.Code)
	}

	// 4. handleOperationEvents with non-flusher writer
	nonFlusherOp := &nonFlusherWriter{ResponseWriter: httptest.NewRecorder()}
	srvNil.handleOperationEvents(nonFlusherOp, r, "op-00000000000000000000000000000001")
}

func TestServer_StartDefaultListenAddr(t *testing.T) {
	dir := missingDaemonStateRoot(t)
	srv, err := NewServer(Config{
		StateDir:   dir,
		ListenAddr: "", // tests default 127.0.0.1:0
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start with default listen address failed: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	if srv.Endpoint() == "" {
		t.Errorf("expected valid endpoint")
	}
}

func TestServer_WriteOwnerRecordAndEndpointFile_Errors(t *testing.T) {
	// WriteEndpointFile invalid URL
	rec := EndpointRecord{
		SchemaVersion: SchemaVersion,
		PID:           100,
		Endpoint:      "not-a-valid-url:bad",
	}
	if err := WriteEndpointFile(t.TempDir(), rec); err == nil {
		t.Errorf("expected error for invalid endpoint URL in WriteEndpointFile")
	}

	// writeOwnerRecord into non-existent path
	err := writeOwnerRecord("/non/existent/dir", "/non/existent/dir/owner.json", "/non/existent/daemon", "rt-1", 100, "", time.Now())
	if err == nil {
		t.Errorf("expected error writing owner record to non-existent dir")
	}
}

func TestServer_StartListenErrors(t *testing.T) {
	dir := missingDaemonStateRoot(t)

	// 1. Invalid host:port format
	srv1, err := NewServer(Config{
		StateDir:   dir,
		ListenAddr: "127.0.0.1:invalid:extra",
	})
	if err == nil {
		if err := srv1.Start(); err == nil {
			t.Errorf("expected Start to fail for invalid host:port format")
		}
	}

	// 2. Non-loopback listen address
	dir2 := missingDaemonStateRoot(t)
	srv2, err := NewServer(Config{
		StateDir:   dir2,
		ListenAddr: "192.168.1.50:8080",
	})
	if err == nil {
		if err := srv2.Start(); err == nil {
			t.Errorf("expected Start to fail for non-loopback listen address")
		}
	}

	// 3. Port already in use
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on test port: %v", err)
	}
	defer ln.Close()

	dir3 := missingDaemonStateRoot(t)
	srv3, err := NewServer(Config{
		StateDir:   dir3,
		ListenAddr: ln.Addr().String(),
	})
	if err == nil {
		if err := srv3.Start(); err == nil {
			t.Errorf("expected Start to fail for port already in use")
		}
	}
}
