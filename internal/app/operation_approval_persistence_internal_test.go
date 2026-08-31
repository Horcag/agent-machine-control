package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func operationApprovalPersistenceFixture(t *testing.T) (domain.Approval, domain.Operation, domain.Receipt) {
	t.Helper()
	now := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)
	scopes := domain.NewScopeSet(domain.ScopeMachineWrite, domain.ScopeOperationAdmin)
	actor, err := domain.NewActorContext("operator:persistence", "operator:persistence", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	approved := domain.Operation{
		Kind: "machine.stop", Target: "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor,
		Reason: "persist exact operation approval", Deadline: now.Add(time.Minute),
		IdempotencyKey: "persist-operation-approval", RequiredCapability: string(domain.CapabilityMachineStop),
		RequiredScopes: []string{domain.ScopeMachineWrite}, Classification: domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard, Parameters: map[string]any{"mode": "turn-off"},
	}
	fingerprint, err := approved.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	issued := domain.Approval{
		ID: "app-operation-0123456789abcdef0123456789abcdef", Actor: actor.EffectiveActor,
		Target: approved.Target, AuthorizedClass: approved.Classification, Fingerprint: fingerprint,
		IdempotencyKey: approved.IdempotencyKey, IssuedAt: now, ExpiresAt: approved.Deadline,
	}
	issuance, issuanceReceipt, err := buildOperationApprovalIssuanceEvidence(actor, approved, issued)
	if err != nil {
		t.Fatal(err)
	}
	return issued, issuance, issuanceReceipt
}

func TestPersistIssuedOperationApprovalFailsClosedAtEveryStoreBoundary(t *testing.T) {
	issued, issuance, issuanceReceipt := operationApprovalPersistenceFixture(t)
	root := t.TempDir()
	receiptDir := filepath.Join(root, "receipts")
	approvalDir := filepath.Join(root, "approvals")
	if err := os.MkdirAll(receiptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(approvalDir, 0o700); err != nil {
		t.Fatal(err)
	}

	missingAudit := &RecoveryService{
		auditStore:   audit.NewStore(filepath.Join(root, "missing", "audit")),
		receiptStore: receipt.NewStore(receiptDir), approvalStore: approval.NewStore(approvalDir),
	}
	if err := missingAudit.persistIssuedOperationApproval(context.Background(), nil, issued, issuance, issuanceReceipt); err == nil {
		t.Fatal("missing audit directory unexpectedly persisted issuance")
	}

	sentinel := errors.New("receipt ensure failed")
	failedReceipt := &RecoveryService{
		auditStore:    audit.NewStore(filepath.Join(root, "audit-receipt")),
		receiptStore:  receipt.NewStore(receiptDir, receipt.WithEnsureHook(func(context.Context, domain.Receipt) error { return sentinel })),
		approvalStore: approval.NewStore(approvalDir),
	}
	if err := os.MkdirAll(filepath.Join(root, "audit-receipt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := failedReceipt.persistIssuedOperationApproval(context.Background(), &issued, issued, issuance, issuanceReceipt); !errors.Is(err, sentinel) {
		t.Fatalf("receipt failure = %v", err)
	}

	auditFile := filepath.Join(root, "audit-file")
	if err := os.WriteFile(auditFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedTerminalAudit := &RecoveryService{
		auditStore: audit.NewStore(auditFile), receiptStore: receipt.NewStore(filepath.Join(root, "receipts-terminal")),
		approvalStore: approval.NewStore(approvalDir),
	}
	if err := os.MkdirAll(filepath.Join(root, "receipts-terminal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := failedTerminalAudit.persistIssuedOperationApproval(context.Background(), &issued, issued, issuance, issuanceReceipt); err == nil {
		t.Fatal("terminal audit failure was ignored")
	}
}
