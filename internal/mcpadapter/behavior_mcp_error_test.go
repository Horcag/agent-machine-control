package mcpadapter

import (
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMcpToolErrorAllPaths(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "unknown error",
		},
		{
			name:     "connection refused",
			err:      errors.New("dial tcp 127.0.0.1:80: connection refused"),
			expected: "service connection failed: daemon is unreachable",
		},
		{
			name:     "dial tcp",
			err:      errors.New("dial tcp error occurred"),
			expected: "service connection failed: daemon is unreachable",
		},
		{
			name:     "unauthorized",
			err:      errors.New("unauthorized access"),
			expected: "authentication failed",
		},
		{
			name:     "token",
			err:      errors.New("invalid token provided"),
			expected: "authentication failed",
		},
		{
			name:     "not found",
			err:      errors.New("item not found in database"),
			expected: "requested resource not found",
		},
		{
			name:     "404 status",
			err:      errors.New("received 404 status"),
			expected: "requested resource not found",
		},
		{
			name:     "timeout",
			err:      errors.New("request timeout"),
			expected: "operation timeout exceeded",
		},
		{
			name:     "deadline exceeded",
			err:      errors.New("context deadline exceeded"),
			expected: "operation timeout exceeded",
		},
		{
			name:     "domain error prefix",
			err:      errors.New("domain: invalid operation parameter"),
			expected: "domain: invalid operation parameter",
		},
		{
			name:     "default fallback error",
			err:      errors.New("some completely unknown database error"),
			expected: "an internal daemon error occurred",
		},
		{
			name:     "domain error needing truncation",
			err:      errors.New("domain: " + strings.Repeat("a", 250)),
			expected: "domain: " + strings.Repeat("a", 189) + "...", // 197 chars of "domain: aaaa..." + 3 chars of "..." = 200
		},
		{
			name:     "input error timeout",
			err:      NewInputError("timeout is required"),
			expected: "invalid input: timeout is required",
		},
		{
			name:     "input error target GUID",
			err:      NewInputError("invalid target GUID"),
			expected: "invalid input: invalid target GUID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := mcpToolError(tt.err)
			if !res.IsError {
				t.Error("expected IsError to be true")
			}
			if len(res.Content) != 1 {
				t.Fatalf("expected 1 content element, got %d", len(res.Content))
			}
			txt, ok := res.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("expected TextContent type")
			}
			if txt.Text != tt.expected {
				t.Errorf("expected text %q, got %q", tt.expected, txt.Text)
			}
			if len(txt.Text) > 200 {
				t.Errorf("expected text length <= 200, got %d", len(txt.Text))
			}
		})
	}
}
