package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func TestRecoveryService_MissingBackendAndInputValidation(t *testing.T) {
	svc := app.NewRecoveryService(nil, nil, nil, nil, nil)
	ctx := context.Background()
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// 1. PolicyDeniedError.Error()
	deniedErr := &app.PolicyDeniedError{
		Reason:  policy.DenialApprovalRequired,
		Message: "approval required",
	}
	if deniedErr.Error() != "policy denied (approval_required): approval required" {
		t.Errorf("unexpected error string: %s", deniedErr.Error())
	}

	// 2. ListCheckpoints with nil backend
	if _, err := svc.ListCheckpoints(ctx, targetID); !errors.Is(err, app.ErrMissingBackend) {
		t.Errorf("expected ErrMissingBackend, got %v", err)
	}

	// 3. ListCheckpoints with invalid GUID
	backend := &mockBackend{}
	fullSvc, _ := setupTestRecovery(t, backend)
	if _, err := fullSvc.ListCheckpoints(ctx, "invalid-guid"); err == nil {
		t.Errorf("expected error for invalid GUID in ListCheckpoints")
	}

	// 4. StartMachine with nil backend
	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "start test",
		IdempotencyKey: "key-nil-backend",
		Timeout:        30 * time.Second,
	}
	if _, _, err := svc.StartMachine(ctx, req); !errors.Is(err, app.ErrMissingBackend) {
		t.Errorf("expected ErrMissingBackend, got %v", err)
	}

	// 5. StopMachine with nil backend
	if _, _, err := svc.StopMachine(ctx, req, "shutdown"); !errors.Is(err, app.ErrMissingBackend) {
		t.Errorf("expected ErrMissingBackend, got %v", err)
	}

	// 6. CreateCheckpoint with nil backend
	if _, _, err := svc.CreateCheckpoint(ctx, req, "snap-1"); !errors.Is(err, app.ErrMissingBackend) {
		t.Errorf("expected ErrMissingBackend, got %v", err)
	}

	// 7. CreateCheckpoint with empty name
	if _, _, err := fullSvc.CreateCheckpoint(ctx, req, ""); err == nil {
		t.Errorf("expected error for empty name in CreateCheckpoint")
	}

	// 8. RestoreCheckpoint with nil backend
	if _, _, err := svc.RestoreCheckpoint(ctx, req, "e4a523d4-6b99-4d62-a5e2-4752c0f20001"); !errors.Is(err, app.ErrMissingBackend) {
		t.Errorf("expected ErrMissingBackend, got %v", err)
	}

	// 9. RestoreCheckpoint with invalid GUID
	if _, _, err := fullSvc.RestoreCheckpoint(ctx, req, "invalid-chk-guid"); err == nil {
		t.Errorf("expected error for invalid checkpoint GUID in RestoreCheckpoint")
	}
}
