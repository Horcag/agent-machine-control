package mcpadapter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/operations"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool handlers

func (a *Adapter) Doctor(ctx context.Context, _ *mcp.CallToolRequest, _ DoctorInput) (*mcp.CallToolResult, DoctorResult, error) {
	svc := a.getDiscoveryService()
	report, err := svc.Doctor(ctx)
	if err != nil {
		return mcpToolError(err), DoctorResult{}, nil
	}

	caps := report.Capabilities.Slice()
	if caps == nil {
		caps = []string{}
	}

	return nil, DoctorResult{
		SchemaVersion: SchemaVersion,
		Status:        report.Status,
		Ready:         report.Ready,
		Reason:        report.Reason,
		Message:       report.Message,
		Capabilities:  caps,
		ObservedAt:    report.ObservedAt.UTC().Format(time.RFC3339),
	}, nil
}

func (a *Adapter) MachineList(ctx context.Context, _ *mcp.CallToolRequest, _ MachineListInput) (*mcp.CallToolResult, MachineListResult, error) {
	svc := a.getDiscoveryService()
	machines, err := svc.List(ctx)
	if err != nil {
		return mcpToolError(err), MachineListResult{}, nil
	}

	sort.Slice(machines, func(i, j int) bool {
		if machines[i].Name == machines[j].Name {
			return machines[i].ID < machines[j].ID
		}
		return machines[i].Name < machines[j].Name
	})

	dtos := make([]MachineDTO, len(machines))
	for i, m := range machines {
		dtos[i] = convertToMachineDTO(m)
	}

	return nil, MachineListResult{
		SchemaVersion:   SchemaVersion,
		ObservationType: string(domain.ObservationObserved),
		Machines:        dtos,
	}, nil
}

func (a *Adapter) MachineInspect(ctx context.Context, _ *mcp.CallToolRequest, in MachineInspectInput) (*mcp.CallToolResult, MachineInspectResult, error) {
	if err := domain.ValidateMachineGUID(in.ID); err != nil {
		return mcpToolError(NewInputError("invalid machine GUID")), MachineInspectResult{}, nil
	}

	svc := a.getDiscoveryService()
	m, err := svc.Inspect(ctx, in.ID)
	if err != nil {
		return mcpToolError(err), MachineInspectResult{}, nil
	}

	return nil, MachineInspectResult{
		SchemaVersion:   SchemaVersion,
		ObservationType: string(domain.ObservationObserved),
		Machine:         convertToMachineDTO(m),
	}, nil
}

func (a *Adapter) CheckpointList(ctx context.Context, _ *mcp.CallToolRequest, in CheckpointListInput) (*mcp.CallToolResult, CheckpointListResult, error) {
	if err := domain.ValidateMachineGUID(in.ID); err != nil {
		return mcpToolError(NewInputError("invalid machine GUID")), CheckpointListResult{}, nil
	}

	svc := a.getRecoveryService()
	checkpoints, err := svc.ListCheckpoints(ctx, in.ID)
	if err != nil {
		return mcpToolError(err), CheckpointListResult{}, nil
	}

	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].CreatedAt.Before(checkpoints[j].CreatedAt)
	})

	dtos := make([]CheckpointDTO, len(checkpoints))
	for i, c := range checkpoints {
		dtos[i] = convertToCheckpointDTO(c)
	}

	return nil, CheckpointListResult{
		SchemaVersion:   SchemaVersion,
		ObservationType: string(domain.ObservationObserved),
		Checkpoints:     dtos,
	}, nil
}

func (a *Adapter) MachineStart(ctx context.Context, _ *mcp.CallToolRequest, in MachineStartInput) (*mcp.CallToolResult, MachineMutationResult, error) {
	if err := validateMutationParams(in.ID, in.Reason, in.IdempotencyKey); err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}
	if err := domain.ValidateOperationParameters("machine.start", nil); err != nil {
		return mcpToolError(NewInputError("invalid parameters")), MachineMutationResult{}, nil
	}
	timeout, err := parseTimeout(in.Timeout, true)
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	opDTO, err := cl.CreateOperation(ctx, daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         in.ID,
		Reason:         in.Reason,
		IdempotencyKey: in.IdempotencyKey,
		TimeoutSeconds: int(timeout.Seconds()),
	})
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	finalDTO, err := cl.WaitOperation(ctx, opDTO.OperationID, timeout, 0)
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	if finalDTO.State != "completed" {
		return mcpToolError(fmt.Errorf("operation failed: %s (category: %s)", finalDTO.ErrorMessage, finalDTO.ErrorCategory)), MachineMutationResult{}, nil
	}

	if finalDTO.ReceiptID == "" {
		return mcpToolError(errors.New("operation completed but no receipt ID was returned")), MachineMutationResult{}, nil
	}
	rcpt, err := cl.GetReceipt(ctx, finalDTO.ReceiptID)
	if err != nil {
		return mcpToolError(fmt.Errorf("failed to retrieve operation receipt: %w", err)), MachineMutationResult{}, nil
	}

	obsDTO := MachineDTO{
		ID:              in.ID,
		State:           string(domain.MachineStateRunning),
		ObservationType: string(domain.ObservationInferred),
	}

	return nil, MachineMutationResult{
		SchemaVersion: SchemaVersion,
		Receipt:       *rcpt,
		Machine:       &obsDTO,
	}, nil
}

