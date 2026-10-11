package mcpadapter

import (
	"context"
	"errors"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type GuestInput struct {
	Target         string   `json:"target,omitempty"`
	ArtifactIDs    []string `json:"artifact_ids,omitempty"`
	Source         string   `json:"source,omitempty" jsonschema:"Explicit local payload path, never sent to the guest"`
	Destination    string   `json:"destination,omitempty" jsonschema:"Explicit new local output path"`
	MaxOutput      int64    `json:"max_output"`
	Reason         string   `json:"reason"`
	IdempotencyKey string   `json:"idempotency_key"`
	Deadline       string   `json:"deadline" jsonschema:"Exact canonical RFC3339Nano operation deadline"`
	ApprovalID     string   `json:"approval_id,omitempty"`
}

func (a *Adapter) guestOperation(ctx context.Context, in GuestInput, operation string) (*mcp.CallToolResult, app.GuestOperationResult, error) {
	var zero app.GuestOperationResult
	deadline, err := time.Parse(time.RFC3339Nano, in.Deadline)
	if err != nil {
		return mcpToolError(NewInputError("invalid guest deadline")), zero, nil
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), zero, nil
	}
	p := app.GuestOperationParams{Target: in.Target, Operation: operation, ArtifactIDs: in.ArtifactIDs, MaxOutput: in.MaxOutput, Reason: in.Reason, IdempotencyKey: in.IdempotencyKey, Deadline: deadline, ApprovalID: in.ApprovalID}
	var input stream.ContextReader
	source, err := client.OpenGuestPayload(ctx, operation, in.Source)
	if err != nil {
		return mcpToolError(err), zero, nil
	}
	if source != nil {
		defer source.Close()
		p.Size, p.SHA256 = source.Size, source.SHA256
		input = source.Input()
	}
	var result app.GuestOperationResult
	if operation == "get" || operation == "exec" {
		if in.Destination == "" {
			return mcpToolError(NewInputError("new local destination is required")), zero, nil
		}
		result, err = cl.GuestOperationToFile(ctx, p, input, in.Destination)
	} else {
		if in.Destination != "" {
			return mcpToolError(NewInputError("unexpected destination")), zero, nil
		}
		result, err = cl.GuestOperation(ctx, p, input, nil)
	}
	if source != nil {
		err = errors.Join(err, source.Verify())
	}
	if err != nil {
		return mcpToolError(err), result, nil
	}
	return nil, result, nil
}
func (a *Adapter) GuestPrepare(ctx context.Context, _ *mcp.CallToolRequest, in GuestInput) (*mcp.CallToolResult, app.GuestOperationResult, error) {
	return a.guestOperation(ctx, in, "prepare")
}
func (a *Adapter) GuestPut(ctx context.Context, _ *mcp.CallToolRequest, in GuestInput) (*mcp.CallToolResult, app.GuestOperationResult, error) {
	return a.guestOperation(ctx, in, "put")
}
func (a *Adapter) GuestGet(ctx context.Context, _ *mcp.CallToolRequest, in GuestInput) (*mcp.CallToolResult, app.GuestOperationResult, error) {
	return a.guestOperation(ctx, in, "get")
}
func (a *Adapter) GuestExec(ctx context.Context, _ *mcp.CallToolRequest, in GuestInput) (*mcp.CallToolResult, app.GuestOperationResult, error) {
	return a.guestOperation(ctx, in, "exec")
}
func (a *Adapter) GuestRemoveOwned(ctx context.Context, _ *mcp.CallToolRequest, in GuestInput) (*mcp.CallToolResult, app.GuestOperationResult, error) {
	return a.guestOperation(ctx, in, "remove-owned")
}
func (a *Adapter) addGuestTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{Name: "guest_prepare", Description: "Explicitly prepare the enrolled guest worker using server-provisioned workspace identity"}, a.GuestPrepare)
	mcp.AddTool(server, &mcp.Tool{Name: "guest_put", Description: "Stream an explicit local source file and return an opaque guest ownership reference"}, a.GuestPut)
	mcp.AddTool(server, &mcp.Tool{Name: "guest_get", Description: "Download an opaque owned artifact to a new local destination after verification"}, a.GuestGet)
	mcp.AddTool(server, &mcp.Tool{Name: "guest_exec", Description: "Execute an explicit local UTF-8 script and publish verified output to a new local destination"}, a.GuestExec)
	mcp.AddTool(server, &mcp.Tool{Name: "guest_remove_owned", Description: "Remove only artifacts identified by immutable opaque ownership references"}, a.GuestRemoveOwned)
}
