package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func runSessionApprove(ctx context.Context, cl *client.Client, prompter Prompter, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printSessionApproveUsage(stderr)
		return ExitUsage
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		printSessionApproveUsage(stdout)
		return ExitSuccess
	}
	action := args[0]
	positionals, flagArgs := splitPositionalAndFlags(args[1:])
	fs := flag.NewFlagSet("session approve "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)

	var reason, idempotencyKey, validityText, data, term string
	var cols, rows uint
	var force, jsonOutput bool
	fs.StringVar(&reason, "reason", "", "Exact mutation reason (required)")
	fs.StringVar(&idempotencyKey, "idempotency-key", "", "Exact mutation idempotency key (required)")
	fs.StringVar(&validityText, "valid-for", "", "Approval validity and operation deadline window (1s-5m, required)")
	fs.StringVar(&data, "data", "", "Exact session write data")
	fs.StringVar(&term, "term", domain.DefaultTermType, "Terminal emulation type for open")
	fs.UintVar(&cols, "cols", uint(domain.DefaultCols), "Terminal columns for open")
	fs.UintVar(&rows, "rows", uint(domain.DefaultRows), "Terminal rows for open")
	fs.BoolVar(&force, "force", false, "Approve forced close")
	fs.BoolVar(&jsonOutput, "json", false, "Output JSON format")
	if err := fs.Parse(flagArgs); err != nil {
		return ExitUsage
	}

	validFor, err := validateSessionApprovalCLIInputs(reason, idempotencyKey, validityText)
	if err != nil {
		fmt.Fprintf(stderr, "amc session approve: %v\n", err)
		return ExitUsage
	}
	req, targetLabel, err := buildSessionApprovalRequest(action, positionals, data, reason, idempotencyKey, validFor, uint16(cols), uint16(rows), term, force)
	if err != nil {
		fmt.Fprintf(stderr, "amc session approve: %v\n", err)
		return ExitUsage
	}

	prompt := fmt.Sprintf("Issue one-use %s approval for agent:mcp-local on %s?", action, targetLabel)
	if prompter == nil || !prompter.PromptConfirmation(prompt) {
		fmt.Fprintln(stderr, "amc session approve: confirmation declined")
		return ExitDenied
	}
	resp, err := cl.IssueSessionApproval(ctx, req)
	if err != nil {
		return mapClientError(err, stderr, "session approve")
	}
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(resp)
		return ExitSuccess
	}
	fmt.Fprintf(stdout, "Approval ID: %s\nDeadline: %s\nOperation: %s on %s\n", resp.ApprovalID, resp.Deadline, resp.Operation.Kind, resp.Operation.Target)
	return ExitSuccess
}

func validateSessionApprovalCLIInputs(reason, idempotencyKey, validityText string) (time.Duration, error) {
	if err := domain.ValidateReason(reason); err != nil {
		return 0, fmt.Errorf("invalid --reason: %w", err)
	}
	if err := domain.ValidateIdempotencyKey(idempotencyKey); err != nil {
		return 0, fmt.Errorf("invalid --idempotency-key: %w", err)
	}
	validFor, err := time.ParseDuration(validityText)
	if err != nil || validFor < time.Second || validFor > 5*time.Minute || validFor%time.Millisecond != 0 {
		return 0, fmt.Errorf("invalid --valid-for %q; use millisecond precision between 1s and 5m", validityText)
	}
	return validFor, nil
}

func buildSessionApprovalRequest(action string, positionals []string, data, reason, key string, validFor time.Duration, cols, rows uint16, term string, force bool) (daemon.SessionApprovalIssueRequest, string, error) {
	req := daemon.SessionApprovalIssueRequest{
		Kind: "session." + action, Reason: reason, IdempotencyKey: key,
		ValidForMillis: int64(validFor / time.Millisecond),
	}
	switch action {
	case "open":
		if len(positionals) != 1 {
			return req, "", fmt.Errorf("open requires exactly one machine GUID")
		}
		req.Target = positionals[0]
		req.Cols, req.Rows, req.Term = cols, rows, term
		return req, req.Target, nil
	case "write":
		if len(positionals) < 1 {
			return req, "", fmt.Errorf("write requires a session ID and exact data")
		}
		if data == "" && len(positionals) > 1 {
			data = strings.Join(positionals[1:], " ") + "\n"
		}
		if data == "" {
			return req, "", fmt.Errorf("write requires exact data via an argument or --data")
		}
		req.SessionID, req.Data = positionals[0], data
		return req, req.SessionID, nil
	case "control":
		if len(positionals) != 2 {
			return req, "", fmt.Errorf("control requires a session ID and control key")
		}
		req.SessionID, req.Key = positionals[0], positionals[1]
		return req, req.SessionID, nil
	case "close":
		if len(positionals) != 1 {
			return req, "", fmt.Errorf("close requires exactly one session ID")
		}
		req.SessionID = positionals[0]
		req.Force = force
		return req, req.SessionID, nil
	default:
		return req, "", fmt.Errorf("unsupported operation %q; use open, write, control, or close", action)
	}
}

func printSessionApproveUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: amc session approve <open|write|control|close> ... --reason <text> --idempotency-key <key> --valid-for <1s-5m> [--json]")
}
