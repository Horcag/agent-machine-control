package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

type targetCoordinatorHarness struct {
	state       *statedir.StateDir
	inventory   *TrustedInventory
	store       *target.Store
	journal     *target.MutationJournal
	coordinator *TargetCoordinator
	observation domain.MachineObservation
	now         time.Time
	commits     *int
}

func newTargetCoordinatorHarness(t *testing.T, root string, now time.Time, hook target.MutationJournalHook, commits *int) *targetCoordinatorHarness {
	t.Helper()
	root = filepath.Join(root, "state")
	state, err := statedir.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	observation := targetObservation(t, domain.LocalHostID, targetVMA, "private-display-name")
	inventory, err := NewTrustedInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	storeOptions := []target.Option{}
	if commits != nil {
		storeOptions = append(storeOptions, target.WithOperations(target.Operations{Replace: func(_ context.Context, source, destination string) target.CommitResult {
			*commits++
			if err := os.Rename(source, destination); err != nil {
				return target.CommitResult{Err: err}
			}
			return target.CommitResult{Committed: true}
		}}))
	}
	store, err := target.NewStore(state.TargetsDir(), storeOptions...)
	if err != nil {
		t.Fatal(err)
	}
	refresh := func(context.Context) error {
		return inventory.ApplySnapshot(HostSnapshot{
			HostID: domain.LocalHostID, Health: HostHealthObserved, Machines: []domain.MachineObservation{observation},
		})
	}
	service, err := NewTargetService(inventory, store, WithTargetRefresh(refresh))
	if err != nil {
		t.Fatal(err)
	}
	journalOptions := []target.MutationJournalOption{}
	if hook != nil {
		journalOptions = append(journalOptions, target.WithMutationJournalHook(hook))
	}
	journal, err := target.NewMutationJournal(state.TargetsDir(), journalOptions...)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewTargetCoordinator(
		service, journal, audit.NewStore(state.AuditDir(), audit.WithClock(func() time.Time { return now })),
		receipt.NewStore(state.ReceiptsDir()), approval.NewStore(state.ApprovalsDir()),
		WithTargetCoordinatorClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &targetCoordinatorHarness{
		state: state, inventory: inventory, store: store, journal: journal, coordinator: coordinator,
		observation: observation, now: now, commits: commits,
	}
}

func targetOperator(t *testing.T) domain.ActorContext {
	t.Helper()
	scopes := domain.NewScopeSet(domain.ScopeTargetAdmin)
	actor, err := domain.NewActorContext("operator:target-test", "operator:target-test", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func issueEnrollApproval(t *testing.T, harness *targetCoordinatorHarness, key string, aliases []string, reasons ...string) *TargetApprovalGrant {
	t.Helper()
	reason := "enroll synthetic local VM"
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	grant, err := harness.coordinator.IssueApproval(context.Background(), TargetApprovalIssueParams{
		Kind: "target.enroll", Aliases: aliases, Caller: targetOperator(t), Reason: reason,
		IdempotencyKey: key, ValidFor: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("IssueApproval: %v", err)
	}
	return grant
}

func TestTargetCoordinatorEnrollApprovalAndRedactedEvidence(t *testing.T) {
	now := time.Date(2026, 8, 31, 5, 0, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	aliases := []string{"private-alias"}
	grant := issueEnrollApproval(t, harness, "target-enroll-redacted", aliases)
	result, err := harness.coordinator.Mutate(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Aliases: aliases, Caller: targetOperator(t), Reason: "enroll synthetic local VM",
		IdempotencyKey: "target-enroll-redacted", Deadline: grant.Deadline, ApprovalID: grant.ApprovalID,
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if !result.Publication.Committed || !result.Publication.Durable || result.Receipt.Target != domain.MachineRef("local:"+targetVMA) {
		t.Fatalf("result = %+v", result)
	}
	if err := result.Receipt.Validate(); err != nil {
		t.Fatalf("receipt validation: %v", err)
	}
	for _, dir := range []string{harness.state.AuditDir(), harness.state.ReceiptsDir(), harness.state.ApprovalsDir(), filepath.Join(harness.state.TargetsDir(), "mutations")} {
		assertDirectoryOmitsText(t, dir, "private-alias", "private-display-name")
	}
	if _, err := harness.coordinator.Mutate(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Reference: targetVMA, Aliases: aliases, Caller: targetOperator(t), Reason: "enroll synthetic local VM",
		IdempotencyKey: "target-enroll-redacted", Deadline: grant.Deadline, ApprovalID: grant.ApprovalID,
	}); err != nil {
		t.Fatalf("exact canonical retry: %v", err)
	}
}

func TestTargetCoordinatorIdenticalEnrollConsumesApprovalAndRecordsAdmission(t *testing.T) {
	now := time.Date(2026, 8, 31, 5, 15, 0, 0, time.UTC)
	commits := 0
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, &commits)
	aliases := []string{"primary"}
	seed := issueEnrollApproval(t, harness, "target-identical-seed", aliases)
	if _, err := harness.coordinator.Mutate(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Aliases: aliases, Caller: targetOperator(t), Reason: "enroll synthetic local VM",
		IdempotencyKey: "target-identical-seed", Deadline: seed.Deadline, ApprovalID: seed.ApprovalID,
	}); err != nil {
		t.Fatal(err)
	}

	grant := issueEnrollApproval(t, harness, "target-identical-fresh", aliases, "confirm identical target authority")
	result, err := harness.coordinator.Mutate(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Aliases: aliases, Caller: targetOperator(t), Reason: "confirm identical target authority",
		IdempotencyKey: "target-identical-fresh", Deadline: grant.Deadline, ApprovalID: grant.ApprovalID,
	})
	if err != nil || !result.Publication.Committed || !result.Publication.Durable {
		t.Fatalf("identical mutation = %+v, %v", result, err)
	}
	if consumed, err := harness.coordinator.approvalStore.IsConsumedContext(context.Background(), grant.ApprovalID); err != nil || !consumed {
		t.Fatalf("identical mutation approval consumed = %t, %v", consumed, err)
	}
	if admissions := countTargetAdmissionEvents(t, harness, "target-identical-fresh"); admissions != 1 {
		t.Fatalf("identical mutation admission events = %d, want 1", admissions)
	}
	record, err := harness.journal.LookupKeyContext(context.Background(), targetOperator(t).EffectiveActor, "target-identical-fresh")
	if err != nil || record.State != target.MutationFinalized || !record.EffectApplied || !record.Committed || !record.Durable {
		t.Fatalf("identical mutation record = %+v, %v", record, err)
	}
}

func countTargetAdmissionEvents(t *testing.T, harness *targetCoordinatorHarness, key string) int {
	t.Helper()
	events, err := harness.coordinator.auditStore.Tail(audit.MaxTailLimit)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.EventType == audit.EventAdmissionIntent && event.IdempotencyKey == key {
			count++
		}
	}
	return count
}

func TestTargetCoordinatorClearApprovalRetryAndExactMutationReplay(t *testing.T) {
	now := time.Date(2026, 8, 31, 5, 30, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	enroll := issueEnrollApproval(t, harness, "target-clear-seed", []string{"primary"})
	if _, err := harness.coordinator.Mutate(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Aliases: []string{"primary"}, Caller: targetOperator(t), Reason: "enroll synthetic local VM",
		IdempotencyKey: "target-clear-seed", Deadline: enroll.Deadline, ApprovalID: enroll.ApprovalID,
	}); err != nil {
		t.Fatal(err)
	}
	if shown, err := harness.coordinator.Show(context.Background()); err != nil || shown.Locator.String() != "local:"+targetVMA {
		t.Fatalf("Show = %+v, %v", shown, err)
	}
	issue := TargetApprovalIssueParams{
		Kind: "target.clear", Caller: targetOperator(t), Reason: "clear synthetic target authority",
		IdempotencyKey: "target-clear", ValidFor: time.Minute,
	}
	grant, err := harness.coordinator.IssueApproval(context.Background(), issue)
	if err != nil {
		t.Fatal(err)
	}
	replayedGrant, err := harness.coordinator.IssueApproval(context.Background(), issue)
	if err != nil || replayedGrant.ApprovalID != grant.ApprovalID || !replayedGrant.Deadline.Equal(grant.Deadline) {
		t.Fatalf("approval replay = %+v, %v", replayedGrant, err)
	}
	collision := issue
	collision.Reason = "different clear authority"
	if _, err := harness.coordinator.IssueApproval(context.Background(), collision); !errors.Is(err, receipt.ErrIdempotencyCollision) {
		t.Fatalf("approval collision = %v", err)
	}
	params := TargetMutationParams{
		Kind: "target.clear", Caller: targetOperator(t), Reason: issue.Reason,
		IdempotencyKey: issue.IdempotencyKey, Deadline: grant.Deadline, ApprovalID: grant.ApprovalID,
	}
	cleared, err := harness.coordinator.Mutate(context.Background(), params)
	if err != nil || !cleared.Publication.Durable {
		t.Fatalf("clear = %+v, %v", cleared, err)
	}
	if _, err := harness.store.Load(context.Background()); !errors.Is(err, target.ErrNoDefault) {
		t.Fatalf("target remains after clear: %v", err)
	}
	replayed, err := harness.coordinator.Mutate(context.Background(), params)
	if err != nil || replayed.Receipt.ReceiptID != cleared.Receipt.ReceiptID {
		t.Fatalf("clear replay = %+v, %v", replayed, err)
	}
}

func TestTargetCoordinatorRepairsCommittedEffectAcrossRestartWithoutSecondReplace(t *testing.T) {
	now := time.Date(2026, 8, 31, 6, 0, 0, 0, time.UTC)
	root := t.TempDir()
	commits := 0
	failEffect := true
	harness := newTargetCoordinatorHarness(t, root, now, func(action string) error {
		if action == "effect" && failEffect {
			failEffect = false
			return errors.New("synthetic effect-journal failure")
		}
		return nil
	}, &commits)
	grant := issueEnrollApproval(t, harness, "target-enroll-restart", []string{"primary"}, "restart effect truth test")
	params := TargetMutationParams{
		Kind: "target.enroll", Aliases: []string{"primary"}, Caller: targetOperator(t), Reason: "restart effect truth test",
		IdempotencyKey: "target-enroll-restart", Deadline: grant.Deadline, ApprovalID: grant.ApprovalID,
	}
	first, err := harness.coordinator.Mutate(context.Background(), params)
	requireCommittedTargetFailure(t, first, err, commits)

	restarted := newTargetCoordinatorHarness(t, root, now.Add(time.Second), nil, &commits)
	requireTargetStartupReconcile(t, restarted, params, commits)
	repaired, err := restarted.coordinator.Mutate(context.Background(), params)
	requireRepairedTargetResult(t, repaired, first, err, commits)
	again, err := restarted.coordinator.Mutate(context.Background(), params)
	if err != nil || again.Receipt.ReceiptID != repaired.Receipt.ReceiptID || commits != 1 {
		t.Fatalf("terminal retry = %+v, %v commits=%d", again, err, commits)
	}
}

func requireCommittedTargetFailure(t *testing.T, result TargetMutationResult, err error, commits int) {
	t.Helper()
	if err == nil || !result.Publication.Committed {
		t.Fatalf("first result = %+v, %v", result, err)
	}
	if commits != 1 {
		t.Fatalf("replace commits after first attempt = %d", commits)
	}
}

func requireTargetStartupReconcile(t *testing.T, harness *targetCoordinatorHarness, params TargetMutationParams, commits int) {
	t.Helper()
	if reconciled, err := harness.coordinator.ReconcileStartup(context.Background()); err != nil || reconciled != 1 {
		t.Fatalf("startup reconcile = %d, %v", reconciled, err)
	}
	if commits != 1 {
		t.Fatalf("startup reconcile repeated namespace replace: %d", commits)
	}
	record, err := harness.journal.LookupKeyContext(context.Background(), params.Caller.EffectiveActor, params.IdempotencyKey)
	if err != nil || record.State != target.MutationFinalized || !record.Durable {
		t.Fatalf("reconciled record = %+v, %v", record, err)
	}
}

func requireRepairedTargetResult(t *testing.T, repaired, first TargetMutationResult, err error, commits int) {
	t.Helper()
	if err != nil {
		t.Fatalf("restart retry: %v", err)
	}
	if commits != 1 {
		t.Fatalf("restart repeated namespace replace: %d", commits)
	}
	if repaired.Receipt.ReceiptID != first.Receipt.ReceiptID || !repaired.Publication.Durable {
		t.Fatalf("repaired = %+v first = %+v", repaired, first)
	}
}

func TestTargetCoordinatorStartupCancelsNoEffectReservation(t *testing.T) {
	now := time.Date(2026, 8, 31, 6, 15, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	operator := targetOperator(t)
	plan, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Caller: operator, Reason: "reserve without target effect",
		IdempotencyKey: "target-no-effect", Deadline: now.Add(time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	op, err := buildTargetOperation(plan, operator, "reserve without target effect", "target-no-effect", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.journal.ReserveContext(context.Background(), op, plan.PriorHash, plan.DesiredHash, plan.StateHash, plan.AliasCount, now); err != nil {
		t.Fatal(err)
	}
	if reconciled, err := harness.coordinator.ReconcileStartup(context.Background()); err != nil || reconciled != 1 {
		t.Fatalf("startup reconcile = %d, %v", reconciled, err)
	}
	if _, err := harness.journal.LookupKeyContext(context.Background(), operator.EffectiveActor, "target-no-effect"); !errors.Is(err, target.ErrMutationNotFound) {
		t.Fatalf("no-effect reservation remains: %v", err)
	}
}

func TestTargetCoordinatorStartupRejectsUnknownAuthorityState(t *testing.T) {
	now := time.Date(2026, 8, 31, 6, 30, 0, 0, time.UTC)
	harness := newTargetCoordinatorHarness(t, t.TempDir(), now, nil, nil)
	operator := targetOperator(t)
	plan, err := harness.coordinator.prepareMutationPlan(context.Background(), TargetMutationParams{
		Kind: "target.enroll", Aliases: []string{"primary"}, Caller: operator, Reason: "reserve target authority",
		IdempotencyKey: "target-drift", Deadline: now.Add(time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	op, err := buildTargetOperation(plan, operator, "reserve target authority", "target-drift", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.journal.ReserveContext(context.Background(), op, plan.PriorHash, plan.DesiredHash, plan.StateHash, plan.AliasCount, now); err != nil {
		t.Fatal(err)
	}
	otherLocator, err := domain.NewMachineLocator(domain.LocalHostID, "d4a523d4-6b99-4d62-a5e2-4752c0f20002")
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
	if reconciled, err := harness.coordinator.ReconcileStartup(context.Background()); !errors.Is(err, target.ErrMutationDrift) || reconciled != 0 {
		t.Fatalf("startup reconcile = %d, %v; want fail-closed drift", reconciled, err)
	}
}

func TestTargetCoordinatorRejectsAgentBeforeReservation(t *testing.T) {
	harness := newTargetCoordinatorHarness(t, t.TempDir(), time.Now().UTC(), nil, nil)
	scopes := domain.NewScopeSet(domain.ScopeMachineWrite)
	agent, err := domain.NewActorContext("agent:mcp-local", "agent:mcp-local", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.coordinator.IssueApproval(context.Background(), TargetApprovalIssueParams{
		Kind: "target.enroll", Caller: agent, Reason: "forbidden agent enrollment", IdempotencyKey: "agent-target", ValidFor: time.Minute,
	}); !errors.Is(err, target.ErrAccessDenied) {
		t.Fatalf("IssueApproval error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(harness.state.TargetsDir(), "mutations"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("agent created reservations: %v, %v", entries, err)
	}
	if _, err := harness.store.Load(context.Background()); !errors.Is(err, target.ErrNoDefault) {
		t.Fatalf("agent changed target: %v", err)
	}
}

func TestTargetCoordinatorRejectsDelegatedAndUnscopedOperators(t *testing.T) {
	harness := newTargetCoordinatorHarness(t, t.TempDir(), time.Now().UTC(), nil, nil)
	targetScope := domain.NewScopeSet(domain.ScopeTargetAdmin)
	delegated, err := domain.NewActorContext("operator:delegating", "operator:delegate", targetScope, targetScope)
	if err != nil {
		t.Fatal(err)
	}
	machineScope := domain.NewScopeSet(domain.ScopeMachineWrite)
	unscoped, err := domain.NewActorContext("operator:unscoped", "operator:unscoped", machineScope, machineScope)
	if err != nil {
		t.Fatal(err)
	}
	for name, caller := range map[string]domain.ActorContext{"delegated": delegated, "unscoped": unscoped} {
		t.Run(name, func(t *testing.T) {
			if _, err := harness.coordinator.IssueApproval(context.Background(), TargetApprovalIssueParams{
				Kind: "target.enroll", Caller: caller, Reason: "forbidden target mutation",
				IdempotencyKey: "forbidden-" + name, ValidFor: time.Minute,
			}); !errors.Is(err, target.ErrAccessDenied) {
				t.Fatalf("IssueApproval error = %v", err)
			}
		})
	}
}

func assertDirectoryOmitsText(t *testing.T, dir string, forbidden ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range forbidden {
			if strings.Contains(string(payload), value) {
				t.Fatalf("%s persisted forbidden plaintext %q", filepath.Join(dir, entry.Name()), value)
			}
		}
	}
}
