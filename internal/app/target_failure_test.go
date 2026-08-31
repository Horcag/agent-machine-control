package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestTargetServiceRejectsInvalidDependenciesAndPlans(t *testing.T) {
	store, _ := targetStore(t)
	inventory := targetInventory(t, nil, targetObservation(t, domain.LocalHostID, targetVMA, "vm-alpha"))
	if _, err := NewTargetService(nil, store); err == nil {
		t.Fatal("nil inventory unexpectedly accepted")
	}
	if _, err := NewTargetService(inventory, nil); err == nil {
		t.Fatal("nil store unexpectedly accepted")
	}
	if _, err := NewTargetService(inventory, store, WithTargetRefresh(nil)); err == nil {
		t.Fatal("nil refresh unexpectedly accepted")
	}

	service := targetService(t, inventory, store)
	plan, err := service.PrepareEnrollDefaultTarget(context.Background(), targetVMA, []string{"primary"})
	if err != nil {
		t.Fatal(err)
	}
	invalidPlans := []TargetPlan{
		{},
		func() TargetPlan { changed := plan; changed.StateHash = "changed"; return changed }(),
		func() TargetPlan { changed := plan; changed.Desired = nil; return changed }(),
		func() TargetPlan { changed := plan; changed.Kind = "target.clear"; return changed }(),
	}
	for index, invalid := range invalidPlans {
		if _, err := service.CommitTargetPlan(context.Background(), invalid); err == nil {
			t.Fatalf("invalid plan %d unexpectedly committed", index)
		}
	}
	if _, err := service.CommitTargetPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if publication, err := service.ClearDefaultTarget(context.Background()); err != nil || !publication.Committed || !publication.Durable {
		t.Fatalf("ClearDefaultTarget = %+v, %v", publication, err)
	}
}

