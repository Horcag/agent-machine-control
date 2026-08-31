package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func runMachineStart(
	ctx context.Context,
	recoverySvc *app.RecoveryService,
	actor domain.ActorContext,
	prompter Prompter,
	nowFn func() time.Time,
	directMode bool,
	stateDir string,
	args []string,
	stdout, stderr io.Writer,
) int {
	positionals, flagArgs := splitPositionalAndFlags(args)

	flags := flag.NewFlagSet("machine start", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common, err := parseCommonFlags(flags, flagArgs, stderr, "machine start")
	if err != nil {
		return ExitUsage
	}

	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "amc machine start: requires exactly one machine reference")
		return ExitUsage
	}

	targetID := positionals[0]

	if !directMode {
		dReq := daemon.CreateOperationRequest{
			Kind:           "machine.start",
			Target:         targetID,
			Reason:         common.Reason,
			IdempotencyKey: common.IdempotencyKey,
			TimeoutSeconds: int(common.Timeout.Seconds()),
		}
		return executeMachineStateDaemonMutation(
			ctx,
			stateDir,
			dReq,
			common,
			stdout, stderr,
			"start",
			targetID,
			domain.MachineStateRunning,
		)
	}
	canonicalTarget, err := recoverySvc.ResolveTargetReference(ctx, targetID)
	if err != nil {
		return mapMutationError(err, stderr, "machine start")
	}

	req := app.MutationRequest{
		TargetID:       string(canonicalTarget),
		Actor:          actor,
		Reason:         common.Reason,
		IdempotencyKey: common.IdempotencyKey,
		Timeout:        common.Timeout,
		Approval:       common.Approval,
	}

	rcpt, obs, err := recoverySvc.StartMachine(ctx, req)
	var deniedErr *app.PolicyDeniedError
	if errors.As(err, &deniedErr) && deniedErr.Reason == policy.DenialApprovalRequired && common.Approval == nil && prompter != nil {
		promptMsg := fmt.Sprintf("Destructive operation machine.start on %s requires confirmation (no rollback checkpoint found)", canonicalTarget)
		newIdempotencyKey := domain.DeriveApprovalIdempotencyKey(common.IdempotencyKey)
		if promptedAppr, dl, ok := promptForApproval(prompter, nowFn, actor, string(canonicalTarget), "machine.start", domain.CapabilityMachineStart, domain.ClassReversibleMutation, common.Reason, newIdempotencyKey, common.Timeout, nil, promptMsg); ok {
			if issueErr := recoverySvc.IssueApproval(ctx, *promptedAppr); issueErr != nil {
				return mapMutationError(issueErr, stderr, "machine start")
			}
			req.Approval = promptedAppr
			req.Deadline = dl
			req.IdempotencyKey = newIdempotencyKey
			rcpt, obs, err = recoverySvc.StartMachine(ctx, req)
		}
	}

	if err != nil {
		return mapMutationError(err, stderr, "machine start")
	}

	if common.JSON {
		obsDTO := ConvertToMachineDTO(obs)
		envelope := MachineMutationOutputEnvelope{
			SchemaVersion: SchemaVersion,
			Receipt:       receipt.ConvertToDTO(rcpt),
			Machine:       &obsDTO,
		}
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "amc machine start: failed to write JSON\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	fmt.Fprintf(stdout, "Machine started successfully.\n")
	fmt.Fprintf(stdout, "Receipt ID:    %s\n", rcpt.ReceiptID)
	fmt.Fprintf(stdout, "State:         %s\n", obs.State)
	if rcpt.RollbackRef != "" {
		fmt.Fprintf(stdout, "Rollback Ref:  %s\n", rcpt.RollbackRef)
	}
	return ExitSuccess
}
