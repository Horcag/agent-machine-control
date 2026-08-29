package hyperv_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestAdapter_ListCheckpoints_ArrayAndSingleAndEmpty(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	t.Run("ArrayOfCheckpoints", func(t *testing.T) {
		jsonPayload := `{
			"schema_version": "1",
			"checkpoints": [
				{
					"id": "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
					"name": "snap-1",
					"vm_id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
					"parent_id": "",
					"checkpoint_type": "Standard",
					"creation_time": "2026-08-29T10:00:00Z"
				},
				{
					"id": "e4a523d4-6b99-4d62-a5e2-4752c0f20002",
					"name": "snap-2",
					"vm_id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
					"parent_id": "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
					"checkpoint_type": "Standard",
					"creation_time": "2026-08-29T11:00:00Z"
				}
			]
		}`

		mock := &mockExecutor{
			executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
				return []byte(jsonPayload), nil, nil
			},
		}
		adapter := hyperv.New(
			hyperv.WithExecutor(mock),
			hyperv.WithNowFunc(func() time.Time { return now }),
		)

		list, err := adapter.ListCheckpoints(context.Background(), targetID)
		if err != nil {
			t.Fatalf("unexpected ListCheckpoints error: %v", err)
		}
		if len(list) != 2 {
			t.Fatalf("expected 2 checkpoints, got %d", len(list))
		}
		if list[0].Name != "snap-1" || list[1].Name != "snap-2" {
			t.Errorf("unexpected checkpoint names: %v, %v", list[0].Name, list[1].Name)
		}
	})

	t.Run("SingleCheckpointObject", func(t *testing.T) {
		jsonPayload := `{
			"schema_version": "1",
			"checkpoints": {
				"id": "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
				"name": "snap-single",
				"vm_id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
				"checkpoint_type": "Standard",
				"creation_time": "2026-08-29T10:00:00Z"
			}
		}`

		mock := &mockExecutor{
			executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
				return []byte(jsonPayload), nil, nil
			},
		}
		adapter := hyperv.New(
			hyperv.WithExecutor(mock),
			hyperv.WithNowFunc(func() time.Time { return now }),
		)

		list, err := adapter.ListCheckpoints(context.Background(), targetID)
		if err != nil {
			t.Fatalf("unexpected ListCheckpoints error: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("expected 1 checkpoint, got %d", len(list))
		}
		if list[0].Name != "snap-single" {
			t.Errorf("expected snap-single, got %s", list[0].Name)
		}
	})

	t.Run("EmptyCheckpointsArray", func(t *testing.T) {
		jsonPayload := `{"schema_version": "1", "checkpoints": []}`
		mock := &mockExecutor{
			executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
				return []byte(jsonPayload), nil, nil
			},
		}
		adapter := hyperv.New(
			hyperv.WithExecutor(mock),
			hyperv.WithNowFunc(func() time.Time { return now }),
		)

		list, err := adapter.ListCheckpoints(context.Background(), targetID)
		if err != nil {
			t.Fatalf("unexpected ListCheckpoints error: %v", err)
		}
		if len(list) != 0 {
			t.Fatalf("expected 0 checkpoints, got %d", len(list))
		}
	})
}

func TestAdapter_CreateCheckpoint_SuccessAndErrors(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapName := "pre-test-checkpoint"

	jsonSuccess := `{
		"schema_version": "1",
		"success": true,
		"checkpoint": {
			"id": "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			"name": "pre-test-checkpoint",
			"vm_id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			"parent_id": "",
			"checkpoint_type": "Standard",
			"creation_time": "2026-08-29T15:00:00Z"
		}
	}`

	var capturedEnv []string
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, env []string) ([]byte, []byte, error) {
			capturedEnv = env
			return []byte(jsonSuccess), nil, nil
		},
	}
	adapter := hyperv.New(
		hyperv.WithExecutor(mock),
		hyperv.WithNowFunc(func() time.Time { return now }),
	)

	snap, err := adapter.CreateCheckpoint(context.Background(), targetID, snapName)
	if err != nil {
		t.Fatalf("unexpected CreateCheckpoint error: %v", err)
	}

	if snap.Name != snapName {
		t.Errorf("expected snap name %s, got %s", snapName, snap.Name)
	}

	foundName := false
	for _, env := range capturedEnv {
		if strings.Contains(env, "AMC_SNAPSHOT_NAME="+snapName) {
			foundName = true
		}
	}
	if !foundName {
		t.Errorf("expected AMC_SNAPSHOT_NAME in env, got %v", capturedEnv)
	}
}

func TestAdapter_RestoreCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"

	jsonSuccess := `{
		"schema_version": "1",
		"success": true,
		"machine": {
			"id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			"name": "win11-target",
			"state": "Off",
			"generation": 2,
			"version": "10.0"
		}
	}`

	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(jsonSuccess), nil, nil
		},
	}
	adapter := hyperv.New(
		hyperv.WithExecutor(mock),
		hyperv.WithNowFunc(func() time.Time { return now }),
	)

	obs, err := adapter.RestoreCheckpoint(context.Background(), targetID, snapID)
	if err != nil {
		t.Fatalf("RestoreCheckpoint failed: %v", err)
	}
	if obs.State != domain.MachineStateOff {
		t.Errorf("expected state Off, got %s", obs.State)
	}
}

func TestAdapter_Capabilities(t *testing.T) {
	adapter := hyperv.New()
	caps, err := adapter.Capabilities(context.Background(), "target-guid")
	if err != nil {
		t.Fatalf("Capabilities error: %v", err)
	}
	if !caps.Has(domain.CapabilityMachineStart) {
		t.Errorf("expected CapabilityMachineStart")
	}
	if !caps.Has(domain.CapabilityCheckpointRestore) {
		t.Errorf("expected CapabilityCheckpointRestore")
	}
}

func TestAdapter_Checkpoint_ParserErrors(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	cases := []struct {
		name     string
		response string
	}{
		{"empty response", ""},
		{"malformed json", "{not-valid-json}"},
		{"trailing data", `{"schema_version":"1","checkpoints":[]} trailing`},
		{"bad schema", `{"schema_version":"2","checkpoints":[]}`},
		{"error category", `{"schema_version":"1","error_category":"checkpoint_not_found"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockExecutor{
				executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
					return []byte(tc.response), nil, nil
				},
			}
			adapter := hyperv.New(hyperv.WithExecutor(mock))
			if _, err := adapter.ListCheckpoints(context.Background(), targetID); err == nil {
				t.Errorf("expected error for list %s", tc.name)
			}
			if _, err := adapter.CreateCheckpoint(context.Background(), targetID, "snap"); err == nil {
				t.Errorf("expected error for create %s", tc.name)
			}
		})
	}
}
