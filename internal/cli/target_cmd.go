package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

type targetOutput struct {
	SchemaVersion string `json:"schema_version"`
	Locator       string `json:"locator"`
	ProviderVMID  string `json:"provider_vm_id"`
}

type targetCandidatesOutput struct {
	SchemaVersion string         `json:"schema_version"`
	Candidates    []targetOutput `json:"candidates"`
}

type targetApprovalOutput struct {
	SchemaVersion string `json:"schema_version"`
	ApprovalID    string `json:"approval_id"`
	Deadline      string `json:"deadline"`
	ExpiresAt     string `json:"expires_at"`
	Target        string `json:"target"`
}

func runTarget(ctx context.Context, service *app.TargetService, coordinator *app.TargetCoordinator, actor domain.ActorContext, prompter Prompter, directMode bool, stateDir string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "amc target: missing subcommand (expected 'candidates', 'show', 'approve', 'enroll', or 'clear')")
		return ExitUsage
	}
	if service == nil {
		fmt.Fprintln(stderr, "amc target: protected target authority is unavailable")
		return ExitBackendUnavailable
	}
	switch args[0] {
	case "candidates":
		return runTargetCandidates(ctx, service, actor, args[1:], stdout, stderr)
	case "show":
		return runTargetShow(ctx, service, args[1:], stdout, stderr)
	case "approve":
		return runTargetApprove(ctx, service, coordinator, actor, prompter, directMode, stateDir, args[1:], stdout, stderr)
	case "enroll", "clear":
		return runTargetMutation(ctx, service, coordinator, actor, directMode, stateDir, args[0], args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprintln(stdout, "Usage: amc target <candidates|show|approve|enroll|clear> [flags] [reference]")
		return ExitSuccess
	default:
		fmt.Fprintf(stderr, "amc target: unknown subcommand %q\n", args[0])
		return ExitUsage
	}
}

func runTargetCandidates(ctx context.Context, service *app.TargetService, actor domain.ActorContext, args []string, stdout, stderr io.Writer) int {
	if !isTargetOperator(actor) {
		fmt.Fprintln(stderr, "amc target candidates: operator target:admin authority is required")
		return ExitDenied
	}
	jsonOutput, ok := parseTargetObserveFlags(args, stderr, "target candidates")
	if !ok {
		return ExitUsage
	}
	candidates, err := service.ListLocalCandidates(ctx)
	if err != nil {
		return mapCLIError(err, stderr, "target candidates")
	}
	values := make([]targetOutput, len(candidates))
	for index, candidate := range candidates {
		values[index] = targetOutput{SchemaVersion: SchemaVersion, Locator: candidate.Locator.String(), ProviderVMID: candidate.ProviderVMID}
	}
	if jsonOutput {
		if err := writeJSON(stdout, targetCandidatesOutput{SchemaVersion: SchemaVersion, Candidates: values}); err != nil {
			return ExitMalformedProvider
		}
		return ExitSuccess
	}
	for _, candidate := range values {
		fmt.Fprintln(stdout, candidate.Locator)
	}
	return ExitSuccess
}

func runTargetShow(ctx context.Context, service *app.TargetService, args []string, stdout, stderr io.Writer) int {
	jsonOutput, ok := parseTargetObserveFlags(args, stderr, "target show")
	if !ok {
		return ExitUsage
	}
	resolution, err := service.ShowDefaultTarget(ctx)
	if err != nil {
		return mapCLIError(err, stderr, "target show")
	}
	value := targetOutput{SchemaVersion: SchemaVersion, Locator: resolution.Locator.String(), ProviderVMID: resolution.ProviderVMID}
	if jsonOutput {
		if err := writeJSON(stdout, value); err != nil {
			return ExitMalformedProvider
		}
		return ExitSuccess
	}
	fmt.Fprintln(stdout, value.Locator)
	return ExitSuccess
}

