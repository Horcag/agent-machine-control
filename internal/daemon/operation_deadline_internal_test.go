package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestResolveOperationDeadline(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Nanosecond)
	exactMax := now.Add(time.Hour)
	maxPlusOne := exactMax.Add(time.Nanosecond)
	tests := []struct {
		name        string
		request     CreateOperationRequest
		wantTimeout time.Duration
		wantErr     bool
	}{
		{name: "default", wantTimeout: 30 * time.Second},
		{name: "exact max timeout", request: CreateOperationRequest{TimeoutSeconds: 3600}, wantTimeout: time.Hour},
		{name: "max plus one timeout", request: CreateOperationRequest{TimeoutSeconds: 3601}, wantErr: true},
		{name: "negative timeout", request: CreateOperationRequest{TimeoutSeconds: -1}, wantErr: true},
		{name: "overflow scale timeout", request: CreateOperationRequest{TimeoutSeconds: math.MaxInt}, wantErr: true},
		{name: "past deadline", request: CreateOperationRequest{Deadline: &past}, wantErr: true},
		{name: "exact max deadline", request: CreateOperationRequest{Deadline: &exactMax}, wantTimeout: time.Hour},
		{name: "far future deadline", request: CreateOperationRequest{Deadline: &maxPlusOne}, wantErr: true},
		{name: "conflicting fields", request: CreateOperationRequest{TimeoutSeconds: 1, Deadline: &exactMax}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deadline, timeout, err := resolveOperationDeadline(test.request, now)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected deadline validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveOperationDeadline failed: %v", err)
			}
			if timeout != test.wantTimeout {
				t.Fatalf("timeout = %s, want %s", timeout, test.wantTimeout)
			}
			if !deadline.Equal(now.Add(test.wantTimeout)) {
				t.Fatalf("deadline = %s, want %s", deadline, now.Add(test.wantTimeout))
			}
		})
	}
}

func TestCreateOperationRequestUnmarshalJSONDeadlineBoundaries(t *testing.T) {
	valid := `{"kind":"machine.start","target":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","reason":"deadline boundary","idempotency_key":"deadline-boundary"}`
	for name, body := range map[string]string{
		"null":       valid[:len(valid)-1] + `,"deadline":null}`,
		"non-string": valid[:len(valid)-1] + `,"deadline":1}`,
		"malformed":  valid[:len(valid)-1] + `,"deadline":"not-a-deadline"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var request CreateOperationRequest
			err := json.Unmarshal([]byte(body), &request)
			if name == "null" {
				if err != nil || request.Deadline != nil || request.deadlineText != "" {
					t.Fatalf("request=%+v err=%v", request, err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected deadline decoding error")
			}
		})
	}
}

type deadlineAdmissionBackend struct{ calls atomic.Int32 }

func (b *deadlineAdmissionBackend) called() { b.calls.Add(1) }
func (b *deadlineAdmissionBackend) Doctor(context.Context) (app.DoctorReport, error) {
	b.called()
	return app.DoctorReport{}, nil
}
func (b *deadlineAdmissionBackend) ListMachines(context.Context) ([]domain.MachineObservation, error) {
	b.called()
	return nil, nil
}
func (b *deadlineAdmissionBackend) InspectMachine(context.Context, string) (domain.MachineObservation, error) {
	b.called()
	return domain.MachineObservation{}, nil
}
func (b *deadlineAdmissionBackend) Capabilities(context.Context, string) (domain.CapabilitySet, error) {
	b.called()
	return domain.DirectMachineCapabilities(), nil
}
func (b *deadlineAdmissionBackend) StartMachine(context.Context, string) (domain.MachineObservation, error) {
	b.called()
	return domain.MachineObservation{}, nil
}
func (b *deadlineAdmissionBackend) StopMachine(context.Context, string, string) (domain.MachineObservation, error) {
	b.called()
	return domain.MachineObservation{}, nil
}
func (b *deadlineAdmissionBackend) ListCheckpoints(context.Context, string) ([]domain.CheckpointObservation, error) {
	b.called()
	return nil, nil
}
func (b *deadlineAdmissionBackend) CreateCheckpoint(context.Context, string, string) (domain.CheckpointObservation, error) {
	b.called()
	return domain.CheckpointObservation{}, nil
}
func (b *deadlineAdmissionBackend) RestoreCheckpoint(context.Context, string, string) (domain.MachineObservation, error) {
	b.called()
	return domain.MachineObservation{}, nil
}

func TestInvalidOperationDeadlineRejectedBeforeManagerAndProvider(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	backend := &deadlineAdmissionBackend{}
	stateDir := missingDaemonStateRoot(t)
	server, err := NewServer(Config{
		StateDir: stateDir, ListenAddr: "127.0.0.1:0", Backend: backend,
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	token, err := auth.ReadTokenFile(filepath.Join(stateDir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}

	deadline := now.Add(time.Hour + time.Nanosecond)
	payload, err := json.Marshal(CreateOperationRequest{
		Kind: "machine.start", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason: "reject invalid deadline", IdempotencyKey: "invalid-deadline", Deadline: &deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.Endpoint()+"/v1/operations", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	if got := backend.calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want zero", got)
	}
	entries, err := os.ReadDir(filepath.Join(stateDir, "operations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			t.Fatalf("operation manager persisted %q for rejected request", entry.Name())
		}
	}
}

func TestOversizedOperationBodyRejectedBeforeManagerAndProvider(t *testing.T) {
	backend := &deadlineAdmissionBackend{}
	stateDir := missingDaemonStateRoot(t)
	server, err := NewServer(Config{StateDir: stateDir, ListenAddr: "127.0.0.1:0", Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	token, err := auth.ReadTokenFile(filepath.Join(stateDir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"kind":"machine.start","target":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","reason":"` + strings.Repeat("x", 65*1024) + `","idempotency_key":"oversized-operation"}`
	request, err := http.NewRequest(http.MethodPost, server.Endpoint()+"/v1/operations", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	if got := backend.calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want zero", got)
	}
	entries, err := os.ReadDir(filepath.Join(stateDir, "operations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			t.Fatalf("operation manager persisted %q for oversized request", entry.Name())
		}
	}
}
