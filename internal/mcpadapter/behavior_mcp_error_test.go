package mcpadapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
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
			name:     "default fallback error",
			err:      errors.New("some completely unknown database error"),
			expected: "an internal daemon error occurred",
		},
		{
			name: "approval required from daemon",
			err: fmt.Errorf("%w: %w", client.ErrDenied, &client.APIError{
				StatusCode: http.StatusForbidden, Category: "approval_required",
				Message: "destructive/privileged operation requires active operator approval",
			}),
			expected: "approval_required: operator approval required for this operation",
		},
		{
			name: "unrelated policy denial remains protected",
			err: fmt.Errorf("%w: %w", client.ErrDenied, &client.APIError{
				StatusCode: http.StatusForbidden, Category: "forbidden", Message: "sensitive internal detail",
			}),
			expected: "access_denied: operation denied by policy",
		},
		{
			name:     "domain prefix remains private",
			err:      errors.New("domain: " + strings.Repeat("a", 250)),
			expected: "an internal daemon error occurred",
		},
		{
			name:     "input error truncation",
			err:      NewInputError(strings.Repeat("a", 250)),
			expected: "invalid input: " + strings.Repeat("a", 182) + "...",
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

func TestMcpToolErrorTypedCategories(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{hyperv.ErrHostUnavailable, "backend_host_unavailable: backend management host is unavailable"},
		{domain.ErrMachineHostUnavailable, "backend_host_unavailable: backend management host is unavailable"},
		{hyperv.ErrModuleMissing, "backend_module_missing: Hyper-V PowerShell module is unavailable"},
		{hyperv.ErrExecutableNotFound, "backend_unavailable: machine backend is unavailable"},
		{hyperv.ErrBackendUnavailable, "backend_unavailable: machine backend is unavailable"},
		{hyperv.ErrAccessDenied, "backend_access_denied: machine backend access denied"},
		{domain.ErrMachineAccessDenied, "backend_access_denied: machine backend access denied"},
		{hyperv.ErrMalformedResponse, "backend_malformed_response: machine backend returned an invalid response"},
		{hyperv.ErrUnexpectedSchemaVersion, "backend_malformed_response: machine backend returned an invalid response"},
		{hyperv.ErrTrailingData, "backend_malformed_response: machine backend returned an invalid response"},
		{hyperv.ErrDuplicateMachineID, "backend_malformed_response: machine backend returned an invalid response"},
		{hyperv.ErrOutputExceededLimit, "backend_output_limit: machine backend output exceeded its size limit"},
		{hyperv.ErrCommandTimeout, "operation_timeout: operation timeout exceeded"},
		{context.DeadlineExceeded, "operation_timeout: operation timeout exceeded"},
		{client.ErrTimeout, "operation_timeout: operation timeout exceeded"},
		{context.Canceled, "operation_canceled: operation was canceled"},
		{client.ErrDaemonUnavailable, "service connection failed: daemon is unreachable"},
		{client.ErrMalformedResponse, "daemon_malformed_response: daemon returned an invalid response"},
		{client.ErrNotFound, "requested resource not found"},
		{hyperv.ErrMachineNotFound, "requested resource not found"},
		{client.ErrInvalidArgument, "invalid_argument: invalid operation input"},
		{domain.ErrInvalidMachineID, "invalid_argument: invalid operation input"},
		{client.ErrConflict, "operation_conflict: operation conflicts with current state"},
		{client.ErrDenied, "access_denied: operation denied by policy"},
		{&client.APIError{StatusCode: http.StatusUnauthorized, Message: "secret"}, "authentication failed"},
		{&client.APIError{StatusCode: http.StatusInternalServerError, Category: "secret", Message: "secret"}, "an internal daemon error occurred"},
		{errors.Join(target.ErrNoDefault, hyperv.ErrAccessDenied), "target is not enrolled"},
		{errors.Join(errProtectedTargetUnavailable, hyperv.ErrHostUnavailable), "backend_host_unavailable: backend management host is unavailable"},
		{errors.Join(context.Canceled, &client.APIError{StatusCode: http.StatusForbidden, Category: "approval_required"}), "approval_required: operator approval required for this operation"},
	}
	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			result := mcpToolError(fmt.Errorf("secret-value /synthetic/private/key: %w", tt.err))
			text := result.Content[0].(*mcp.TextContent).Text
			if !result.IsError || text != tt.want {
				t.Fatalf("tool error = %q, want %q", text, tt.want)
			}
		})
	}
}

func TestMcpToolErrorArbitraryMessagesRemainPrivate(t *testing.T) {
	for _, hint := range []string{"dial tcp", "connection refused", "token", "unauthorized", "not found", "404", "timeout", "deadline exceeded", "domain:"} {
		result := mcpToolError(errors.New(hint + " secret-value /synthetic/private/key"))
		if text := result.Content[0].(*mcp.TextContent).Text; text != "an internal daemon error occurred" {
			t.Errorf("arbitrary message hint %q selected public text %q", hint, text)
		}
	}
}

func TestReadyDoctorThenBackendObservationFailure(t *testing.T) {
	observer := getTestObserver()
	observer.inspectErr = fmt.Errorf("synthetic credential=secret-value /synthetic/private/key: %w", hyperv.ErrHostUnavailable)
	observer.listErr = observer.inspectErr
	a := &Adapter{allowUnscopedTestTargetFallback: true, discoveryService: app.NewDiscoveryService(observer)}
	result, doctor, err := a.Doctor(t.Context(), nil, DoctorInput{})
	if result != nil || err != nil || !doctor.Ready {
		t.Fatalf("doctor result=%v report=%+v error=%v", result, doctor, err)
	}
	inspect, _, err := a.MachineInspect(t.Context(), nil, MachineInspectInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001"})
	if err != nil || inspect == nil {
		t.Fatalf("inspect result=%v error=%v", inspect, err)
	}
	list, _, err := a.MachineList(t.Context(), nil, MachineListInput{})
	if err != nil || list == nil {
		t.Fatalf("list result=%v error=%v", list, err)
	}
	for _, result := range []*mcp.CallToolResult{inspect, list} {
		if text := result.Content[0].(*mcp.TextContent).Text; !result.IsError || text != "backend_host_unavailable: backend management host is unavailable" {
			t.Fatalf("observation error = %q", text)
		}
	}
}

func TestSessionWrite_ApprovalRequiredReportsOperation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sessions/sess-12345678901234567890123456789012/write" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"category":"approval_required","message":"sensitive internal detail"}}`))
	}))
	defer srv.Close()

	a := &Adapter{client: client.New(srv.URL, "test-token")}
	result, _, err := a.SessionWrite(t.Context(), nil, SessionWriteInput{
		SessionID: "sess-12345678901234567890123456789012", Data: "\r",
		Reason: "test denied write", IdempotencyKey: "test-denied-write", Timeout: "30s",
	})
	if err != nil || result == nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("expected MCP tool denial, got result=%v err=%v", result, err)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; text != "approval_required: session.write requires operator approval" {
		t.Fatalf("unexpected denial text: %q", text)
	}
}
