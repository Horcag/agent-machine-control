package cli_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

type recordingPrompter struct {
	prompts []string
}

func (p *recordingPrompter) PromptConfirmation(prompt string) bool {
	p.prompts = append(p.prompts, prompt)
	return true
}

func TestCLIDirectPublicReferencesCanonicalizeApprovalAndProviderBoundaries(t *testing.T) {
	const vmID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	const checkpointID = "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	locator, err := domain.NewMachineLocator(domain.LocalHostID, vmID)
	if err != nil {
		t.Fatal(err)
	}
	observation := domain.MachineObservation{
		HostID: domain.LocalHostID, Locator: locator, ID: vmID, Name: "private-display",
		State: domain.MachineStateOff, RawState: "Off", Generation: 2, Version: "10.0",
		MemoryAssignedBytes: 1024, Capabilities: domain.DirectMachineCapabilities(),
		ObservedAt: now, ObservationType: domain.ObservationObserved,
	}
	var providerTargets []string
	backend := &mockBackend{
		listMachinesFn: func(context.Context) ([]domain.MachineObservation, error) {
			return []domain.MachineObservation{observation}, nil
		},
		capabilitiesFn: func(_ context.Context, id string) (domain.CapabilitySet, error) {
			providerTargets = append(providerTargets, id)
			return domain.DirectMachineCapabilities(), nil
		},
		listCheckpointsFn: func(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
			providerTargets = append(providerTargets, id)
			return []domain.CheckpointObservation{{
				ID: checkpointID, Name: "baseline", VMID: id, CheckpointType: "Standard",
				CreatedAt: now.Add(-time.Hour), ObservedAt: now, ObservationType: domain.ObservationObserved,
			}}, nil
		},
		startMachineFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			providerTargets = append(providerTargets, id)
			return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
		},
		stopMachineFn: func(_ context.Context, id, _ string) (domain.MachineObservation, error) {
			providerTargets = append(providerTargets, id)
			return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
		},
		createCheckpointFn: func(_ context.Context, id, name string) (domain.CheckpointObservation, error) {
			providerTargets = append(providerTargets, id)
			return domain.CheckpointObservation{ID: checkpointID, Name: name, VMID: id}, nil
		},
		restoreCheckpointFn: func(_ context.Context, id, _ string) (domain.MachineObservation, error) {
			providerTargets = append(providerTargets, id)
			return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
		},
	}

	state, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	targetStore, err := target.NewStore(state.TargetsDir())
	if err != nil {
		t.Fatal(err)
	}
	defaultTarget, err := target.NewDefault(locator, []string{"primary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := targetStore.Save(context.Background(), defaultTarget); err != nil {
		t.Fatal(err)
	}
	inventory, err := app.NewTrustedInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	refresh := func(context.Context) error {
		return inventory.ApplySnapshot(app.HostSnapshot{HostID: domain.LocalHostID, Health: app.HostHealthObserved, Machines: []domain.MachineObservation{observation}})
	}
	targetService, err := app.NewTargetService(inventory, targetStore, app.WithTargetRefresh(refresh))
	if err != nil {
		t.Fatal(err)
	}
	recovery := app.NewRecoveryService(
		backend, lease.NewManager(state.LeasesDir()), audit.NewStore(state.AuditDir()), receipt.NewStore(state.ReceiptsDir()), approval.NewStore(state.ApprovalsDir()),
		app.WithRecoveryTargetResolver(targetService), app.WithRecoveryClock(func() time.Time { return now }),
	)
	scopes := domain.NewScopeSet(domain.ScopeMachineWrite)
	actor, err := domain.NewActorContext("operator:direct-test", "operator:direct-test", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	prompter := &recordingPrompter{}
	application := cli.NewApp(app.NewDiscoveryService(backend), cli.WithRecoveryService(recovery), cli.WithActor(actor), cli.WithPrompter(prompter), cli.WithClock(func() time.Time { return now }), cli.WithStateDir(filepath.Dir(state.TargetsDir())))

	commands := [][]string{
		{"--direct", "machine", "start", "primary", "--reason", "start public alias", "--idempotency-key", "public-start"},
		{"--direct", "machine", "stop", "default", "--reason", "stop public default", "--idempotency-key", "public-stop"},
		{"--direct", "checkpoint", "list", vmID},
		{"--direct", "checkpoint", "create", locator.String(), "--name", "public-checkpoint", "--reason", "create public checkpoint", "--idempotency-key", "public-create"},
		{"--direct", "checkpoint", "restore", "primary", checkpointID, "--reason", "restore public checkpoint", "--idempotency-key", "public-restore"},
	}
	runPublicReferenceCommands(t, application, commands)
	assertCanonicalProviderTargets(t, providerTargets, vmID)
	assertCanonicalApprovalPrompts(t, prompter.prompts, locator.String())
}

func runPublicReferenceCommands(t *testing.T, application *cli.App, commands [][]string) {
	t.Helper()
	for _, command := range commands {
		var stdout, stderr bytes.Buffer
		if code := application.Run(command, &stdout, &stderr); code != cli.ExitSuccess {
			t.Fatalf("command %v code=%d stderr=%s", command, code, stderr.String())
		}
	}
}

func assertCanonicalProviderTargets(t *testing.T, providerTargets []string, vmID string) {
	t.Helper()
	for _, targetID := range providerTargets {
		if targetID != vmID {
			t.Fatalf("provider target = %q, want GUID %q; all=%v", targetID, vmID, providerTargets)
		}
	}
}

func assertCanonicalApprovalPrompts(t *testing.T, prompts []string, locator string) {
	t.Helper()
	if len(prompts) != 2 {
		t.Fatalf("approval prompts = %v, want checkpoint create/restore only", prompts)
	}
	for _, prompt := range prompts {
		if !strings.Contains(prompt, locator) || strings.Contains(prompt, "primary") || strings.Contains(prompt, "default") {
			t.Fatalf("approval prompt did not bind canonical target: %q", prompt)
		}
	}
}