//nolint:dupl
func (a *Adapter) MachineStop(ctx context.Context, _ *mcp.CallToolRequest, in MachineStopInput) (*mcp.CallToolResult, MachineMutationResult, error) {
	if err := validateMutationParams(in.ID, in.Reason, in.IdempotencyKey); err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}
	if err := domain.ValidateOperationParameters("machine.stop", map[string]any{"mode": in.Mode}); err != nil {
		return mcpToolError(NewInputError("invalid stop mode")), MachineMutationResult{}, nil
	}
	timeout, err := parseTimeout(in.Timeout, true)
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	opDTO, err := cl.CreateOperation(ctx, daemon.CreateOperationRequest{
		Kind:           "machine.stop",
		Target:         in.ID,
		Reason:         in.Reason,
		IdempotencyKey: in.IdempotencyKey,
		TimeoutSeconds: int(timeout.Seconds()),
		Parameters:     map[string]any{"mode": in.Mode},
	})
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	finalDTO, err := cl.WaitOperation(ctx, opDTO.OperationID, timeout, 0)
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	if finalDTO.State != "completed" {
		return mcpToolError(fmt.Errorf("operation failed: %s (category: %s)", finalDTO.ErrorMessage, finalDTO.ErrorCategory)), MachineMutationResult{}, nil
	}

	if finalDTO.ReceiptID == "" {
		return mcpToolError(errors.New("operation completed but no receipt ID was returned")), MachineMutationResult{}, nil
	}
	rcpt, err := cl.GetReceipt(ctx, finalDTO.ReceiptID)
	if err != nil {
		return mcpToolError(fmt.Errorf("failed to retrieve operation receipt: %w", err)), MachineMutationResult{}, nil
	}

	obsDTO := MachineDTO{
		ID:              in.ID,
		State:           string(domain.MachineStateOff),
		ObservationType: string(domain.ObservationInferred),
	}

	return nil, MachineMutationResult{
		SchemaVersion: SchemaVersion,
		Receipt:       *rcpt,
		Machine:       &obsDTO,
	}, nil
}

func (a *Adapter) CheckpointCreate(ctx context.Context, _ *mcp.CallToolRequest, in CheckpointCreateInput) (*mcp.CallToolResult, CheckpointMutationResult, error) {
	if err := validateMutationParams(in.ID, in.Reason, in.IdempotencyKey); err != nil {
		return mcpToolError(err), CheckpointMutationResult{}, nil
	}
	if err := domain.ValidateOperationParameters("checkpoint.create", map[string]any{"name": in.Name}); err != nil {
		return mcpToolError(NewInputError("invalid checkpoint name")), CheckpointMutationResult{}, nil
	}
	timeout, err := parseTimeout(in.Timeout, true)
	if err != nil {
		return mcpToolError(err), CheckpointMutationResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), CheckpointMutationResult{}, nil
	}

	opDTO, err := cl.CreateOperation(ctx, daemon.CreateOperationRequest{
		Kind:           "checkpoint.create",
		Target:         in.ID,
		Reason:         in.Reason,
		IdempotencyKey: in.IdempotencyKey,
		TimeoutSeconds: int(timeout.Seconds()),
		Parameters:     map[string]any{"name": in.Name},
	})
	if err != nil {
		return mcpToolError(err), CheckpointMutationResult{}, nil
	}

	finalDTO, err := cl.WaitOperation(ctx, opDTO.OperationID, timeout, 0)
	if err != nil {
		return mcpToolError(err), CheckpointMutationResult{}, nil
	}

	if finalDTO.State != "completed" {
		return mcpToolError(fmt.Errorf("operation failed: %s (category: %s)", finalDTO.ErrorMessage, finalDTO.ErrorCategory)), CheckpointMutationResult{}, nil
	}

	if finalDTO.ReceiptID == "" {
		return mcpToolError(errors.New("operation completed but no receipt ID was returned")), CheckpointMutationResult{}, nil
	}
	rcpt, err := cl.GetReceipt(ctx, finalDTO.ReceiptID)
	if err != nil {
		return mcpToolError(fmt.Errorf("failed to retrieve operation receipt: %w", err)), CheckpointMutationResult{}, nil
	}

	snapDTO := CheckpointDTO{
		ID:              rcpt.RollbackRef,
		Name:            in.Name,
		VMID:            in.ID,
		ObservationType: string(domain.ObservationInferred),
	}

	return nil, CheckpointMutationResult{
		SchemaVersion: SchemaVersion,
		Receipt:       *rcpt,
		Checkpoint:    &snapDTO,
	}, nil
}

