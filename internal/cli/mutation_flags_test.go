package cli_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestDefaultPrompter_PromptConfirmation(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"Y\n", true},
		{"YES\n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false},
		{"other\n", false},
	}

	for _, tc := range cases {
		t.Run(strings.TrimSpace(tc.input), func(t *testing.T) {
			var out bytes.Buffer
			prompter := &cli.DefaultPrompter{
				Stdin:  strings.NewReader(tc.input),
				Stdout: &out,
			}
			result := prompter.PromptConfirmation("Confirm?")
			if result != tc.expected {
				t.Errorf("for input %q expected %v, got %v", tc.input, tc.expected, result)
			}
		})
	}
}

func TestOperationApprovalReferenceFlagValidationAndDirectRefusal(t *testing.T) {
	application := setupTestApp(t, &mockBackend{}, &testPrompter{confirm: true})
	base := []string{
		"--direct", "machine", "start", "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		"--reason", "validate approval flags", "--idempotency-key", "validate-approval-flags",
	}
	tests := []struct {
		name string
		args []string
	}{
		{name: "id only", args: append(append([]string{}, base...), "--approval-id", "app-operation-0123456789abcdef0123456789abcdef")},
		{name: "invalid id", args: append(append([]string{}, base...), "--approval-id", "../bad", "--deadline", "2026-08-31T04:00:00Z")},
		{name: "invalid deadline", args: append(append([]string{}, base...), "--approval-id", "app-operation-0123456789abcdef0123456789abcdef", "--deadline", "not-a-deadline")},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		if code := application.Run(test.args, &stdout, &stderr); code != cli.ExitUsage {
			t.Fatalf("%s code=%d stdout=%s stderr=%s", test.name, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	valid := append(append([]string{}, base...),
		"--approval-id", "app-operation-0123456789abcdef0123456789abcdef",
		"--deadline", "2026-08-31T04:00:00Z",
	)
	if code := application.Run(valid, &stdout, &stderr); code != cli.ExitUsage || !strings.Contains(stderr.String(), "requires daemon mode") {
		t.Fatalf("direct refusal code=%d stderr=%s", code, stderr.String())
	}
}

func TestCLI_ApprovalFile_Rejected(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"

	dir := t.TempDir()
	appFile := dir + "/approval.json"
	_ = os.WriteFile(appFile, []byte(`{}`), 0600)

	backend := &mockBackend{}
	appInstance := setupTestApp(t, backend, nil)

	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{
		"--direct", "checkpoint", "restore", targetID, snapID,
		"--approval-file", appFile,
		"--reason", "testing with approval file",
		"--idempotency-key", "idemp-key-file",
	}, &stdout, &stderr)

	if code != cli.ExitUsage {
		t.Fatalf("expected ExitUsage when --approval-file is passed, got %d. stderr: %s", code, stderr.String())
	}
}

func TestCLI_InteractiveApproval_ConfirmationSucceeds(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"

	backend := &mockBackend{
		restoreCheckpointFn: func(_ context.Context, id, _ string) (domain.MachineObservation, error) {
			return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
		},
	}
	prompter := &testPrompter{confirm: true}
	appInstance := setupTestApp(t, backend, prompter)

	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{
		"--direct", "checkpoint", "restore", targetID, snapID,
		"--reason", "testing interactive confirmation",
		"--idempotency-key", "idemp-key-interactive",
	}, &stdout, &stderr)

	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess with confirmed interactive prompt, got %d. stderr: %s", code, stderr.String())
	}
}

func TestCLI_MutationErrorMappings(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"

	errCases := []struct {
		name         string
		restoreErr   error
		expectedCode int
	}{
		{"policy denied", &app.PolicyDeniedError{Reason: "denied", Message: "policy rejection"}, cli.ExitDenied},
		{"approval consumed", domain.ErrApprovalConsumed, cli.ExitDenied},
		{"approval expired", domain.ErrApprovalExpired, cli.ExitDenied},
		{"approval mismatch", domain.ErrApprovalFingerprintMismatch, cli.ExitDenied},
		{"idempotency collision", receipt.ErrIdempotencyCollision, cli.ExitConflict},
		{"audit unavailable", app.ErrAuditUnavailable, cli.ExitBackendUnavailable},
	}

	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &mockBackend{
				restoreCheckpointFn: func(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
					return domain.MachineObservation{}, tc.restoreErr
				},
			}
			prompter := &testPrompter{confirm: true}
			appInstance := setupTestApp(t, backend, prompter)

			var stdout, stderr bytes.Buffer
			code := appInstance.Run([]string{
				"--direct", "checkpoint", "restore", targetID, snapID,
				"--reason", "testing error mapping",
				"--idempotency-key", "key-err-map",
			}, &stdout, &stderr)

			if code != tc.expectedCode {
				t.Errorf("for error %v expected exit code %d, got %d. stderr: %s", tc.restoreErr, tc.expectedCode, code, stderr.String())
			}
		})
	}
}
