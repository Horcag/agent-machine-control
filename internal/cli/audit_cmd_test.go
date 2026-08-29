package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestCLI_Audit_TailAndShow(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		t.Fatalf("client.Discover failed: %v", err)
	}

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// Create operation and wait for receipt
	opDTO, err := cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "audit test operation",
		IdempotencyKey: "key-audit-cli-1",
	})
	if err != nil {
		t.Fatalf("CreateOperation failed: %v", err)
	}

	finalDTO, err := cl.WaitOperation(context.Background(), opDTO.OperationID, 0, 0)
	if err != nil {
		t.Fatalf("WaitOperation failed: %v", err)
	}

	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc, cli.WithStateDir(stateDir))

	// 1. amc audit tail --json
	var tailStdout, tailStderr bytes.Buffer
	code := appInstance.Run([]string{"audit", "tail", "--json"}, &tailStdout, &tailStderr)
	if code != cli.ExitSuccess {
		t.Fatalf("audit tail returned code %d; stderr: %s", code, tailStderr.String())
	}

	var tailEnv cli.AuditTailOutputEnvelope
	if err := json.Unmarshal(tailStdout.Bytes(), &tailEnv); err != nil {
		t.Fatalf("unmarshal audit tail output failed: %v", err)
	}
	if len(tailEnv.Events) == 0 {
		t.Errorf("expected at least 1 audit event")
	}

	if finalDTO.ReceiptID == "" {
		t.Fatalf("expected non-empty ReceiptID")
	}

	// 2. amc audit show <receipt-id> --json
	var showStdout, showStderr bytes.Buffer
	code = appInstance.Run([]string{"audit", "show", finalDTO.ReceiptID, "--json"}, &showStdout, &showStderr)
	if code != cli.ExitSuccess {
		t.Fatalf("audit show returned code %d; stderr: %s", code, showStderr.String())
	}

	var rcptEnv cli.ReceiptOutputEnvelope
	if err := json.Unmarshal(showStdout.Bytes(), &rcptEnv); err != nil {
		t.Fatalf("unmarshal receipt output failed: %v", err)
	}
	if rcptEnv.Receipt.ReceiptID != finalDTO.ReceiptID {
		t.Errorf("expected receipt ID %s, got %s", finalDTO.ReceiptID, rcptEnv.Receipt.ReceiptID)
	}

	// 3. amc audit show <receipt-id> (human)
	showStdout.Reset()
	showStderr.Reset()
	code = appInstance.Run([]string{"audit", "show", finalDTO.ReceiptID}, &showStdout, &showStderr)
	if code != cli.ExitSuccess {
		t.Fatalf("audit show human returned code %d; stderr: %s", code, showStderr.String())
	}
	if !strings.Contains(showStdout.String(), finalDTO.ReceiptID) {
		t.Errorf("expected receipt ID in human output: %s", showStdout.String())
	}
}

func TestCLI_Audit_CommandUsageAndErrors(t *testing.T) {
	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc)

	var stdout, stderr bytes.Buffer

	// 1. Missing subcommand
	code := appInstance.Run([]string{"audit"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for missing subcommand, got %d", code)
	}

	// 2. Unknown subcommand
	stderr.Reset()
	code = appInstance.Run([]string{"audit", "unknown"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for unknown subcommand, got %d", code)
	}

	// 3. Help subcommand
	stdout.Reset()
	code = appInstance.Run([]string{"audit", "help"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Errorf("expected ExitSuccess for audit help, got %d", code)
	}

	// 4. Show missing receiptID
	stderr.Reset()
	code = appInstance.Run([]string{"audit", "show"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for show without receipt ID, got %d", code)
	}
}

func TestCLI_Audit_HumanTailAndEmpty(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc, cli.WithStateDir(stateDir))

	// 1. Human tail on empty audit
	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{"audit", "tail"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("audit tail human failed: %d", code)
	}

	// 2. Submit operation so audit is populated
	cl, _ := client.Discover(stateDir, client.TokenTypeOperator)
	opDTO, _ := cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason:         "populated audit test",
		IdempotencyKey: "key-populated-audit-1",
	})
	_, _ = cl.WaitOperation(context.Background(), opDTO.OperationID, 0, 0)

	// 3. Human tail on populated audit
	stdout.Reset()
	stderr.Reset()
	code = appInstance.Run([]string{"audit", "tail", "-n", "5"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("audit tail with -n failed: %d", code)
	}
	if !strings.Contains(stdout.String(), "machine.start") {
		t.Errorf("expected machine.start in human audit tail output: %s", stdout.String())
	}
}

func TestCLI_Audit_Follow(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		t.Fatalf("client.Discover failed: %v", err)
	}

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// Create initial operation so history is populated
	op1, _ := cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "initial op",
		IdempotencyKey: "test-follow-hist",
	})
	_, _ = cl.WaitOperation(context.Background(), op1.OperationID, 0, 0)

	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc, cli.WithStateDir(stateDir))

	// 1. Human follow
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		// Submit live op while follow is active
		_, _ = cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
			Kind:           "machine.start",
			Target:         targetID,
			Reason:         "live op",
			IdempotencyKey: "key-follow-live-1",
		})
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	code := appInstance.RunWithContext(ctx, []string{"audit", "tail", "--follow"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("audit tail --follow failed: %d", code)
	}

	// 2. JSON follow
	stdout.Reset()
	stderr.Reset()
	ctxJSON, cancelJSON := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
			Kind:           "machine.start",
			Target:         targetID,
			Reason:         "live op json",
			IdempotencyKey: "key-follow-live-2",
		})
		time.Sleep(50 * time.Millisecond)
		cancelJSON()
	}()

	code = appInstance.RunWithContext(ctxJSON, []string{"audit", "tail", "--follow", "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("audit tail --follow --json failed: %d", code)
	}
}

