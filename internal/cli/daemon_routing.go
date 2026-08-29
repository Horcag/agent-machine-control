package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func executeMachineStateDaemonMutation(
	ctx context.Context,
	stateDir string,
	req daemon.CreateOperationRequest,
	common *CommonFlags,
	stdout, stderr io.Writer,
	opVerb string,
	targetID string,
	expectedState domain.MachineLifecycleState,
) int {
	return executeDaemonMutation(
		ctx,
		stateDir,
		req,
		common.Timeout,
		common.Async,
		common.JSON,
		stdout, stderr,
		opVerb,
		func(w io.Writer, _ *daemon.OperationDTO, rcpt *receipt.DTO) {
			fmt.Fprintf(w, "Machine %sed successfully.\n", opVerb)
			if rcpt != nil {
				fmt.Fprintf(w, "Receipt ID:    %s\n", rcpt.ReceiptID)
				fmt.Fprintf(w, "State:         %s\n", expectedState)
				if rcpt.RollbackRef != "" {
					fmt.Fprintf(w, "Rollback Ref:  %s\n", rcpt.RollbackRef)
				}
			}
		},
		func(w io.Writer, _ *daemon.OperationDTO, rcpt *receipt.DTO) error {
			var rcptDTO receipt.DTO
			if rcpt != nil {
				rcptDTO = *rcpt
			}
			obsDTO := MachineOutputDTO{
				ID:              targetID,
				State:           expectedState,
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

func executeDaemonMutation(
	ctx context.Context,
	stateDir string,
	req daemon.CreateOperationRequest,
	timeout time.Duration,
	async bool,
	jsonOutput bool,
	stdout, stderr io.Writer,
	opName string,
	formatSuccessHuman func(w io.Writer, op *daemon.OperationDTO, rcpt *receipt.DTO),
	formatSuccessJSON func(w io.Writer, op *daemon.OperationDTO, rcpt *receipt.DTO) error,
) int {
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		fmt.Fprintf(stderr, "amc %s: daemon is unavailable; run 'amcd run' or use '--direct' for in-process recovery\n", opName)
		return ExitBackendUnavailable
	}

	opDTO, err := cl.CreateOperation(ctx, req)
	if err != nil {
		return mapClientError(err, stderr, opName)
	}

	if async {
		return handleAsyncDaemonOutput(opDTO, jsonOutput, stdout, stderr, opName)
	}

	finalDTO, err := cl.WaitOperation(ctx, opDTO.OperationID, timeout, 0)
	if err != nil {
		return mapClientError(err, stderr, opName)
	}

	return handleFinalDaemonState(ctx, cl, finalDTO, jsonOutput, stdout, stderr, opName, formatSuccessHuman, formatSuccessJSON)
}

func handleAsyncDaemonOutput(opDTO *daemon.OperationDTO, jsonOutput bool, stdout, stderr io.Writer, opName string) int {
	if jsonOutput {
		envelope := OperationOutputEnvelope{
			SchemaVersion: SchemaVersion,
			Operation:     opDTO,
		}
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "amc %s: failed to write JSON output\n", opName)
			return ExitMalformedProvider
		}
		return ExitSuccess
	}
	fmt.Fprintf(stdout, "Operation %s submitted successfully (state: %s).\n", opDTO.OperationID, opDTO.State)
	return ExitSuccess
}

func handleFinalDaemonState(
	ctx context.Context,
	cl *client.Client,
	finalDTO *daemon.OperationDTO,
	jsonOutput bool,
	stdout, stderr io.Writer,
	opName string,
	formatSuccessHuman func(w io.Writer, op *daemon.OperationDTO, rcpt *receipt.DTO),
	formatSuccessJSON func(w io.Writer, op *daemon.OperationDTO, rcpt *receipt.DTO) error,
) int {
	switch finalDTO.State {
	case "completed":
		var rcpt *receipt.DTO
		if finalDTO.ReceiptID != "" {
			if r, err := cl.GetReceipt(ctx, finalDTO.ReceiptID); err == nil {
				rcpt = r
			}
		}
		if jsonOutput {
			if formatSuccessJSON != nil {
				if err := formatSuccessJSON(stdout, finalDTO, rcpt); err != nil {
					fmt.Fprintf(stderr, "amc %s: failed to write JSON output\n", opName)
					return ExitMalformedProvider
				}
			}
			return ExitSuccess
		}
		if formatSuccessHuman != nil {
			formatSuccessHuman(stdout, finalDTO, rcpt)
		}
		return ExitSuccess

	case "cancelled":
		fmt.Fprintf(stderr, "amc %s: operation cancelled: %s\n", opName, finalDTO.ErrorMessage)
		return ExitDenied

	case "failed":
		return mapFailureCategory(finalDTO.ErrorCategory, finalDTO.ErrorMessage, stderr, opName)

	default:
		fmt.Fprintf(stderr, "amc %s: operation ended with unexpected state %s\n", opName, finalDTO.State)
		return ExitBackendUnavailable
	}
}

func mapClientError(err error, stderr io.Writer, opName string) int {
	if errors.Is(err, client.ErrNotFound) {
		fmt.Fprintf(stderr, "amc %s: resource not found\n", opName)
		return ExitNotFound
	}
	if errors.Is(err, client.ErrDenied) {
		fmt.Fprintf(stderr, "amc %s: permission denied or approval required\n", opName)
		return ExitDenied
	}
	if errors.Is(err, client.ErrConflict) {
		fmt.Fprintf(stderr, "amc %s: state or idempotency conflict\n", opName)
		return ExitConflict
	}
	if errors.Is(err, client.ErrTimeout) {
		fmt.Fprintf(stderr, "amc %s: operation timed out\n", opName)
		return ExitTimeout
	}
	if errors.Is(err, client.ErrInvalidArgument) {
		fmt.Fprintf(stderr, "amc %s: invalid argument: %v\n", opName, err)
		return ExitUsage
	}
	if errors.Is(err, client.ErrDaemonUnavailable) {
		fmt.Fprintf(stderr, "amc %s: daemon is unavailable; run 'amcd run' or use '--direct' for in-process recovery\n", opName)
		return ExitBackendUnavailable
	}
	if errors.Is(err, client.ErrMalformedResponse) {
		fmt.Fprintf(stderr, "amc %s: malformed server response\n", opName)
		return ExitMalformedProvider
	}
	fmt.Fprintf(stderr, "amc %s: %v\n", opName, err)
	return ExitBackendUnavailable
}

func mapFailureCategory(category, message string, stderr io.Writer, opName string) int {
	switch category {
	case "policy_denied":
		fmt.Fprintf(stderr, "amc %s: policy denied: %s\n", opName, message)
		return ExitDenied
	case "timeout":
		fmt.Fprintf(stderr, "amc %s: operation timed out\n", opName)
		return ExitTimeout
	case "conflict":
		fmt.Fprintf(stderr, "amc %s: state conflict\n", opName)
		return ExitConflict
	case "daemon_crash_recovered":
		fmt.Fprintf(stderr, "amc %s: operation aborted (daemon crash recovered)\n", opName)
		return ExitBackendUnavailable
	default:
		if message != "" {
			fmt.Fprintf(stderr, "amc %s: operation failed: %s\n", opName, message)
		} else {
			fmt.Fprintf(stderr, "amc %s: operation failed\n", opName)
		}
		return ExitBackendUnavailable
	}
}

// MapClientErrorForTest exports mapClientError for package tests.
func MapClientErrorForTest(err error, stderr io.Writer, opName string) int {
	return mapClientError(err, stderr, opName)
}

// MapFailureCategoryForTest exports mapFailureCategory for package tests.
func MapFailureCategoryForTest(category, message string, stderr io.Writer, opName string) int {
	return mapFailureCategory(category, message, stderr, opName)
}
