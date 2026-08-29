package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/client"
)

func TestCLI_DaemonRouting_MachineStart_SyncAndAsync(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc, cli.WithStateDir(stateDir))

	// 1. Sync machine start --json (waits for terminal completion)
	var syncStdout, syncStderr bytes.Buffer
	code := appInstance.Run([]string{
		"machine", "start", targetID,
		"--reason", "sync start test",
		"--idempotency-key", "key-sync-start-1",
		"--json",
	}, &syncStdout, &syncStderr)

	if code != cli.ExitSuccess {
		t.Fatalf("sync machine start returned code %d; stderr: %s", code, syncStderr.String())
	}

	var syncEnv cli.MachineMutationOutputEnvelope
	if err := json.Unmarshal(syncStdout.Bytes(), &syncEnv); err != nil {
		t.Fatalf("unmarshal sync start failed: %v", err)
	}
	if syncEnv.Receipt.ReceiptID == "" {
		t.Errorf("expected receipt ID in sync start response")
	}

	// 2. Async machine start --async --json
	var asyncStdout, asyncStderr bytes.Buffer
	code = appInstance.Run([]string{
		"machine", "start", targetID,
		"--reason", "async start test",
		"--idempotency-key", "key-async-start-1",
		"--async",
		"--json",
	}, &asyncStdout, &asyncStderr)

	if code != cli.ExitSuccess {
		t.Fatalf("async machine start returned code %d; stderr: %s", code, asyncStderr.String())
	}

	var asyncEnv cli.OperationOutputEnvelope
	if err := json.Unmarshal(asyncStdout.Bytes(), &asyncEnv); err != nil {
		t.Fatalf("unmarshal async start failed: %v", err)
	}
	if asyncEnv.Operation.OperationID == "" {
		t.Errorf("expected operation ID in async response")
	}

	// 3. Async machine stop (human output)
	var stopStdout, stopStderr bytes.Buffer
	code = appInstance.Run([]string{
		"machine", "stop", targetID,
		"--mode", "shutdown",
		"--reason", "async stop test",
		"--idempotency-key", "key-async-stop-1",
		"--async",
	}, &stopStdout, &stopStderr)

	if code != cli.ExitSuccess {
		t.Fatalf("async machine stop returned code %d; stderr: %s", code, stopStderr.String())
	}
	if !strings.Contains(stopStdout.String(), "submitted successfully") {
		t.Errorf("expected submission confirmation in human output: %s", stopStdout.String())
	}
}

func TestCLI_DaemonRouting_DestructiveCheckpoint_DeniedWithoutApproval(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc, cli.WithStateDir(stateDir))

	// 1. Checkpoint create via daemon without approval -> denied with ExitDenied (7)
	var createStdout, createStderr bytes.Buffer
	code := appInstance.Run([]string{
		"checkpoint", "create", targetID,
		"--name", "snap-daemon-test",
		"--reason", "daemon create test",
		"--idempotency-key", "key-chk-create-daemon-1",
		"--json",
	}, &createStdout, &createStderr)

	if code != cli.ExitDenied {
		t.Fatalf("expected ExitDenied (7) for destructive daemon mutation without approval, got %d; stderr: %s", code, createStderr.String())
	}
	if !strings.Contains(createStderr.String(), "policy denied") {
		t.Errorf("expected policy denied in stderr, got: %s", createStderr.String())
	}

	// 2. Checkpoint restore via daemon without approval -> denied with ExitDenied (7)
	var restoreStdout, restoreStderr bytes.Buffer
	code = appInstance.Run([]string{
		"checkpoint", "restore", targetID, snapID,
		"--reason", "daemon restore test",
		"--idempotency-key", "key-chk-restore-daemon-1",
		"--json",
	}, &restoreStdout, &restoreStderr)

	if code != cli.ExitDenied {
		t.Fatalf("expected ExitDenied (7) for destructive daemon mutation without approval, got %d; stderr: %s", code, restoreStderr.String())
	}
	if !strings.Contains(restoreStderr.String(), "policy denied") {
		t.Errorf("expected policy denied in stderr, got: %s", restoreStderr.String())
	}
}

func TestCLI_DaemonRouting_ErrorMapping(t *testing.T) {
	var stderr bytes.Buffer

	// Test mapClientError
	code := cli.MapClientErrorForTest(client.ErrNotFound, &stderr, "test")
	if code != cli.ExitNotFound {
		t.Errorf("expected ExitNotFound, got %d", code)
	}

	code = cli.MapClientErrorForTest(client.ErrDenied, &stderr, "test")
	if code != cli.ExitDenied {
		t.Errorf("expected ExitDenied, got %d", code)
	}

	code = cli.MapClientErrorForTest(client.ErrConflict, &stderr, "test")
	if code != cli.ExitConflict {
		t.Errorf("expected ExitConflict, got %d", code)
	}

	code = cli.MapClientErrorForTest(client.ErrTimeout, &stderr, "test")
	if code != cli.ExitTimeout {
		t.Errorf("expected ExitTimeout, got %d", code)
	}

	code = cli.MapClientErrorForTest(client.ErrInvalidArgument, &stderr, "test")
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage, got %d", code)
	}

	code = cli.MapClientErrorForTest(client.ErrMalformedResponse, &stderr, "test")
	if code != cli.ExitMalformedProvider {
		t.Errorf("expected ExitMalformedProvider, got %d", code)
	}

	// Test mapFailureCategory
	code = cli.MapFailureCategoryForTest("policy_denied", "denied msg", &stderr, "test")
	if code != cli.ExitDenied {
		t.Errorf("expected ExitDenied, got %d", code)
	}

	code = cli.MapFailureCategoryForTest("timeout", "timeout msg", &stderr, "test")
	if code != cli.ExitTimeout {
		t.Errorf("expected ExitTimeout, got %d", code)
	}

	code = cli.MapFailureCategoryForTest("conflict", "conflict msg", &stderr, "test")
	if code != cli.ExitConflict {
		t.Errorf("expected ExitConflict, got %d", code)
	}

	code = cli.MapFailureCategoryForTest("daemon_crash_recovered", "crash", &stderr, "test")
	if code != cli.ExitBackendUnavailable {
		t.Errorf("expected ExitBackendUnavailable, got %d", code)
	}
}
