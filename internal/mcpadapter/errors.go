package mcpadapter

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/target"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func mcpToolError(err error) *mcp.CallToolResult {
	if err == nil {
		return mcpToolErrorText("unknown error")
	}
	if cleanMsg := protectedToolError(err); cleanMsg != "" {
		return mcpToolErrorText(cleanMsg)
	}

	var cleanMsg string
	if inputErr, ok := errors.AsType[*InputError](err); ok {
		cleanMsg = inputErr.Error()
	} else {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "connection refused") || strings.Contains(msg, "dial tcp"):
			cleanMsg = "service connection failed: daemon is unreachable"
		case strings.Contains(msg, "unauthorized") || strings.Contains(msg, "token"):
			cleanMsg = "authentication failed"
		case strings.Contains(msg, "not found") || strings.Contains(msg, "404"):
			cleanMsg = "requested resource not found"
		case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
			cleanMsg = "operation timeout exceeded"
		case strings.Contains(msg, "domain:"):
			cleanMsg = msg
		default:
			cleanMsg = "an internal daemon error occurred"
		}
	}
	if len(cleanMsg) > 200 {
		cleanMsg = cleanMsg[:197] + "..."
	}
	return mcpToolErrorText(cleanMsg)
}

func protectedToolError(err error) string {
	if isApprovalRequired(err) {
		return "approval_required: operator approval required for this operation"
	}
	switch {
	case errors.Is(err, target.ErrNoDefault), errors.Is(err, target.ErrDifferentTarget):
		return "target is not enrolled"
	case errors.Is(err, errProtectedTargetUnavailable):
		return "protected target is unavailable"
	default:
		return ""
	}
}

func isApprovalRequired(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden && apiErr.Category == "approval_required"
}

func mcpToolErrorText(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{Text: message},
		},
	}
}
