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
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func runCheckpointRestore(
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

	flags := flag.NewFlagSet("checkpoint restore", flag.ContinueOnError)
	flags.SetOutput(stderr)

	common, err := parseCommonFlags(flags, flagArgs, stderr, "checkpoint restore")
	if err != nil {
		return ExitUsage
	}

	if len(positionals) != 2 {
		fmt.Fprintln(stderr, "amc checkpoint restore: requires exactly <machine-reference> and <checkpoint-guid>")
		return ExitUsage
	}

	targetID := positionals[0]
	checkpointID := positionals[1]

	if err := domain.ValidateMachineGUID(checkpointID); err != nil {
		fmt.Fprintf(stderr, "amc checkpoint restore: invalid checkpoint GUID %q\n", checkpointID)
		return ExitUsage
	}

	if !directMode {
		dReq := daemon.CreateOperationRequest{
			Kind:           "checkpoint.restore",
			Target:         targetID,
			Reason:         common.Reason,
			IdempotencyKey: common.IdempotencyKey,
			TimeoutSeconds: int(common.Timeout.Seconds()),
			Parameters:     map[string]any{"checkpoint_id": checkpointID},
		}
		applyDaemonApprovalReference(&dReq, common)
		return executeDaemonMutation(
			ctx,
			stateDir,
			dReq,
			common.Timeout,
			common.Async,
			common.JSON,
			stdout, stderr,
			"checkpoint restore",
			func(w io.Writer, _ *daemon.OperationDTO, rcpt *receipt.DTO) {
				fmt.Fprintf(w, "Checkpoint restored successfully.\n")
				if rcpt != nil {
					fmt.Fprintf(w, "Receipt ID:    %s\n", rcpt.ReceiptID)
					fmt.Fprintf(w, "State:         Off\n")
				}
			},
			func(w io.Writer, _ *daemon.OperationDTO, rcpt *receipt.DTO) error {
				var rcptDTO receipt.DTO
				if rcpt != nil {
					rcptDTO = *rcpt
				}
				obsDTO := MachineOutputDTO{
					ID:              targetID,
					State:           domain.MachineStateOff,
					ObservationType: domain.ObservationInferred,
				}
				envelope := MachineMutationOutputEnvelope{
					SchemaVersion: SchemaVersion,
					Receipt:       rcptDTO,
					Machine:       &obsDTO,
				}
				return writeJSON(w, envelope)
			},
		)
	}
	if rejectDirectApprovalReference(common, stderr, "checkpoint restore") {
		return ExitUsage
	}
	canonicalTarget, err := recoverySvc.ResolveTargetReference(ctx, targetID)
	if err != nil {
		return mapMutationError(err, stderr, "checkpoint restore")
	}

	appr, reqDeadline, approvalExit := prepareCheckpointRestoreApproval(ctx, recoverySvc, actor, prompter, nowFn, string(canonicalTarget), checkpointID, common, stderr)
	if approvalExit != ExitSuccess {
		return approvalExit
	}

	req := app.MutationRequest{
		TargetID:       string(canonicalTarget),
		Actor:          actor,
		Reason:         common.Reason,
		IdempotencyKey: common.IdempotencyKey,
		Timeout:        common.Timeout,
		Deadline:       reqDeadline,
		Approval:       appr,
	}

	rcpt, obs, err := recoverySvc.RestoreCheckpoint(ctx, req, checkpointID)
	if err != nil {
		return mapMutationError(err, stderr, "checkpoint restore")
	}

	if common.JSON {
		obsDTO := ConvertToMachineDTO(obs)
		envelope := MachineMutationOutputEnvelope{
			SchemaVersion: SchemaVersion,
			Receipt:       receipt.ConvertToDTO(rcpt),
			Machine:       &obsDTO,
		}
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "amc checkpoint restore: failed to write JSON\n")
			return ExitMalformedProvider
		}
		return ExitSuccess
	}

	fmt.Fprintf(stdout, "Checkpoint restored successfully.\n")
	fmt.Fprintf(stdout, "Receipt ID:    %s\n", rcpt.ReceiptID)
	fmt.Fprintf(stdout, "State:         %s\n", obs.State)
	return ExitSuccess
}

func prepareCheckpointRestoreApproval(
	ctx context.Context,
	recoverySvc *app.RecoveryService,
	actor domain.ActorContext,
	prompter Prompter,
	nowFn func() time.Time,
	targetID, checkpointID string,
	common *CommonFlags,
	stderr io.Writer,
) (*domain.Approval, time.Time, int) {
	if common.Approval != nil || prompter == nil {
		return common.Approval, time.Time{}, ExitSuccess
	}
	promptMsg := fmt.Sprintf("Destructive operation checkpoint.restore on %s requires confirmation", targetID)
	params := map[string]any{"checkpoint_id": checkpointID}
	appr, deadline, ok := promptForApproval(prompter, nowFn, actor, targetID, "checkpoint.restore", domain.CapabilityCheckpointRestore, domain.ClassDestructivePrivileged, common.Reason, common.IdempotencyKey, common.Timeout, params, promptMsg)
	if !ok {
		fmt.Fprintln(stderr, "amc checkpoint restore: operation aborted by operator")
		return nil, time.Time{}, ExitDenied
	}
	if err := recoverySvc.IssueApproval(ctx, *appr); err != nil {
		fmt.Fprintln(stderr, "amc checkpoint restore: failed to issue server approval")
		return nil, time.Time{}, ExitBackendUnavailable
	}
	return appr, deadline, ExitSuccess
}

func mapMutationError(err error, stderr io.Writer, opName string) int {
	var deniedErr *app.PolicyDeniedError
	if errors.As(err, &deniedErr) {
		fmt.Fprintf(stderr, "amc %s: %s\n", opName, deniedErr.Error())
		return ExitDenied
	}
	if errors.Is(err, domain.ErrApprovalConsumed) {
		fmt.Fprintf(stderr, "amc %s: approval has already been consumed\n", opName)
		return ExitDenied
	}
	if errors.Is(err, domain.ErrApprovalExpired) {
		fmt.Fprintf(stderr, "amc %s: approval has expired\n", opName)
		return ExitDenied
	}
	if errors.Is(err, domain.ErrApprovalActorMismatch) ||
		errors.Is(err, domain.ErrApprovalTargetMismatch) ||
		errors.Is(err, domain.ErrApprovalFingerprintMismatch) ||
		errors.Is(err, domain.ErrApprovalKeyMismatch) ||
		errors.Is(err, domain.ErrApprovalClassMismatch) {
		fmt.Fprintf(stderr, "amc %s: approval record does not match operation\n", opName)
		return ExitDenied
	}
	if errors.Is(err, receipt.ErrIdempotencyCollision) {
		fmt.Fprintf(stderr, "amc %s: idempotency key collision\n", opName)
		return ExitConflict
	}
	if errors.Is(err, lease.ErrLeaseConflict) || errors.Is(err, lease.ErrLeaseUnverifiableOwner) || errors.Is(err, lease.ErrLeaseFencingViolation) {
		fmt.Fprintf(stderr, "amc %s: lease conflict: %v\n", opName, err)
		return ExitConflict
	}
	if errors.Is(err, app.ErrAuditUnavailable) {
		fmt.Fprintf(stderr, "amc %s: audit storage is unavailable\n", opName)
		return ExitBackendUnavailable
	}
	return mapCLIError(err, stderr, opName)
}
