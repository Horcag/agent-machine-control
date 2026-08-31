package mcpadapter

import (
	"context"
	"sort"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (a *Adapter) MachineList(ctx context.Context, _ *mcp.CallToolRequest, _ MachineListInput) (*mcp.CallToolResult, MachineListResult, error) {
	svc := a.getDiscoveryService()
	resolution, resolveErr := a.resolveTarget(ctx, "")
	if resolveErr != nil {
		return mcpToolError(resolveErr), MachineListResult{}, nil
	}
	var machines []domain.MachineObservation
	var err error
	if resolution == nil {
		machines, err = svc.List(ctx)
	} else {
		machine, inspectErr := svc.Inspect(ctx, resolution.ProviderVMID)
		machines, err = []domain.MachineObservation{machine}, inspectErr
	}
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
	for i, machine := range machines {
		dtos[i] = convertToMachineDTO(machine)
	}
	return nil, MachineListResult{SchemaVersion: SchemaVersion, ObservationType: string(domain.ObservationObserved), Machines: dtos}, nil
}

func (a *Adapter) MachineInspect(ctx context.Context, _ *mcp.CallToolRequest, in MachineInspectInput) (*mcp.CallToolResult, MachineInspectResult, error) {
	resolution, err := a.resolveTarget(ctx, in.ID)
	if err != nil {
		return mcpToolError(err), MachineInspectResult{}, nil
	}
	if resolution == nil && domain.ValidateMachineGUID(in.ID) != nil {
		return mcpToolError(NewInputError("invalid machine GUID")), MachineInspectResult{}, nil
	}
	targetID := in.ID
	if resolution != nil {
		targetID = resolution.ProviderVMID
	}
	machine, err := a.getDiscoveryService().Inspect(ctx, targetID)
	if err != nil {
		return mcpToolError(err), MachineInspectResult{}, nil
	}
	return nil, MachineInspectResult{SchemaVersion: SchemaVersion, ObservationType: string(domain.ObservationObserved), Machine: convertToMachineDTO(machine)}, nil
}

func (a *Adapter) CheckpointList(ctx context.Context, _ *mcp.CallToolRequest, in CheckpointListInput) (*mcp.CallToolResult, CheckpointListResult, error) {
	resolution, err := a.resolveTarget(ctx, in.ID)
	if err != nil {
		return mcpToolError(err), CheckpointListResult{}, nil
	}
	if resolution == nil && domain.ValidateMachineGUID(in.ID) != nil {
		return mcpToolError(NewInputError("invalid machine GUID")), CheckpointListResult{}, nil
	}
	targetID := in.ID
	if resolution != nil {
		targetID = resolution.ProviderVMID
	}
	checkpoints, err := a.getRecoveryService().ListCheckpoints(ctx, targetID)
	if err != nil {
		return mcpToolError(err), CheckpointListResult{}, nil
	}
	sort.Slice(checkpoints, func(i, j int) bool { return checkpoints[i].CreatedAt.Before(checkpoints[j].CreatedAt) })
	dtos := make([]CheckpointDTO, len(checkpoints))
	for i, checkpoint := range checkpoints {
		dtos[i] = convertToCheckpointDTO(checkpoint)
	}
	return nil, CheckpointListResult{SchemaVersion: SchemaVersion, ObservationType: string(domain.ObservationObserved), Checkpoints: dtos}, nil
}
