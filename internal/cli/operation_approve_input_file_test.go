package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestOperationApproveInputFileFlagsPreserveRequest(t *testing.T) {
	input := domain.ConsoleInput{Kind: "type", Text: "synthetic approval input"}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "input action.json")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"double-dash-separated", []string{"--input-file", path}},
		{"single-dash-separated", []string{"-input-file", path}},
		{"double-dash-equals", []string{"--input-file=" + path}},
		{"single-dash-equals", []string{"-input-file=" + path}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			application := desktopCLIHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				assertDesktopCLIRequest(t, r, "/v1/operation-approvals", daemon.OperationApprovalIssueRequest{
					Kind: "console.input", Target: "synthetic-target", Reason: "preserve exact approval reason",
					IdempotencyKey: "synthetic-input-approval", ValidForMillis: 60000, Beneficiary: "self",
					Parameters: domain.ConsoleInputParameters(input),
				})
				_ = json.NewEncoder(w).Encode(daemon.OperationApprovalIssueResponse{ApprovalID: "synthetic-approval"})
			})
			application.prompter = targetCommandPrompter{}
			args := append([]string{"operation", "approve", "console.input", "synthetic-target"}, tc.args...)
			args = append(args, "--reason", "preserve exact approval reason", "--idempotency-key", "synthetic-input-approval", "--valid-for", "1m", "--json")
			var stdout, stderr bytes.Buffer
			if code := application.Run(args, &stdout, &stderr); code != ExitSuccess {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			var response daemon.OperationApprovalIssueResponse
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil || response.ApprovalID != "synthetic-approval" || calls != 1 {
				t.Fatalf("response=%+v err=%v calls=%d", response, err, calls)
			}
		})
	}
}

func TestOperationApproveMissingInputFileValueStopsBeforeIssuance(t *testing.T) {
	for _, args := range [][]string{
		{"--input-file"},
		{"-input-file"},
		{"--input-file", "--json"},
		{"-input-file", "--json"},
		{"--input-file="},
		{"-input-file="},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			application := desktopCLIHTTP(t, func(http.ResponseWriter, *http.Request) {
				t.Error("missing input-file value reached approval issuance")
			})
			prompter := &inputFileApprovalPrompter{}
			application.prompter = prompter
			command := []string{"operation", "approve", "console.input", "synthetic-target", "--reason", "synthetic approval reason", "--idempotency-key", "synthetic-missing-input", "--valid-for", "1m"}
			command = append(command, args...)
			var stdout, stderr bytes.Buffer
			if code := application.Run(command, &stdout, &stderr); code != ExitUsage || stdout.Len() != 0 || stderr.Len() == 0 || prompter.calls != 0 {
				t.Fatalf("code=%d stdout=%s stderr=%s confirmations=%d", code, stdout.String(), stderr.String(), prompter.calls)
			}
		})
	}
}

type inputFileApprovalPrompter struct{ calls int }

func (p *inputFileApprovalPrompter) PromptConfirmation(string) bool {
	p.calls++
	return true
}
