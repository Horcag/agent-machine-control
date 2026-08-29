package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func runAudit(ctx context.Context, stateDir string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "amc audit: missing subcommand (expected 'tail' or 'show')")
		return ExitUsage
	}

	switch args[0] {
	case "tail":
		return runAuditTail(ctx, stateDir, args[1:], stdout, stderr)
	case "show":
		return runAuditShow(ctx, stateDir, args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, "Usage: amc audit <tail|show> [flags] [args]")
		return ExitSuccess
	default:
		fmt.Fprintf(stderr, "amc audit: unknown subcommand %q (expected 'tail' or 'show')\n", args[0])
		return ExitUsage
	}
}

func runAuditTail(ctx context.Context, stateDir string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit tail", flag.ContinueOnError)
	fs.SetOutput(stderr)

	count := fs.Int("n", 50, "number of events to show")
	follow := fs.Bool("follow", false, "stream events live")
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON output")
	_ = fs.String("state-dir", "", "state directory path")

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		fmt.Fprintf(stderr, "amc audit tail: daemon is unavailable; run 'amcd run'\n")
		return ExitBackendUnavailable
	}

	if *follow {
		return streamAuditFollow(ctx, cl, *count, *jsonOutput, stdout, stderr)
	}

	eventsList, err := cl.GetAudit(ctx, *count)
	if err != nil {
		return mapClientError(err, stderr, "audit tail")
	}

	return renderAuditSnapshot(stdout, stderr, eventsList, *jsonOutput)
}

func streamAuditFollow(ctx context.Context, cl *client.Client, count int, isJSON bool, stdout, stderr io.Writer) int {
	// 1. Subscribe to global events FIRST to ensure no race gap
	eventsCh, errCh, unsub, err := cl.WatchGlobalEvents(ctx)
	if err != nil {
		return mapClientError(err, stderr, "audit tail")
	}
	defer unsub()

	// 2. Fetch bounded historical snapshot
	eventsList, err := cl.GetAudit(ctx, count)
	if err != nil {
		return mapClientError(err, stderr, "audit tail")
	}

	// 3. Render historical events and track seen receipts for deduplication
	seenReceipts := renderHistoricalAudit(stdout, eventsList, isJSON)

	// 4. Stream live events, deduplicating any terminal receipt already shown in snapshot
	return consumeLiveAuditEvents(ctx, eventsCh, errCh, seenReceipts, isJSON, stdout, stderr)
}

func renderHistoricalAudit(stdout io.Writer, eventsList []audit.Event, isJSON bool) map[string]struct{} {
	seenReceipts := make(map[string]struct{})
	for _, ev := range eventsList {
		if ev.ReceiptID != "" {
			seenReceipts[ev.ReceiptID] = struct{}{}
		}
		printFollowHistoricalEvent(stdout, ev, isJSON)
	}
	return seenReceipts
}

func consumeLiveAuditEvents(
	ctx context.Context,
	eventsCh <-chan domain.Event,
	errCh <-chan error,
	seenReceipts map[string]struct{},
	isJSON bool,
	stdout, stderr io.Writer,
) int {
	for {
		select {
		case <-ctx.Done():
			return ExitSuccess
		case err, ok := <-errCh:
			if ok && err != nil {
				fmt.Fprintf(stderr, "amc audit tail: stream error: %v\n", err)
				return ExitBackendUnavailable
			}
			return ExitSuccess
		case ev, ok := <-eventsCh:
			if !ok {
				return ExitSuccess
			}
			if ev.ReceiptID != "" {
				if _, seen := seenReceipts[string(ev.ReceiptID)]; seen {
					continue
				}
				seenReceipts[string(ev.ReceiptID)] = struct{}{}
			}
			printFollowLiveEvent(stdout, ev, isJSON)
		}
	}
}

