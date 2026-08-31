package daemoncli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/bootstrap"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type bootstrapCommandService interface {
	Status(context.Context, string) (app.BootstrapResult, error)
	Ensure(context.Context, app.BootstrapMutationRequest) (app.BootstrapResult, error)
	Start(context.Context, app.BootstrapMutationRequest) (app.BootstrapResult, error)
	Stop(context.Context, app.BootstrapMutationRequest) (app.BootstrapResult, error)
	Remove(context.Context, app.BootstrapMutationRequest) (app.BootstrapResult, error)
}

var bootstrapServiceFactory = func(stateDir string, _ bool) (bootstrapCommandService, error) {
	sd, err := statedir.Resolve(stateDir)
	if err != nil {
		return nil, err
	}
	return app.NewBootstrapService(
		bootstrap.NewPowerShellAdapter(), bootstrap.NewLocalDaemon(),
		audit.NewStore(sd.AuditDir()), receipt.NewStore(sd.ReceiptsDir()),
	), nil
}

func runBootstrap(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printBootstrapUsage(stderr)
		return ExitUsage
	}
	switch args[0] {
	case "status":
		return runBootstrapStatus(args[1:], stdout, stderr)
	case "ensure", "start", "stop", "remove":
		return runBootstrapMutation(args[0], args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printBootstrapUsage(stdout)
		return ExitSuccess
	default:
		fmt.Fprintf(stderr, "amcd bootstrap: unknown command %q\n", args[0])
		return ExitUsage
	}
}

func runBootstrapStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("amcd bootstrap status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", "", "state directory path")
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return ExitUsage
	}
	service, err := bootstrapServiceFactory(*stateDir, false)
	if err != nil {
		return reportBootstrapError(stderr, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := service.Status(ctx, *stateDir)
	emitBootstrapResult(stdout, result, *jsonOutput)
	if err != nil {
		return reportBootstrapError(stderr, err)
	}
	return ExitSuccess
}

func runBootstrapMutation(action string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("amcd bootstrap "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", "", "state directory path")
	reason := fs.String("reason", "", "operator reason")
	idempotencyKey := fs.String("idempotency-key", "", "idempotency key")
	timeout := fs.Duration("timeout", 45*time.Second, "absolute operation timeout")
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *timeout <= 0 {
		return ExitUsage
	}
	if err := domain.ValidateReason(*reason); err != nil {
		fmt.Fprintln(stderr, "amcd bootstrap: --reason is required")
		return ExitUsage
	}
	if err := domain.ValidateIdempotencyKey(*idempotencyKey); err != nil {
		fmt.Fprintln(stderr, "amcd bootstrap: --idempotency-key is required")
		return ExitUsage
	}
	service, err := bootstrapServiceFactory(*stateDir, true)
	if err != nil {
		return reportBootstrapError(stderr, err)
	}
	deadline := time.Now().Add(*timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	req := app.BootstrapMutationRequest{
		StateDir: *stateDir, Reason: *reason, IdempotencyKey: *idempotencyKey, Deadline: deadline,
	}
	result, err := invokeBootstrapMutation(ctx, service, action, req)
	emitBootstrapResult(stdout, result, *jsonOutput)
	if err != nil {
		return reportBootstrapError(stderr, err)
	}
	return ExitSuccess
}

func invokeBootstrapMutation(ctx context.Context, service bootstrapCommandService, action string, req app.BootstrapMutationRequest) (app.BootstrapResult, error) {
	switch action {
	case "ensure":
		return service.Ensure(ctx, req)
	case "start":
		return service.Start(ctx, req)
	case "stop":
		return service.Stop(ctx, req)
	case "remove":
		return service.Remove(ctx, req)
	default:
		return app.BootstrapResult{}, domain.ErrInvalidOperationKind
	}
}

func emitBootstrapResult(w io.Writer, result app.BootstrapResult, jsonOutput bool) {
	if result.SchemaVersion == 0 {
		return
	}
	if jsonOutput {
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	fmt.Fprintf(w, "amcd bootstrap: %s", result.Status)
	if result.Reason != "" {
		fmt.Fprintf(w, " (%s)", result.Reason)
	}
	fmt.Fprintln(w)
}

func reportBootstrapError(stderr io.Writer, err error) int {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, app.ErrBootstrapUnhealthy):
		fmt.Fprintln(stderr, "amcd bootstrap: operation timed out")
		return ExitTimeout
	case errors.Is(err, app.ErrBootstrapPriorFailed):
		fmt.Fprintln(stderr, "amcd bootstrap: prior exact attempt failed; use a new idempotency key for a new intent")
		return ExitConflict
	case errors.Is(err, app.ErrBootstrapDrift):
		fmt.Fprintln(stderr, "amcd bootstrap: owned state drift detected; no ambiguous mutation was performed")
		return ExitConflict
	case errors.Is(err, app.ErrBootstrapAbsent):
		fmt.Fprintln(stderr, "amcd bootstrap: owned task is absent")
		return ExitNotFound
	case errors.Is(err, app.ErrBootstrapUnsupported):
		fmt.Fprintln(stderr, "amcd bootstrap: current host is not a supported WSL/Windows task environment")
		return ExitBackendUnavailable
	default:
		fmt.Fprintln(stderr, "amcd bootstrap: lifecycle operation failed")
		return ExitBackendUnavailable
	}
}

func printBootstrapUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: amcd bootstrap <ensure|status|start|stop|remove> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Mutation flags:")
	fmt.Fprintln(w, "  --state-dir <path>       Override state directory path")
	fmt.Fprintln(w, "  --reason <text>          Required operator reason")
	fmt.Fprintln(w, "  --idempotency-key <key>  Required exact-retry key")
	fmt.Fprintln(w, "  --timeout <duration>     Operation timeout (default 45s)")
	fmt.Fprintln(w, "  --json                   Emit machine-readable JSON")
}
