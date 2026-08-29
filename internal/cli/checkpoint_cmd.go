package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func runCheckpoint(
	ctx context.Context,
	recoverySvc *app.RecoveryService,
	actor domain.ActorContext,
	prompter Prompter,
	nowFn func() time.Time,
	directMode bool,
	args []string,
	stdout, stderr io.Writer,
) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "amc checkpoint: missing subcommand (expected 'list', 'create', or 'restore')")
		return ExitUsage
	}

	switch args[0] {
	case "list":
		return runCheckpointList(ctx, recoverySvc, args[1:], stdout, stderr)
	case "create":
		if !directMode {
			fmt.Fprintln(stderr, "amc checkpoint create: daemon transport is not yet available; use '--direct' for in-process recovery")
			return ExitBackendUnavailable
		}
		return runCheckpointCreate(ctx, recoverySvc, actor, prompter, nowFn, args[1:], stdout, stderr)
	case "restore":
		if !directMode {
			fmt.Fprintln(stderr, "amc checkpoint restore: daemon transport is not yet available; use '--direct' for in-process recovery")
			return ExitBackendUnavailable
		}
		return runCheckpointRestore(ctx, recoverySvc, actor, prompter, nowFn, args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, "Usage: amc checkpoint <list|create|restore> [flags] [args]")
		return ExitSuccess
	default:
		fmt.Fprintf(stderr, "amc checkpoint: unknown subcommand %q (expected 'list', 'create', or 'restore')\n", args[0])
		return ExitUsage
	}
}

func runCheckpointList(ctx context.Context, recoverySvc *app.RecoveryService, args []string, stdout, stderr io.Writer) int {
	var jsonOutput bool
	var positional []string

	for _, arg := range args {
		switch {
		case arg == "--json" || arg == "-json":
			jsonOutput = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "amc checkpoint list: unknown flag %q\n", arg)
			return ExitUsage
		default:
			positional = append(positional, arg)
		}
	}

	if len(positional) == 0 {
		fmt.Fprintln(stderr, "amc checkpoint list: missing required machine GUID")
		return ExitUsage
	}
	if len(positional) > 1 {
		fmt.Fprintf(stderr, "amc checkpoint list: unexpected argument %q\n", positional[1])
		return ExitUsage
	}

	targetID := positional[0]
	if err := domain.ValidateMachineGUID(targetID); err != nil {
		fmt.Fprintf(stderr, "amc checkpoint list: invalid machine GUID %q\n", targetID)
		return ExitUsage
	}

	checkpoints, err := recoverySvc.ListCheckpoints(ctx, targetID)
	if err != nil {
		return mapCLIError(err, stderr, "checkpoint list")
	}

	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].CreatedAt.Before(checkpoints[j].CreatedAt)
	})

	if jsonOutput {
		dtos := make([]CheckpointOutputDTO, len(checkpoints))
		for i, c := range checkpoints {
			dtos[i] = ConvertToCheckpointDTO(c)
		}
		envelope := CheckpointListOutputEnvelope{
			SchemaVersion:   SchemaVersion,
			ObservationType: domain.ObservationObserved,
			Checkpoints:     dtos,
		}
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "amc checkpoint list: failed to write JSON\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	if len(checkpoints) == 0 {
		fmt.Fprintln(stdout, "No checkpoints found.")
		return ExitSuccess
	}

	w := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tTYPE\tCREATED")
	for _, c := range checkpoints {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			c.ID,
			c.Name,
			c.CheckpointType,
			c.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = w.Flush()

	return ExitSuccess
}

func runCheckpointCreate(
	ctx context.Context,
	recoverySvc *app.RecoveryService,
	actor domain.ActorContext,
	prompter Prompter,
	nowFn func() time.Time,
	args []string,
	stdout, stderr io.Writer,
) int {
	positionals, flagArgs := splitPositionalAndFlags(args)

	flags := flag.NewFlagSet("checkpoint create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "checkpoint name (required)")

	common, err := parseCommonFlags(flags, flagArgs, stderr, "checkpoint create")
	if err != nil {
		return ExitUsage
	}

	if *name == "" {
		fmt.Fprintln(stderr, "amc checkpoint create: missing required flag --name")
		return ExitUsage
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "amc checkpoint create: requires exactly one machine GUID")
		return ExitUsage
	}

	targetID := positionals[0]
	if err := domain.ValidateMachineGUID(targetID); err != nil {
		fmt.Fprintf(stderr, "amc checkpoint create: invalid machine GUID %q\n", targetID)
		return ExitUsage
	}

	appr := common.Approval
	var reqDeadline time.Time
	if appr == nil && prompter != nil {
		promptMsg := fmt.Sprintf("Destructive operation checkpoint.create on %s requires confirmation", targetID)
		params := map[string]any{"name": *name}
		promptedAppr, dl, ok := promptForApproval(prompter, nowFn, actor, targetID, "checkpoint.create", domain.CapabilityCheckpointCreate, domain.ClassDestructivePrivileged, common.Reason, common.IdempotencyKey, common.Timeout, params, promptMsg)
		if !ok {
			fmt.Fprintln(stderr, "amc checkpoint create: operation aborted by operator")
			return ExitDenied
		}
		appr = promptedAppr
		reqDeadline = dl
	}

	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         common.Reason,
		IdempotencyKey: common.IdempotencyKey,
		Timeout:        common.Timeout,
		Deadline:       reqDeadline,
		Approval:       appr,
	}

	rcpt, snap, err := recoverySvc.CreateCheckpoint(ctx, req, *name)
	if err != nil {
		return mapMutationError(err, stderr, "checkpoint create")
	}

	if common.JSON {
		snapDTO := ConvertToCheckpointDTO(snap)
		envelope := CheckpointMutationOutputEnvelope{
			SchemaVersion: SchemaVersion,
			Receipt:       receipt.ConvertToDTO(rcpt),
			Checkpoint:    &snapDTO,
		}
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "amc checkpoint create: failed to write JSON\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	fmt.Fprintf(stdout, "Checkpoint created successfully.\n")
	fmt.Fprintf(stdout, "Receipt ID:       %s\n", rcpt.ReceiptID)
	fmt.Fprintf(stdout, "Checkpoint ID:    %s\n", snap.ID)
	fmt.Fprintf(stdout, "Checkpoint Name:  %s\n", snap.Name)
	return ExitSuccess
}
