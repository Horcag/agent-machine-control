package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestValidateOperationApprovalIssueRequestRejectsEveryInvalidBoundary(t *testing.T) {
	valid := OperationApprovalIssueRequest{
		Kind: "machine.stop", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason: "validate approval request", IdempotencyKey: "validate-approval-request",
		ValidForMillis: 1_000, Beneficiary: "self", Parameters: map[string]any{"mode": "turn-off"},
	}
	tests := []struct {
		name   string
		mutate func(*OperationApprovalIssueRequest)
	}{
		{name: "reason", mutate: func(r *OperationApprovalIssueRequest) { r.Reason = "" }},
		{name: "key", mutate: func(r *OperationApprovalIssueRequest) { r.IdempotencyKey = "" }},
		{name: "validity low", mutate: func(r *OperationApprovalIssueRequest) { r.ValidForMillis = 999 }},
		{name: "validity high", mutate: func(r *OperationApprovalIssueRequest) { r.ValidForMillis = 300_001 }},
		{name: "beneficiary", mutate: func(r *OperationApprovalIssueRequest) { r.Beneficiary = "agent:other" }},
		{name: "parameters", mutate: func(r *OperationApprovalIssueRequest) { r.Parameters = map[string]any{"mode": "invalid"} }},
		{name: "kind", mutate: func(r *OperationApprovalIssueRequest) { r.Kind = "session.open" }},
		{name: "target", mutate: func(r *OperationApprovalIssueRequest) { r.Target = "" }},
	}
	for _, test := range tests {
		request := valid
		test.mutate(&request)
		if err := validateOperationApprovalIssueRequest(request); err == nil {
			t.Fatalf("%s request unexpectedly valid: %+v", test.name, request)
		}
	}
	valid.Beneficiary = "agent:mcp-local"
	if err := validateOperationApprovalIssueRequest(valid); err != nil {
		t.Fatalf("valid MCP request: %v", err)
	}
}

func TestWriteOperationApprovalIssueErrorSanitizesCategories(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		status   int
		category string
	}{
		{name: "forbidden", err: app.ErrOperationApprovalForbidden, status: http.StatusForbidden, category: "forbidden"},
		{name: "not required", err: app.ErrOperationApprovalNotRequired, status: http.StatusConflict, category: "approval_not_required"},
		{name: "target", err: target.ErrNoDefault, status: http.StatusConflict, category: "target_not_enrolled"},
		{name: "policy", err: &app.PolicyDeniedError{Reason: policy.DenialMissingScope, Message: "insufficient scope"}, status: http.StatusForbidden, category: string(policy.DenialMissingScope)},
		{name: "default", err: errors.New("private backend text"), status: http.StatusBadRequest, category: "invalid_argument"},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		writeOperationApprovalIssueError(recorder, test.err)
		if recorder.Code != test.status {
			t.Fatalf("%s status=%d", test.name, recorder.Code)
		}
		var envelope ErrorEnvelope
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error.Category != test.category || envelope.Error.Message == "private backend text" {
			t.Fatalf("%s envelope=%+v", test.name, envelope)
		}
	}
}

func TestWriteTargetResolutionErrorSanitizesCategories(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		status   int
		category string
	}{
		{name: "missing default", err: target.ErrNoDefault, status: http.StatusConflict, category: "target_not_enrolled"},
		{name: "different target", err: target.ErrDifferentTarget, status: http.StatusBadRequest, category: "target_mismatch"},
		{name: "inventory unavailable", err: target.ErrInventoryRefresh, status: http.StatusServiceUnavailable, category: "target_unavailable"},
		{name: "unknown", err: errors.New("private backend text"), status: http.StatusBadRequest, category: "invalid_target"},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		writeTargetResolutionError(recorder, test.err)
		var envelope ErrorEnvelope
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != test.status || envelope.Error.Category != test.category || envelope.Error.Message == "private backend text" {
			t.Fatalf("%s response=%d envelope=%+v", test.name, recorder.Code, envelope)
		}
	}
}
