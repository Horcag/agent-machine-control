package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/operations"
)

func runOperation(ctx context.Context, stateDir string, prompter Prompter, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "amc operation: missing subcommand (expected 'approve', 'list', 'show', 'wait', or 'cancel')")
		return ExitUsage
	}

	switch args[0] {
	case "approve":
		return runOperationApprove(ctx, stateDir, prompter, args[1:], stdout, stderr)
	case "list":
		return runOperationList(ctx, stateDir, args[1:], stdout, stderr)
	case "show":
		return runOperationShow(ctx, stateDir, args[1:], stdout, stderr)
	case "wait":
		return runOperationWait(ctx, stateDir, args[1:], stdout, stderr)
	case "cancel":
		return runOperationCancel(ctx, stateDir, args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, "Usage: amc operation <approve|list|show|wait|cancel> [flags] [args]")
		return ExitSuccess
	default:
		fmt.Fprintf(stderr, "amc operation: unknown subcommand %q (expected 'approve', 'list', 'show', 'wait', or 'cancel')\n", args[0])
		return ExitUsage
	}
}

func runOperationList(ctx context.Context, stateDir string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("operation list", flag.ContinueOnError)
	fs.SetOutput(stderr)

	stateFilter := fs.String("state", "", "filter by operation state")
	machineFilter := fs.String("machine", "", "filter by machine GUID")
	limit := fs.Int("limit", 50, "maximum operations to return")
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON output")
	_ = fs.String("state-dir", "", "state directory path")

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		fmt.Fprintf(stderr, "amc operation list: daemon is unavailable; run 'amcd run'\n")
		return ExitBackendUnavailable
	}

	opts := operations.ListOptions{
		State:   domain.OperationState(*stateFilter),
		Machine: domain.MachineRef(*machineFilter),
		Limit:   *limit,
	}

	list, err := cl.ListOperations(ctx, opts)
	if err != nil {
		return mapClientError(err, stderr, "operation list")
	}

	if *jsonOutput {
		if err := writeJSON(stdout, OperationListOutputEnvelope{
			SchemaVersion: SchemaVersion,
			Operations:    list,
		}); err != nil {
			fmt.Fprintf(stderr, "amc operation list: failed to write JSON output\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	if len(list) == 0 {
		fmt.Fprintln(stdout, "No operations found.")
		return ExitSuccess
	}

	w := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tKIND\tTARGET\tACTOR\tSTATE\tCREATED")
	for _, op := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			op.OperationID,
			op.Kind,
			op.Target,
			op.Actor,
			op.State,
			op.CreatedAt,
		)
	}
	_ = w.Flush()

	return ExitSuccess
}

