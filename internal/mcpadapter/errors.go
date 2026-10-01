package mcpadapter

import (
	"context"
	"errors"
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
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
		cleanMsg = classifiedToolError(err)
	}
	if len(cleanMsg) > 200 {
		cleanMsg = cleanMsg[:197] + "..."
	}
	return mcpToolErrorText(cleanMsg)
}

// operationFailure carries daemon failure metadata without forwarding its raw message.
type operationFailure struct {
	category string
}

func (e *operationFailure) Error() string { return "operation failed" }

func operationToolError(err error, operationID, receiptID string) *mcp.CallToolResult {
	result := mcpToolError(err)
	var references string
	if domain.ValidateOperationID(operationID) == nil {
		references = "operation_id: " + operationID
	}
	if domain.ValidateReceiptID(receiptID) == nil {
		if references != "" {
			references += "; "
		}
		references += "receipt_id: " + receiptID
	}
	if references != "" {
		result.Content = append(result.Content, &mcp.TextContent{Text: references})
	}
	return result
}

// Category keys are allowlisted protocol metadata, never provider message text.
func categoryToolError(category string) string {
	messages := map[string]string{
		"approval_required":               "approval_required: operator approval required for this operation",
		"target_not_enrolled":             "target is not enrolled",
		"target_mismatch":                 "target is not enrolled",
		"target_unavailable":              "protected target is unavailable",
		"host_unavailable":                "backend_host_unavailable: backend management host is unavailable",
		"module_missing":                  "backend_module_missing: Hyper-V PowerShell module is unavailable",
		"backend_error":                   "backend_error: backend operation failed",
		"backend_unavailable":             "backend_unavailable: machine backend is unavailable",
		"access_denied":                   "backend_access_denied: machine backend access denied",
		"malformed_response":              "backend_malformed_response: machine backend returned an invalid response",
		"output_limit":                    "backend_output_limit: machine backend output exceeded its size limit",
		"machine_not_found":               "requested resource not found",
		"checkpoint_not_found":            "requested resource not found",
		"not_found":                       "requested resource not found",
		"invalid_argument":                "invalid_argument: invalid operation input",
		"invalid_state":                   "operation_conflict: operation conflicts with current state",
		"conflict":                        "operation_conflict: operation conflicts with current state",
		"unauthorized":                    "authentication failed",
		"unauthenticated_caller":          "authentication failed",
		"forbidden":                       "access_denied: operation denied by policy",
		"policy":                          "access_denied: operation denied by policy",
		"policy_denial":                   "access_denied: operation denied by policy",
		"forbidden_operation":             "access_denied: operation denied by policy",
		"missing_required_scope":          "access_denied: operation denied by policy",
		"delegation_exceeds_authority":    "access_denied: operation denied by policy",
		"approval_record_expired":         "approval_expired: operator approval has expired",
		"approval_record_consumed":        "approval_consumed: operator approval has already been consumed",
		"approval_record_not_yet_valid":   "approval_invalid: operator approval is not yet valid",
		"approval_record_mismatch":        "approval_invalid: operator approval does not match this operation",
		"missing_verified_rollback_point": "rollback_required: operation requires a verified rollback point",
		"timeout":                         "operation_timeout: operation timeout exceeded",
		"deadline_exceeded":               "operation_timeout: operation timeout exceeded",
		"deadline_passed":                 "operation_timeout: operation timeout exceeded",
		"cancelled":                       "operation_canceled: operation was canceled",
		"caller_canceled":                 "operation_canceled: operation was canceled",
		"persistence_failure":             "operation_persistence_failure: operation record could not be saved",
		"finalization_error":              "operation_finalization_error: operation could not be finalized",
		"daemon_crash_recovered":          "operation_interrupted: operation was interrupted by daemon restart",
	}
	if message := messages[category]; message != "" {
		return message
	}
	if message, ok := domain.CanonicalFailureMessage(category); ok {
		return category + ": " + message
	}
	return ""
}