func TestTargetServiceRefreshAndResolutionFailuresRemainFailClosed(t *testing.T) {
	observation := targetObservation(t, domain.LocalHostID, targetVMA, "vm-alpha")
	inventory := targetInventory(t, nil, observation)
	store, _ := targetStore(t)
	synthetic := errors.New("synthetic refresh failure")
	service, err := NewTargetService(inventory, store, WithTargetRefresh(func(context.Context) error { return synthetic }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PrepareEnrollDefaultTarget(context.Background(), targetVMA, nil); !errors.Is(err, target.ErrInventoryRefresh) {
		t.Fatalf("refresh failure = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.PrepareEnrollDefaultTarget(canceled, targetVMA, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh = %v", err)
	}

	service = targetService(t, inventory, store)
	remote, err := domain.NewMachineLocator("remote-a", targetVMA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.resolveCanonical(context.Background(), remote); !errors.Is(err, target.ErrUnsupportedHost) {
		t.Fatalf("remote canonical resolution = %v", err)
	}
	if _, err := service.resolveCanonical(canceled, observation.Locator); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled canonical resolution = %v", err)
	}
	badEntry := MachineIndexEntry{Locator: observation.Locator, Observation: observation}
	badEntry.Observation.ID = targetVMB
	if _, err := resolutionFromEntry(badEntry); err == nil {
		t.Fatal("mismatched provider identity unexpectedly accepted")
	}
	if err := (TargetResolution{Locator: remote, ProviderVMID: targetVMA}).Validate(); !errors.Is(err, target.ErrUnsupportedHost) {
		t.Fatalf("remote resolution validation = %v", err)
	}
}

func TestTargetCoordinatorRejectsInvalidApprovalBindingsBeforeEffect(t *testing.T) {
	if _, err := NewTargetCoordinator(nil, nil, nil, nil, nil); err == nil {
		t.Fatal("nil coordinator dependencies unexpectedly accepted")
	}
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	if _, err := harness.coordinator.IssueApproval(context.Background(), TargetApprovalIssueParams{
		Kind: "target.enroll", Caller: targetOperator(t), Reason: "too short", IdempotencyKey: "target-too-short", ValidFor: time.Millisecond,
	}); err == nil {
		t.Fatal("short approval validity unexpectedly accepted")
	}
	if _, err := harness.coordinator.IssueApproval(context.Background(), TargetApprovalIssueParams{
		Kind: "target.enroll", Caller: targetOperator(t), Reason: "too long", IdempotencyKey: "target-too-long", ValidFor: 6 * time.Minute,
	}); err == nil {
		t.Fatal("long approval validity unexpectedly accepted")
	}
	grant := issueEnrollApproval(t, harness, "target-binding", []string{"primary"}, "bind exact mutation")
	base := TargetMutationParams{
		Kind: "target.enroll", Aliases: []string{"primary"}, Caller: targetOperator(t), Reason: "bind exact mutation",
		IdempotencyKey: "target-binding", Deadline: grant.Deadline, ApprovalID: grant.ApprovalID,
	}
	missing := base
	missing.ApprovalID = ""
	if _, err := harness.coordinator.Mutate(context.Background(), missing); !errors.Is(err, target.ErrApprovalRequired) {
		t.Fatalf("missing approval error = %v", err)
	}
	malformed := base
	malformed.ApprovalID = "bad"
	if _, err := harness.coordinator.Mutate(context.Background(), malformed); !errors.Is(err, target.ErrApprovalRequired) {
		t.Fatalf("malformed approval error = %v", err)
	}
	mismatch := base
	mismatch.Reason = "different target mutation"
	if _, err := harness.coordinator.Mutate(context.Background(), mismatch); !errors.Is(err, target.ErrApprovalRequired) {
		t.Fatalf("approval binding mismatch = %v", err)
	}
	if _, err := harness.journal.LookupKeyContext(context.Background(), base.Caller.EffectiveActor, base.IdempotencyKey); !errors.Is(err, target.ErrMutationNotFound) {
		t.Fatalf("denied mutation retained reservation: %v", err)
	}
	invalidKind := base
	invalidKind.Kind = "machine.start"
	invalidKind.IdempotencyKey = "target-invalid-kind"
	if _, err := harness.coordinator.Mutate(context.Background(), invalidKind); !errors.Is(err, domain.ErrInvalidOperationKind) {
		t.Fatalf("invalid target operation kind = %v", err)
	}
}

func TestTargetCoordinatorHelpersBindRestartIdentity(t *testing.T) {
	now := time.Date(2026, 8, 31, 11, 0, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	operator := targetOperator(t)
	plan, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Caller: operator, Reason: "prepare restart identity",
		IdempotencyKey: "target-helper-plan", Deadline: now.Add(time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	op, err := buildTargetOperation(plan, operator, "prepare restart identity", "target-helper-plan", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	record, err := harness.journal.ReserveContext(context.Background(), op, plan.PriorHash, plan.DesiredHash, plan.StateHash, plan.AliasCount, now)
	if err != nil {
		t.Fatal(err)
	}
	collision := *record
	collision.Kind = "target.clear"
	if _, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Caller: operator, Reason: "prepare restart identity",
		IdempotencyKey: "target-helper-plan", Deadline: now.Add(time.Minute),
	}, &collision); !errors.Is(err, target.ErrMutationCollision) {
		t.Fatalf("reserved plan collision = %v", err)
	}
	if _, err := harness.coordinator.repairPublication(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	clearPlan := newTargetPlan("target.clear", plan.Resolution, plan.Desired, nil)
	if _, err := harness.coordinator.repairPublication(context.Background(), clearPlan); err != nil {
		t.Fatal(err)
	}
	receipt := targetEffectReceiptFromRecord(*record, now.Add(-time.Minute))
	if !receipt.CompletedAt.Equal(record.CreatedAt) {
		t.Fatalf("receipt completion = %s, want reservation time %s", receipt.CompletedAt, record.CreatedAt)
	}
	if targetApprovalCollision(domain.Approval{}, domain.Approval{}) {
		t.Fatal("identical approvals reported as collision")
	}
	issued := domain.Approval{ID: "approval-00000000000000000000000000000001"}
	if !targetApprovalCollision(domain.Approval{}, issued) {
		t.Fatal("different approvals were not reported as collision")
	}
	coordinator := &TargetCoordinator{}
	if coordinator.now().IsZero() {
		t.Fatal("nil coordinator clock returned zero time")
	}
}

func TestTargetCoordinatorCrashBoundaryHelpersFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	operator := targetOperator(t)
	plan, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Caller: operator, Reason: "test target crash boundary",
		IdempotencyKey: "target-crash-boundary", Deadline: now.Add(time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	op, err := buildTargetOperation(plan, operator, "test target crash boundary", "target-crash-boundary", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	record, err := harness.journal.ReserveContext(context.Background(), op, plan.PriorHash, plan.DesiredHash, plan.StateHash, plan.AliasCount, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.store.Save(context.Background(), plan.Desired.Clone()); err != nil {
		t.Fatal(err)
	}
	publication, commitErr, err := harness.coordinator.applyTargetPlan(context.Background(), plan, op, record, nil, true)
	if err != nil || commitErr != nil || !publication.Durable {
		t.Fatalf("repair applied plan = %+v, %v, %v", publication, commitErr, err)
	}

	drift := *record
	drift.PriorHash = "different-prior"
	drift.DesiredHash = "different-desired"
	if _, _, err := harness.coordinator.applyTargetPlan(context.Background(), plan, op, &drift, nil, true); !errors.Is(err, target.ErrMutationDrift) {
		t.Fatalf("drifted target plan error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := harness.coordinator.currentTargetHash(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("currentTargetHash cancellation = %v", err)
	}
	if _, err := harness.coordinator.repairCurrentPublication(canceled, *record); !errors.Is(err, context.Canceled) {
		t.Fatalf("repairCurrentPublication cancellation = %v", err)
	}
	if err := harness.coordinator.cancelNoEffectTargetRecord(canceled, *record); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelNoEffectTargetRecord cancellation = %v", err)
	}
	if _, err := harness.coordinator.ReconcileStartup(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReconcileStartup cancellation = %v", err)
	}
	if err := harness.coordinator.finalizeTargetEffect(context.Background(), op, domain.Receipt{}); err == nil {
		t.Fatal("invalid terminal receipt unexpectedly finalized")
	}
}

func TestTargetCoordinatorCancelsReservationWhenAdmissionPersistenceFails(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 30, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	operator := targetOperator(t)
	grant := issueEnrollApproval(t, harness, "target-admission-failure", nil, "test admission durability")
	plan, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Caller: operator, Reason: "test admission durability",
		IdempotencyKey: "target-admission-failure", Deadline: grant.Deadline,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	op, err := buildTargetOperation(plan, operator, "test admission durability", "target-admission-failure", grant.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	record, err := harness.journal.ReserveContext(context.Background(), op, plan.PriorHash, plan.DesiredHash, plan.StateHash, plan.AliasCount, now)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := harness.coordinator.approvalStore.LoadIssuedContext(context.Background(), grant.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(harness.state.AuditDir()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := harness.coordinator.applyTargetPlan(context.Background(), plan, op, record, issued, false); err == nil {
		t.Fatal("missing audit namespace unexpectedly admitted target mutation")
	}
	if _, err := harness.journal.LookupContext(context.Background(), op); !errors.Is(err, target.ErrMutationNotFound) {
		t.Fatalf("failed admission retained reservation: %v", err)
	}
	if _, err := harness.store.Load(context.Background()); !errors.Is(err, target.ErrNoDefault) {
		t.Fatalf("failed admission changed target: %v", err)
	}
}

func TestTargetApprovalEvidenceRejectsInvalidOperationsAndCanceledPersistence(t *testing.T) {
	now := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	operator := targetOperator(t)
	if _, err := harness.coordinator.IssueApproval(context.Background(), TargetApprovalIssueParams{
		Kind: "machine.start", Caller: operator, Reason: "invalid approval kind",
		IdempotencyKey: "target-invalid-approval-kind", ValidFor: time.Minute,
	}); !errors.Is(err, domain.ErrInvalidOperationKind) {
		t.Fatalf("invalid approval kind = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := harness.coordinator.IssueApproval(canceled, TargetApprovalIssueParams{
		Kind: "target.enroll", Caller: operator, Reason: "canceled target approval",
		IdempotencyKey: "target-canceled-approval", ValidFor: time.Minute,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled approval issue = %v", err)
	}
	if _, _, err := buildTargetApprovalIssuanceEvidence(operator, domain.Operation{}, domain.Approval{}); err == nil {
		t.Fatal("invalid approved operation unexpectedly produced issuance evidence")
	}
}

func TestTargetCoordinatorStartupReleasesConsumedNoEffectApproval(t *testing.T) {
	now := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	operator := targetOperator(t)
	grant := issueEnrollApproval(t, harness, "target-consumed-no-effect", nil, "cancel consumed no-effect target")
	plan, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Caller: operator, Reason: "cancel consumed no-effect target",
		IdempotencyKey: "target-consumed-no-effect", Deadline: grant.Deadline,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	op, err := buildTargetOperation(plan, operator, "cancel consumed no-effect target", "target-consumed-no-effect", grant.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	record, err := harness.journal.ReserveContext(context.Background(), op, plan.PriorHash, plan.DesiredHash, plan.StateHash, plan.AliasCount, now)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := harness.coordinator.approvalStore.LoadIssuedContext(context.Background(), grant.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.coordinator.approvalStore.MarkConsumedContext(context.Background(), *issued, now); err != nil {
		t.Fatal(err)
	}
	if err := harness.coordinator.cancelNoEffectTargetRecord(context.Background(), *record); err != nil {
		t.Fatal(err)
	}
	if consumed, err := harness.coordinator.approvalStore.IsConsumedContext(context.Background(), grant.ApprovalID); err != nil || consumed {
		t.Fatalf("released approval consumed = %t, %v", consumed, err)
	}
	if changed, err := harness.coordinator.reconcileTargetRecord(context.Background(), target.MutationRecord{State: target.MutationFinalized}); err != nil || changed {
		t.Fatalf("finalized startup record = %t, %v", changed, err)
	}
}

func TestTargetCoordinatorReconstructsReservedClearAfterCommittedRemoval(t *testing.T) {
	now := time.Date(2026, 8, 31, 14, 30, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	operator := targetOperator(t)
	enroll := issueEnrollApproval(t, harness, "target-reserved-clear-seed", nil)
	if _, err := harness.coordinator.Mutate(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Caller: operator, Reason: "enroll synthetic local VM",
		IdempotencyKey: "target-reserved-clear-seed", Deadline: enroll.Deadline, ApprovalID: enroll.ApprovalID,
	}); err != nil {
		t.Fatal(err)
	}
	clearPlan, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.clear", Caller: operator, Reason: "clear reserved authority",
		IdempotencyKey: "target-reserved-clear", Deadline: now.Add(time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	clearOp, err := buildTargetOperation(clearPlan, operator, "clear reserved authority", "target-reserved-clear", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	record, err := harness.journal.ReserveContext(context.Background(), clearOp, clearPlan.PriorHash, clearPlan.DesiredHash, clearPlan.StateHash, clearPlan.AliasCount, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.store.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	reconstructed, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.clear", Caller: operator, Reason: "clear reserved authority",
		IdempotencyKey: "target-reserved-clear", Deadline: now.Add(time.Minute),
	}, record)
	if err != nil || reconstructed.Desired != nil || reconstructed.Resolution.Locator.String() != "local:"+targetVMA {
		t.Fatalf("reserved clear reconstruction = %+v, %v", reconstructed, err)
	}
	invalid := *record
	invalid.Target = "invalid"
	if _, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.clear", Caller: operator, Reason: "clear reserved authority",
		IdempotencyKey: "target-reserved-clear", Deadline: now.Add(time.Minute),
	}, &invalid); !errors.Is(err, target.ErrMutationCollision) {
		t.Fatalf("invalid reserved clear target = %v", err)
	}
}

func TestTargetCoordinatorFinalizationAndApprovalPersistenceFailuresRemainRetryable(t *testing.T) {
	now := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	operator := targetOperator(t)
	plan, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Caller: operator, Reason: "test finalization retry",
		IdempotencyKey: "target-finalization-retry", Deadline: now.Add(time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	op, err := buildTargetOperation(plan, operator, "test finalization retry", "target-finalization-retry", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	record, err := harness.journal.ReserveContext(context.Background(), op, plan.PriorHash, plan.DesiredHash, plan.StateHash, plan.AliasCount, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.coordinator.finalizeAppliedTargetRecord(context.Background(), *record); !errors.Is(err, target.ErrMutationFinalization) {
		t.Fatalf("uncommitted finalization = %v", err)
	}
	otherLocator, err := domain.NewMachineLocator(domain.LocalHostID, targetVMB)
	if err != nil {
		t.Fatal(err)
	}
	other, err := target.NewDefault(otherLocator, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.store.Save(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.coordinator.repairCurrentPublication(context.Background(), *record); !errors.Is(err, target.ErrMutationDrift) {
		t.Fatalf("drifted publication repair = %v", err)
	}

	issued := domain.Approval{
		ID: "approval-00000000000000000000000000000001", Actor: operator.EffectiveActor, Target: op.Target,
		AuthorizedClass: domain.ClassDestructivePrivileged, IdempotencyKey: op.IdempotencyKey,
		IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	issued.Fingerprint, err = op.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	issuanceOp, issuanceReceipt, err := buildTargetApprovalIssuanceEvidence(operator, op, issued)
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.coordinator.persistTargetApproval(context.Background(), &issued, issued, issuanceOp, domain.Receipt{}); err == nil {
		t.Fatal("invalid issuance receipt unexpectedly persisted")
	}
	if err := os.RemoveAll(harness.state.AuditDir()); err != nil {
		t.Fatal(err)
	}
	if err := harness.coordinator.persistTargetApproval(context.Background(), &issued, issued, issuanceOp, issuanceReceipt); err == nil {
		t.Fatal("missing audit namespace unexpectedly finalized approval issuance")
	}
	if err := validateTargetOperator(domain.ActorContext{}); !errors.Is(err, target.ErrAccessDenied) {
		t.Fatalf("invalid operator context = %v", err)
	}
}

func TestTargetCoordinatorStartupFinalizationKeepsRetryableJournalTruth(t *testing.T) {
	now := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	for _, missingDir := range []string{"receipts", "audit"} {
		t.Run(missingDir, func(t *testing.T) {
			requireRetryableTargetFinalization(t, now, missingDir)
		})
	}

	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	missingRecord := target.MutationRecord{
		SchemaVersion: 2, Kind: "target.clear", Actor: targetOperator(t).EffectiveActor,
		Target: "local:" + targetVMA, IdempotencyKey: "target-missing-finalization-record",
		PriorHash: "prior", DesiredHash: target.StateDigest(nil), TransitionHash: "transition",
		State: target.MutationPending, CreatedAt: now, Deadline: now.Add(time.Minute),
	}
	if err := harness.coordinator.finalizeAppliedTargetRecord(context.Background(), missingRecord); !errors.Is(err, target.ErrMutationNotFound) {
		t.Fatalf("missing finalization record = %v", err)
	}
}

func requireRetryableTargetFinalization(t *testing.T, now time.Time, missingDir string) {
	t.Helper()
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	operator := targetOperator(t)
	key := "target-finalize-" + missingDir
	plan, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Caller: operator, Reason: "resume target finalization",
		IdempotencyKey: key, Deadline: now.Add(time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	op, err := buildTargetOperation(plan, operator, "resume target finalization", key, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	record, err := harness.journal.ReserveContext(context.Background(), op, plan.PriorHash, plan.DesiredHash, plan.StateHash, plan.AliasCount, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.store.Save(context.Background(), plan.Desired.Clone()); err != nil {
		t.Fatal(err)
	}
	path := harness.state.ReceiptsDir()
	if missingDir == "audit" {
		path = harness.state.AuditDir()
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := harness.coordinator.finalizeAppliedTargetRecord(context.Background(), *record); err == nil {
		t.Fatalf("missing %s namespace unexpectedly finalized target", missingDir)
	}
	loaded, err := harness.journal.LookupContext(context.Background(), op)
	if err != nil || !loaded.EffectApplied || loaded.State != target.MutationFinalizing {
		t.Fatalf("retryable journal truth = %+v, %v", loaded, err)
	}
}

func TestTargetOperationValidationFailures(t *testing.T) {
	harness := newTargetCoordinatorHarness(t, t.TempDir(), time.Date(2026, 8, 31, 17, 0, 0, 0, time.UTC), nil, nil)
	operator := targetOperator(t)
	plan, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Caller: operator, Reason: "validate target operation",
		IdempotencyKey: "target-operation-validation", Deadline: harness.now.Add(time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildTargetOperation(plan, operator, "", "target-operation-validation", harness.now.Add(time.Minute)); err == nil {
		t.Fatal("empty target reason unexpectedly accepted")
	}
	invalidParameters := plan
	invalidParameters.AliasCount = -1
	if _, err := buildTargetOperation(invalidParameters, operator, "validate target operation", "target-operation-validation", harness.now.Add(time.Minute)); err == nil {
		t.Fatal("negative target alias count unexpectedly accepted")
	}
	if err := (TargetResolution{}).Validate(); err == nil {
		t.Fatal("empty target resolution unexpectedly accepted")
	}
	invalidResolution := plan
	invalidResolution.Resolution = TargetResolution{}
	if err := validateTargetPlan(invalidResolution); err == nil {
		t.Fatal("plan with invalid resolution unexpectedly accepted")
	}
	if err := harness.coordinator.service.validateExplicitTargetReference(targetVMB, plan.Resolution.Locator); err == nil {
		t.Fatal("missing explicit target unexpectedly accepted")
	}
}

func TestTargetClearValidationFailures(t *testing.T) {
	harness := newTargetCoordinatorHarness(t, t.TempDir(), time.Date(2026, 8, 31, 17, 30, 0, 0, time.UTC), nil, nil)
	plan, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Caller: targetOperator(t), Reason: "validate target clear",
		IdempotencyKey: "target-clear-validation", Deadline: harness.now.Add(time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.coordinator.service.PrepareClearDefaultTarget(context.Background()); !errors.Is(err, target.ErrNoDefault) {
		t.Fatalf("clear without target = %v", err)
	}
	if _, err := harness.coordinator.service.ClearDefaultTarget(context.Background()); !errors.Is(err, target.ErrNoDefault) {
		t.Fatalf("direct clear without target = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := harness.coordinator.service.prepareReservedClear(canceled, plan.Resolution.Locator); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reserved clear = %v", err)
	}
	remote, err := domain.NewMachineLocator("remote-a", targetVMA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.coordinator.service.prepareReservedClear(context.Background(), remote); !errors.Is(err, target.ErrUnsupportedHost) {
		t.Fatalf("remote reserved clear = %v", err)
	}
	if _, err := harness.coordinator.service.loadOptional(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled optional target load = %v", err)
	}

	cancelDuringRefresh, cancelRefresh := context.WithCancel(context.Background())
	failingRefresh, err := NewTargetService(harness.inventory, harness.store, WithTargetRefresh(func(context.Context) error {
		cancelRefresh()
		return errors.New("synthetic refresh failure after cancellation")
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failingRefresh.PrepareClearDefaultTarget(cancelDuringRefresh); !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh cancellation precedence = %v", err)
	}

	emptyInventory := targetInventory(t, nil)
	missingInventoryService := targetService(t, emptyInventory, harness.store)
	if _, err := harness.store.Save(context.Background(), plan.Desired.Clone()); err != nil {
		t.Fatal(err)
	}
	if _, err := missingInventoryService.PrepareClearDefaultTarget(context.Background()); err == nil {
		t.Fatal("clear plan without current inventory unexpectedly accepted")
	}
}
