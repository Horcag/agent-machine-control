package mcpadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mutationTargetVMID       = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	mutationTargetOtherVMID  = "c4a523d4-6b99-4d62-a5e2-4752c0f20002"
	mutationTargetCheckpoint = "c4a523d4-6b99-4d62-a5e2-4752c0f20003"
	mutationTargetOperation  = "op-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mutationTargetReceipt    = "rcpt-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestMCPMutationsSubmitCanonicalEnrolledTarget(t *testing.T) {
	locator := mutationTargetLocator(t)
	references := []struct {
		name  string
		value string
	}{
		{name: "omitted", value: ""},
		{name: "default", value: "default"},
		{name: "exact alias", value: "primary"},
		{name: "enrolled GUID", value: mutationTargetVMID},
		{name: "canonical locator", value: locator.String()},
	}
	mutations := []struct {
		name string
		call func(*Adapter, string) (*mcp.CallToolResult, error)
	}{
		{name: "machine start", call: func(adapter *Adapter, id string) (*mcp.CallToolResult, error) {
			result, _, err := adapter.MachineStart(t.Context(), nil, MachineStartInput{ID: id, Reason: "canonical start", IdempotencyKey: "canonical-start", Timeout: "30s"})
			return result, err
		}},
		{name: "machine stop", call: func(adapter *Adapter, id string) (*mcp.CallToolResult, error) {
			result, _, err := adapter.MachineStop(t.Context(), nil, MachineStopInput{ID: id, Mode: "shutdown", Reason: "canonical stop", IdempotencyKey: "canonical-stop", Timeout: "30s"})
			return result, err
		}},
		{name: "checkpoint create", call: func(adapter *Adapter, id string) (*mcp.CallToolResult, error) {
			result, _, err := adapter.CheckpointCreate(t.Context(), nil, CheckpointCreateInput{ID: id, Name: "baseline", Reason: "canonical checkpoint", IdempotencyKey: "canonical-checkpoint", Timeout: "30s"})
			return result, err
		}},
		{name: "checkpoint restore", call: func(adapter *Adapter, id string) (*mcp.CallToolResult, error) {
			result, _, err := adapter.CheckpointRestore(t.Context(), nil, CheckpointRestoreInput{ID: id, CheckpointID: mutationTargetCheckpoint, Reason: "canonical restore", IdempotencyKey: "canonical-restore", Timeout: "30s"})
			return result, err
		}},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			for _, reference := range references {
				t.Run(reference.name, func(t *testing.T) {
					capturedTarget := ""
					server := mutationTargetServer(t, &capturedTarget)
					defer server.Close()

					adapter := newEnrolledMutationAdapter(t, server.URL)
					toolError, err := mutation.call(adapter, reference.value)
					if err != nil || toolError != nil {
						t.Fatalf("mutation error=%+v err=%v", toolError, err)
					}
					if capturedTarget != locator.String() {
						t.Fatalf("submitted target = %q, want %q", capturedTarget, locator)
					}
				})
			}
		})
	}
}

func TestMCPMutationRejectsDifferentTargetBeforeClientOrProvider(t *testing.T) {
	clientCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		clientCalls++
	}))
	defer server.Close()

	adapter := newEnrolledMutationAdapter(t, server.URL)
	backend := &countingTargetBackend{MockObserver: getTestObserver()}
	adapter.discoveryService = app.NewDiscoveryService(backend)
	adapter.recoveryService = app.NewRecoveryService(backend, nil, nil, nil, nil)
	checks := []struct {
		name string
		call func() (*mcp.CallToolResult, error)
	}{
		{name: "machine start", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.MachineStart(t.Context(), nil, MachineStartInput{ID: mutationTargetOtherVMID, Reason: "reject different start", IdempotencyKey: "reject-different-start", Timeout: "30s"})
			return result, err
		}},
		{name: "machine stop", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.MachineStop(t.Context(), nil, MachineStopInput{ID: mutationTargetOtherVMID, Mode: "shutdown", Reason: "reject different stop", IdempotencyKey: "reject-different-stop", Timeout: "30s"})
			return result, err
		}},
		{name: "checkpoint create", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.CheckpointCreate(t.Context(), nil, CheckpointCreateInput{ID: mutationTargetOtherVMID, Name: "baseline", Reason: "reject different checkpoint", IdempotencyKey: "reject-different-checkpoint", Timeout: "30s"})
			return result, err
		}},
		{name: "checkpoint restore", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.CheckpointRestore(t.Context(), nil, CheckpointRestoreInput{ID: mutationTargetOtherVMID, CheckpointID: mutationTargetCheckpoint, Reason: "reject different restore", IdempotencyKey: "reject-different-restore", Timeout: "30s"})
			return result, err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			toolError, err := check.call()
			if err != nil || toolError == nil || !toolError.IsError {
				t.Fatalf("different target error=%+v err=%v", toolError, err)
			}
		})
	}
	if clientCalls != 0 {
		t.Fatalf("daemon client calls = %d, want 0", clientCalls)
	}
	if calls := backend.calls(); calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
}

