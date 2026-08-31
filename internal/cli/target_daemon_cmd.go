package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func issueDaemonTargetApproval(ctx context.Context, stateDir string, kind domain.OperationKind, reference string, aliases []string, reason, key string, validFor time.Duration, jsonOutput bool, stdout, stderr io.Writer) int {
	clientValue, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		return mapClientError(err, stderr, "target approve")
	}
	response, err := clientValue.IssueTargetApproval(ctx, daemon.TargetApprovalIssueRequest{Kind: string(kind), Reference: reference, Aliases: aliases, Reason: reason, IdempotencyKey: key, ValidForMillis: int64(validFor / time.Millisecond)})
	if err != nil {
		return mapClientError(err, stderr, "target approve")
	}
	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(response); err != nil {
			return ExitMalformedProvider
		}
		return ExitSuccess
	}
	fmt.Fprintf(stdout, "Approval ID: %s\nDeadline: %s\nTarget: %s\n", response.ApprovalID, response.Deadline, response.Operation.Target)
	return ExitSuccess
}

func executeDaemonTargetMutation(ctx context.Context, stateDir, action, reference string, aliases []string, reason, key, approvalID, deadline string, jsonOutput bool, stdout, stderr io.Writer) int {
	clientValue, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		return mapClientError(err, stderr, "target "+action)
	}
	request := daemon.TargetMutationRequest{Reference: reference, Aliases: aliases, Reason: reason, IdempotencyKey: key, ApprovalID: approvalID, Deadline: deadline}
	var response any
	if action == "enroll" {
		response, err = clientValue.EnrollTarget(ctx, request)
	} else {
		response, err = clientValue.ClearTarget(ctx, request)
	}
	if err != nil {
		return mapClientError(err, stderr, "target "+action)
	}
	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(response); err != nil {
			return ExitMalformedProvider
		}
		return ExitSuccess
	}
	responseTarget, ok := response.(*daemon.TargetResponse)
	if !ok {
		return ExitMalformedProvider
	}
	fmt.Fprintln(stdout, responseTarget.Target.Locator)
	return ExitSuccess
}
