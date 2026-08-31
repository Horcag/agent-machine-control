package mcpadapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MockObserver implements app.MachineObserver for testing.
type MockObserver struct {
	doctorReport app.DoctorReport
	doctorErr    error
	machines     []domain.MachineObservation
	listErr      error
	inspect      domain.MachineObservation
	inspectErr   error
	checkpoints  []domain.CheckpointObservation
	chkListErr   error
}

func (m *MockObserver) Doctor(_ context.Context) (app.DoctorReport, error) {
	return m.doctorReport, m.doctorErr
}

func (m *MockObserver) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	return m.machines, m.listErr
}

func (m *MockObserver) InspectMachine(_ context.Context, _ string) (domain.MachineObservation, error) {
	return m.inspect, m.inspectErr
}

func (m *MockObserver) Capabilities(_ context.Context, _ string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(), nil
}

func (m *MockObserver) StartMachine(_ context.Context, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func (m *MockObserver) StopMachine(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func (m *MockObserver) ListCheckpoints(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
	return m.checkpoints, m.chkListErr
}

func (m *MockObserver) CreateCheckpoint(_ context.Context, _ string, _ string) (domain.CheckpointObservation, error) {
	return domain.CheckpointObservation{}, nil
}

func (m *MockObserver) RestoreCheckpoint(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func getExposedTools(ctx context.Context, t *testing.T) *mcp.ListToolsResult {
	a := NewAdapter("")
	server := a.BuildServer()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("failed to connect server: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("failed to connect client: %v", err)
	}
	t.Cleanup(func() { clientSession.Close() })

	toolsResult, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	return toolsResult
}

func TestToolList(t *testing.T) {
	ctx := t.Context()
	toolsResult := getExposedTools(ctx, t)

	expectedTools := map[string]bool{
		"doctor":             true,
		"machine_list":       true,
		"machine_inspect":    true,
		"checkpoint_list":    true,
		"machine_start":      true,
		"machine_stop":       true,
		"checkpoint_create":  true,
		"checkpoint_restore": true,
		"operation_list":     true,
		"operation_show":     true,
		"operation_wait":     true,
		"receipt_show":       true,
		// Session tools
		"session_open":    true,
		"session_read":    true,
		"session_write":   true,
		"session_control": true,
		"session_wait":    true,
		"session_list":    true,
		"session_show":    true,
		"session_close":   true,
	}

	foundTools := make(map[string]bool)
	for _, tool := range toolsResult.Tools {
		foundTools[tool.Name] = true
		if !expectedTools[tool.Name] {
			t.Errorf("Unexpected tool exposed: %s", tool.Name)
		}
	}

	for exp := range expectedTools {
		if !foundTools[exp] {
			t.Errorf("Expected tool %s was not exposed", exp)
		}
	}

	if len(toolsResult.Tools) != 20 {
		t.Errorf("expected exactly 20 tools, got %d", len(toolsResult.Tools))
	}
}

func TestSchemaSnapshot(t *testing.T) {
	ctx := t.Context()
	toolsResult := getExposedTools(ctx, t)

	marshalled, err := json.MarshalIndent(toolsResult.Tools, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal tools: %v", err)
	}

	snapshotDir := filepath.Join("testdata", "schemas")
	snapshotPath := filepath.Join(snapshotDir, "tools.json")

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(snapshotDir, 0755); err != nil {
			t.Fatalf("failed to create snapshot dir: %v", err)
		}
		if err := os.WriteFile(snapshotPath, marshalled, 0600); err != nil {
			t.Fatalf("failed to write golden snapshot: %v", err)
		}
	}

	goldenContent, err := os.ReadFile(snapshotPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("Golden snapshot file missing. Run with UPDATE_GOLDEN=1 to generate it.")
		}
		t.Fatalf("failed to read golden snapshot: %v", err)
	}

	var goldenTools, actualTools []mcp.Tool
	if err := json.Unmarshal(goldenContent, &goldenTools); err != nil {
		t.Fatalf("failed to unmarshal golden tools: %v", err)
	}
	if err := json.Unmarshal(marshalled, &actualTools); err != nil {
		t.Fatalf("failed to unmarshal actual tools: %v", err)
	}

	if !reflect.DeepEqual(goldenTools, actualTools) {
		t.Errorf("Exposed tools schema does not match the golden snapshot at %s.", snapshotPath)
	}
}

func getTestObserver() *MockObserver {
	return &MockObserver{
		doctorReport: app.DoctorReport{
			Status:       app.DoctorReady,
			Ready:        true,
			Message:      "Mocked ready",
			Capabilities: domain.ReadOnlyMachineCapabilities(),
			ObservedAt:   time.Unix(1000000, 0).UTC(),
		},
		machines: []domain.MachineObservation{
			{
				ID:                  "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
				Name:                "VM-1",
				State:               domain.MachineStateRunning,
				RawState:            "Running",
				Generation:          2,
				Version:             "10.0",
				UptimeMs:            5000,
				CPUUsagePercent:     12,
				MemoryAssignedBytes: 4096 * 1024 * 1024,
				Capabilities:        domain.ReadOnlyMachineCapabilities(),
				ObservedAt:          time.Unix(1000000, 0).UTC(),
				ObservationType:     domain.ObservationObserved,
			},
		},
		inspect: domain.MachineObservation{
			ID:                  "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			Name:                "VM-1",
			State:               domain.MachineStateRunning,
			RawState:            "Running",
			Generation:          2,
			Version:             "10.0",
			UptimeMs:            5000,
			CPUUsagePercent:     12,
			MemoryAssignedBytes: 4096 * 1024 * 1024,
			Capabilities:        domain.ReadOnlyMachineCapabilities(),
			ObservedAt:          time.Unix(1000000, 0).UTC(),
			ObservationType:     domain.ObservationObserved,
		},
		checkpoints: []domain.CheckpointObservation{
			{
				ID:              "c4a523d4-6b99-4d62-a5e2-4752c0f20002",
				Name:            "Check-1",
				VMID:            "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
				CheckpointType:  "Standard",
				CreatedAt:       time.Unix(900000, 0).UTC(),
				ObservedAt:      time.Unix(1000000, 0).UTC(),
				ObservationType: domain.ObservationObserved,
			},
		},
	}
}

func TestObserveToolsDoctor(t *testing.T) {
	ctx := t.Context()
	mockObs := getTestObserver()
	a := &Adapter{
		allowUnscopedTestTargetFallback: true,
		discoveryService:                app.NewDiscoveryService(mockObs),
		recoveryService:                 app.NewRecoveryService(mockObs, nil, nil, nil, nil),
	}

	resCall, doctorRes, err := a.Doctor(ctx, nil, DoctorInput{})
	if err != nil {
		t.Fatalf("Doctor handler error: %v", err)
	}
	if resCall != nil && resCall.IsError {
		t.Fatalf("Doctor call reported tool error: %v", resCall.Content)
	}
	if !doctorRes.Ready || doctorRes.Status != app.DoctorReady {
		t.Errorf("Unexpected doctor result: %+v", doctorRes)
	}
}

func TestObserveToolsMachineList(t *testing.T) {
	ctx := t.Context()
	mockObs := getTestObserver()
	a := &Adapter{
		allowUnscopedTestTargetFallback: true,
		discoveryService:                app.NewDiscoveryService(mockObs),
		recoveryService:                 app.NewRecoveryService(mockObs, nil, nil, nil, nil),
	}

	resCall, listRes, err := a.MachineList(ctx, nil, MachineListInput{})
	if err != nil {
		t.Fatalf("MachineList handler error: %v", err)
	}
	if resCall != nil && resCall.IsError {
		t.Fatalf("MachineList call reported tool error")
	}
	if len(listRes.Machines) != 1 || listRes.Machines[0].Name != "VM-1" {
		t.Errorf("Unexpected machine list result: %+v", listRes)
	}
}

func TestObserveToolsMachineInspect(t *testing.T) {
	ctx := t.Context()
	mockObs := getTestObserver()
	a := &Adapter{
		allowUnscopedTestTargetFallback: true,
		discoveryService:                app.NewDiscoveryService(mockObs),
		recoveryService:                 app.NewRecoveryService(mockObs, nil, nil, nil, nil),
	}

	resCall, inspectRes, err := a.MachineInspect(ctx, nil, MachineInspectInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001"})
	if err != nil {
		t.Fatalf("MachineInspect handler error: %v", err)
	}
	if resCall != nil && resCall.IsError {
		t.Fatalf("MachineInspect call reported tool error")
	}
	if inspectRes.Machine.Name != "VM-1" {
		t.Errorf("Unexpected machine inspect result: %+v", inspectRes)
	}

	// Test invalid ID schema rejection
	resCall, _, err = a.MachineInspect(ctx, nil, MachineInspectInput{ID: "bad-guid"})
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if resCall == nil || !resCall.IsError {
		t.Error("Expected tool error for invalid GUID schema in MachineInspect")
	}
}

func TestObserveToolsCheckpointList(t *testing.T) {
	ctx := t.Context()
	mockObs := getTestObserver()
	a := &Adapter{
		allowUnscopedTestTargetFallback: true,
		discoveryService:                app.NewDiscoveryService(mockObs),
		recoveryService:                 app.NewRecoveryService(mockObs, nil, nil, nil, nil),
	}

	resCall, chkListRes, err := a.CheckpointList(ctx, nil, CheckpointListInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001"})
	if err != nil {
		t.Fatalf("CheckpointList handler error: %v", err)
	}
	if resCall != nil && resCall.IsError {
		t.Fatalf("CheckpointList call reported tool error")
	}
	if len(chkListRes.Checkpoints) != 1 || chkListRes.Checkpoints[0].Name != "Check-1" {
		t.Errorf("Unexpected checkpoint list result: %+v", chkListRes)
	}
}

func setupMockDaemon(t *testing.T, lastReqPath *string) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*lastReqPath = r.URL.Path
		_, _ = io.ReadAll(r.Body)

		if r.Header.Get("Authorization") != "Bearer mock-agent-mcp-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/v1/operations") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"schema_version": "1",
				"operation_id": "op-12345678901234567890123456789012",
				"state": "admitted"
			}`))
			return
		}

		if strings.HasPrefix(r.URL.Path, "/v1/operations/") && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"schema_version": "1",
				"operation_id": "op-12345678901234567890123456789012",
				"state": "completed",
				"receipt_id": "rcpt-12345678901234567890123456789012"
			}`))
			return
		}

		if strings.HasPrefix(r.URL.Path, "/v1/receipts/") && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"schema_version": "1",
				"receipt": {
					"receipt_id": "rcpt-12345678901234567890123456789012",
					"operation_kind": "machine.start",
					"fingerprint": "fp-123",
					"actor": "agent:mcp-local",
					"target": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
					"class": "reversible_mutation",
					"effective_backend": "hyperv",
					"started_at": "2026-08-30T00:00:00Z",
					"completed_at": "2026-08-30T00:00:01Z",
					"outcome": {
						"status": "success",
						"exit_code": 0
					},
					"observation_type": "inferred",
					"redaction_status": "none"
				}
			}`))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(func() { server.Close() })
	return server
}

func TestMutationAndDurableToolsMachineStart(t *testing.T) {
	ctx := t.Context()
	var lastReqPath string
	server := setupMockDaemon(t, &lastReqPath)

	cl := client.New(server.URL, "mock-agent-mcp-token")
	a := &Adapter{client: cl, allowUnscopedTestTargetFallback: true}

	resCall, mutationRes, err := a.MachineStart(ctx, nil, MachineStartInput{
		ID:             "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "Need start",
		IdempotencyKey: "key-123",
		Timeout:        "30s",
	})
	if err != nil {
		t.Fatalf("MachineStart error: %v", err)
	}
	if resCall != nil && resCall.IsError {
		t.Fatalf("MachineStart reported tool error: %+v", resCall.Content)
	}
	if mutationRes.Receipt.ReceiptID != "rcpt-12345678901234567890123456789012" {
		t.Errorf("Unexpected receipt: %+v", mutationRes)
	}
	if lastReqPath != "/v1/receipts/rcpt-12345678901234567890123456789012" {
		t.Errorf("Expected request path /v1/receipts/..., got %s", lastReqPath)
	}
}

func TestMutationAndDurableToolsMachineStop(t *testing.T) {
	ctx := t.Context()
	var lastReqPath string
	server := setupMockDaemon(t, &lastReqPath)

	cl := client.New(server.URL, "mock-agent-mcp-token")
	a := &Adapter{client: cl, allowUnscopedTestTargetFallback: true}

	_, _, err := a.MachineStop(ctx, nil, MachineStopInput{
		ID:             "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Mode:           "shutdown",
		Reason:         "Need stop",
		IdempotencyKey: "key-124",
		Timeout:        "1m",
	})
	if err != nil {
		t.Fatalf("MachineStop error: %v", err)
	}
}

func TestMutationAndDurableToolsCheckpointCreate(t *testing.T) {
	ctx := t.Context()
	var lastReqPath string
	server := setupMockDaemon(t, &lastReqPath)

	cl := client.New(server.URL, "mock-agent-mcp-token")
	a := &Adapter{client: cl, allowUnscopedTestTargetFallback: true}

	_, _, err := a.CheckpointCreate(ctx, nil, CheckpointCreateInput{
		ID:             "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Name:           "Check-2",
		Reason:         "Need check",
		IdempotencyKey: "key-125",
		Timeout:        "30s",
	})
	if err != nil {
		t.Fatalf("CheckpointCreate error: %v", err)
	}
}

func TestMutationAndDurableToolsCheckpointRestore(t *testing.T) {
	ctx := t.Context()
	var lastReqPath string
	server := setupMockDaemon(t, &lastReqPath)

	cl := client.New(server.URL, "mock-agent-mcp-token")
	a := &Adapter{client: cl, allowUnscopedTestTargetFallback: true}

	_, _, err := a.CheckpointRestore(ctx, nil, CheckpointRestoreInput{
		ID:             "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		CheckpointID:   "c4a523d4-6b99-4d62-a5e2-4752c0f20002",
		Reason:         "Need restore",
		IdempotencyKey: "key-126",
		Timeout:        "30s",
	})
	if err != nil {
		t.Fatalf("CheckpointRestore error: %v", err)
	}
}
