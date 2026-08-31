package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

const (
	targetVMA = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	targetVMB = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
)

func targetObservation(t *testing.T, hostID domain.HostID, id, name string) domain.MachineObservation {
	t.Helper()
	locator, err := domain.NewMachineLocator(hostID, id)
	if err != nil {
		t.Fatalf("NewMachineLocator: %v", err)
	}
	return domain.MachineObservation{
		HostID:              hostID,
		Locator:             locator,
		ID:                  locator.VMID,
		Name:                name,
		State:               domain.MachineStateOff,
		RawState:            "Off",
		Generation:          2,
		Version:             "10.0",
		MemoryAssignedBytes: 1024,
		Capabilities:        domain.ReadOnlyMachineCapabilities(),
		ObservedAt:          time.Date(2026, 8, 31, 1, 0, 0, 0, time.UTC),
		ObservationType:     domain.ObservationObserved,
	}
}

func targetStore(t *testing.T) (*target.Store, string) {
	t.Helper()
	state, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	store, err := target.NewStore(state.TargetsDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store, state.TargetsDir()
}

func targetInventory(t *testing.T, hosts []HostEntry, observations ...domain.MachineObservation) *TrustedInventory {
	t.Helper()
	inventory, err := NewTrustedInventory(hosts)
	if err != nil {
		t.Fatalf("NewTrustedInventory: %v", err)
	}
	byHost := make(map[domain.HostID][]domain.MachineObservation)
	for _, observation := range observations {
		byHost[observation.HostID] = append(byHost[observation.HostID], observation)
	}
	for hostID, machines := range byHost {
		if err := inventory.ApplySnapshot(HostSnapshot{HostID: hostID, Health: HostHealthObserved, Machines: machines}); err != nil {
			t.Fatalf("ApplySnapshot: %v", err)
		}
	}
	return inventory
}

func targetService(t *testing.T, inventory *TrustedInventory, store *target.Store) *TargetService {
	t.Helper()
	service, err := NewTargetService(inventory, store)
	if err != nil {
		t.Fatalf("NewTargetService: %v", err)
	}
	return service
}

func TestTargetServiceEnrollRestartAndResolveExactReferences(t *testing.T) {
	observation := targetObservation(t, domain.LocalHostID, targetVMA, "vm-alpha")
	inventory := targetInventory(t, nil, observation)
	store, dir := targetStore(t)
	service := targetService(t, inventory, store)

	resolution, publication, err := service.EnrollDefaultTarget(context.Background(), observation.Name, []string{"primary"})
	if err != nil || !publication.Committed || !publication.Durable {
		t.Fatalf("EnrollDefaultTarget = %+v, %+v, %v", resolution, publication, err)
	}
	if resolution.Locator != observation.Locator || resolution.ProviderVMID != observation.ID {
		t.Fatalf("resolution = %+v", resolution)
	}

	reopenedStore, err := target.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore restart: %v", err)
	}
	reopened := targetService(t, inventory, reopenedStore)
	for _, reference := range []string{"", "default", "primary", observation.ID, observation.Locator.String()} {
		got, err := reopened.ResolveTarget(context.Background(), reference)
		if err != nil || got.Locator != observation.Locator || got.ProviderVMID != observation.ID {
			t.Fatalf("ResolveTarget(%q) = %+v, %v", reference, got, err)
		}
	}
	if _, err := reopened.ResolveTarget(context.Background(), observation.Name); !errors.Is(err, target.ErrDifferentTarget) {
		t.Fatalf("display-name reference error = %v", err)
	}

	other := targetObservation(t, domain.LocalHostID, targetVMB, "vm-bravo")
	if err := inventory.ApplySnapshot(HostSnapshot{HostID: domain.LocalHostID, Health: HostHealthObserved, Machines: []domain.MachineObservation{observation, other}}); err != nil {
		t.Fatalf("ApplySnapshot other: %v", err)
	}
	if _, err := reopened.ResolveTarget(context.Background(), other.ID); !errors.Is(err, target.ErrDifferentTarget) {
		t.Fatalf("other target error = %v", err)
	}
}

