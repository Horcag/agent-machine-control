package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

type reclassScenario struct {
	name       string
	args       []string
	opKind     string
	stopMode   string
	reason     string
	idempKey   string
	finalState domain.MachineLifecycleState
}

func TestCLI_ReclassificationFingerprintBinding_E2E(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	scenarios := []reclassScenario{
		{
			name: "machine start with no checkpoint escalates and binds reversible fingerprint",
			args: []string{
				"--direct", "machine", "start", targetID,
				"--reason", "emergency start without checkpoint",
				"--idempotency-key", "key-start-escalate",
				"--json",
			},
			opKind:     "machine.start",
			reason:     "emergency start without checkpoint",
			idempKey:   "key-start-escalate",
			finalState: domain.MachineStateRunning,
		},
		{
			name: "machine stop shutdown with no checkpoint escalates and binds reversible fingerprint",
			args: []string{
				"--direct", "machine", "stop", targetID,
				"--mode", "shutdown",
				"--reason", "graceful stop without checkpoint",
				"--idempotency-key", "key-stop-shutdown-escalate",
				"--json",
			},
			opKind:     "machine.stop",
			stopMode:   "shutdown",
			reason:     "graceful stop without checkpoint",
			idempKey:   "key-stop-shutdown-escalate",
			finalState: domain.MachineStateOff,
		},
		{
			name: "machine stop save with no checkpoint escalates and binds reversible fingerprint",
			args: []string{
				"--direct", "machine", "stop", targetID,
				"--mode", "save",
				"--reason", "save stop without checkpoint",
				"--idempotency-key", "key-stop-save-escalate",
				"--json",
			},
			opKind:     "machine.stop",
			stopMode:   "save",
			reason:     "save stop without checkpoint",
			idempKey:   "key-stop-save-escalate",
			finalState: domain.MachineStateSaved,
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			runReclassificationScenario(t, targetID, now, sc)
		})
	}
}

func runReclassificationScenario(t *testing.T, targetID string, now time.Time, sc reclassScenario) {
	dir := t.TempDir()
	leasesDir := filepath.Join(dir, "leases")
	auditDir := filepath.Join(dir, "audit")
	receiptsDir := filepath.Join(dir, "receipts")
	approvalsDir := filepath.Join(dir, "approvals")

	_ = os.MkdirAll(leasesDir, 0700)
	_ = os.MkdirAll(auditDir, 0700)
	_ = os.MkdirAll(receiptsDir, 0700)
	_ = os.MkdirAll(approvalsDir, 0700)

	var providerCalls int
	backend := &mockBackend{
		capabilitiesFn: func(_ context.Context, _ string) (domain.CapabilitySet, error) {
			return domain.DirectMachineCapabilities(), nil
		},
		listCheckpointsFn: func(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
			return nil, nil // No checkpoints!
		},
		startMachineFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			providerCalls++
			return domain.MachineObservation{
				ID:              id,
				State:           sc.finalState,
				ObservedAt:      now,
				ObservationType: domain.ObservationObserved,
			}, nil
		},
		stopMachineFn: func(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
			providerCalls++
			return domain.MachineObservation{
				ID:              id,
				State:           sc.finalState,
				ObservedAt:      now,
				ObservationType: domain.ObservationObserved,
			}, nil
		},
	}

	leaseMgr := lease.NewManager(leasesDir, lease.WithClock(func() time.Time { return now }))
	auditStore := audit.NewStore(auditDir, audit.WithClock(func() time.Time { return now }))
	receiptStore := receipt.NewStore(receiptsDir)
	approvalStore := approval.NewStore(approvalsDir)

	recoverySvc := app.NewRecoveryService(
		backend,
		leaseMgr,
		auditStore,
		receiptStore,
		approvalStore,
		app.WithRecoveryClock(func() time.Time { return now }),
	)
	discoverySvc := app.NewDiscoveryService(backend)

	prompter := &countingPrompter{}
	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))

	appInstance := cli.NewApp(
		discoverySvc,
		cli.WithRecoveryService(recoverySvc),
		cli.WithActor(actor),
		cli.WithPrompter(prompter),
		cli.WithClock(func() time.Time { return now }),
	)

	var stdout, stderr bytes.Buffer
	code := appInstance.Run(sc.args, &stdout, &stderr)

	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d. stderr: %s", code, stderr.String())
	}

	if prompter.calls != 1 {
		t.Errorf("expected exactly 1 interactive confirmation prompt, got %d", prompter.calls)
	}

	if providerCalls != 1 {
		t.Errorf("expected exactly 1 provider call, got %d", providerCalls)
	}

	var env cli.MachineMutationOutputEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to unmarshal JSON output: %v\nstdout: %s", err, stdout.String())
	}

	if env.Receipt.Class != domain.ClassDestructivePrivileged {
		t.Errorf("expected receipt class %s, got %s", domain.ClassDestructivePrivileged, env.Receipt.Class)
	}

	if env.Receipt.Outcome.Status != domain.OutcomeSuccess {
		t.Errorf("expected outcome success, got %s", env.Receipt.Outcome.Status)
	}

	verifyExpectedReversibleFingerprint(t, targetID, now, actor, env, sc)
}

func verifyExpectedReversibleFingerprint(
	t *testing.T,
	targetID string,
	now time.Time,
	actor domain.ActorContext,
	env cli.MachineMutationOutputEnvelope,
	sc reclassScenario,
) {
	var params map[string]any
	if sc.stopMode != "" {
		params = map[string]any{"mode": sc.stopMode}
	}
	expectedOp := domain.Operation{
		Kind:                domain.OperationKind(sc.opKind),
		Target:              domain.MachineRef(targetID),
		Actor:               actor,
		Reason:              sc.reason,
		Deadline:            now.Add(30 * time.Second),
		IdempotencyKey:      sc.idempKey,
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          params,
	}
	if sc.opKind == "machine.stop" {
		expectedOp.RequiredCapability = string(domain.CapabilityMachineStop)
	}
	expectedFP, err := expectedOp.Fingerprint()
	if err != nil {
		t.Fatalf("failed to compute expected fingerprint: %v", err)
	}

	if env.Receipt.Fingerprint != string(expectedFP) {
		t.Errorf("expected receipt fingerprint %s, got %s", expectedFP, env.Receipt.Fingerprint)
	}
}
