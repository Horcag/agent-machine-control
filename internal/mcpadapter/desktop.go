package mcpadapter

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type DesktopObserveInput struct {
	Target         string `json:"target,omitempty"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
	WindowID       string `json:"window_id,omitempty" jsonschema:"Optional native HWND for UI Automation tree"`
	WindowIdentity string `json:"window_identity,omitempty" jsonschema:"Copy the identity paired with window_id from the observed window"`
}

type DesktopObserveResult struct {
	SchemaVersion     string                 `json:"schema_version"`
	Frame             ConsoleFrameMetadata   `json:"frame"`
	Desktop           domain.DesktopResponse `json:"desktop"`
	Cursor            *domain.DesktopCursor  `json:"cursor,omitempty"`
	HelperAvailable   bool                   `json:"helper_available"`
	CoordinateSpace   string                 `json:"coordinate_space"`
	LabGrantID        string                 `json:"lab_grant_id,omitempty" jsonschema:"Optional: automatically selects the current caller active grant for this VM"`
	LabGrantExpiresAt time.Time              `json:"lab_grant_expires_at,omitzero"`
}

func (a *Adapter) DesktopObserve(ctx context.Context, call *mcp.CallToolRequest, in DesktopObserveInput) (*mcp.CallToolResult, DesktopObserveResult, error) {
	if in.Width == 0 && in.Height == 0 {
		in.Width, in.Height = 1024, 768
	}
	image, frame, err := a.ConsoleScreenshot(ctx, call, ConsoleScreenshotInput{Target: in.Target, Width: in.Width, Height: in.Height})
	if err != nil || image == nil || image.IsError {
		return image, DesktopObserveResult{}, err
	}
	in.Target = frame.Frame.VMID // Bind semantic evidence and authority to the captured VM.
	out := DesktopObserveResult{SchemaVersion: SchemaVersion, Frame: frame.Frame, CoordinateSpace: "console input uses frame pixels; guest desktop actions use native screen pixels"}
	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), out, nil
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return mcpToolError(err), out, nil
	}
	req := domain.DesktopRequest{RequestID: hex.EncodeToString(id[:]), Deadline: time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339Nano), Action: "windows"}
	if in.WindowID != "" {
		req.Action = "uia.tree"
		req.WindowID = in.WindowID
		req.WindowIdentity = in.WindowIdentity
	}
	result, helperErr := cl.DesktopAction(ctx, app.DesktopActionRequest{Target: in.Target, Request: req})
	if helperErr == nil && result.Response.Success {
		out.Desktop = result.Response
		out.Cursor = result.Response.Cursor
		out.HelperAvailable = true
	}
	if grant, grantErr := cl.ActiveConsoleLabGrant(ctx, in.Target); grantErr == nil && grant.State == "active" {
		out.LabGrantID = grant.Grant.GrantID
		out.LabGrantExpiresAt = grant.Grant.ExpiresAt
	}
	return image, out, nil
}

type DesktopActInput struct {
	Target         string                 `json:"target,omitempty"`
	Action         *domain.DesktopRequest `json:"action,omitempty" jsonschema:"Semantic guest action; native guest pixel coordinates"`
	Input          *domain.ConsoleInput   `json:"input,omitempty" jsonschema:"Hypervisor input in captured frame pixels; keyboard works without guest helper"`
	LabGrantID     string                 `json:"lab_grant_id,omitempty"`
	Reason         string                 `json:"reason"`
	IdempotencyKey string                 `json:"idempotency_key"`
	Deadline       string                 `json:"deadline"`
	ObserveAfter   bool                   `json:"observe_after,omitempty" jsonschema:"Return a fresh PNG and window metadata from the same VM after the action"`
}

type DesktopActResult struct {
	SchemaVersion string                  `json:"schema_version"`
	Result        app.DesktopActionResult `json:"result"`
	Observation   *DesktopObserveResult   `json:"observation,omitempty"`
}

func (a *Adapter) DesktopAct(ctx context.Context, call *mcp.CallToolRequest, in DesktopActInput) (*mcp.CallToolResult, DesktopActResult, error) {
	out := DesktopActResult{SchemaVersion: SchemaVersion}
	if (in.Action == nil) == (in.Input == nil) {
		return mcpToolError(NewInputError("provide exactly one action or input")), out, nil
	}
	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), out, nil
	}
	if in.LabGrantID == "" {
		grant, grantErr := cl.ActiveConsoleLabGrant(ctx, in.Target)
		if grantErr != nil {
			return mcpToolError(grantErr), out, nil
		}
		if grant.State != "active" {
			return mcpToolError(NewInputError("operator must enable the VM lab mode")), out, nil
		}
		in.LabGrantID = grant.Grant.GrantID
	}
	if in.Input != nil {
		rcpt, inputErr := cl.ConsoleInput(ctx, app.ConsoleInputRequest{Target: in.Target, Input: *in.Input, Reason: in.Reason, IdempotencyKey: in.IdempotencyKey, Deadline: in.Deadline, LabGrantID: in.LabGrantID})
		err = inputErr
		out.Result.Receipt = &rcpt
	} else {
		req := *in.Action
		if req.RequestID == "" {
			sum := sha256.Sum256([]byte("desktop-request\x00" + in.IdempotencyKey))
			req.RequestID = hex.EncodeToString(sum[:16])
		}
		if req.Deadline == "" {
			req.Deadline = in.Deadline
		}
		if req.Deadline != in.Deadline {
			return mcpToolError(NewInputError("action deadline must match operation deadline")), out, nil
		}
		out.Result, err = cl.DesktopAction(ctx, app.DesktopActionRequest{Target: in.Target, Request: req, Reason: in.Reason, IdempotencyKey: in.IdempotencyKey, LabGrantID: in.LabGrantID})
	}
	if err != nil {
		return mcpToolError(err), out, nil
	}
	return a.observeDesktopActionResult(ctx, call, in, out)
}

func (a *Adapter) observeDesktopActionResult(ctx context.Context, call *mcp.CallToolRequest, in DesktopActInput, out DesktopActResult) (*mcp.CallToolResult, DesktopActResult, error) {
	if in.ObserveAfter {
		if out.Result.Receipt != nil {
			in.Target = string(out.Result.Receipt.Target)
		}
		image, observation, observeErr := a.DesktopObserve(ctx, call, DesktopObserveInput{Target: in.Target})
		if observeErr == nil && image != nil && !image.IsError {
			out.Observation = &observation
			return image, out, nil
		}
	}
	return nil, out, nil
}
