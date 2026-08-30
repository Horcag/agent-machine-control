package audit_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func terminalReceipt() domain.Receipt {
	return domain.Receipt{
		ReceiptID:              "rcpt-0123456789abcdef0123456789abcdef",
		OperationKind:          "session.write",
		Fingerprint:            "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		IdempotencyFingerprint: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		IdempotencyKey:         "audit-verify-key",
		Actor:                  "agent:audit-verify",
		Target:                 "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Class:                  domain.ClassReversibleMutation,
		EffectiveBackend:       "amcd",
		StartedAt:              time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		CompletedAt:            time.Date(2026, 8, 30, 12, 0, 1, 0, time.UTC),
		Outcome:                domain.ExecutionOutcome{Status: domain.OutcomeSuccess},
		ObservationType:        domain.ObservationObserved,
		RollbackRef:            "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		RedactionStatus:        domain.RedactionApplied,
	}
}

func TestEnsureTerminalOutcomeIsExactAndIdempotent(t *testing.T) {
	store := audit.NewStore(t.TempDir())
	receipt := terminalReceipt()
	if err := store.EnsureTerminalOutcome(receipt); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureTerminalOutcome(receipt); err != nil {
		t.Fatalf("repeated ensure failed: %v", err)
	}
	if err := store.VerifyTerminalOutcome(receipt); err != nil {
		t.Fatalf("idempotent ensure left invalid evidence: %v", err)
	}
	conflicting := receipt
	conflicting.Outcome.ExitCode = 1
	if err := store.EnsureTerminalOutcome(conflicting); !errors.Is(err, audit.ErrTerminalEvidenceInvalid) {
		t.Fatalf("conflicting ensure error = %v", err)
	}
}

func TestVerifyTerminalOutcomeValidMissingAndMismatched(t *testing.T) {
	dir := t.TempDir()
	store := audit.NewStore(dir)
	receipt := terminalReceipt()
	if err := store.RecordTerminalOutcome(receipt); err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyTerminalOutcome(receipt); err != nil {
		t.Fatalf("valid terminal evidence rejected: %v", err)
	}

	mismatched := receipt
	mismatched.Target = "c4a523d4-6b99-4d62-a5e2-4752c0f20002"
	if err := store.VerifyTerminalOutcome(mismatched); !errors.Is(err, audit.ErrTerminalEvidenceInvalid) {
		t.Fatalf("mismatched evidence error = %v", err)
	}
	missing := receipt
	missing.ReceiptID = "rcpt-ffffffffffffffffffffffffffffffff"
	if err := store.VerifyTerminalOutcome(missing); !errors.Is(err, audit.ErrTerminalEvidenceInvalid) {
		t.Fatalf("missing evidence error = %v", err)
	}
}

func TestVerifyTerminalOutcomeRejectsUnavailableAndDuplicateEvidence(t *testing.T) {
	receipt := terminalReceipt()
	var unavailable *audit.Store
	if err := unavailable.VerifyTerminalOutcome(receipt); !errors.Is(err, audit.ErrTerminalEvidenceInvalid) {
		t.Fatalf("nil store error = %v", err)
	}
	if err := audit.NewStore(t.TempDir()).VerifyTerminalOutcome(receipt); !errors.Is(err, audit.ErrTerminalEvidenceInvalid) {
		t.Fatalf("absent log error = %v", err)
	}

	store := audit.NewStore(t.TempDir())
	if err := store.RecordTerminalOutcome(receipt); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTerminalOutcome(receipt); err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyTerminalOutcome(receipt); !errors.Is(err, audit.ErrTerminalEvidenceInvalid) {
		t.Fatalf("duplicate evidence error = %v", err)
	}
}

func TestVerifyTerminalOutcomeRejectsInvalidEnvelopeAndCorruption(t *testing.T) {
	receipt := terminalReceipt()
	dir := t.TempDir()
	invalidEnvelope := `{"schema_version":"0","timestamp":"2026-08-30T12:00:00Z","event_type":"terminal_outcome"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, audit.AuditFileName), []byte(invalidEnvelope), 0600); err != nil {
		t.Fatal(err)
	}
	if err := audit.NewStore(dir).VerifyTerminalOutcome(receipt); !errors.Is(err, audit.ErrTerminalEvidenceInvalid) {
		t.Fatalf("invalid envelope error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, audit.AuditFileName), []byte("{corrupt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := audit.NewStore(dir).VerifyTerminalOutcome(receipt); !errors.Is(err, audit.ErrTerminalEvidenceInvalid) {
		t.Fatalf("corrupt evidence error = %v", err)
	}
}
