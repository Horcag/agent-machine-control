package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

const cliTestVMID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

type mockBackendWithOps struct{}

func (m *mockBackendWithOps) Doctor(_ context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}
func (m *mockBackendWithOps) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	locator, _ := domain.NewMachineLocator(domain.LocalHostID, cliTestVMID)
	return []domain.MachineObservation{{
		HostID: domain.LocalHostID, Locator: locator, ID: cliTestVMID, Name: "cli-test-vm",
		State: domain.MachineStateOff, RawState: "Off", Generation: 2, Version: "10.0",
		MemoryAssignedBytes: 1024, Capabilities: domain.DirectMachineCapabilities(),
		ObservedAt: time.Date(2026, 8, 31, 4, 0, 0, 0, time.UTC), ObservationType: domain.ObservationObserved,
	}}, nil
}
func (m *mockBackendWithOps) InspectMachine(_ context.Context, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}
func (m *mockBackendWithOps) Capabilities(_ context.Context, _ string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(
		domain.CapabilityMachineStart,
		domain.CapabilityMachineStop,
		domain.CapabilityCheckpointCreate,
		domain.CapabilityCheckpointRestore,
	), nil
}
func (m *mockBackendWithOps) StartMachine(_ context.Context, id string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
}
func (m *mockBackendWithOps) StopMachine(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}
func (m *mockBackendWithOps) ListCheckpoints(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
	return []domain.CheckpointObservation{
		{
			ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			Name:            "base",
			VMID:            id,
			CheckpointType:  "Standard",
			CreatedAt:       time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
			ObservedAt:      time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
			ObservationType: domain.ObservationObserved,
		},
	}, nil
}
func (m *mockBackendWithOps) CreateCheckpoint(_ context.Context, _ string, _ string) (domain.CheckpointObservation, error) {
	return domain.CheckpointObservation{}, nil
}
func (m *mockBackendWithOps) RestoreCheckpoint(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func setupDaemonForCLI(t *testing.T) (*daemon.Server, string) {
	dir := t.TempDir()
	state, err := statedir.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	store, err := target.NewStore(state.TargetsDir())
	if err != nil {
		t.Fatal(err)
	}
	locator, _ := domain.NewMachineLocator(domain.LocalHostID, cliTestVMID)
	value, _ := target.NewDefault(locator, []string{"primary"})
	if _, err := store.Save(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	srv, err := daemon.NewServer(daemon.Config{
		StateDir:   dir,
		ListenAddr: "127.0.0.1:0",
		Backend:    &mockBackendWithOps{},
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	return srv, dir
}

func TestCLI_Operation_ListShowWaitCancel(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		t.Fatalf("client.Discover failed: %v", err)
	}

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// Create operation via client first
	opDTO, err := cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "cli test operation",
		IdempotencyKey: "key-cli-op-1",
	})
	if err != nil {
		t.Fatalf("CreateOperation failed: %v", err)
	}

	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc, cli.WithStateDir(stateDir))

	// 1. amc operation list --json
	var listStdout, listStderr bytes.Buffer
	code := appInstance.Run([]string{"operation", "list", "--json"}, &listStdout, &listStderr)
	if code != cli.ExitSuccess {
		t.Fatalf("operation list returned code %d; stderr: %s", code, listStderr.String())
	}

	var listEnv cli.OperationListOutputEnvelope
	if err := json.Unmarshal(listStdout.Bytes(), &listEnv); err != nil {
		t.Fatalf("unmarshal list output failed: %v", err)
	}
	if len(listEnv.Operations) == 0 {
		t.Errorf("expected at least 1 operation in list")
	}

	assertAliasFilteredOperation(t, appInstance)

	// 2. amc operation show <op-id> --json
	var showStdout, showStderr bytes.Buffer
	code = appInstance.Run([]string{"operation", "show", opDTO.OperationID, "--json"}, &showStdout, &showStderr)
	if code != cli.ExitSuccess {
		t.Fatalf("operation show returned code %d; stderr: %s", code, showStderr.String())
	}

	var showEnv cli.OperationOutputEnvelope
	if err := json.Unmarshal(showStdout.Bytes(), &showEnv); err != nil {
		t.Fatalf("unmarshal show output failed: %v", err)
	}
	if showEnv.Operation.OperationID != opDTO.OperationID {
		t.Errorf("expected op ID %s, got %s", opDTO.OperationID, showEnv.Operation.OperationID)
	}

	// 3. amc operation wait <op-id>
	var waitStdout, waitStderr bytes.Buffer
	code = appInstance.Run([]string{"operation", "wait", opDTO.OperationID}, &waitStdout, &waitStderr)
	if code != cli.ExitSuccess {
		t.Fatalf("operation wait returned code %d; stderr: %s", code, waitStderr.String())
	}
	if !strings.Contains(waitStdout.String(), "completed") {
		t.Errorf("expected completed in wait output: %s", waitStdout.String())
	}

	// 4. amc operation cancel
	var cancelStdout, cancelStderr bytes.Buffer
	code = appInstance.Run([]string{"operation", "cancel", opDTO.OperationID, "--reason", "test cancel"}, &cancelStdout, &cancelStderr)
	// Completed operations return conflict/error when cancelled
	if code != cli.ExitConflict && code != cli.ExitSuccess {
		t.Errorf("expected ExitConflict or ExitSuccess for terminal cancel, got %d", code)
	}
}

