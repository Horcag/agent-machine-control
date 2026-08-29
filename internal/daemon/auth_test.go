package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type mockDaemonBackend struct{}

func (m *mockDaemonBackend) Doctor(_ context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}

func (m *mockDaemonBackend) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	return nil, nil
}

func (m *mockDaemonBackend) InspectMachine(_ context.Context, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func (m *mockDaemonBackend) Capabilities(_ context.Context, _ string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(domain.CapabilityMachineStart, domain.CapabilityMachineStop), nil
}

func (m *mockDaemonBackend) StartMachine(_ context.Context, id string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
}

func (m *mockDaemonBackend) StopMachine(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func (m *mockDaemonBackend) ListCheckpoints(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
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

func (m *mockDaemonBackend) CreateCheckpoint(_ context.Context, _ string, _ string) (domain.CheckpointObservation, error) {
	return domain.CheckpointObservation{}, nil
}

func (m *mockDaemonBackend) RestoreCheckpoint(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func setupTestServer(t *testing.T) (*daemon.Server, string, string, string) {
	dir := t.TempDir()
	srv, err := daemon.NewServer(daemon.Config{
		StateDir:   dir,
		ListenAddr: "127.0.0.1:0",
		Backend:    &mockDaemonBackend{},
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	authDir := srv.Endpoint() // endpoint URL
	opToken, err := auth.ReadTokenFile(dir+"/auth", auth.TokenTypeOperator)
	if err != nil {
		t.Fatalf("read opToken failed: %v", err)
	}
	agToken, err := auth.ReadTokenFile(dir+"/auth", auth.TokenTypeAgentMCP)
	if err != nil {
		t.Fatalf("read agToken failed: %v", err)
	}

	return srv, authDir, opToken, agToken
}

func TestServer_Auth_MissingOrInvalidToken(t *testing.T) {
	srv, endpoint, _, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// 1. Missing Authorization header
	req, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for missing token, got %d", resp.StatusCode)
	}

	// 2. Invalid Bearer token
	req, _ = http.NewRequest(http.MethodGet, endpoint+"/v1/health", nil)
	req.Header.Set("Authorization", "Bearer invalid-token-1234")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for invalid token, got %d", resp2.StatusCode)
	}
}

func TestServer_Auth_OriginRejected(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	req, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+opToken)
	req.Header.Set("Origin", "http://evil.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for Origin header, got %d", resp.StatusCode)
	}
}

func TestServer_Auth_SpoofedHeadersIgnored(t *testing.T) {
	srv, endpoint, _, agToken := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// Submit operation using agent token, but trying to spoof X-Actor or X-Scopes as operator/admin
	body := daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "spoofing test",
		IdempotencyKey: "key-spoof-1",
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+agToken)
	req.Header.Set("X-Actor", "operator:admin")
	req.Header.Set("X-Scopes", "audit:read,operation:cancel")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 202/200, got %d", resp.StatusCode)
	}

	var opDTO daemon.OperationDTO
	_ = json.NewDecoder(resp.Body).Decode(&opDTO)

	// Verify actor recorded on server-side is agent:mcp-local, ignoring spoofed header!
	if opDTO.Actor != "agent:mcp-local" {
		t.Errorf("expected Actor to be agent:mcp-local, but server trusted spoofed header: %s", opDTO.Actor)
	}
}