func TestTargetServiceListsFreshLocalCandidatesWithoutAuthorityMutation(t *testing.T) {
	first := targetObservation(t, domain.LocalHostID, targetVMA, "vm-alpha")
	second := targetObservation(t, domain.LocalHostID, targetVMB, "vm-bravo")
	inventory := targetInventory(t, nil, second, first)
	store, _ := targetStore(t)
	service := targetService(t, inventory, store)

	candidates, err := service.ListLocalCandidates(context.Background())
	if err != nil {
		t.Fatalf("ListLocalCandidates: %v", err)
	}
	if len(candidates) != 2 || candidates[0].Locator.String() != first.Locator.String() || candidates[1].Locator.String() != second.Locator.String() {
		t.Fatalf("candidates = %+v", candidates)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, target.ErrNoDefault) {
		t.Fatalf("candidate listing changed target authority: %v", err)
	}
}

func TestTargetServiceRefreshesAndPreparesZeroReferenceWithoutStoreEffect(t *testing.T) {
	observation := targetObservation(t, domain.LocalHostID, targetVMA, "vm-alpha")
	inventory := targetInventory(t, nil)
	store, _ := targetStore(t)
	refreshes := 0
	service, err := NewTargetService(inventory, store, WithTargetRefresh(func(context.Context) error {
		refreshes++
		return inventory.ApplySnapshot(HostSnapshot{HostID: domain.LocalHostID, Health: HostHealthObserved, Machines: []domain.MachineObservation{observation}})
	}))
	if err != nil {
		t.Fatalf("NewTargetService: %v", err)
	}
	plan, err := service.PrepareEnrollDefaultTarget(context.Background(), "", []string{"primary"})
	if err != nil {
		t.Fatalf("PrepareEnrollDefaultTarget: %v", err)
	}
	if refreshes != 1 || plan.Resolution.Locator != observation.Locator || plan.Desired == nil || plan.StateHash == "" {
		t.Fatalf("plan = %+v refreshes=%d", plan, refreshes)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, target.ErrNoDefault) {
		t.Fatalf("prepare changed store: %v", err)
	}
	publication, err := service.CommitTargetPlan(context.Background(), plan)
	if err != nil || !publication.Committed || !publication.Durable {
		t.Fatalf("CommitTargetPlan = %+v, %v", publication, err)
	}
	if _, err := service.ResolveTarget(context.Background(), "default"); err != nil || refreshes != 2 {
		t.Fatalf("ResolveTarget refreshes=%d err=%v", refreshes, err)
	}
}

func TestTargetServiceZeroReferenceFailsClosedForFreshAmbiguity(t *testing.T) {
	one := targetObservation(t, domain.LocalHostID, targetVMA, "vm-alpha")
	two := targetObservation(t, domain.LocalHostID, targetVMB, "vm-bravo")
	inventory := targetInventory(t, nil)
	store, _ := targetStore(t)
	service, err := NewTargetService(inventory, store, WithTargetRefresh(func(context.Context) error {
		return inventory.ApplySnapshot(HostSnapshot{HostID: domain.LocalHostID, Health: HostHealthObserved, Machines: []domain.MachineObservation{one, two}})
	}))
	if err != nil {
		t.Fatalf("NewTargetService: %v", err)
	}
	if _, err := service.PrepareEnrollDefaultTarget(context.Background(), "", nil); !errors.Is(err, domain.ErrMachineReferenceAmbig) {
		t.Fatalf("PrepareEnrollDefaultTarget error = %v", err)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, target.ErrNoDefault) {
		t.Fatalf("ambiguous prepare changed store: %v", err)
	}
}