func printFollowHistoricalEvent(stdout io.Writer, ev audit.Event, isJSON bool) {
	if isJSON {
		_ = writeJSON(stdout, ev)
		return
	}
	status := string(ev.OutcomeStatus)
	if status == "" {
		status = "-"
	}
	fmt.Fprintf(stdout, "%s  %-20s  %-15s  %-15s  %-20s  %s\n",
		ev.Timestamp.UTC().Format(time.RFC3339),
		ev.EventType,
		ev.Actor,
		ev.Target,
		ev.OperationKind,
		status,
	)
}

func printFollowLiveEvent(stdout io.Writer, ev domain.Event, isJSON bool) {
	if isJSON {
		_ = writeJSON(stdout, ev)
		return
	}
	fmt.Fprintf(stdout, "%s  %-20s  %-15s  %-15s  %-20s  %s\n",
		ev.Timestamp.UTC().Format(time.RFC3339),
		ev.EventType,
		ev.State,
		ev.Target,
		ev.OperationID,
		ev.Message,
	)
}

func renderAuditSnapshot(stdout, stderr io.Writer, eventsList []audit.Event, isJSON bool) int {
	if isJSON {
		if err := writeJSON(stdout, AuditTailOutputEnvelope{
			SchemaVersion: SchemaVersion,
			Events:        eventsList,
		}); err != nil {
			fmt.Fprintf(stderr, "amc audit tail: failed to write JSON output\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	if len(eventsList) == 0 {
		fmt.Fprintln(stdout, "No audit events found.")
		return ExitSuccess
	}

	w := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "TIMESTAMP\tEVENT_TYPE\tACTOR\tTARGET\tOPERATION\tSTATUS")
	for _, ev := range eventsList {
		status := string(ev.OutcomeStatus)
		if status == "" {
			status = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			ev.Timestamp.UTC().Format(time.RFC3339),
			ev.EventType,
			ev.Actor,
			ev.Target,
			ev.OperationKind,
			status,
		)
	}
	_ = w.Flush()
	return ExitSuccess
}

func runAuditShow(ctx context.Context, stateDir string, args []string, stdout, stderr io.Writer) int {
	positionals, flagArgs := splitPositionalAndFlags(args)

	fs := flag.NewFlagSet("audit show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON output")
	_ = fs.String("state-dir", "", "state directory path")

	if err := fs.Parse(flagArgs); err != nil {
		return ExitUsage
	}

	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "amc audit show: requires exactly one receipt ID")
		return ExitUsage
	}

	receiptID := positionals[0]
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		fmt.Fprintf(stderr, "amc audit show: daemon is unavailable; run 'amcd run'\n")
		return ExitBackendUnavailable
	}

	rcpt, err := cl.GetReceipt(ctx, receiptID)
	if err != nil {
		return mapClientError(err, stderr, "audit show")
	}

	if *jsonOutput {
		if err := writeJSON(stdout, ReceiptOutputEnvelope{
			SchemaVersion: SchemaVersion,
			Receipt:       *rcpt,
		}); err != nil {
			fmt.Fprintf(stderr, "amc audit show: failed to write JSON output\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	printHumanReceipt(stdout, rcpt)
	return ExitSuccess
}

func printHumanReceipt(w io.Writer, r *receipt.DTO) {
	fmt.Fprintf(w, "Receipt ID:        %s\n", r.ReceiptID)
	fmt.Fprintf(w, "Operation Kind:    %s\n", r.OperationKind)
	fmt.Fprintf(w, "Target:            %s\n", r.Target)
	fmt.Fprintf(w, "Actor:             %s\n", r.Actor)
	fmt.Fprintf(w, "Outcome Status:    %s (exit code %d)\n", r.Outcome.Status, r.Outcome.ExitCode)
	if r.RollbackRef != "" {
		fmt.Fprintf(w, "Rollback Ref:      %s\n", r.RollbackRef)
	}
	fmt.Fprintf(w, "Started At:        %s\n", r.StartedAt)
	fmt.Fprintf(w, "Completed At:      %s\n", r.CompletedAt)
}
