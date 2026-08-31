package app_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func operationApprovalOperator(t *testing.T) domain.ActorContext {
	t.Helper()
	scopes := domain.NewScopeSet(domain.ScopeMachineWrite, domain.ScopeOperationAdmin)
	actor, err := domain.NewActorContext("operator:test", "operator:test", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func TestOperationApprovalExpiredAndConsumedReferencesDenyBeforeEffect(t *testing.T) {
	now := time.Date(2026, 8, 31, 2, 0, 0, 0, time.UTC)
	stopCalls := 0
	backend := &mockBackend{
		stopMachineFn: func(context.Context, string, string) (domain.MachineObservation, error) {
			stopCalls++
			return domain.MachineObservation{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", State: domain.MachineStateOff}, nil
		},
	}
	root := t.TempDir()
	makeService := func(suffix string, sharedApproval *approval.Store) *app.RecoveryService {
		t.Helper()
		leaseDir, auditDir, receiptDir, approvalDir := root+"/leases-"+suffix, root+"/audit-"+suffix, root+"/receipts-"+suffix, root+"/approvals"
		for _, dir := range []string{leaseDir, auditDir, receiptDir, approvalDir} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if sharedApproval == nil {
			sharedApproval = approval.NewStore(approvalDir)
		}
		return app.NewRecoveryService(
			backend, lease.NewManager(leaseDir, lease.WithClock(func() time.Time { return now })),
			audit.NewStore(auditDir, audit.WithClock(func() time.Time { return now })),
			receipt.NewStore(receiptDir), sharedApproval, app.WithRecoveryClock(func() time.Time { return now }),
		)
	}
	sharedApproval := approval.NewStore(root + "/approvals")
	service := makeService("primary", sharedApproval)
	actor := operationApprovalOperator(t)
	issue := func(key string, validity time.Duration) (*app.OperationApprovalGrant, *domain.Approval) {
		t.Helper()
		grant, _, err := service.IssueOperationApproval(context.Background(), app.OperationApprovalIssueParams{
			Kind: "machine.stop", Caller: actor, Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			Reason: "test inactive approval reference", IdempotencyKey: key, ValidFor: validity,
			Parameters: map[string]any{"mode": "turn-off"},
		})
		if err != nil {
			t.Fatal(err)
		}
		op := domain.Operation{
			Kind: grant.Operation.Kind, Target: grant.Operation.Target, Actor: actor,
			Reason: grant.Operation.Reason, Deadline: grant.Deadline, IdempotencyKey: grant.Operation.IdempotencyKey,
			RequiredCapability: string(domain.CapabilityMachineStop), RequiredScopes: []string{domain.ScopeMachineWrite},
			Classification: domain.ClassDestructivePrivileged, EvidenceSensitivity: domain.EvidenceSensitivityStandard,
			Parameters: grant.Operation.Parameters,
		}
		loaded, err := service.LoadOperationApprovalReference(context.Background(), op, grant.ApprovalID)
		if err != nil {
			t.Fatal(err)
		}
		return grant, loaded
	}
	request := func(grant *app.OperationApprovalGrant, loaded *domain.Approval) app.MutationRequest {
		return app.MutationRequest{
			TargetID: string(grant.Operation.Target), Actor: actor, Reason: grant.Operation.Reason,
			IdempotencyKey: grant.Operation.IdempotencyKey, Deadline: grant.Deadline,
			ApprovalID: grant.ApprovalID, Approval: loaded,
		}
	}

	expiredGrant, expiredApproval := issue("expired-operation-approval", time.Second)
	now = now.Add(2 * time.Second)
	expiredReceipt, _, err := service.StopMachine(context.Background(), request(expiredGrant, expiredApproval), "turn-off")
	if !errors.Is(err, domain.ErrApprovalExpired) || expiredReceipt.Outcome.ErrorCategory != "approval_record_expired" || stopCalls != 0 {
		t.Fatalf("expired result receipt=%+v err=%v stopCalls=%d", expiredReceipt, err, stopCalls)
	}

	activeGrant, activeApproval := issue("consumed-operation-approval", time.Minute)
	if _, _, err := service.StopMachine(context.Background(), request(activeGrant, activeApproval), "turn-off"); err != nil {
		t.Fatalf("first approved effect: %v", err)
	}
	if stopCalls != 1 {
		t.Fatalf("first effect calls = %d", stopCalls)
	}
	isolatedReceiptService := makeService("isolated", sharedApproval)
	consumedReceipt, _, err := isolatedReceiptService.StopMachine(context.Background(), request(activeGrant, activeApproval), "turn-off")
	if !errors.Is(err, domain.ErrApprovalConsumed) || consumedReceipt.Outcome.ErrorCategory != "approval_record_consumed" || stopCalls != 1 {
		t.Fatalf("consumed result receipt=%+v err=%v stopCalls=%d", consumedReceipt, err, stopCalls)
	}
}

//nolint:cyclop // One issuance scenario asserts the coupled identity, idempotency, and no-effect contract.
func TestOperationApprovalIssuanceBindsBeneficiaryAndExactOperation(t *testing.T) {
	backend := &mockBackend{
		listCheckpointsFn: func(context.Context, string) ([]domain.CheckpointObservation, error) { return nil, nil },
	}
	svc, _ := setupTestRecovery(t, backend)
	caller := operationApprovalOperator(t)
	base := app.OperationApprovalIssueParams{
		Kind: "machine.start", Caller: caller,
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason: "authorize exact machine start", IdempotencyKey: "operation-approval-self",
		ValidFor: time.Minute,
	}

	self, selfReceipt, err := svc.IssueOperationApproval(context.Background(), base)
	if err != nil {
		t.Fatalf("IssueOperationApproval(self): %v", err)
	}
	if self.ApprovalID == "" || !self.Deadline.Equal(self.ExpiresAt) {
		t.Fatalf("invalid self grant: %+v", self)
	}
	if self.Operation.Target != domain.MachineRef(base.Target) || self.Operation.Kind != base.Kind {
		t.Fatalf("unexpected self summary: %+v", self.Operation)
	}
	if selfReceipt == nil || len(selfReceipt.EvidenceRefs) != 1 || selfReceipt.EvidenceRefs[0] != self.ApprovalID {
		t.Fatalf("issuance receipt does not preserve approval reference: %+v", selfReceipt)
	}

	mcpParams := base
	mcpParams.IdempotencyKey = "operation-approval-mcp"
	mcpParams.Beneficiary = "agent:mcp-local"
	mcp, _, err := svc.IssueOperationApproval(context.Background(), mcpParams)
	if err != nil {
		t.Fatalf("IssueOperationApproval(mcp): %v", err)
	}
	if mcp.ApprovalID == self.ApprovalID {
		t.Fatal("self and MCP approvals must have distinct identities")
	}

	retry, _, err := svc.IssueOperationApproval(context.Background(), base)
	if err != nil || retry.ApprovalID != self.ApprovalID || !retry.Deadline.Equal(self.Deadline) {
		t.Fatalf("idempotent issuance changed grant: %+v err=%v", retry, err)
	}

	changed := base
	changed.Kind = "machine.stop"
	changed.Parameters = map[string]any{"mode": "turn-off"}
	if _, _, err := svc.IssueOperationApproval(context.Background(), changed); !errors.Is(err, receipt.ErrIdempotencyCollision) {
		t.Fatalf("changed exact operation error = %v, want idempotency collision", err)
	}

	for _, call := range backend.calls {
		if call == "StartMachine:"+base.Target || call == "StopMachine:"+base.Target+":turn-off" {
			t.Fatalf("issuance invoked mutating backend: %s", call)
		}
	}
}

func TestOperationApprovalIssuanceRejectsUnauthorizedAndNotRequired(t *testing.T) {
	checkpoint := domain.CheckpointObservation{
		ID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001", Name: "rollback",
		VMID:      "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		CreatedAt: time.Now(), ObservedAt: time.Now(), ObservationType: domain.ObservationObserved,
	}
	backend := &mockBackend{
		listCheckpointsFn: func(context.Context, string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{checkpoint}, nil
		},
	}
	svc, _ := setupTestRecovery(t, backend)
	params := app.OperationApprovalIssueParams{
		Kind: "machine.start", Caller: operationApprovalOperator(t), Target: checkpoint.VMID,
		Reason: "approval should not be required", IdempotencyKey: "operation-approval-not-required",
		ValidFor: time.Minute,
	}
	if _, _, err := svc.IssueOperationApproval(context.Background(), params); !errors.Is(err, app.ErrOperationApprovalNotRequired) {
		t.Fatalf("not-required issuance error = %v", err)
	}

	noAdminScopes := domain.NewScopeSet(domain.ScopeMachineWrite)
	noAdmin, _ := domain.NewActorContext("operator:no-admin", "operator:no-admin", noAdminScopes, noAdminScopes)
	params.Caller = noAdmin
	if _, _, err := svc.IssueOperationApproval(context.Background(), params); !errors.Is(err, app.ErrOperationApprovalForbidden) {
		t.Fatalf("non-admin issuance error = %v", err)
	}

	params.Caller = operationApprovalOperator(t)
	params.Beneficiary = "agent:other"
	if _, _, err := svc.IssueOperationApproval(context.Background(), params); !errors.Is(err, app.ErrOperationApprovalForbidden) {
		t.Fatalf("other beneficiary error = %v", err)
	}

	delegatedScopes := domain.NewScopeSet(domain.ScopeMachineWrite, domain.ScopeOperationAdmin)
	delegated, _ := domain.NewActorContext("operator:test", "agent:mcp-local", delegatedScopes, domain.NewScopeSet(domain.ScopeMachineWrite))
	params.Caller = delegated
	params.Beneficiary = "agent:mcp-local"
	if _, _, err := svc.IssueOperationApproval(context.Background(), params); !errors.Is(err, app.ErrOperationApprovalForbidden) {
		t.Fatalf("delegated issuance error = %v", err)
	}
}

func TestOperationApprovalIssuanceValidatesBoundsKindsAndPersistence(t *testing.T) {
	backend := &mockBackend{listCheckpointsFn: func(context.Context, string) ([]domain.CheckpointObservation, error) { return nil, nil }}
	service, _ := setupTestRecovery(t, backend)
	base := app.OperationApprovalIssueParams{
		Kind: "machine.stop", Caller: operationApprovalOperator(t),
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "validate issuance bounds",
		IdempotencyKey: "validate-issuance-bounds", ValidFor: time.Minute,
		Parameters: map[string]any{"mode": "turn-off"},
	}
	tests := []struct {
		name   string
		mutate func(*app.OperationApprovalIssueParams)
	}{
		{name: "validity low", mutate: func(p *app.OperationApprovalIssueParams) { p.ValidFor = 0 }},
		{name: "validity high", mutate: func(p *app.OperationApprovalIssueParams) { p.ValidFor = 6 * time.Minute }},
		{name: "reason", mutate: func(p *app.OperationApprovalIssueParams) { p.Reason = "" }},
		{name: "key", mutate: func(p *app.OperationApprovalIssueParams) { p.IdempotencyKey = "" }},
		{name: "parameters", mutate: func(p *app.OperationApprovalIssueParams) { p.Parameters = map[string]any{"mode": "invalid"} }},
		{name: "kind", mutate: func(p *app.OperationApprovalIssueParams) { p.Kind = "session.open"; p.Parameters = nil }},
	}
	for _, test := range tests {
		params := base
		test.mutate(&params)
		if _, _, err := service.IssueOperationApproval(context.Background(), params); err == nil {
			t.Fatalf("%s issuance unexpectedly succeeded", test.name)
		}
	}

	for index, exact := range []app.OperationApprovalIssueParams{
		{Kind: "machine.stop", Parameters: map[string]any{"mode": "save"}},
		{Kind: "checkpoint.create", Parameters: map[string]any{"name": "synthetic"}},
		{Kind: "checkpoint.restore", Parameters: map[string]any{"checkpoint_id": "e4a523d4-6b99-4d62-a5e2-4752c0f20001"}},
	} {
		exact.Caller, exact.Target, exact.Reason = base.Caller, base.Target, base.Reason
		exact.IdempotencyKey = fmt.Sprintf("validate-supported-kind-%d", index)
		exact.ValidFor = time.Minute
		if _, _, err := service.IssueOperationApproval(context.Background(), exact); err != nil {
			t.Fatalf("supported %s issuance: %v", exact.Kind, err)
		}
	}

	missingPersistence := app.NewRecoveryService(backend, nil, nil, nil, nil)
	if _, _, err := missingPersistence.IssueOperationApproval(context.Background(), base); err == nil {
		t.Fatal("missing persistence unexpectedly issued approval")
	}
}

func TestLoadOperationApprovalReferenceRejectsInvalidAndMismatchedAuthority(t *testing.T) {
	backend := &mockBackend{listCheckpointsFn: func(context.Context, string) ([]domain.CheckpointObservation, error) { return nil, nil }}
	service, _ := setupTestRecovery(t, backend)
	actor := operationApprovalOperator(t)
	grant, _, err := service.IssueOperationApproval(context.Background(), app.OperationApprovalIssueParams{
		Kind: "machine.stop", Caller: actor, Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason: "load exact approval reference", IdempotencyKey: "load-operation-approval",
		ValidFor: time.Minute, Parameters: map[string]any{"mode": "turn-off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	op := domain.Operation{
		Kind: grant.Operation.Kind, Target: grant.Operation.Target, Actor: actor,
		Reason: grant.Operation.Reason, Deadline: grant.Deadline, IdempotencyKey: grant.Operation.IdempotencyKey,
		RequiredCapability: string(domain.CapabilityMachineStop), RequiredScopes: []string{domain.ScopeMachineWrite},
		Classification: domain.ClassDestructivePrivileged, EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: grant.Operation.Parameters,
	}
	if loaded, err := service.LoadOperationApprovalReference(context.Background(), op, grant.ApprovalID); err != nil || loaded == nil {
		t.Fatalf("valid reference loaded=%+v err=%v", loaded, err)
	}
	var nilService *app.RecoveryService
	if _, err := nilService.LoadOperationApprovalReference(context.Background(), op, grant.ApprovalID); !errors.Is(err, app.ErrInvalidOperationApprovalReference) {
		t.Fatalf("nil service error = %v", err)
	}
	for name, mutate := range map[string]func(*domain.Operation, *string){
		"invalid id": func(_ *domain.Operation, id *string) { *id = "../bad" },
		"missing id": func(_ *domain.Operation, id *string) { *id = "app-operation-ffffffffffffffffffffffffffffffff" },
		"deadline":   func(candidate *domain.Operation, _ *string) { candidate.Deadline = candidate.Deadline.Add(time.Second) },
		"operation":  func(candidate *domain.Operation, _ *string) { candidate.Reason = "changed reason" },
	} {
		candidate, approvalID := op.Clone(), grant.ApprovalID
		mutate(&candidate, &approvalID)
		if _, err := service.LoadOperationApprovalReference(context.Background(), candidate, approvalID); !errors.Is(err, app.ErrInvalidOperationApprovalReference) {
			t.Fatalf("%s error = %v", name, err)
		}
	}
}

func TestOperationApprovalPreparationFailsClosedOnCapabilityAndScopeErrors(t *testing.T) {
	sentinel := errors.New("capability discovery failed")
	backend := &mockBackend{
		listCheckpointsFn: func(context.Context, string) ([]domain.CheckpointObservation, error) { return nil, nil },
		capabilitiesFn:    func(context.Context, string) (domain.CapabilitySet, error) { return nil, sentinel },
	}
	service, _ := setupTestRecovery(t, backend)
	params := app.OperationApprovalIssueParams{
		Kind: "machine.start", Caller: operationApprovalOperator(t),
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "fail closed during preparation",
		IdempotencyKey: "capability-preparation-failure", ValidFor: time.Minute,
	}
	if _, _, err := service.IssueOperationApproval(context.Background(), params); !errors.Is(err, sentinel) {
		t.Fatalf("capability error = %v", err)
	}

	backend.capabilitiesFn = nil
	adminOnly := domain.NewScopeSet(domain.ScopeOperationAdmin)
	caller, err := domain.NewActorContext("operator:admin-only", "operator:admin-only", adminOnly, adminOnly)
	if err != nil {
		t.Fatal(err)
	}
	params.Caller = caller
	params.IdempotencyKey = "scope-preparation-failure"
	if _, _, err := service.IssueOperationApproval(context.Background(), params); err == nil {
		t.Fatal("beneficiary without machine:write unexpectedly received approval")
	}
}