func runOperationShow(ctx context.Context, stateDir string, args []string, stdout, stderr io.Writer) int {
	positionals, flagArgs := splitPositionalAndFlags(args)

	fs := flag.NewFlagSet("operation show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON output")
	_ = fs.String("state-dir", "", "state directory path")

	if err := fs.Parse(flagArgs); err != nil {
		return ExitUsage
	}

	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "amc operation show: requires exactly one operation ID")
		return ExitUsage
	}

	opID := positionals[0]
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		fmt.Fprintf(stderr, "amc operation show: daemon is unavailable; run 'amcd run'\n")
		return ExitBackendUnavailable
	}

	opDTO, err := cl.GetOperation(ctx, opID)
	if err != nil {
		return mapClientError(err, stderr, "operation show")
	}

	if *jsonOutput {
		if err := writeJSON(stdout, OperationOutputEnvelope{
			SchemaVersion: SchemaVersion,
			Operation:     opDTO,
		}); err != nil {
			fmt.Fprintf(stderr, "amc operation show: failed to write JSON output\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	printHumanOperation(stdout, opDTO)
	return ExitSuccess
}

func runOperationWait(ctx context.Context, stateDir string, args []string, stdout, stderr io.Writer) int {
	positionals, flagArgs := splitPositionalAndFlags(args)

	fs := flag.NewFlagSet("operation wait", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeoutStr := fs.String("timeout", "30s", "maximum wait duration")
	afterSeqStr := fs.String("after-seq", "0", "resume after event sequence number")
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON output")
	_ = fs.String("state-dir", "", "state directory path")

	if err := fs.Parse(flagArgs); err != nil {
		return ExitUsage
	}

	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "amc operation wait: requires exactly one operation ID")
		return ExitUsage
	}

	opID := positionals[0]
	timeout, err := time.ParseDuration(*timeoutStr)
	if err != nil || timeout <= 0 {
		fmt.Fprintf(stderr, "amc operation wait: invalid --timeout %q\n", *timeoutStr)
		return ExitUsage
	}

	afterSeq, _ := strconv.ParseUint(*afterSeqStr, 10, 64)

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		fmt.Fprintf(stderr, "amc operation wait: daemon is unavailable; run 'amcd run'\n")
		return ExitBackendUnavailable
	}

	finalDTO, err := cl.WaitOperation(ctx, opID, timeout, afterSeq)
	if err != nil {
		return mapClientError(err, stderr, "operation wait")
	}

	if *jsonOutput {
		if err := writeJSON(stdout, OperationOutputEnvelope{
			SchemaVersion: SchemaVersion,
			Operation:     finalDTO,
		}); err != nil {
			fmt.Fprintf(stderr, "amc operation wait: failed to write JSON output\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	printHumanOperation(stdout, finalDTO)
	return ExitSuccess
}

func runOperationCancel(ctx context.Context, stateDir string, args []string, stdout, stderr io.Writer) int {
	positionals, flagArgs := splitPositionalAndFlags(args)

	fs := flag.NewFlagSet("operation cancel", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reason := fs.String("reason", "", "cancellation reason (required)")
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON output")
	_ = fs.String("state-dir", "", "state directory path")

	if err := fs.Parse(flagArgs); err != nil {
		return ExitUsage
	}

	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "amc operation cancel: requires exactly one operation ID")
		return ExitUsage
	}

	if *reason == "" {
		fmt.Fprintln(stderr, "amc operation cancel: missing required flag --reason")
		return ExitUsage
	}

	opID := positionals[0]
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		fmt.Fprintf(stderr, "amc operation cancel: daemon is unavailable; run 'amcd run'\n")
		return ExitBackendUnavailable
	}

	resp, err := cl.CancelOperation(ctx, opID, *reason)
	if err != nil {
		return mapClientError(err, stderr, "operation cancel")
	}

	if *jsonOutput {
		if err := writeJSON(stdout, resp); err != nil {
			fmt.Fprintf(stderr, "amc operation cancel: failed to write JSON output\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	fmt.Fprintf(stdout, "Operation %s cancellation requested.\n", opID)
	return ExitSuccess
}

func printHumanOperation(w io.Writer, op *daemon.OperationDTO) {
	fmt.Fprintf(w, "Operation ID:   %s\n", op.OperationID)
	fmt.Fprintf(w, "Kind:           %s\n", op.Kind)
	fmt.Fprintf(w, "Target:         %s\n", op.Target)
	fmt.Fprintf(w, "Actor:          %s\n", op.Actor)
	fmt.Fprintf(w, "State:          %s\n", op.State)
	if op.ReceiptID != "" {
		fmt.Fprintf(w, "Receipt ID:     %s\n", op.ReceiptID)
	}
	if op.ErrorCategory != "" {
		fmt.Fprintf(w, "Error Category: %s\n", op.ErrorCategory)
	}
	if op.ErrorMessage != "" {
		fmt.Fprintf(w, "Error Message:  %s\n", op.ErrorMessage)
	}
	if op.CreatedAt != "" {
		fmt.Fprintf(w, "Created At:     %s\n", op.CreatedAt)
	}
	if op.CompletedAt != "" {
		fmt.Fprintf(w, "Completed At:   %s\n", op.CompletedAt)
	}
}
