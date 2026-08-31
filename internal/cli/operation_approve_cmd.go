package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func runOperationApprove(ctx context.Context, stateDir string, prompter Prompter, args []string, stdout, stderr io.Writer) int {
	positionals, flagArgs := splitPositionalAndFlags(args)
	flags := flag.NewFlagSet("operation approve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	reason := flags.String("reason", "", "exact operation reason (required)")
	key := flags.String("idempotency-key", "", "exact operation idempotency key (required)")
	validityText := flags.String("valid-for", "", "approval validity and exact deadline window (1s-5m, required)")
	mode := flags.String("mode", "shutdown", "machine.stop mode")
	name := flags.String("name", "checkpoint", "checkpoint.create name")
	forMCP := flags.Bool("for-mcp", false, "authorize exact agent:mcp-local execution")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON output")
	_ = flags.String("state-dir", "", "state directory path")
	if err := flags.Parse(flagArgs); err != nil {
		return ExitUsage
	}
	if len(positionals) < 2 || len(positionals) > 3 {
		printOperationApproveUsage(stderr)
		return ExitUsage
	}
	validFor, err := validateSessionApprovalCLIInputs(*reason, *key, *validityText)
	if err != nil {
		fmt.Fprintf(stderr, "amc operation approve: %v\n", err)
		return ExitUsage
	}
	request := daemon.OperationApprovalIssueRequest{
		Kind: positionals[0], Target: positionals[1], Reason: *reason, IdempotencyKey: *key,
		ValidForMillis: int64(validFor / time.Millisecond), Beneficiary: "self",
	}
	if *forMCP {
		request.Beneficiary = "agent:mcp-local"
	}
	if err := populateOperationApprovalParameters(&request, positionals, *mode, *name); err != nil {
		return operationApproveUsageError(stderr, err.Error())
	}
	beneficiary := request.Beneficiary
	if prompter == nil || !prompter.PromptConfirmation(fmt.Sprintf("Issue one-use %s approval for %s on %s?", request.Kind, beneficiary, request.Target)) {
		fmt.Fprintln(stderr, "amc operation approve: confirmation declined")
		return ExitDenied
	}
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		fmt.Fprintln(stderr, "amc operation approve: daemon is unavailable; run 'amcd run'")
		return ExitBackendUnavailable
	}
	response, err := cl.IssueOperationApproval(ctx, request)
	if err != nil {
		return mapClientError(err, stderr, "operation approve")
	}
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(response); err != nil {
			return ExitMalformedProvider
		}
		return ExitSuccess
	}
	fmt.Fprintf(stdout, "Approval ID: %s\nDeadline: %s\nExpires At: %s\nOperation: %s on %s\n", response.ApprovalID, response.Deadline, response.ExpiresAt, response.Operation.Kind, response.Operation.Target)
	return ExitSuccess
}

func populateOperationApprovalParameters(request *daemon.OperationApprovalIssueRequest, positionals []string, mode, name string) error {
	switch request.Kind {
	case "machine.start":
		if len(positionals) != 2 {
			return fmt.Errorf("machine.start requires exactly one target")
		}
	case "machine.stop":
		if len(positionals) != 2 {
			return fmt.Errorf("machine.stop requires exactly one target")
		}
		request.Parameters = map[string]any{"mode": mode}
	case "checkpoint.create":
		if len(positionals) != 2 {
			return fmt.Errorf("checkpoint.create requires exactly one target")
		}
		request.Parameters = map[string]any{"name": name}
	case "checkpoint.restore":
		if len(positionals) != 3 {
			return fmt.Errorf("checkpoint.restore requires target and checkpoint GUID")
		}
		request.Parameters = map[string]any{"checkpoint_id": positionals[2]}
	default:
		return fmt.Errorf("unsupported kind; use machine.start, machine.stop, checkpoint.create, or checkpoint.restore")
	}
	return nil
}

func operationApproveUsageError(stderr io.Writer, message string) int {
	fmt.Fprintf(stderr, "amc operation approve: %s\n", message)
	return ExitUsage
}

func printOperationApproveUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: amc operation approve <kind> <target> [checkpoint-guid] --reason <text> --idempotency-key <key> --valid-for <1s-5m> [--for-mcp] [--json]")
}
