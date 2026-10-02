package mcpadapter

import (
	"context"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ConsoleScreenshotInput struct {
	Target string `json:"target,omitempty" jsonschema:"Optional enrolled target reference"`
	Width  int    `json:"width" jsonschema:"Maximum image width"`
	Height int    `json:"height" jsonschema:"Maximum image height"`
}

type ConsoleScreenshotResult struct {
	SchemaVersion string               `json:"schema_version"`
	Frame         ConsoleFrameMetadata `json:"frame"`
}

// ConsoleFrameMetadata excludes image data from structured MCP output.
type ConsoleFrameMetadata struct {
	VMID         string    `json:"vm_id"`
	FrameID      string    `json:"frame_id"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	NativeWidth  int       `json:"native_width"`
	NativeHeight int       `json:"native_height"`
	MIMEType     string    `json:"mime_type"`
	SHA256       string    `json:"sha256"`
	ObservedAt   time.Time `json:"observed_at"`
}

type ConsoleInputInput struct {
	Target         string              `json:"target,omitempty" jsonschema:"Optional enrolled target reference"`
	Input          domain.ConsoleInput `json:"input" jsonschema:"Bounded console action in captured frame coordinates"`
	Reason         string              `json:"reason"`
	IdempotencyKey string              `json:"idempotency_key"`
	Deadline       string              `json:"deadline" jsonschema:"Exact canonical UTC operation deadline"`
	ApprovalID     string              `json:"approval_id,omitempty"`
	LabGrantID     string              `json:"lab_grant_id,omitempty"`
}

type ConsoleInputResult struct {
	SchemaVersion string      `json:"schema_version"`
	Receipt       receipt.DTO `json:"receipt"`
}

func (a *Adapter) ConsoleScreenshot(ctx context.Context, _ *mcp.CallToolRequest, in ConsoleScreenshotInput) (*mcp.CallToolResult, ConsoleScreenshotResult, error) {
	if in.Width <= 0 || in.Height <= 0 || in.Width > domain.MaxConsolePixels/in.Height {
		return mcpToolError(NewInputError("invalid console dimensions")), ConsoleScreenshotResult{}, nil
	}
	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), ConsoleScreenshotResult{}, nil
	}
	frame, err := cl.ConsoleScreenshot(ctx, app.ConsoleScreenshotRequest{Target: in.Target, Width: in.Width, Height: in.Height})
	if err != nil {
		return mcpToolError(err), ConsoleScreenshotResult{}, nil
	}
	content := &mcp.ImageContent{Data: frame.Data, MIMEType: frame.MIMEType}
	frame.Data = nil
	out := ConsoleScreenshotResult{SchemaVersion: SchemaVersion, Frame: ConsoleFrameMetadata{VMID: frame.VMID, FrameID: frame.FrameID, Width: frame.Width, Height: frame.Height, NativeWidth: frame.NativeWidth, NativeHeight: frame.NativeHeight, MIMEType: frame.MIMEType, SHA256: frame.SHA256, ObservedAt: frame.ObservedAt}}
	return &mcp.CallToolResult{Content: []mcp.Content{content}}, out, nil
}

func (a *Adapter) ConsoleInput(ctx context.Context, _ *mcp.CallToolRequest, in ConsoleInputInput) (*mcp.CallToolResult, ConsoleInputResult, error) {
	in.Input = defaultConsoleButton(in.Input)
	if err := in.Input.Validate(); err != nil {
		return mcpToolError(NewInputError(err.Error())), ConsoleInputResult{}, nil
	}
	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), ConsoleInputResult{}, nil
	}
	rcpt, err := cl.ConsoleInput(ctx, app.ConsoleInputRequest{Target: in.Target, Input: in.Input, Reason: in.Reason, IdempotencyKey: in.IdempotencyKey, Deadline: in.Deadline, ApprovalID: in.ApprovalID, LabGrantID: in.LabGrantID})
	if err != nil {
		return mcpToolError(err), ConsoleInputResult{}, nil
	}
	return nil, ConsoleInputResult{SchemaVersion: SchemaVersion, Receipt: receipt.ConvertToDTO(rcpt)}, nil
}

// defaultConsoleButton applies the advertised MCP default before payload binding.
func defaultConsoleButton(input domain.ConsoleInput) domain.ConsoleInput {
	if input.Button == "" && (input.Kind == "click" || input.Kind == "drag") {
		input.Button = "left"
	}
	return input
}