// Only known typed errors select public messages. Provider output, paths, tokens,
// and arbitrary domain-prefixed strings must never become MCP error text.
func classifiedToolError(err error) string {
	if failure, ok := errors.AsType[*operationFailure](err); ok {
		if message := categoryToolError(failure.category); message != "" {
			return message
		}
		return "operation_failed: operation failed; inspect its receipt for details"
	}
	if apiErr, ok := errors.AsType[*client.APIError](err); ok {
		if message := categoryToolError(apiErr.Category); message != "" {
			return message
		}
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return "authentication failed"
		case http.StatusForbidden:
			return "access_denied: operation denied by policy"
		}
	}
	categories := []struct {
		message string
		errors  []error
	}{
		{"operation_canceled: operation was canceled", []error{context.Canceled}},
		{"operation_timeout: operation timeout exceeded", []error{context.DeadlineExceeded, hyperv.ErrCommandTimeout, client.ErrTimeout, domain.ErrSessionWaitTimeout}},
		{"backend_host_unavailable: backend management host is unavailable", []error{hyperv.ErrHostUnavailable, domain.ErrMachineHostUnavailable, domain.ErrMachineHostDisabled}},
		{"backend_module_missing: Hyper-V PowerShell module is unavailable", []error{hyperv.ErrModuleMissing}},
		{"backend_unavailable: machine backend is unavailable", []error{hyperv.ErrBackendUnavailable, hyperv.ErrExecutableNotFound}},
		{"backend_access_denied: machine backend access denied", []error{hyperv.ErrAccessDenied, domain.ErrMachineAccessDenied}},
		{"backend_malformed_response: machine backend returned an invalid response", []error{hyperv.ErrMalformedResponse, hyperv.ErrUnexpectedSchemaVersion, hyperv.ErrTrailingData, hyperv.ErrDuplicateMachineID}},
		{"backend_output_limit: machine backend output exceeded its size limit", []error{hyperv.ErrOutputExceededLimit}},
		{"requested resource not found", []error{hyperv.ErrMachineNotFound, hyperv.ErrCheckpointNotFound, domain.ErrMachineReferenceMiss, domain.ErrSessionNotFound, client.ErrNotFound}},
		{"operation_conflict: operation conflicts with current state", []error{hyperv.ErrInvalidState, domain.ErrSessionConflict, domain.ErrSessionClosed, client.ErrConflict}},
		{"access_denied: operation denied by policy", []error{hyperv.ErrRemoteRouteReadOnly, domain.ErrSessionAccessDenied, client.ErrDenied}},
		{"service connection failed: daemon is unreachable", []error{client.ErrDaemonUnavailable}},
		{"daemon_malformed_response: daemon returned an invalid response", []error{client.ErrMalformedResponse}},
		{"invalid_argument: invalid operation input", []error{client.ErrInvalidArgument, domain.ErrInvalidMachineID, domain.ErrInvalidMachineRef, domain.ErrInvalidMachineLocator, domain.ErrNonCanonicalParameter}},
	}
	for _, category := range categories {
		for _, sentinel := range category.errors {
			if errors.Is(err, sentinel) {
				return category.message
			}
		}
	}
	return "an internal daemon error occurred"
}

func protectedToolError(err error) string {
	if isApprovalRequired(err) {
		return "approval_required: operator approval required for this operation"
	}
	switch {
	case errors.Is(err, target.ErrNoDefault), errors.Is(err, target.ErrDifferentTarget):
		return "target is not enrolled"
	case errors.Is(err, errProtectedTargetUnavailable):
		if isProtectedBackendFailure(err) {
			return classifiedToolError(err)
		}
		return "protected target is unavailable"
	default:
		return ""
	}
}

// Enrollment authority and unknown store/identity errors remain protected. Only
// typed provider and context causes can describe a failed enrolled observation.
func isProtectedBackendFailure(err error) bool {
	causes := []error{
		context.Canceled, context.DeadlineExceeded,
		hyperv.ErrExecutableNotFound, hyperv.ErrCommandTimeout, hyperv.ErrOutputExceededLimit,
		hyperv.ErrBackendUnavailable, hyperv.ErrAccessDenied, hyperv.ErrModuleMissing,
		hyperv.ErrHostUnavailable, hyperv.ErrMachineNotFound, hyperv.ErrMalformedResponse,
		hyperv.ErrUnexpectedSchemaVersion, hyperv.ErrTrailingData, hyperv.ErrDuplicateMachineID,
		hyperv.ErrCheckpointNotFound, hyperv.ErrInvalidState, hyperv.ErrRemoteRouteReadOnly,
		domain.ErrMachineHostUnavailable, domain.ErrMachineAccessDenied, domain.ErrMachineHostDisabled,
	}
	for _, cause := range causes {
		if errors.Is(err, cause) {
			return true
		}
	}
	return false
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
