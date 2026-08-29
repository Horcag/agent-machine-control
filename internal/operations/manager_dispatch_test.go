package operations_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/operations"
)

func TestManager_DispatchKindsAndAgentActorFilter(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	b := &mockBackend{}
	mgr, hub, _ := setupTestManager(t, b)

	act := domain.ActorContext{
		AuthenticatedCaller:  "operator:local",
		EffectiveActor:       "operator:local",
		CallerPermissions:    domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"),
		EffectivePermissions: domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"),
	}

	kinds := []struct {
		kind   domain.OperationKind
		cap    domain.Capability
		params map[string]any
	}{
		{"machine.stop", domain.CapabilityMachineStop, map[string]any{"mode": "shutdown"}},
		{"checkpoint.create", domain.CapabilityCheckpointCreate, map[string]any{"name": "snap-1"}},
		{"checkpoint.restore", domain.CapabilityCheckpointRestore, map[string]any{"checkpoint_id": "e4a523d4-6b99-4d62-a5e2-4752c0f20001"}},
	}

	for i, k := range kinds {
		op := domain.Operation{
			Kind:                k.kind,
			Target:              domain.MachineRef(targetID),
			Actor:               act,
			Reason:              "test dispatch kinds",
			Deadline:            time.Now().Add(time.Minute),
			IdempotencyKey:      fmt.Sprintf("key-kind-%d", i),
			RequiredCapability:  string(k.cap),
			RequiredScopes:      []string{"machine:write"},
			Classification:      domain.ClassReversibleMutation,
			EvidenceSensitivity: domain.EvidenceSensitivityStandard,
			Parameters:          k.params,
		}

		rec, _, err := mgr.Submit(context.Background(), op, 30*time.Second)
		if err != nil {
			t.Fatalf("Submit %s failed: %v", k.kind, err)
		}

		ch, unsub, _ := hub.Subscribe(context.Background(), rec.ID, 0)
		for ev := range ch {
			if ev.State.IsTerminal() {
				break
			}
		}
		unsub()
	}

	// Test Agent listing operations filtered to own actor
	agentAct := domain.ActorContext{
		AuthenticatedCaller:  "agent:mcp-local",
		EffectiveActor:       "agent:mcp-local",
		CallerPermissions:    domain.NewScopeSet("machine:read", "machine:write"),
		EffectivePermissions: domain.NewScopeSet("machine:read", "machine:write"),
	}
	agentList, err := mgr.List(operations.ListOptions{}, agentAct)
	if err != nil {
		t.Fatalf("Agent list failed: %v", err)
	}
	for _, rec := range agentList {
		if rec.Actor != "agent:mcp-local" {
			t.Errorf("agent saw operation belonging to %s", rec.Actor)
		}
	}
}

func TestManager_SubmitValidationErrorAndEmptyList(t *testing.T) {
	b := &mockBackend{}
	mgr, _, _ := setupTestManager(t, b)

	// Missing target -> validation error
	_, _, err := mgr.Submit(context.Background(), domain.Operation{}, 30*time.Second)
	if err == nil {
		t.Errorf("expected error for invalid operation")
	}

	// List on non-existent dir
	emptyList, err := operations.ListRecords("/nonexistent/operations/dir", operations.ListOptions{})
	if err != nil || len(emptyList) != 0 {
		t.Errorf("expected empty list on non-existent dir, got list %v, err %v", emptyList, err)
	}
}

func TestManager_GetAndCancelNotFound(t *testing.T) {
	b := &mockBackend{}
	mgr, _, _ := setupTestManager(t, b)

	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write", "operation:cancel"), domain.NewScopeSet("machine:write", "operation:cancel"))

	nonExistentID := "op-00000000000000000000000000000000"

	// Get non-existent
	if _, err := mgr.Get(nonExistentID, actor); !errors.Is(err, operations.ErrOperationNotFound) {
		t.Errorf("expected ErrOperationNotFound, got %v", err)
	}

	// Cancel non-existent
	if err := mgr.Cancel(nonExistentID, actor, "reason"); !errors.Is(err, operations.ErrOperationNotFound) {
		t.Errorf("expected ErrOperationNotFound, got %v", err)
	}

	// Invalid format rejection
	if _, err := mgr.Get("invalid-id", actor); !errors.Is(err, domain.ErrInvalidOperationID) {
		t.Errorf("expected ErrInvalidOperationID, got %v", err)
	}
}