func parseTargetObserveFlags(args []string, stderr io.Writer, command string) (bool, bool) {
	if len(args) == 0 {
		return false, true
	}
	if len(args) == 1 && (args[0] == "--json" || args[0] == "-json") {
		return true, true
	}
	fmt.Fprintf(stderr, "amc %s: unexpected argument %q\n", command, args[0])
	return false, false
}

//nolint:cyclop // This is the intentionally explicit parser and dispatch boundary for one approval command.
func runTargetApprove(ctx context.Context, _ *app.TargetService, coordinator *app.TargetCoordinator, actor domain.ActorContext, prompter Prompter, directMode bool, stateDir string, args []string, stdout, stderr io.Writer) int {
	if !isTargetOperator(actor) {
		fmt.Fprintln(stderr, "amc target approve: operator target:admin authority is required")
		return ExitDenied
	}
	positionals, flagArgs := splitPositionalAndFlags(args)
	if len(positionals) < 1 || len(positionals) > 2 || (positionals[0] != "enroll" && positionals[0] != "clear") || (positionals[0] == "clear" && len(positionals) != 1) {
		fmt.Fprintln(stderr, "Usage: amc target approve <enroll [reference]|clear> --reason <text> --idempotency-key <key> --valid-for <1s-5m> [--alias local] [--json]")
		return ExitUsage
	}
	flags := flag.NewFlagSet("target approve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	reason := flags.String("reason", "", "required reason")
	key := flags.String("idempotency-key", "", "required idempotency key")
	validForText := flags.String("valid-for", "", "approval validity")
	alias := flags.String("alias", "", "optional exact target alias")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(flagArgs); err != nil || domain.ValidateReason(*reason) != nil || domain.ValidateIdempotencyKey(*key) != nil {
		fmt.Fprintln(stderr, "amc target approve: valid --reason and --idempotency-key are required")
		return ExitUsage
	}
	validFor, err := time.ParseDuration(*validForText)
	if err != nil || validFor < time.Second || validFor > 5*time.Minute {
		fmt.Fprintln(stderr, "amc target approve: --valid-for must be between 1s and 5m")
		return ExitUsage
	}
	aliases := exactAlias(*alias)
	reference := ""
	if len(positionals) == 2 {
		reference = positionals[1]
	}
	kind := domain.OperationKind("target." + positionals[0])
	if prompter == nil || !prompter.PromptConfirmation(fmt.Sprintf("Issue one-use %s approval for %s?", kind, reference)) {
		fmt.Fprintln(stderr, "amc target approve: confirmation declined")
		return ExitDenied
	}
	if !directMode {
		return issueDaemonTargetApproval(ctx, stateDir, kind, reference, aliases, *reason, *key, validFor, *jsonOutput, stdout, stderr)
	}
	if coordinator == nil {
		fmt.Fprintln(stderr, "amc target approve: direct target coordinator is unavailable")
		return ExitBackendUnavailable
	}
	grant, err := coordinator.IssueApproval(ctx, app.TargetApprovalIssueParams{Kind: kind, Reference: reference, Aliases: aliases, Caller: actor, Reason: *reason, IdempotencyKey: *key, ValidFor: validFor})
	if err != nil {
		return mapTargetCommandError(err, stderr, "target approve")
	}
	if *jsonOutput {
		return writeTargetApprovalJSON(stdout, grant)
	}
	fmt.Fprintf(stdout, "Approval ID: %s\nDeadline: %s\nTarget: %s\n", grant.ApprovalID, grant.Deadline.Format(time.RFC3339Nano), grant.Operation.Target)
	return ExitSuccess
}

//nolint:cyclop // This is the intentionally explicit parser and dispatch boundary for one target mutation.
func runTargetMutation(ctx context.Context, _ *app.TargetService, coordinator *app.TargetCoordinator, actor domain.ActorContext, directMode bool, stateDir, action string, args []string, stdout, stderr io.Writer) int {
	if !isTargetOperator(actor) {
		fmt.Fprintf(stderr, "amc target %s: operator target:admin authority is required\n", action)
		return ExitDenied
	}
	positionals, flagArgs := splitPositionalAndFlags(args)
	if (action == "clear" && len(positionals) != 0) || (action == "enroll" && len(positionals) > 1) {
		fmt.Fprintf(stderr, "amc target %s: invalid target reference\n", action)
		return ExitUsage
	}
	flags := flag.NewFlagSet("target "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	reason := flags.String("reason", "", "required reason")
	key := flags.String("idempotency-key", "", "required idempotency key")
	approvalID := flags.String("approval-id", "", "exact approval ID")
	deadlineText := flags.String("deadline", "", "exact approval deadline")
	alias := flags.String("alias", "", "optional exact target alias")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(flagArgs); err != nil || domain.ValidateReason(*reason) != nil || domain.ValidateIdempotencyKey(*key) != nil || domain.ValidateApprovalID(*approvalID) != nil {
		fmt.Fprintf(stderr, "amc target %s: valid reason, idempotency key, and approval ID are required\n", action)
		return ExitUsage
	}
	deadline, err := time.Parse(time.RFC3339Nano, *deadlineText)
	if err != nil || deadline.UTC().Format(time.RFC3339Nano) != *deadlineText {
		fmt.Fprintf(stderr, "amc target %s: --deadline must be the exact approval deadline\n", action)
		return ExitUsage
	}
	reference := ""
	if len(positionals) == 1 {
		reference = positionals[0]
	}
	if !directMode {
		return executeDaemonTargetMutation(ctx, stateDir, action, reference, exactAlias(*alias), *reason, *key, *approvalID, *deadlineText, *jsonOutput, stdout, stderr)
	}
	if coordinator == nil {
		fmt.Fprintf(stderr, "amc target %s: direct target coordinator is unavailable\n", action)
		return ExitBackendUnavailable
	}
	result, err := coordinator.Mutate(ctx, app.TargetMutationParams{Kind: domain.OperationKind("target." + action), Reference: reference, Aliases: exactAlias(*alias), Caller: actor, Reason: *reason, IdempotencyKey: *key, Deadline: deadline, ApprovalID: *approvalID})
	if err != nil {
		return mapTargetCommandError(err, stderr, "target "+action)
	}
	value := targetOutput{SchemaVersion: SchemaVersion, Locator: result.Resolution.Locator.String(), ProviderVMID: result.Resolution.ProviderVMID}
	if *jsonOutput {
		if err := writeJSON(stdout, value); err != nil {
			return ExitMalformedProvider
		}
		return ExitSuccess
	}
	fmt.Fprintln(stdout, value.Locator)
	return ExitSuccess
}

func exactAlias(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

func isTargetOperator(actor domain.ActorContext) bool {
	return actor.Validate() == nil && !actor.IsDelegated() && actor.HasScope(domain.ScopeTargetAdmin)
}

func writeTargetApprovalJSON(stdout io.Writer, grant *app.TargetApprovalGrant) int {
	if grant == nil {
		return ExitBackendUnavailable
	}
	value := targetApprovalOutput{SchemaVersion: SchemaVersion, ApprovalID: grant.ApprovalID, Deadline: grant.Deadline.UTC().Format(time.RFC3339Nano), ExpiresAt: grant.ExpiresAt.UTC().Format(time.RFC3339Nano), Target: string(grant.Operation.Target)}
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		return ExitMalformedProvider
	}
	return ExitSuccess
}

func mapTargetCommandError(err error, stderr io.Writer, command string) int {
	if errors.Is(err, target.ErrAccessDenied) || errors.Is(err, target.ErrApprovalRequired) {
		fmt.Fprintf(stderr, "amc %s: target authority is denied\n", command)
		return ExitDenied
	}
	return mapCLIError(err, stderr, command)
}
