package mcpadapter

import (
	"context"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (a *Adapter) ConsoleRecord(ctx context.Context, _ *mcp.CallToolRequest, in app.ConsoleRecordRequest) (*mcp.CallToolResult, app.ConsoleRecording, error) {
	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), app.ConsoleRecording{}, nil
	}
	out, err := cl.ConsoleRecord(ctx, in)
	if err != nil {
		return mcpToolError(err), app.ConsoleRecording{}, nil
	}
	content := &mcp.ImageContent{Data: out.Data, MIMEType: out.MIMEType}
	out.Data = nil
	return &mcp.CallToolResult{Content: []mcp.Content{content}}, out, nil
}