//nolint:dupl
func (a *Adapter) CheckpointRestore(ctx context.Context, _ *mcp.CallToolRequest, in CheckpointRestoreInput) (*mcp.CallToolResult, MachineMutationResult, error) {
	if err := validateMutationParams(in.ID, in.Reason, in.IdempotencyKey); err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}
	if err := domain.ValidateOperationParameters("checkpoint.restore", map[string]any{"checkpoint_id": in.CheckpointID}); err != nil {
		return mcpToolError(NewInputError("invalid checkpoint ID")), MachineMutationResult{}, nil
	}
	timeout, err := parseTimeout(in.Timeout, true)
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	opDTO, err := cl.CreateOperation(ctx, daemon.CreateOperationRequest{
		Kind:           "checkpoint.restore",
		Target:         in.ID,
		Reason:         in.Reason,
		IdempotencyKey: in.IdempotencyKey,
		TimeoutSeconds: int(timeout.Seconds()),
		Parameters:     map[string]any{"checkpoint_id": in.CheckpointID},
	})
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	finalDTO, err := cl.WaitOperation(ctx, opDTO.OperationID, timeout, 0)
	if err != nil {
		return mcpToolError(err), MachineMutationResult{}, nil
	}

	if finalDTO.State != "completed" {
		return mcpToolError(fmt.Errorf("operation failed: %s (category: %s)", finalDTO.ErrorMessage, finalDTO.ErrorCategory)), MachineMutationResult{}, nil
	}

	if finalDTO.ReceiptID == "" {
		return mcpToolError(errors.New("operation completed but no receipt ID was returned")), MachineMutationResult{}, nil
	}
	rcpt, err := cl.GetReceipt(ctx, finalDTO.ReceiptID)
	if err != nil {
		return mcpToolError(fmt.Errorf("failed to retrieve operation receipt: %w", err)), MachineMutationResult{}, nil
	}

	obsDTO := MachineDTO{
		ID:              in.ID,
		State:           string(domain.MachineStateOff),
		ObservationType: string(domain.ObservationInferred),
	}

	return nil, MachineMutationResult{
		SchemaVersion: SchemaVersion,
		Receipt:       *rcpt,
		Machine:       &obsDTO,
	}, nil
}

func (a *Adapter) OperationList(ctx context.Context, _ *mcp.CallToolRequest, in OperationListInput) (*mcp.CallToolResult, OperationListResult, error) {
	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), OperationListResult{}, nil
	}

	opsList, err := cl.ListOperations(ctx, operations.ListOptions{
		State:   domain.OperationState(in.State),
		Machine: domain.MachineRef(in.Machine),
		Limit:   in.Limit,
	})
	if err != nil {
		return mcpToolError(err), OperationListResult{}, nil
	}

	return nil, OperationListResult{
		SchemaVersion: SchemaVersion,
		Operations:    opsList,
	}, nil
}

func (a *Adapter) OperationShow(ctx context.Context, _ *mcp.CallToolRequest, in OperationShowInput) (*mcp.CallToolResult, OperationResult, error) {
	if err := domain.ValidateOperationID(in.OperationID); err != nil {
		return mcpToolError(NewInputError("invalid operation ID")), OperationResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), OperationResult{}, nil
	}

	opDTO, err := cl.GetOperation(ctx, in.OperationID)
	if err != nil {
		return mcpToolError(err), OperationResult{}, nil
	}

	return nil, OperationResult{
		SchemaVersion: SchemaVersion,
		Operation:     opDTO,
	}, nil
}

func (a *Adapter) OperationWait(ctx context.Context, _ *mcp.CallToolRequest, in OperationWaitInput) (*mcp.CallToolResult, OperationResult, error) {
	if err := domain.ValidateOperationID(in.OperationID); err != nil {
		return mcpToolError(NewInputError("invalid operation ID")), OperationResult{}, nil
	}
	timeout, err := parseTimeout(in.Timeout, false)
	if err != nil {
		return mcpToolError(err), OperationResult{}, nil
	}

	var afterSeq uint64
	if in.AfterSeq != "" {
		parsed, err := strconv.ParseUint(in.AfterSeq, 10, 64)
		if err != nil {
			return mcpToolError(NewInputError("invalid after_seq")), OperationResult{}, nil
		}
		afterSeq = parsed
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), OperationResult{}, nil
	}

	opDTO, err := cl.WaitOperation(ctx, in.OperationID, timeout, afterSeq)
	if err != nil {
		return mcpToolError(err), OperationResult{}, nil
	}

	return nil, OperationResult{
		SchemaVersion: SchemaVersion,
		Operation:     opDTO,
	}, nil
}

func (a *Adapter) ReceiptShow(ctx context.Context, _ *mcp.CallToolRequest, in ReceiptShowInput) (*mcp.CallToolResult, ReceiptResult, error) {
	if err := domain.ValidateReceiptID(in.ReceiptID); err != nil {
		return mcpToolError(NewInputError("invalid receipt ID")), ReceiptResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), ReceiptResult{}, nil
	}

	rcpt, err := cl.GetReceipt(ctx, in.ReceiptID)
	if err != nil {
		return mcpToolError(err), ReceiptResult{}, nil
	}

	return nil, ReceiptResult{
		SchemaVersion: SchemaVersion,
		Receipt:       *rcpt,
	}, nil
}
