package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestValidation_OperationID(t *testing.T) {
	// Valid
	validID := "op-00000000000000000000000000000001"
	if err := domain.ValidateOperationID(validID); err != nil {
		t.Errorf("expected valid operation ID, got %v", err)
	}

	// Invalid prefix
	if err := domain.ValidateOperationID("xx-00000000000000000000000000000001"); err == nil {
		t.Errorf("expected error for invalid prefix")
	}

	// Invalid length
	if err := domain.ValidateOperationID("op-123"); err == nil {
		t.Errorf("expected error for invalid length")
	}

	// Non-hex
	if err := domain.ValidateOperationID("op-0000000000000000000000000000000z"); err == nil {
		t.Errorf("expected error for non-hex character")
	}
}

func TestValidation_ReceiptID(t *testing.T) {
	// Valid
	validID := "rcpt-00000000000000000000000000000001"
	if err := domain.ValidateReceiptID(validID); err != nil {
		t.Errorf("expected valid receipt ID, got %v", err)
	}

	// Invalid prefix
	if err := domain.ValidateReceiptID("xxxx-00000000000000000000000000000001"); err == nil {
		t.Errorf("expected error for invalid prefix")
	}

	// Invalid length
	if err := domain.ValidateReceiptID("rcpt-123"); err == nil {
		t.Errorf("expected error for invalid length")
	}

	// Non-hex
	if err := domain.ValidateReceiptID("rcpt-0000000000000000000000000000000z"); err == nil {
		t.Errorf("expected error for non-hex character")
	}
}

func TestValidation_PathSafeID(t *testing.T) {
	errBase := errors.New("err base")
	if err := domain.ValidatePathSafeID("valid-id_123", errBase); err != nil {
		t.Errorf("expected valid id, got %v", err)
	}

	if err := domain.ValidatePathSafeID("../bad", errBase); err == nil {
		t.Errorf("expected error for ..")
	}
	if err := domain.ValidatePathSafeID("bad/id", errBase); err == nil {
		t.Errorf("expected error for /")
	}
	if err := domain.ValidatePathSafeID("bad\\id", errBase); err == nil {
		t.Errorf("expected error for \\")
	}
}

func TestValidation_BackendRollbackEvidence(t *testing.T) {
	if err := domain.ValidateBackendID("hyperv-cim"); err != nil {
		t.Errorf("expected valid backend ID, got %v", err)
	}
	if err := domain.ValidateBackendID(""); err == nil {
		t.Errorf("expected error for empty backend ID")
	}

	if err := domain.ValidateRollbackRef("snap-1"); err != nil {
		t.Errorf("expected valid rollback ref, got %v", err)
	}
	if err := domain.ValidateRollbackRef(""); err == nil {
		t.Errorf("expected error for empty rollback ref")
	}

	if err := domain.ValidateEvidenceRef("sha256:0000000000000000000000000000000000000000000000000000000000000000"); err != nil {
		t.Errorf("expected valid evidence ref, got %v", err)
	}
	if err := domain.ValidateEvidenceRef("/tmp/evidence"); err == nil {
		t.Errorf("expected error for / in evidence ref")
	}
	if err := domain.ValidateEvidenceRef("evidence\\path"); err == nil {
		t.Errorf("expected error for \\ in evidence ref")
	}
	if err := domain.ValidateEvidenceRef("evidence space"); err == nil {
		t.Errorf("expected error for space in evidence ref")
	}
}

func TestValidation_ParamMachineStart(t *testing.T) {
	if err := domain.ValidateOperationParameters("machine.start", nil); err != nil {
		t.Errorf("expected nil for valid machine.start, got %v", err)
	}
	if err := domain.ValidateOperationParameters("machine.start", map[string]any{"extra": 1}); err == nil {
		t.Errorf("expected error for extra param on machine.start")
	}
}

func TestValidation_ParamMachineStop(t *testing.T) {
	if err := domain.ValidateOperationParameters("machine.stop", nil); err != nil {
		t.Errorf("expected nil for empty machine.stop, got %v", err)
	}
	for _, mode := range []string{"shutdown", "save", "turn-off"} {
		if err := domain.ValidateOperationParameters("machine.stop", map[string]any{"mode": mode}); err != nil {
			t.Errorf("expected nil for %s mode, got %v", mode, err)
		}
	}
	if err := domain.ValidateOperationParameters("machine.stop", map[string]any{"mode": "invalid"}); err == nil {
		t.Errorf("expected error for invalid mode")
	}
	if err := domain.ValidateOperationParameters("machine.stop", map[string]any{"mode": 123}); err == nil {
		t.Errorf("expected error for non-string mode")
	}
	if err := domain.ValidateOperationParameters("machine.stop", map[string]any{"bad": "key"}); err == nil {
		t.Errorf("expected error for missing mode key")
	}
	if err := domain.ValidateOperationParameters("machine.stop", map[string]any{"mode": "shutdown", "extra": 1}); err == nil {
		t.Errorf("expected error for extra param on machine.stop")
	}
}

