package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

func runGuest(ctx context.Context, prompter Prompter, direct bool, state string, args []string, stdout, stderr io.Writer) int {
	if direct {
		fmt.Fprintln(stderr, "amc: guest commands require amcd; direct recovery remains independent")
		return ExitBackendUnavailable
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: amc guest [approve] <prepare|put|get|exec|remove-owned> --target <reference> --reason <reason> --idempotency-key <key> --deadline <RFC3339Nano> [--source <local file>] [--destination <new local file>] [--artifact-ids <opaque IDs>] [--max-output <bytes>]")
		return ExitUsage
	}
	approve := args[0] == "approve"
	if approve {
		args = args[1:]
		if len(args) == 0 {
			return ExitUsage
		}
	}
	operation := args[0]
	switch operation {
	case "prepare", "put", "get", "exec", "remove-owned":
	default:
		return ExitUsage
	}
	flags, err := parseGuestCLI(operation, args[1:], approve, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitUsage
	}
	p := flags.params
	sourcePath, destination, validity := flags.source, flags.destination, flags.validity
	cl, err := client.Discover(state, client.TokenTypeOperator)
	if err != nil {
		return mapClientError(err, stderr, "guest")
	}
	var input stream.ContextReader
	source, err := client.OpenGuestPayload(ctx, operation, sourcePath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitUsage
	}
	if source != nil {
		defer source.Close()
		p.Size, p.SHA256 = source.Size, source.SHA256
		input = source.Input()
	}
	if approve {
		return approveGuestCLI(ctx, cl, prompter, p, validity, flags.beneficiary, stdout, stderr)
	}
	result, err := executeGuestCLI(ctx, cl, p, input, destination)
	if source != nil {
		err = errors.Join(err, source.Verify())
	}
	if err != nil {
		return mapClientError(err, stderr, "guest")
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return ExitBackendUnavailable
	}
	return ExitSuccess
}

type guestCLIFlags struct {
	params                        app.GuestOperationParams
	source, destination, validity string
	beneficiary                   string
}

func parseGuestCLI(operation string, args []string, approve bool, stderr io.Writer) (guestCLIFlags, error) {
	fs := flag.NewFlagSet("guest "+operation, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var p app.GuestOperationParams
	var sourcePath, destination, ids, deadline, validity, beneficiary string
	p.Operation = operation
	fs.StringVar(&p.Target, "target", "", "Enrolled target reference")
	fs.StringVar(&p.Reason, "reason", "", "Exact reason")
	fs.StringVar(&p.IdempotencyKey, "idempotency-key", "", "Exact operation key")
	fs.StringVar(&p.ApprovalID, "approval-id", "", "One-use server approval")
	fs.StringVar(&deadline, "deadline", "", "Exact RFC3339Nano deadline")
	fs.StringVar(&sourcePath, "source", "", "Local payload file")
	fs.StringVar(&destination, "destination", "", "New local output file")
	fs.StringVar(&ids, "artifact-ids", "", "Comma-separated opaque ownership references")
	fs.StringVar(&beneficiary, "beneficiary", "agent:mcp-local", "Approval beneficiary: self or agent:mcp-local")
	fs.StringVar(&validity, "valid-for", "", "Approval window (1s-5m)")
	fs.Int64Var(&p.MaxOutput, "max-output", 0, "Bounded output bytes")
	jsonOutput := fs.Bool("json", false, "JSON result")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return guestCLIFlags{}, fmt.Errorf("invalid guest arguments")
	}
	_ = jsonOutput
	if ids != "" {
		p.ArtifactIDs = strings.Split(ids, ",")
	}
	if !approve {
		var err error
		p.Deadline, err = time.Parse(time.RFC3339Nano, deadline)
		if err != nil {
			return guestCLIFlags{}, fmt.Errorf("invalid guest arguments")
		}
	}
	return guestCLIFlags{p, sourcePath, destination, validity, beneficiary}, nil
}

func executeGuestCLI(ctx context.Context, cl *client.Client, p app.GuestOperationParams, input stream.ContextReader, destination string) (app.GuestOperationResult, error) {
	var err error
	var result app.GuestOperationResult
	if p.Operation == "get" || p.Operation == "exec" {
		if destination == "" {

			return result, fmt.Errorf("invalid guest destination")
		}
		result, err = cl.GuestOperationToFile(ctx, p, input, destination)
	} else {
		if destination != "" {
			return result, fmt.Errorf("invalid guest destination")
		}
		result, err = cl.GuestOperation(ctx, p, input, nil)
	}
	return result, err
}

func approveGuestCLI(ctx context.Context, cl *client.Client, prompter Prompter, p app.GuestOperationParams, validity, beneficiary string, stdout, stderr io.Writer) int {
	validFor, err := validateSessionApprovalCLIInputs(p.Reason, p.IdempotencyKey, validity)
	if err != nil {
		return ExitUsage
	}
	if prompter == nil || !prompter.PromptConfirmation(fmt.Sprintf("Issue one-use guest.%s approval for %s on %s?", p.Operation, beneficiary, p.Target)) {
		return ExitDenied
	}
	grant, err := cl.IssueGuestApproval(ctx, daemon.GuestApprovalRequest{Params: p, ValidForMillis: validFor.Milliseconds(), Beneficiary: beneficiary})
	if err != nil {
		return mapClientError(err, stderr, "guest approve")
	}
	if json.NewEncoder(stdout).Encode(grant) != nil {
		return ExitBackendUnavailable
	}
	return ExitSuccess

}
