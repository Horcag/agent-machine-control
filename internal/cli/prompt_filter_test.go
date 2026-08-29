package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

type countingPrompter struct {
	calls int
}

func (p *countingPrompter) PromptConfirmation(_ string) bool {
	p.calls++
	return true
}

func TestCLI_PromptOnlyOnApprovalRequired_NeverOnOtherErrors(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	categories := []struct {
		name         string
		backendSetup func(b *mockBackend, dir string)
		expectedExit int
	}{
		{
			name: "backend unavailable",
			backendSetup: func(b *mockBackend, _ string) {
				b.startMachineFn = func(_ context.Context, _ string) (domain.MachineObservation, error) {
					return domain.MachineObservation{}, hyperv.ErrBackendUnavailable
				}
			},
			expectedExit: cli.ExitBackendUnavailable,
		},
		{
			name: "capability error",
			backendSetup: func(b *mockBackend, _ string) {
				b.capabilitiesFn = func(_ context.Context, _ string) (domain.CapabilitySet, error) {
					return domain.CapabilitySet{}, errors.New("failed to query hyperv capabilities")
				}
			},
			expectedExit: cli.ExitBackendUnavailable,
		},
		{
			name: "audit failure",
			backendSetup: func(_ *mockBackend, dir string) {
				// make audit dir read only so CheckWritable fails
				_ = os.Chmod(filepath.Join(dir, "audit"), 0500)
			},
			expectedExit: cli.ExitBackendUnavailable,
		},
		{
			name: "lease conflict",
			backendSetup: func(_ *mockBackend, dir string) {
				// Pre-create an active unexpired lease
				lDir := filepath.Join(dir, "leases")
				activeLease := `{"schema_version":"1","machine_id":"` + targetID + `","owner_id":"other:999:abc","runtime_id":"test-runtime","pid":999,"operation_kind":"machine.start","fingerprint":"fp-active","acquired_at":"2026-08-29T12:00:00Z","expires_at":"2026-08-29T13:00:00Z","fencing_generation":1}`
				_ = os.WriteFile(filepath.Join(lDir, targetID+".lease.json"), []byte(activeLease), 0600)
			},
			expectedExit: cli.ExitConflict,
		},
		{
			name: "invalid state",
			backendSetup: func(b *mockBackend, _ string) {
				b.startMachineFn = func(_ context.Context, _ string) (domain.MachineObservation, error) {
					return domain.MachineObservation{}, hyperv.ErrInvalidState
				}
			},
			expectedExit: cli.ExitConflict,
		},
		{
			name: "timeout",
			backendSetup: func(b *mockBackend, _ string) {
				b.startMachineFn = func(_ context.Context, _ string) (domain.MachineObservation, error) {
					return domain.MachineObservation{}, hyperv.ErrCommandTimeout
				}
			},
			expectedExit: cli.ExitTimeout,
		},
		{
			name: "malformed provider",
			backendSetup: func(b *mockBackend, _ string) {
				b.startMachineFn = func(_ context.Context, _ string) (domain.MachineObservation, error) {
					return domain.MachineObservation{}, hyperv.ErrMalformedResponse
				}
			},
			expectedExit: cli.ExitMalformedProvider,
		},
	}

	for _, tc := range categories {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			leasesDir := filepath.Join(dir, "leases")
			auditDir := filepath.Join(dir, "audit")
			receiptsDir := filepath.Join(dir, "receipts")
			approvalsDir := filepath.Join(dir, "approvals")

			_ = os.MkdirAll(leasesDir, 0700)
			_ = os.MkdirAll(auditDir, 0700)
			_ = os.MkdirAll(receiptsDir, 0700)
			_ = os.MkdirAll(approvalsDir, 0700)

			now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
			backend := &mockBackend{
				listCheckpointsFn: func(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
					// Return a rollback checkpoint so start is reversible and policy does not deny initially
					return []domain.CheckpointObservation{
						{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20099", VMID: id, Name: "rollback-1", CreatedAt: now.Add(-time.Hour), ObservedAt: now, ObservationType: domain.ObservationObserved},
					}, nil
				},
				startMachineFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
					return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
				},
				capabilitiesFn: func(_ context.Context, _ string) (domain.CapabilitySet, error) {
					return domain.DirectMachineCapabilities(), nil
				},
			}

			tc.backendSetup(backend, dir)

			leaseMgr := lease.NewManager(leasesDir, lease.WithClock(func() time.Time { return now }))
			auditStore := audit.NewStore(auditDir, audit.WithClock(func() time.Time { return now }), audit.WithLockTimeout(50*time.Millisecond))
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
			code := appInstance.Run([]string{
				"--direct", "machine", "start", targetID,
				"--reason", "test error category",
				"--idempotency-key", "key-error-" + tc.name,
			}, &stdout, &stderr)

			if code != tc.expectedExit {
				t.Errorf("expected exit code %d, got %d. stderr: %s", tc.expectedExit, code, stderr.String())
			}

			// CRITICAL ASSERTION: prompter must NEVER have been called!
			if prompter.calls != 0 {
				t.Fatalf("expected 0 prompt calls for error category %q, but got %d", tc.name, prompter.calls)
			}
		})
	}
}
