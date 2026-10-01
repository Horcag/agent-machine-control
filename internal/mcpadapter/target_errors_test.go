package mcpadapter

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func requireEnrolledFailureText(t *testing.T, result *mcp.CallToolResult, err error, want string) {
	t.Helper()
	if err != nil || result == nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("enrolled failure result=%v error=%v", result, err)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; text != want {
		t.Fatalf("enrolled failure text=%q, want %q", text, want)
	}
}

func TestEnrolledReadBackendFailuresRemainActionable(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{hyperv.ErrHostUnavailable, "backend_host_unavailable: backend management host is unavailable"},
		{hyperv.ErrModuleMissing, "backend_module_missing: Hyper-V PowerShell module is unavailable"},
		{hyperv.ErrAccessDenied, "backend_access_denied: machine backend access denied"},
		{hyperv.ErrMalformedResponse, "backend_malformed_response: machine backend returned an invalid response"},
		{hyperv.ErrCommandTimeout, "operation_timeout: operation timeout exceeded"},
		{context.DeadlineExceeded, "operation_timeout: operation timeout exceeded"},
		{context.Canceled, "operation_canceled: operation was canceled"},
	}
	for _, test := range cases {
		t.Run(test.err.Error(), func(t *testing.T) {
			adapter, backend := mcpExactTargetHarness(t)
			result, doctor, err := adapter.Doctor(t.Context(), nil, DoctorInput{})
			if result != nil || err != nil || !doctor.Ready {
				t.Fatalf("doctor=%+v tool error=%v error=%v", doctor, result, err)
			}
			backend.inspectErr = fmt.Errorf("synthetic credential=secret-value /synthetic/private/key: %w", test.err)
			result, _, err = adapter.MachineList(t.Context(), nil, MachineListInput{})
			requireEnrolledFailureText(t, result, err, test.want)
			result, _, err = adapter.MachineInspect(t.Context(), nil, MachineInspectInput{ID: "primary"})
			requireEnrolledFailureText(t, result, err, test.want)
			if backend.inspectCalls != 2 || backend.listCalls != 0 {
				t.Fatalf("inspect calls=%d fleet calls=%d", backend.inspectCalls, backend.listCalls)
			}
		})
	}
}

func TestEnrolledReadUnknownAndIdentityFailuresRemainProtected(t *testing.T) {
	for _, cause := range []error{errors.New("host_unavailable token=secret-value"), domain.ErrInvalidMachineLocator} {
		adapter, backend := mcpExactTargetHarness(t)
		backend.inspectErr = fmt.Errorf("synthetic private identity=secret-value: %w", cause)
		result, _, err := adapter.MachineList(t.Context(), nil, MachineListInput{})
		requireEnrolledFailureText(t, result, err, "protected target is unavailable")
		result, _, err = adapter.MachineInspect(t.Context(), nil, MachineInspectInput{ID: "primary"})
		requireEnrolledFailureText(t, result, err, "protected target is unavailable")
	}
}

func TestEnrollmentAuthorityErrorsPrecedeBackendFailures(t *testing.T) {
	for _, cause := range []error{target.ErrNoDefault, target.ErrDifferentTarget} {
		result := mcpToolError(errors.Join(errProtectedTargetUnavailable, cause, hyperv.ErrHostUnavailable))
		requireEnrolledFailureText(t, result, nil, "target is not enrolled")
	}
}
