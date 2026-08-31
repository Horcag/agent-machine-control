package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/operations"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

const clientTestVMID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

type mockBackend struct{}

func (m *mockBackend) Doctor(_ context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}

func (m *mockBackend) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	locator, _ := domain.NewMachineLocator(domain.LocalHostID, clientTestVMID)
	return []domain.MachineObservation{{
		HostID: domain.LocalHostID, Locator: locator, ID: clientTestVMID, Name: "client-test-vm",
		State: domain.MachineStateOff, RawState: "Off", Generation: 2, Version: "10.0",
		MemoryAssignedBytes: 1024, Capabilities: domain.DirectMachineCapabilities(),
		ObservedAt: time.Date(2026, 8, 31, 4, 0, 0, 0, time.UTC), ObservationType: domain.ObservationObserved,
	}}, nil
}

func (m *mockBackend) InspectMachine(_ context.Context, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func (m *mockBackend) Capabilities(_ context.Context, _ string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(domain.CapabilityMachineStart, domain.CapabilityMachineStop), nil
}

func (m *mockBackend) StartMachine(_ context.Context, id string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
}

func (m *mockBackend) StopMachine(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func (m *mockBackend) ListCheckpoints(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
	return []domain.CheckpointObservation{
		{
			ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			Name:            "base",
			VMID:            id,
			CheckpointType:  "Standard",
			CreatedAt:       time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
			ObservedAt:      time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
			ObservationType: domain.ObservationObserved,
		},
	}, nil
}

func (m *mockBackend) CreateCheckpoint(_ context.Context, _ string, _ string) (domain.CheckpointObservation, error) {
	return domain.CheckpointObservation{}, nil
}

func (m *mockBackend) RestoreCheckpoint(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func setupDaemon(t *testing.T) (*daemon.Server, string) {
	dir := t.TempDir()
	state, err := statedir.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	store, err := target.NewStore(state.TargetsDir())
	if err != nil {
		t.Fatal(err)
	}
	locator, _ := domain.NewMachineLocator(domain.LocalHostID, clientTestVMID)
	value, _ := target.NewDefault(locator, nil)
	if _, err := store.Save(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	srv, err := daemon.NewServer(daemon.Config{
		StateDir:   dir,
		ListenAddr: "127.0.0.1:0",
		Backend:    &mockBackend{},
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	return srv, dir
}

func TestClient_DiscoverAndHealth(t *testing.T) {
	srv, stateDir := setupDaemon(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	health, err := cl.Health(context.Background())
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}

	if health.Status != "ok" || health.SchemaVersion != "1" {
		t.Errorf("unexpected health payload: %+v", health)
	}
}

func TestClient_Discover_Unavailable(t *testing.T) {
	emptyDir := t.TempDir()
	_, err := client.Discover(emptyDir, client.TokenTypeOperator)
	if !errors.Is(err, client.ErrDaemonUnavailable) {
		t.Fatalf("expected ErrDaemonUnavailable, got: %v", err)
	}
}

func TestClient_OperationsLifecycleAndWait(t *testing.T) {
	srv, stateDir := setupDaemon(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	req := daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "test client create",
		IdempotencyKey: "key-client-1",
	}

	opDTO, err := cl.CreateOperation(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateOperation failed: %v", err)
	}

	if opDTO.OperationID == "" {
		t.Fatalf("expected non-empty operation ID")
	}

	// WaitOperation blocks event-driven until completion
	finalDTO, err := cl.WaitOperation(context.Background(), opDTO.OperationID, 10*time.Second, 0)
	if err != nil {
		t.Fatalf("WaitOperation failed: %v", err)
	}

	if finalDTO.State != "completed" {
		t.Errorf("expected state completed, got %s", finalDTO.State)
	}
	if finalDTO.ReceiptID == "" {
		t.Errorf("expected receipt ID on completed operation")
	}

	// GetReceipt
	rcpt, err := cl.GetReceipt(context.Background(), finalDTO.ReceiptID)
	if err != nil {
		t.Fatalf("GetReceipt failed: %v", err)
	}
	if rcpt.ReceiptID != finalDTO.ReceiptID {
		t.Errorf("expected receipt ID %s, got %s", finalDTO.ReceiptID, rcpt.ReceiptID)
	}

	// GetAudit
	eventsList, err := cl.GetAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetAudit failed: %v", err)
	}
	if len(eventsList) == 0 {
		t.Errorf("expected audit events to be returned")
	}
}

func TestClient_ListCancelAndStop(t *testing.T) {
	srv, stateDir := setupDaemon(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	req := daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "test client ops",
		IdempotencyKey: "key-client-2",
	}

	opDTO, err := cl.CreateOperation(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateOperation failed: %v", err)
	}

	// ListOperations
	list, err := cl.ListOperations(context.Background(), operations.ListOptions{Limit: 5})
	if err != nil {
		t.Fatalf("ListOperations failed: %v", err)
	}
	if len(list) == 0 {
		t.Errorf("expected at least 1 operation in list")
	}

	// CancelOperation
	_, _ = cl.CancelOperation(context.Background(), opDTO.OperationID, "cancel test")

	// GetOperation
	fetched, err := cl.GetOperation(context.Background(), opDTO.OperationID)
	if err != nil {
		t.Fatalf("GetOperation failed: %v", err)
	}
	if fetched.OperationID != opDTO.OperationID {
		t.Errorf("expected op ID %s, got %s", opDTO.OperationID, fetched.OperationID)
	}

	// StopDaemon
	stopResp, err := cl.StopDaemon(context.Background())
	if err != nil {
		t.Fatalf("StopDaemon failed: %v", err)
	}
	if stopResp.Status != "stopping" {
		t.Errorf("expected status stopping, got %s", stopResp.Status)
	}
}

func TestClient_ErrorsAndAPIError(t *testing.T) {
	apiErr := &client.APIError{StatusCode: 400, Category: "invalid_argument", Message: "bad param"}
	if apiErr.Error() != "bad param" {
		t.Errorf("expected 'bad param', got %q", apiErr.Error())
	}
}

func TestClient_EdgeCasesAndInputValidation(t *testing.T) {
	cl := client.New("http://127.0.0.1:0", "dummy-token")

	// Empty op IDs
	if _, err := cl.GetOperation(context.Background(), ""); !errors.Is(err, domain.ErrInvalidOperationID) {
		t.Errorf("expected ErrInvalidOperationID, got %v", err)
	}
	if _, err := cl.CancelOperation(context.Background(), "", "test"); !errors.Is(err, domain.ErrInvalidOperationID) {
		t.Errorf("expected ErrInvalidOperationID, got %v", err)
	}
	if _, _, _, err := cl.WatchEvents(context.Background(), "", 0); !errors.Is(err, domain.ErrInvalidOperationID) {
		t.Errorf("expected ErrInvalidOperationID, got %v", err)
	}
	if _, err := cl.GetReceipt(context.Background(), "invalid-id"); err == nil {
		t.Errorf("expected error for invalid receipt ID")
	}

	// Endpoint getter
	if cl.Endpoint() != "http://127.0.0.1:0" {
		t.Errorf("unexpected endpoint: %s", cl.Endpoint())
	}
}

func TestClient_DiscoverErrors(t *testing.T) {
	_, err := client.Discover("/nonexistent/state/dir", client.TokenTypeOperator)
	if err == nil {
		t.Errorf("expected error for non-existent state dir")
	}
}

func TestClient_HTTPErrorMapping(t *testing.T) {
	statusResponses := map[string]int{
		"op-00000000000000000000000000000400": http.StatusBadRequest,
		"op-00000000000000000000000000000401": http.StatusUnauthorized,
		"op-00000000000000000000000000000403": http.StatusForbidden,
		"op-00000000000000000000000000000404": http.StatusNotFound,
		"op-00000000000000000000000000000409": http.StatusConflict,
		"op-00000000000000000000000000000429": http.StatusTooManyRequests,
		"op-00000000000000000000000000000500": http.StatusInternalServerError,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/v1/operations/")
		if key == "op-00000000000000000000000000000bad" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not-json"))
			return
		}
		if sc, ok := statusResponses[key]; ok {
			w.WriteHeader(sc)
			_ = json.NewEncoder(w).Encode(daemon.ErrorEnvelope{Error: daemon.ErrorField{Message: "err"}})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cl := client.New(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))

	testCases := []struct {
		path        string
		expectedErr error
	}{
		{"op-00000000000000000000000000000400", client.ErrInvalidArgument},
		{"op-00000000000000000000000000000401", client.ErrDenied},
		{"op-00000000000000000000000000000403", client.ErrDenied},
		{"op-00000000000000000000000000000404", client.ErrNotFound},
		{"op-00000000000000000000000000000409", client.ErrConflict},
		{"op-00000000000000000000000000000429", client.ErrDaemonUnavailable},
		{"op-00000000000000000000000000000500", client.ErrMalformedResponse},
		{"op-00000000000000000000000000000bad", client.ErrMalformedResponse},
	}

	for _, tc := range testCases {
		if _, err := cl.GetOperation(context.Background(), tc.path); !errors.Is(err, tc.expectedErr) {
			t.Errorf("path %s: expected %v, got %v", tc.path, tc.expectedErr, err)
		}
	}
}

func TestClient_Health(t *testing.T) {
	srv, stateDir := setupDaemon(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	h, err := cl.Health(context.Background())
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("expected status ok, got %s", h.Status)
	}
}

func TestClientIssueOperationApproval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/operation-approvals" {
			http.NotFound(w, r)
			return
		}
		var request daemon.OperationApprovalIssueRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Beneficiary != "agent:mcp-local" || request.Parameters["mode"] != "turn-off" {
			t.Fatalf("request = %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema_version":"1","approval_id":"app-operation-0123456789abcdef0123456789abcdef","deadline":"2026-08-31T03:00:00Z","expires_at":"2026-08-31T03:00:00Z","operation":{"kind":"machine.stop","target":"local:c4a523d4-6b99-4d62-a5e2-4752c0f20001","reason":"typed client issuance","idempotency_key":"typed-client-issuance","parameters":{"mode":"turn-off"}}}`))
	}))
	defer server.Close()

	cl := client.New(server.URL, strings.Repeat("a", 64))
	grant, err := cl.IssueOperationApproval(context.Background(), daemon.OperationApprovalIssueRequest{
		Kind: "machine.stop", Target: clientTestVMID, Reason: "typed client issuance",
		IdempotencyKey: "typed-client-issuance", ValidForMillis: 60_000,
		Beneficiary: "agent:mcp-local", Parameters: map[string]any{"mode": "turn-off"},
	})
	if err != nil || grant.ApprovalID == "" || grant.Operation.Target == clientTestVMID {
		t.Fatalf("grant=%+v err=%v", grant, err)
	}
}

func TestClientCreateOperationCanonicalizesApprovalDeadline(t *testing.T) {
	deadline := time.Date(2026, 8, 31, 4, 5, 6, 700_000_000, time.FixedZone("UTC+01", 3600))
	wantDeadline := deadline.UTC().Format(time.RFC3339Nano)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/operations" {
			http.NotFound(w, r)
			return
		}
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var gotDeadline string
		if err := json.Unmarshal(request["deadline"], &gotDeadline); err != nil {
			t.Fatal(err)
		}
		if gotDeadline != wantDeadline {
			t.Fatalf("deadline = %q, want canonical %q", gotDeadline, wantDeadline)
		}
		_, _ = w.Write([]byte(`{"schema_version":"1"}`))
	}))
	defer server.Close()

	cl := client.New(server.URL, strings.Repeat("a", 64))
	if _, err := cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
		Kind: "machine.stop", Target: clientTestVMID, Reason: "typed client canonical deadline",
		IdempotencyKey: "typed-client-canonical-deadline",
		ApprovalID:     "app-operation-0123456789abcdef0123456789abcdef",
		Deadline:       &deadline,
		Parameters:     map[string]any{"mode": "turn-off"},
	}); err != nil {
		t.Fatal(err)
	}
}