func TestCLI_Audit_DaemonUnavailable(t *testing.T) {
	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc, cli.WithStateDir(t.TempDir()))

	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{"audit", "tail"}, &stdout, &stderr)
	if code != cli.ExitBackendUnavailable {
		t.Errorf("expected ExitBackendUnavailable for missing daemon on tail, got %d", code)
	}

	stderr.Reset()
	code = appInstance.Run([]string{"audit", "show", "rcpt-00000000000000000000000000000001"}, &stdout, &stderr)
	if code != cli.ExitBackendUnavailable {
		t.Errorf("expected ExitBackendUnavailable for missing daemon on show, got %d", code)
	}
}

func TestCLI_Audit_ShowNotFoundAndWithNow(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc, cli.WithStateDir(stateDir), cli.WithNow(func() time.Time { return now }))

	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{"audit", "show", "rcpt-00000000000000000000000000000000"}, &stdout, &stderr)
	if code != cli.ExitNotFound {
		t.Errorf("expected ExitNotFound (5) for missing receipt, got %d", code)
	}

	// Flag error
	stderr.Reset()
	code = appInstance.Run([]string{"audit", "tail", "--unknown-flag"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for unknown flag, got %d", code)
	}

	stderr.Reset()
	code = appInstance.Run([]string{"audit", "show", "--unknown-flag"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for unknown flag on show, got %d", code)
	}
}

func TestCLI_Audit_Follow_PublishDuringHandoff(t *testing.T) {
	srv, stateDir := setupDaemonForCLI(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		t.Fatalf("client.Discover failed: %v", err)
	}

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// 1. Submit initial operation to populate historical audit log
	opInit, err := cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "initial history op",
		IdempotencyKey: "test-init-1",
	})
	if err != nil {
		t.Fatalf("initial CreateOperation failed: %v", err)
	}
	finalInit, err := cl.WaitOperation(context.Background(), opInit.OperationID, 0, 0)
	if err != nil {
		t.Fatalf("WaitOperation failed: %v", err)
	}
	if finalInit.ReceiptID == "" {
		t.Fatalf("expected non-empty ReceiptID for initial operation")
	}

	discoverySvc := app.NewDiscoveryService(&mockBackendWithOps{})
	appInstance := cli.NewApp(discoverySvc, cli.WithStateDir(stateDir))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout syncBuffer
	var stderr bytes.Buffer

	// Launch concurrent publishers during the follow startup / handoff phase
	liveReceipts := make(chan string, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		opLive, _ := cl.CreateOperation(context.Background(), daemon.CreateOperationRequest{
			Kind:           "machine.start",
			Target:         targetID,
			Reason:         "publish during handoff",
			IdempotencyKey: "test-live-1",
		})
		finalLive, _ := cl.WaitOperation(context.Background(), opLive.OperationID, 0, 0)
		liveReceipts <- finalLive.ReceiptID
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		code := appInstance.RunWithContext(ctx, []string{"audit", "tail", "--follow", "--json", "-n", "10"}, &stdout, &stderr)
		if code != cli.ExitSuccess {
			t.Errorf("audit tail --follow during handoff failed with code %d: %s", code, stderr.String())
		}
	}()

	liveReceiptID := <-liveReceipts

	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), liveReceiptID) {
			found = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if !found {
		t.Fatalf("expected audit follow output to contain live receipt %s", liveReceiptID)
	}

	output := stdout.String()
	count := strings.Count(output, liveReceiptID)
	if count != 1 {
		t.Errorf("expected live receipt to appear exactly once, got %d", count)
	}
}
