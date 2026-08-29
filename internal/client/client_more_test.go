package client_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/operations"
)

func TestClient_AuditAndReceiptAndListFilters(t *testing.T) {
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
		Reason:         "audit and receipt test",
		IdempotencyKey: "key-more-client-1",
	}

	opDTO, err := cl.CreateOperation(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateOperation failed: %v", err)
	}

	finalDTO, err := cl.WaitOperation(context.Background(), opDTO.OperationID, time.Minute, 0)
	if err != nil {
		t.Fatalf("WaitOperation failed: %v", err)
	}

	// 1. GetAudit with limit
	eventsList, err := cl.GetAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetAudit failed: %v", err)
	}
	if len(eventsList) == 0 {
		t.Errorf("expected audit events")
	}

	// 2. GetReceipt
	if finalDTO.ReceiptID != "" {
		rcpt, err := cl.GetReceipt(context.Background(), finalDTO.ReceiptID)
		if err != nil {
			t.Fatalf("GetReceipt failed: %v", err)
		}
		if rcpt.ReceiptID != finalDTO.ReceiptID {
			t.Errorf("expected receipt ID %s, got %s", finalDTO.ReceiptID, rcpt.ReceiptID)
		}
	}

	// 3. ListOperations with state and machine filters
	list, err := cl.ListOperations(context.Background(), operations.ListOptions{
		State:   domain.OpStateCompleted,
		Machine: domain.MachineRef(targetID),
		Limit:   5,
	})
	if err != nil {
		t.Fatalf("ListOperations failed: %v", err)
	}
	if len(list) == 0 {
		t.Errorf("expected at least 1 completed operation in list")
	}
}

func TestClient_ErrorsAndStopDaemon(t *testing.T) {
	apiErr := &client.APIError{
		StatusCode: 403,
		Category:   "forbidden",
		Message:    "access denied by policy",
	}
	if apiErr.Error() != "access denied by policy" {
		t.Errorf("unexpected error string: %s", apiErr.Error())
	}

	srv, stateDir := setupDaemon(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	opCl, _ := client.Discover(stateDir, client.TokenTypeOperator)
	agCl, _ := client.Discover(stateDir, client.TokenTypeAgentMCP)

	// StopDaemon with agent token -> ErrDenied
	if _, err := agCl.StopDaemon(context.Background()); !errors.Is(err, client.ErrDenied) {
		t.Errorf("expected ErrDenied for agent stopping daemon, got %v", err)
	}

	// StopDaemon with operator token -> success
	if _, err := opCl.StopDaemon(context.Background()); err != nil {
		t.Errorf("expected success for operator stopping daemon, got %v", err)
	}

	nonExistentOpID := "op-00000000000000000000000000000000"

	// Cancel non-existent operation
	if _, err := opCl.CancelOperation(context.Background(), nonExistentOpID, "reason"); !errors.Is(err, client.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// Get non-existent operation
	if _, err := opCl.GetOperation(context.Background(), nonExistentOpID); !errors.Is(err, client.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	// Invalid ID format
	if _, err := opCl.GetOperation(context.Background(), "invalid-id"); !errors.Is(err, domain.ErrInvalidOperationID) {
		t.Errorf("expected ErrInvalidOperationID, got %v", err)
	}
}