func TestTargetServiceInitializationIsConcurrentAndCachesOutcome(t *testing.T) {
	state, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	adapter := NewAdapter(state.Root())
	const callers = 64
	services := make(chan *app.TargetService, callers)
	errors := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Go(func() {
			service, getErr := adapter.getTargetService()
			services <- service
			errors <- getErr
		})
	}
	group.Wait()
	close(services)
	close(errors)

	var first *app.TargetService
	for service := range services {
		if service == nil {
			t.Fatal("getTargetService returned nil service")
		}
		if first == nil {
			first = service
		} else if service != first {
			t.Fatal("concurrent calls returned different target services")
		}
	}
	for getErr := range errors {
		if getErr != nil {
			t.Fatalf("getTargetService error = %v", getErr)
		}
	}

	failed := NewAdapter("")
	_, firstErr := failed.getTargetService()
	_, secondErr := failed.getTargetService()
	if firstErr == nil || secondErr == nil || firstErr != secondErr {
		t.Fatalf("cached initialization errors = %v, %v", firstErr, secondErr)
	}
}

func newEnrolledMutationAdapter(t *testing.T, endpoint string) *Adapter {
	t.Helper()
	state, err := statedir.Resolve(t.TempDir())
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
	locator := mutationTargetLocator(t)
	defaultTarget, err := target.NewDefault(locator, []string{"primary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(context.Background(), defaultTarget); err != nil {
		t.Fatal(err)
	}
	inventory, err := app.NewTrustedInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	observed := getTestObserver().inspect
	observed.ID = mutationTargetVMID
	observed.HostID = domain.LocalHostID
	observed.Locator = locator
	now := time.Now().UTC()
	checkpoint := domain.CheckpointObservation{ID: mutationTargetCheckpoint, VMID: mutationTargetVMID, Name: "baseline", CreatedAt: now, ObservedAt: now, ObservationType: domain.ObservationObserved}
	service, err := app.NewTargetService(inventory, store, app.WithTargetRefresh(func(context.Context) error {
		return inventory.ApplySnapshot(app.HostSnapshot{HostID: domain.LocalHostID, Health: app.HostHealthObserved, Machines: []domain.MachineObservation{observed}})
	}))
	if err != nil {
		t.Fatal(err)
	}
	observer := &MockObserver{inspect: observed, checkpoints: []domain.CheckpointObservation{checkpoint}}
	return &Adapter{
		targetService:    service,
		discoveryService: app.NewDiscoveryService(observer),
		recoveryService:  app.NewRecoveryService(observer, nil, nil, nil, nil),
		client:           client.New(endpoint, "test-token"),
	}
}

func mutationTargetLocator(t *testing.T) domain.MachineLocator {
	t.Helper()
	locator, err := domain.NewMachineLocator(domain.LocalHostID, mutationTargetVMID)
	if err != nil {
		t.Fatal(err)
	}
	return locator
}

func mutationTargetServer(t *testing.T, capturedTarget *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/operations":
			var request daemon.CreateOperationRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode operation request: %v", err)
			}
			*capturedTarget = request.Target
			_, _ = fmt.Fprintf(w, "{\"schema_version\":\"1\",\"operation_id\":\"%s\",\"state\":\"admitted\"}", mutationTargetOperation)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/"+mutationTargetOperation:
			_, _ = fmt.Fprintf(w, "{\"schema_version\":\"1\",\"operation_id\":\"%s\",\"state\":\"completed\",\"receipt_id\":\"%s\"}", mutationTargetOperation, mutationTargetReceipt)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/receipts/"+mutationTargetReceipt:
			_, _ = fmt.Fprintf(w, "{\"schema_version\":\"1\",\"receipt\":{\"receipt_id\":\"%s\",\"operation_kind\":\"machine.start\",\"fingerprint\":\"fp-test\",\"actor\":\"agent:mcp-local\",\"target\":\"local:%s\",\"class\":\"reversible_mutation\",\"effective_backend\":\"hyperv\",\"started_at\":\"2026-08-31T00:00:00Z\",\"completed_at\":\"2026-08-31T00:00:01Z\",\"outcome\":{\"status\":\"success\",\"exit_code\":0},\"observation_type\":\"observed\",\"rollback_ref\":\"%s\",\"redaction_status\":\"applied\"}}", mutationTargetReceipt, mutationTargetVMID, mutationTargetCheckpoint)
		default:
			http.NotFound(w, r)
		}
	}))
}
