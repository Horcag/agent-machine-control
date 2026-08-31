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

func issueEnrollApproval(t *testing.T, harness *targetCoordinatorHarness, key string, aliases []string) *TargetApprovalGrant {
	t.Helper()
	grant, err := harness.coordinator.IssueApproval(context.Background(), TargetApprovalIssueParams{
		Kind: "target.enroll", Aliases: aliases, Caller: targetOperator(t), Reason: "enroll synthetic local VM",
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
	grant := issueEnrollApproval(t, harness, "target-enroll-restart", []string{"primary"})
	params := TargetMutationParams{
		Kind: "target.enroll", Aliases: []string{"primary"}, Caller: targetOperator(t), Reason: "restart effect truth test",
		IdempotencyKey: "target-enroll-restart", Deadline: grant.Deadline, ApprovalID: grant.ApprovalID,
	}
	first, err := harness.coordinator.Mutate(context.Background(), params)
	if err == nil || !first.Publication.Committed {
		t.Fatalf("first result = %+v, %v", first, err)
	}
	if commits != 1 {
		t.Fatalf("replace commits after first attempt = %d", commits)
	}

	restarted := newTargetCoordinatorHarness(t, root, now.Add(time.Second), nil, &commits)
	repaired, err := restarted.coordinator.Mutate(context.Background(), params)
	if err != nil {
		t.Fatalf("restart retry: %v", err)
	}
	if commits != 1 {
		t.Fatalf("restart repeated namespace replace: %d", commits)
	}
	if repaired.Receipt.ReceiptID != first.Receipt.ReceiptID || !repaired.Publication.Durable {
		t.Fatalf("repaired = %+v first = %+v", repaired, first)
	}
	again, err := restarted.coordinator.Mutate(context.Background(), params)
	if err != nil || again.Receipt.ReceiptID != repaired.Receipt.ReceiptID || commits != 1 {
		t.Fatalf("terminal retry = %+v, %v commits=%d", again, err, commits)
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