func TestTargetServiceDisplayNameRecreationDoesNotRetarget(t *testing.T) {
	original := targetObservation(t, domain.LocalHostID, targetVMA, "same-name")
	inventory := targetInventory(t, nil, original)
	store, _ := targetStore(t)
	service := targetService(t, inventory, store)
	if _, _, err := service.EnrollDefaultTarget(context.Background(), original.Name, nil); err != nil {
		t.Fatalf("EnrollDefaultTarget: %v", err)
	}
	recreated := targetObservation(t, domain.LocalHostID, targetVMB, original.Name)
	if err := inventory.ApplySnapshot(HostSnapshot{HostID: domain.LocalHostID, Health: HostHealthObserved, Machines: []domain.MachineObservation{recreated}}); err != nil {
		t.Fatalf("ApplySnapshot recreated: %v", err)
	}
	if _, err := service.ShowDefaultTarget(context.Background()); !errors.Is(err, domain.ErrMachineReferenceStale) {
		t.Fatalf("ShowDefaultTarget error = %v", err)
	}
	if _, err := service.ResolveTarget(context.Background(), original.Name); !errors.Is(err, target.ErrDifferentTarget) {
		t.Fatalf("display-name resolution error = %v", err)
	}
}

func TestTargetServiceEnrollmentFailsClosedForInvalidInventoryState(t *testing.T) {
	local := targetObservation(t, domain.LocalHostID, targetVMA, "vm-alpha")
	remote := targetObservation(t, "remote-a", targetVMB, "vm-remote")
	tests := []struct {
		name    string
		setup   func(*testing.T) *TrustedInventory
		ref     string
		wantErr error
	}{
		{"missing", func(t *testing.T) *TrustedInventory { return targetInventory(t, nil) }, "missing", domain.ErrMachineReferenceMiss},
		{"disabled", func(t *testing.T) *TrustedInventory {
			return targetInventory(t, []HostEntry{{ID: domain.LocalHostID, Address: "local", Enabled: false}}, local)
		}, local.ID, domain.ErrMachineHostDisabled},
		{"stale", func(t *testing.T) *TrustedInventory {
			inventory := targetInventory(t, nil, local)
			_ = inventory.ApplySnapshot(HostSnapshot{HostID: domain.LocalHostID, Health: HostHealthObserved})
			return inventory
		}, local.ID, domain.ErrMachineReferenceStale},
		{"unavailable", func(t *testing.T) *TrustedInventory {
			inventory := targetInventory(t, nil, local)
			_ = inventory.ApplySnapshot(HostSnapshot{HostID: domain.LocalHostID, Health: HostHealthUnavailable})
			return inventory
		}, local.ID, domain.ErrMachineHostUnavailable},
		{"denied", func(t *testing.T) *TrustedInventory {
			inventory := targetInventory(t, nil, local)
			_ = inventory.ApplySnapshot(HostSnapshot{HostID: domain.LocalHostID, Health: HostHealthAccessDenied})
			return inventory
		}, local.ID, domain.ErrMachineAccessDenied},
		{"ambiguous", func(t *testing.T) *TrustedInventory {
			other := targetObservation(t, domain.LocalHostID, targetVMB, local.Name)
			return targetInventory(t, nil, local, other)
		}, local.Name, domain.ErrMachineReferenceAmbig},
		{"remote", func(t *testing.T) *TrustedInventory {
			return targetInventory(t, []HostEntry{{ID: "remote-a", Address: "remote.example", Enabled: true}}, remote)
		}, remote.ID, target.ErrUnsupportedHost},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, _ := targetStore(t)
			service := targetService(t, test.setup(t), store)
			if _, _, err := service.EnrollDefaultTarget(context.Background(), test.ref, nil); !errors.Is(err, test.wantErr) {
				t.Fatalf("EnrollDefaultTarget error = %v, want %v", err, test.wantErr)
			}
			if _, err := store.Load(context.Background()); !errors.Is(err, target.ErrNoDefault) {
				t.Fatalf("rejected enrollment persisted state: %v", err)
			}
		})
	}
}

