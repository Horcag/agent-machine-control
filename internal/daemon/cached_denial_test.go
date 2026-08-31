package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type countingBackend struct {
	mutations atomic.Int64
}

func (c *countingBackend) Doctor(_ context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}

func (c *countingBackend) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	return []domain.MachineObservation{daemonTestObservation(daemonTestVMID)}, nil
}

func (c *countingBackend) InspectMachine(_ context.Context, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func (c *countingBackend) Capabilities(_ context.Context, _ string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(domain.CapabilityMachineStart, domain.CapabilityMachineStop), nil
}

func (c *countingBackend) StartMachine(_ context.Context, id string) (domain.MachineObservation, error) {
	c.mutations.Add(1)
	return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
}

func (c *countingBackend) StopMachine(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
	c.mutations.Add(1)
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func (c *countingBackend) ListCheckpoints(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
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

func (c *countingBackend) CreateCheckpoint(_ context.Context, _, _ string) (domain.CheckpointObservation, error) {
	c.mutations.Add(1)
	return domain.CheckpointObservation{}, nil
}

func (c *countingBackend) RestoreCheckpoint(_ context.Context, _, _ string) (domain.MachineObservation, error) {
	c.mutations.Add(1)
	return domain.MachineObservation{}, nil
}

func getReceiptCount(t *testing.T, stateDir string) int {
	sd, err := statedir.Resolve(stateDir)
	if err != nil {
		t.Fatalf("failed to resolve state dir: %v", err)
	}
	files, err := os.ReadDir(sd.ReceiptsDir())
	if err != nil {
		t.Fatalf("failed to read receipts dir: %v", err)
	}
	count := 0
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".json") && !strings.Contains(f.Name(), ".tmp.") {
			count++
		}
	}
	return count
}

func getAuditTerminalCount(t *testing.T, stateDir string) int {
	sd, err := statedir.Resolve(stateDir)
	if err != nil {
		t.Fatalf("failed to resolve state dir: %v", err)
	}
	auditPath := filepath.Join(sd.AuditDir(), "audit.jsonl")
	data, err := os.ReadFile(auditPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("failed to read audit log: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	count := 0
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		var ev struct {
			EventType string `json:"event_type"`
		}
		if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
			t.Fatalf("failed to parse audit event: %v", err)
		}
		if ev.EventType == "terminal_outcome" {
			count++
		}
	}
	return count
}

func startServerHelper(t *testing.T, dir string, be app.Backend) (*daemon.Server, string) {
	seedDaemonTestTarget(t, dir)
	srv, err := daemon.NewServer(daemon.Config{
		StateDir:   dir,
		ListenAddr: "127.0.0.1:0",
		Backend:    be,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	return srv, srv.Endpoint()
}

func createFirstDenial(ctx context.Context, t *testing.T, srv *daemon.Server, cl *client.Client, stateDir string) *daemon.OperationDTO {
	deadline1 := time.Now().Add(10 * time.Minute)
	req1 := daemon.CreateOperationRequest{
		Kind:           "machine.stop",
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "test cached denial wait readable",
		Parameters:     map[string]any{"mode": "turn-off"},
		IdempotencyKey: "key-cached-denial-wait-readable",
		Deadline:       &deadline1,
	}

	op1, err := cl.CreateOperation(ctx, req1)
	if err != nil {
		_ = srv.Shutdown(ctx)
		t.Fatalf("req1 failed: %v", err)
	}

	op1Final, err := cl.WaitOperation(ctx, op1.OperationID, 5*time.Second, 0)
	if err != nil {
		_ = srv.Shutdown(ctx)
		t.Fatalf("wait req1 failed: %v", err)
	}

	if op1Final.ErrorCategory != "approval_required" {
		_ = srv.Shutdown(ctx)
		t.Fatalf("expected approval_required denial, got %v %v", op1Final.ErrorCategory, op1Final.ErrorMessage)
	}

	rcptCount1 := getReceiptCount(t, stateDir)
	auditTerminalCount1 := getAuditTerminalCount(t, stateDir)

	if rcptCount1 != 1 {
		_ = srv.Shutdown(ctx)
		t.Fatalf("expected exactly 1 receipt, got %d", rcptCount1)
	}
	if auditTerminalCount1 != 1 {
		_ = srv.Shutdown(ctx)
		t.Fatalf("expected exactly 1 audit terminal event, got %d", auditTerminalCount1)
	}

	return op1Final
}

func deleteOpFiles(t *testing.T, stateDir string, opID string) {
	sd, err := statedir.Resolve(stateDir)
	if err != nil {
		t.Fatalf("failed to resolve state dir: %v", err)
	}
	opFilePath := filepath.Join(sd.OperationsDir(), fmt.Sprintf("%s.json", opID))
	eventsFilePath := filepath.Join(sd.OperationsDir(), fmt.Sprintf("%s.events.jsonl", opID))

	if err := os.Remove(opFilePath); err != nil {
		t.Fatalf("failed to delete operation file: %v", err)
	}
	if _, err := os.Lstat(eventsFilePath); err == nil {
		if err := os.Remove(eventsFilePath); err != nil {
			t.Fatalf("failed to delete events file: %v", err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("failed to stat events file: %v", err)
	}
}

func TestDaemon_CachedDenialWaitReadable(t *testing.T) {
	stateDir := t.TempDir()
	backend := &countingBackend{}

	srv1, endpoint1 := startServerHelper(t, stateDir, backend)
	opToken, err := auth.ReadTokenFile(stateDir+"/auth", auth.TokenTypeOperator)
	if err != nil {
		_ = srv1.Shutdown(context.Background())
		t.Fatalf("read opToken failed: %v", err)
	}

	cl1 := client.New(endpoint1, opToken)
	ctx := context.Background()

	op1Final := createFirstDenial(ctx, t, srv1, cl1, stateDir)

	// Cleanly shutdown the first server
	if err := srv1.Shutdown(ctx); err != nil {
		t.Fatalf("failed to shutdown first server: %v", err)
	}

	// Delete the exact synthetic operation record and event file if present, checking all errors
	deleteOpFiles(t, stateDir, op1Final.OperationID)

	// Start a NEW server/manager on the SAME state directory
	srv2, endpoint2 := startServerHelper(t, stateDir, backend)
	defer func() { _ = srv2.Shutdown(context.Background()) }()

	cl2 := client.New(endpoint2, opToken)

	// Retry with regenerated deadline
	deadline2 := time.Now().Add(20 * time.Minute)
	req2 := daemon.CreateOperationRequest{
		Kind:           "machine.stop",
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "test cached denial wait readable",
		Parameters:     map[string]any{"mode": "turn-off"},
		IdempotencyKey: "key-cached-denial-wait-readable",
		Deadline:       &deadline2,
	}

	op2, err := cl2.CreateOperation(ctx, req2)
	if err != nil {
		t.Fatalf("req2 retry failed: %v", err)
	}

	// Assert failed denial is immediately Get/Wait-readable via derived canonical operation ID
	op2Queried, err := cl2.GetOperation(ctx, op2.OperationID)
	if err != nil {
		t.Fatalf("GetOperation failed on retry: %v", err)
	}

	verifyOperationDetails(t, op2Queried, op2.OperationID, op1Final.ErrorCategory, op1Final.ErrorMessage)

	op2Wait, err := cl2.WaitOperation(ctx, op2.OperationID, 2*time.Second, 0)
	if err != nil {
		t.Fatalf("WaitOperation failed on retry: %v", err)
	}
	verifyOperationDetails(t, op2Wait, op2.OperationID, op1Final.ErrorCategory, op1Final.ErrorMessage)

	// Assert same receipt/category/message, exactly one receipt, exactly one audit terminal event, and no provider mutation calls
	rcptCount2 := getReceiptCount(t, stateDir)
	auditTerminalCount2 := getAuditTerminalCount(t, stateDir)

	if rcptCount2 != 1 {
		t.Errorf("expected exactly 1 receipt on retry, got %d", rcptCount2)
	}
	if auditTerminalCount2 != 1 {
		t.Errorf("expected exactly 1 audit terminal event on retry, got %d", auditTerminalCount2)
	}
	if muts := backend.mutations.Load(); muts != 0 {
		t.Errorf("expected 0 provider mutation calls, got %d", muts)
	}

	// Repeat once and assert same persisted operation ID/record
	op3, err := cl2.CreateOperation(ctx, req2)
	if err != nil {
		t.Fatalf("req3 retry failed: %v", err)
	}
	if op3.OperationID != op2.OperationID {
		t.Fatalf("expected same operation ID on repeated retry, got %s vs %s", op2.OperationID, op3.OperationID)
	}

	rcptCount3 := getReceiptCount(t, stateDir)
	auditTerminalCount3 := getAuditTerminalCount(t, stateDir)

	if rcptCount3 != 1 {
		t.Errorf("expected exactly 1 receipt on repeated retry, got %d", rcptCount3)
	}
	if auditTerminalCount3 != 1 {
		t.Errorf("expected exactly 1 audit terminal event on repeated retry, got %d", auditTerminalCount3)
	}
}

func verifyOperationDetails(t *testing.T, op *daemon.OperationDTO, expectedID, expectedCat, expectedMsg string) {
	t.Helper()
	if op.OperationID != expectedID {
		t.Errorf("expected ID %s, got %s", expectedID, op.OperationID)
	}
	if op.ErrorCategory != expectedCat {
		t.Errorf("expected error category %q, got %q", expectedCat, op.ErrorCategory)
	}
	if op.ErrorMessage != expectedMsg {
		t.Errorf("expected error message %q, got %q", expectedMsg, op.ErrorMessage)
	}
}
