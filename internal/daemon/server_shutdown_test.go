package daemon_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type blockedFakeBackend struct {
	startInvoked atomic.Int64
	cancelled    atomic.Bool
	blockChan    chan struct{}
}

func (b *blockedFakeBackend) Doctor(_ context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}

func (b *blockedFakeBackend) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	return []domain.MachineObservation{daemonTestObservation(daemonTestVMID)}, nil
}

func (b *blockedFakeBackend) InspectMachine(_ context.Context, id string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func (b *blockedFakeBackend) Capabilities(_ context.Context, _ string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(domain.CapabilityMachineStart, domain.CapabilityMachineStop), nil
}

func (b *blockedFakeBackend) StartMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	b.startInvoked.Add(1)
	select {
	case <-b.blockChan:
		return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
	case <-ctx.Done():
		b.cancelled.Store(true)
		return domain.MachineObservation{}, ctx.Err()
	}
}

func (b *blockedFakeBackend) StopMachine(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func (b *blockedFakeBackend) ListCheckpoints(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
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

func (b *blockedFakeBackend) CreateCheckpoint(_ context.Context, _ string, _ string) (domain.CheckpointObservation, error) {
	return domain.CheckpointObservation{}, nil
}

func (b *blockedFakeBackend) RestoreCheckpoint(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func submitBlockedOp(t *testing.T, endpoint, opToken, targetID string) string {
	t.Helper()
	body := daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "blocked backend shutdown test",
		IdempotencyKey: "idem-shutdown-blocked-1",
	}
	bodyData, _ := json.Marshal(body)
	postReq, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(bodyData))
	postReq.Header.Set("Authorization", "Bearer "+opToken)
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("submit operation failed: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", postResp.StatusCode)
	}

	var opDTO daemon.OperationDTO
	if err := json.NewDecoder(postResp.Body).Decode(&opDTO); err != nil {
		t.Fatalf("failed to decode op DTO: %v", err)
	}
	return opDTO.OperationID
}

func drainSSEForCancellation(r io.Reader, expectedOpID string) bool {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		line = strings.TrimSpace(line)
		dataStr, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		var ev domain.Event
		if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(dataStr)), &ev); jsonErr == nil {
			if ev.OperationID == expectedOpID && (ev.State == domain.OpStateCancelled || ev.EventType == "terminal") {
				return true
			}
		}
	}
}

func verifyShutdownFilesRemoved(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "daemon", "endpoint.json")); !os.IsNotExist(err) {
		t.Errorf("endpoint.json should be removed after shutdown")
	}
	if _, err := os.Stat(filepath.Join(dir, "daemon", "singleton.lock", "owner.json")); !os.IsNotExist(err) {
		t.Errorf("singleton.lock/owner.json should be removed after shutdown")
	}
}

func TestServer_ShutdownOrdering_BlockedBackendAndSSEWaiter(t *testing.T) {
	blockCh := make(chan struct{})
	backend := &blockedFakeBackend{blockChan: blockCh}
	defer func() {
		select {
		case <-blockCh:
		default:
			close(blockCh)
		}
	}()

	dir := missingDaemonStateRoot(t)
	seedDaemonTestTarget(t, dir)
	srv, err := daemon.NewServer(daemon.Config{
		StateDir:   dir,
		ListenAddr: "127.0.0.1:0",
		Backend:    backend,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	endpoint := srv.Endpoint()
	opToken, err := auth.ReadTokenFile(filepath.Join(dir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatalf("read opToken failed: %v", err)
	}

	// 1. Connect SSE global subscriber
	sseReq, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/events", nil)
	sseReq.Header.Set("Authorization", "Bearer "+opToken)
	sseReq.Header.Set("Accept", "text/event-stream")
	streamClient := &http.Client{Timeout: 0}
	sseResp, err := streamClient.Do(sseReq)
	if err != nil {
		t.Fatalf("connect SSE failed: %v", err)
	}
	defer sseResp.Body.Close()

	// 2. Submit mutation operation against blocked backend
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	opID := submitBlockedOp(t, endpoint, opToken, targetID)

	// Wait until backend invocation begins
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if backend.startInvoked.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if backend.startInvoked.Load() == 0 {
		t.Fatalf("backend StartMachine was never invoked")
	}

	// 3. Trigger daemon shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- srv.Shutdown(shutdownCtx)
	}()

	// 4. SSE subscriber reader should receive cancelled/terminal event and stream must terminate cleanly
	sawCancelled := drainSSEForCancellation(sseResp.Body, opID)

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Logf("Shutdown completed with expected errors if any: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Server.Shutdown timed out / deadlocked")
	}

	if !sawCancelled {
		t.Errorf("expected SSE subscriber to observe cancellation terminal event")
	}

	// 5. Verify endpoint and singleton files are removed
	verifyShutdownFilesRemoved(t, dir)
}