func TestTargetServiceRejectsAliasCollisionsAndCorruptionWithoutFallback(t *testing.T) {
	primary := targetObservation(t, domain.LocalHostID, targetVMA, "vm-alpha")
	other := targetObservation(t, domain.LocalHostID, targetVMB, "vm-bravo")
	inventory := targetInventory(t, nil, primary, other)
	store, dir := targetStore(t)
	service := targetService(t, inventory, store)
	for _, aliases := range [][]string{{" alias "}, {"duplicate", "duplicate"}, {"default"}, {other.Name}, {other.ID}} {
		if _, _, err := service.EnrollDefaultTarget(context.Background(), primary.ID, aliases); err == nil {
			t.Fatalf("aliases %v unexpectedly accepted", aliases)
		}
	}
	if _, _, err := service.EnrollDefaultTarget(context.Background(), primary.ID, []string{"primary"}); err != nil {
		t.Fatalf("valid enrollment: %v", err)
	}
	corrupt := []byte(`{"schema_version":1,"default_locator":"local:` + targetVMA + `","aliases":[],"unknown":true}`)
	if err := os.WriteFile(filepath.Join(dir, target.StateFileName), corrupt, 0600); err != nil {
		t.Fatalf("WriteFile corrupt: %v", err)
	}
	if _, err := service.ResolveTarget(context.Background(), primary.Name); !errors.Is(err, target.ErrInvalidDocument) {
		t.Fatalf("ResolveTarget corruption error = %v", err)
	}
}

func TestTargetMutationPlanUsesCanonicalIdentityBeforeProviderBoundary(t *testing.T) {
	locator, err := domain.NewMachineLocator(domain.LocalHostID, targetVMA)
	if err != nil {
		t.Fatalf("NewMachineLocator: %v", err)
	}
	scopes := domain.NewScopeSet(domain.ScopeMachineWrite)
	actor, err := domain.NewActorContext("user:tester", "user:tester", scopes, scopes)
	if err != nil {
		t.Fatalf("NewActorContext: %v", err)
	}
	operation := domain.Operation{
		Kind:                "machine.start",
		Target:              domain.MachineRef(targetVMA),
		Actor:               actor,
		Reason:              "test canonical target planning",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      "target-plan-1",
		RequiredScopes:      []string{domain.ScopeMachineWrite},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          map[string]any{"force": false},
	}
	plan, err := buildTargetMutationPlan(TargetResolution{Locator: locator, ProviderVMID: targetVMA, DisplayName: "vm-alpha"}, operation)
	if err != nil {
		t.Fatalf("buildTargetMutationPlan: %v", err)
	}
	if plan.Operation.Target != domain.MachineRef(locator.String()) || plan.ProviderVMID != targetVMA {
		t.Fatalf("plan identities = target %q provider %q", plan.Operation.Target, plan.ProviderVMID)
	}
	if plan.Fingerprint == "" || plan.IdempotencyFingerprint == "" {
		t.Fatalf("plan fingerprints are empty: %+v", plan)
	}
	legacy := operation
	legacy.Target = domain.MachineRef(targetVMA)
	legacyFingerprint, err := legacy.Fingerprint()
	if err != nil {
		t.Fatalf("legacy Fingerprint: %v", err)
	}
	if plan.Fingerprint == legacyFingerprint {
		t.Fatal("canonical and provider-only target fingerprints unexpectedly match")
	}
	if _, err := buildTargetMutationPlan(TargetResolution{Locator: locator, ProviderVMID: targetVMB}, operation); err == nil {
		t.Fatal("mismatched provider GUID unexpectedly accepted")
	}
	invalidOperation := operation
	invalidOperation.IdempotencyKey = ""
	if _, err := buildTargetMutationPlan(TargetResolution{Locator: locator, ProviderVMID: targetVMA}, invalidOperation); err == nil {
		t.Fatal("invalid operation unexpectedly accepted")
	}
}