func TestValidation_ParamCheckpointCreate(t *testing.T) {
	if err := domain.ValidateOperationParameters("checkpoint.create", nil); err != nil {
		t.Errorf("expected nil for empty checkpoint.create, got %v", err)
	}
	if err := domain.ValidateOperationParameters("checkpoint.create", map[string]any{"name": "snap-1"}); err != nil {
		t.Errorf("expected nil for valid name, got %v", err)
	}
	if err := domain.ValidateOperationParameters("checkpoint.create", map[string]any{"name": 123}); err == nil {
		t.Errorf("expected error for non-string name")
	}
	if err := domain.ValidateOperationParameters("checkpoint.create", map[string]any{"bad": "key"}); err == nil {
		t.Errorf("expected error for bad key on checkpoint.create")
	}
	if err := domain.ValidateOperationParameters("checkpoint.create", map[string]any{"name": "snap-1", "extra": 1}); err == nil {
		t.Errorf("expected error for extra key on checkpoint.create")
	}
	if err := domain.ValidateOperationParameters("checkpoint.create", map[string]any{"name": strings.Repeat("a", 300)}); err == nil {
		t.Errorf("expected error for overlarge name")
	}
}

func TestValidation_ParamCheckpointRestore(t *testing.T) {
	guid := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	if err := domain.ValidateOperationParameters("checkpoint.restore", map[string]any{"checkpoint_id": guid}); err != nil {
		t.Errorf("expected nil for valid checkpoint.restore, got %v", err)
	}
	if err := domain.ValidateOperationParameters("checkpoint.restore", nil); err == nil {
		t.Errorf("expected error for missing checkpoint_id")
	}
	if err := domain.ValidateOperationParameters("checkpoint.restore", map[string]any{"bad": guid}); err == nil {
		t.Errorf("expected error for bad key on checkpoint.restore")
	}
	if err := domain.ValidateOperationParameters("checkpoint.restore", map[string]any{"checkpoint_id": 123}); err == nil {
		t.Errorf("expected error for non-string checkpoint_id")
	}
	if err := domain.ValidateOperationParameters("checkpoint.restore", map[string]any{"checkpoint_id": "not-a-guid"}); err == nil {
		t.Errorf("expected error for non-guid checkpoint_id")
	}

	if err := domain.ValidateOperationParameters("unknown.kind", nil); !errors.Is(err, domain.ErrInvalidOperationKind) {
		t.Errorf("expected ErrInvalidOperationKind, got %v", err)
	}
}

func TestValidateOperationApprovalIssuanceParameters(t *testing.T) {
	valid := map[string]any{
		"approval_id":          "app-operation-0123456789abcdef0123456789abcdef",
		"approved_fingerprint": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"approved_kind":        "machine.stop", "beneficiary": "agent:mcp-local",
		"deadline": "2026-08-31T03:00:00Z",
	}
	for _, kind := range []domain.OperationKind{"operation.approval.issue", "session.approval.issue"} {
		if err := domain.ValidateOperationParameters(kind, valid); err != nil {
			t.Fatalf("valid %s params: %v", kind, err)
		}
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing", mutate: func(values map[string]any) { delete(values, "deadline") }},
		{name: "non-string", mutate: func(values map[string]any) { values["approved_kind"] = 1 }},
		{name: "approval id", mutate: func(values map[string]any) { values["approval_id"] = "../bad" }},
		{name: "fingerprint", mutate: func(values map[string]any) { values["approved_fingerprint"] = "bad" }},
		{name: "kind", mutate: func(values map[string]any) { values["approved_kind"] = "" }},
		{name: "beneficiary", mutate: func(values map[string]any) { values["beneficiary"] = "" }},
		{name: "deadline", mutate: func(values map[string]any) { values["deadline"] = "2026-08-31T04:00:00+01:00" }},
	}
	for _, test := range tests {
		values := domain.DeepCloneMap(valid)
		test.mutate(values)
		if err := domain.ValidateOperationParameters("operation.approval.issue", values); err == nil {
			t.Fatalf("%s params unexpectedly valid: %+v", test.name, values)
		}
	}
}