func assertAliasFilteredOperation(t *testing.T, appInstance *cli.App) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{"operation", "list", "--machine", "primary", "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("operation list alias filter returned code %d; stderr: %s", code, stderr.String())
	}
	var listEnv cli.OperationListOutputEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &listEnv); err != nil || len(listEnv.Operations) != 1 || listEnv.Operations[0].Target != "local:"+cliTestVMID {
		t.Fatalf("alias-filtered operations = %+v, %v", listEnv.Operations, err)
	}
}

func TestCLI_OperationApproveAndExplicitExecution(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()
	application := cli.NewApp(nil, cli.WithStateDir(stateDir), cli.WithPrompter(&testPrompter{confirm: true}))
	reason := "CLI exact approved turn off"
	key := "cli-operation-approval-execution"

	var approvalOut, approvalErr bytes.Buffer
	code := application.Run([]string{
		"operation", "approve", "machine.stop", cliTestVMID,
		"--mode", "turn-off", "--reason", reason, "--idempotency-key", key,
		"--valid-for", "1m", "--json",
	}, &approvalOut, &approvalErr)
	if code != cli.ExitSuccess {
		t.Fatalf("operation approve code=%d stderr=%s", code, approvalErr.String())
	}
	var grant daemon.OperationApprovalIssueResponse
	if err := json.Unmarshal(approvalOut.Bytes(), &grant); err != nil {
		t.Fatalf("decode approval output: %v", err)
	}
	if grant.ApprovalID == "" || grant.Deadline == "" || grant.ExpiresAt == "" {
		t.Fatalf("incomplete grant: %+v", grant)
	}

	var mutationOut, mutationErr bytes.Buffer
	code = application.Run([]string{
		"machine", "stop", cliTestVMID, "--mode", "turn-off",
		"--reason", reason, "--idempotency-key", key, "--timeout", "10s",
		"--approval-id", grant.ApprovalID, "--deadline", grant.Deadline, "--json",
	}, &mutationOut, &mutationErr)
	if code != cli.ExitSuccess {
		t.Fatalf("approved execution code=%d stderr=%s", code, mutationErr.String())
	}
	if strings.Contains(approvalOut.String(), "authorized_class") || strings.Contains(approvalOut.String(), "fingerprint") {
		t.Fatalf("CLI output exposed internal approval authority: %s", approvalOut.String())
	}
}

