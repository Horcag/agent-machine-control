package mcpadapter

import (
	"context"
	"errors"
	"github.com/Horcag/agent-machine-control/internal/domain"
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
	if err := validateGuestInput(in, operation); err != nil {
		return mcpToolError(err), zero, nil
	}
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
	mcp.AddTool(server, &mcp.Tool{Name: "guest_prepare", InputSchema: guestInputSchema("prepare"), Description: "Prepare the persistent binary worker once before guest_put, guest_get or guest_exec; subsequent operations reuse it with the server-provisioned workspace identity"}, a.GuestPrepare)
	mcp.AddTool(server, &mcp.Tool{Name: "guest_put", InputSchema: guestInputSchema("put"), Description: "Upload a whole local file, including gigabyte archives and installers (up to 1 TiB), in one binary stream with SHA-256 verification. Pass its source path after guest_prepare; returns an opaque ownership reference for guest_get and guest_remove_owned."}, a.GuestPut)
	mcp.AddTool(server, &mcp.Tool{Name: "guest_get", InputSchema: guestInputSchema("get"), Description: "Download an opaque owned artifact to a new local destination after verification"}, a.GuestGet)
	mcp.AddTool(server, &mcp.Tool{Name: "guest_exec", InputSchema: guestInputSchema("exec"), Description: "Send and execute a whole local UTF-8 PowerShell script (up to 16 MiB) through the prepared binary worker in one operation; publish verified output to a new local destination"}, a.GuestExec)
	mcp.AddTool(server, &mcp.Tool{Name: "guest_remove_owned", InputSchema: guestInputSchema("remove-owned"), Description: "Remove only artifacts identified by immutable opaque ownership references"}, a.GuestRemoveOwned)
}

func validateGuestInput(in GuestInput, operation string) error {
	if err := domain.ValidateGuestOperationShape(operation, 0, in.MaxOutput, in.ArtifactIDs); err != nil {
		return NewInputError("invalid_argument: " + guestInputDescription(operation))
	}
	payload := operation == "put" || operation == "exec"
	output := operation == "get" || operation == "exec"
	if (in.Source != "") != payload || (in.Destination != "") != output {
		return NewInputError("invalid_argument: " + guestInputDescription(operation))
	}
	return nil
}

func guestInputDescription(operation string) string {
	switch operation {
	case "prepare":
		return "guest_prepare requires max_output=0 and no source, destination or artifact_ids"
	case "put":
		return "guest_put requires source, max_output=0 and no destination or artifact_ids"
	case "get":
		return "guest_get requires one artifact_id, positive max_output, destination and no source"
	case "exec":
		return "guest_exec requires source, destination, max_output between 1 and 16777216 and no artifact_ids"
	default:
		return "guest_remove_owned requires 1 to 128 unique artifact_ids, max_output=0 and no source or destination"
	}
}

func guestInputSchema(operation string) map[string]any {
	properties := map[string]any{
		"target":          map[string]any{"type": "string"},
		"reason":          map[string]any{"type": "string"},
		"idempotency_key": map[string]any{"type": "string"},
		"deadline":        map[string]any{"type": "string", "description": "Exact canonical RFC3339Nano operation deadline"},
		"approval_id":     map[string]any{"type": "string"},
		"max_output":      map[string]any{"type": "integer", "minimum": 0, "maximum": 0, "description": "Must be zero; this operation produces no output stream"},
	}
	required := []string{"reason", "idempotency_key", "deadline"}
	if operation == "put" || operation == "exec" {
		properties["source"] = map[string]any{"type": "string", "minLength": 1, "description": "Path to the complete local file; streamed as binary with bounded memory. Put accepts up to 1 TiB; exec accepts UTF-8 scripts up to 16 MiB. The local path stays on the host."}
		required = append(required, "source")
	}
	if operation == "get" || operation == "exec" {
		maximum := int64(1 << 40)
		if operation == "exec" {
			maximum = 16 * 1024 * 1024
		}
		properties["max_output"] = map[string]any{"type": "integer", "minimum": 1, "maximum": maximum, "description": "Positive maximum output bytes; output exceeding this limit is rejected"}
		properties["destination"] = map[string]any{"type": "string", "minLength": 1, "description": "Explicit new local output path; existing files are never replaced"}
		required = append(required, "max_output", "destination")
	}
	if operation == "get" || operation == "remove-owned" {
		maximum := 128
		if operation == "get" {
			maximum = 1
		}
		properties["artifact_ids"] = map[string]any{"type": "array", "minItems": 1, "maxItems": maximum, "uniqueItems": true, "items": map[string]any{"type": "string", "pattern": "^[0-9a-f]{32}$"}, "description": "Opaque ownership references returned by guest_put"}
		required = append(required, "artifact_ids")
	}
	return map[string]any{"type": "object", "description": guestInputDescription(operation), "properties": properties, "required": required, "additionalProperties": false}
}
