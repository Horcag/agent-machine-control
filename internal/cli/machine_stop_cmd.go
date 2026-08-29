package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func runMachineStop(
	ctx context.Context,
	recoverySvc *app.RecoveryService,
	actor domain.ActorContext,
	prompter Prompter,
	nowFn func() time.Time,
	directMode bool,
	args []string,
	stdout, stderr io.Writer,
) int {
	if !directMode {
		fmt.Fprintln(stderr, "amc machine stop: daemon transport is not yet available; use '--direct' for in-process recovery")
		return ExitBackendUnavailable
	}

	positionals, flagArgs := splitPositionalAndFlags(args)

	flags := flag.NewFlagSet("machine stop", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "shutdown", "stop mode: shutdown, save, or turn-off")

	common, err := parseCommonFlags(flags, flagArgs, stderr, "machine stop")
	if err != nil {
		return ExitUsage
	}

	if *mode != "shutdown" && *mode != "save" && *mode != "turn-off" {
		fmt.Fprintf(stderr, "amc machine stop: invalid --mode %q (expected shutdown, save, or turn-off)\n", *mode)
		return ExitUsage
	}

	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "amc machine stop: requires exactly one machine GUID")
		return ExitUsage
	}

	targetID := positionals[0]
	if err := domain.ValidateMachineGUID(targetID); err != nil {
		fmt.Fprintf(stderr, "amc machine stop: invalid machine GUID %q\n", targetID)
		return ExitUsage
	}

	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         common.Reason,
		IdempotencyKey: common.IdempotencyKey,
		Timeout:        common.Timeout,
		Approval:       common.Approval,
	}

	rcpt, obs, err := executeStopWithApproval(ctx, recoverySvc, req, *mode, common, prompter, nowFn)
	if err != nil {
		return mapMutationError(err, stderr, "machine stop")
	}

	return emitStopResult(stdout, stderr, rcpt, obs, common.JSON)
}

func executeStopWithApproval(
	ctx context.Context,
	recoverySvc *app.RecoveryService,
	req app.MutationRequest,
	mode string,
	common *CommonFlags,
	prompter Prompter,
	nowFn func() time.Time,
) (domain.Receipt, domain.MachineObservation, error) {
	rcpt, obs, err := recoverySvc.StopMachine(ctx, req, mode)
	var deniedErr *app.PolicyDeniedError
	if errors.As(err, &deniedErr) && deniedErr.Reason == policy.DenialApprovalRequired && common.Approval == nil && prompter != nil {
		promptMsg := fmt.Sprintf("Destructive operation machine.stop (--mode %s) on %s requires confirmation", mode, req.TargetID)
		params := map[string]any{"mode": mode}
		initialClass := domain.ClassReversibleMutation
		if mode == "turn-off" {
			initialClass = domain.ClassDestructivePrivileged
		}
		if promptedAppr, dl, ok := promptForApproval(prompter, nowFn, req.Actor, req.TargetID, "machine.stop", domain.CapabilityMachineStop, initialClass, common.Reason, common.IdempotencyKey, common.Timeout, params, promptMsg); ok {
			req.Approval = promptedAppr
			req.Deadline = dl
			rcpt, obs, err = recoverySvc.StopMachine(ctx, req, mode)
		}
	}
	return rcpt, obs, err
}

func emitStopResult(stdout, stderr io.Writer, rcpt domain.Receipt, obs domain.MachineObservation, jsonOutput bool) int {
	if jsonOutput {
		obsDTO := ConvertToMachineDTO(obs)
		envelope := MachineMutationOutputEnvelope{
			SchemaVersion: SchemaVersion,
			Receipt:       receipt.ConvertToDTO(rcpt),
			Machine:       &obsDTO,
		}
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "amc machine stop: failed to write JSON\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	fmt.Fprintf(stdout, "Machine stopped successfully.\n")
	fmt.Fprintf(stdout, "Receipt ID:    %s\n", rcpt.ReceiptID)
	fmt.Fprintf(stdout, "State:         %s\n", obs.State)
	if rcpt.RollbackRef != "" {
		fmt.Fprintf(stdout, "Rollback Ref:  %s\n", rcpt.RollbackRef)
	}
	return ExitSuccess
}