func TestCLI_OperationApproveValidationConfirmationAndHumanOutput(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()
	confirmed := cli.NewApp(nil, cli.WithStateDir(stateDir), cli.WithPrompter(&testPrompter{confirm: true}))

	for _, args := range [][]string{
		{"operation", "approve"},
		{"operation", "approve", "unknown.kind", cliTestVMID, "--reason", "invalid kind", "--idempotency-key", "invalid-kind", "--valid-for", "1m"},
		{"operation", "approve", "machine.start", cliTestVMID, cliTestVMID, "--reason", "too many targets", "--idempotency-key", "too-many-targets", "--valid-for", "1m"},
		{"operation", "approve", "checkpoint.restore", cliTestVMID, "--reason", "missing checkpoint", "--idempotency-key", "missing-checkpoint", "--valid-for", "1m"},
		{"operation", "approve", "machine.stop", cliTestVMID, "--reason", "invalid validity", "--idempotency-key", "invalid-validity", "--valid-for", "10m"},
	} {
		var stdout, stderr bytes.Buffer
		if code := confirmed.Run(args, &stdout, &stderr); code != cli.ExitUsage {
			t.Fatalf("args %v code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}

	var humanOut, humanErr bytes.Buffer
	code := confirmed.Run([]string{
		"operation", "approve", "checkpoint.create", cliTestVMID, "--name", "coverage checkpoint",
		"--reason", "human approval output", "--idempotency-key", "human-approval-output",
		"--valid-for", "1m", "--for-mcp",
	}, &humanOut, &humanErr)
	if code != cli.ExitSuccess || !strings.Contains(humanOut.String(), "Approval ID:") || !strings.Contains(humanOut.String(), "Expires At:") {
		t.Fatalf("human approval code=%d stdout=%s stderr=%s", code, humanOut.String(), humanErr.String())
	}

	declined := cli.NewApp(nil, cli.WithStateDir(stateDir), cli.WithPrompter(&testPrompter{confirm: false}))
	var declinedOut, declinedErr bytes.Buffer
	code = declined.Run([]string{
		"operation", "approve", "checkpoint.restore", cliTestVMID, "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		"--reason", "decline approval", "--idempotency-key", "declined-approval", "--valid-for", "1m",
	}, &declinedOut, &declinedErr)
	if code != cli.ExitDenied || !strings.Contains(declinedErr.String(), "confirmation declined") {
		t.Fatalf("declined code=%d stderr=%s", code, declinedErr.String())
	}

	unavailable := cli.NewApp(nil, cli.WithStateDir(t.TempDir()), cli.WithPrompter(&testPrompter{confirm: true}))
	var unavailableOut, unavailableErr bytes.Buffer
	code = unavailable.Run([]string{
		"operation", "approve", "machine.stop", cliTestVMID, "--mode", "turn-off",
		"--reason", "daemon unavailable", "--idempotency-key", "unavailable-approval", "--valid-for", "1m",
	}, &unavailableOut, &unavailableErr)
	if code != cli.ExitBackendUnavailable {
		t.Fatalf("unavailable code=%d stderr=%s", code, unavailableErr.String())
	}
}

func TestCLI_Operation_CommandUsageAndErrors(t *testing.T) {
	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc)

	var stdout, stderr bytes.Buffer

	// 1. Missing subcommand
	code := appInstance.Run([]string{"operation"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for missing subcommand, got %d", code)
	}

	// 2. Unknown subcommand
	stderr.Reset()
	code = appInstance.Run([]string{"operation", "unknown"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for unknown subcommand, got %d", code)
	}

	// 3. Help subcommand
	stdout.Reset()
	code = appInstance.Run([]string{"operation", "help"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Errorf("expected ExitSuccess for operation help, got %d", code)
	}

	// 4. Show missing opID
	stderr.Reset()
	code = appInstance.Run([]string{"operation", "show"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for show without ID, got %d", code)
	}

	// 5. Cancel missing reason
	stderr.Reset()
	code = appInstance.Run([]string{"operation", "cancel", "op-123"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for cancel without reason, got %d", code)
	}

	// 6. Wait invalid timeout
	stderr.Reset()
	code = appInstance.Run([]string{"operation", "wait", "op-123", "--timeout", "invalid"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for invalid timeout, got %d", code)
	}
}

func TestCLI_Operation_HumanOutputs(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		t.Fatalf("client.Discover failed: %v", err)
	}

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	opDTO, err := cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "human output test",
		IdempotencyKey: "key-human-op-1",
	})
	if err != nil {
		t.Fatalf("CreateOperation failed: %v", err)
	}

	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc, cli.WithStateDir(stateDir))

	// 1. Human list
	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{"operation", "list"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("operation list human failed: %d", code)
	}

	// 2. Human show
	stdout.Reset()
	stderr.Reset()
	code = appInstance.Run([]string{"operation", "show", opDTO.OperationID}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("operation show human failed: %d", code)
	}
	if !strings.Contains(stdout.String(), opDTO.OperationID) {
		t.Errorf("expected op ID in human show output: %s", stdout.String())
	}
}

func TestCLI_Operation_WaitAndCancelJSON(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		t.Fatalf("client.Discover failed: %v", err)
	}

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	opDTO, err := cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "wait and cancel json test",
		IdempotencyKey: "key-wait-cancel-json-1",
	})
	if err != nil {
		t.Fatalf("CreateOperation failed: %v", err)
	}

	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc, cli.WithStateDir(stateDir))

	// Wait --json
	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{"operation", "wait", opDTO.OperationID, "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("operation wait --json failed: %d; stderr: %s", code, stderr.String())
	}

	// Cancel --json
	stdout.Reset()
	stderr.Reset()
	code = appInstance.Run([]string{"operation", "cancel", opDTO.OperationID, "--reason", "cancel json test", "--json"}, &stdout, &stderr)
	if code != cli.ExitConflict && code != cli.ExitSuccess {
		t.Errorf("expected ExitConflict or ExitSuccess for cancel --json, got %d", code)
	}
}
