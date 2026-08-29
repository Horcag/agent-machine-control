package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Horcag/agent-machine-control/internal/actor"
	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/buildinfo"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

// AppOption configures App dependencies.
type AppOption func(*App)

// WithClock configures a custom clock function on App.
func WithClock(fn func() time.Time) AppOption {
	return func(a *App) {
		a.nowFn = fn
	}
}

// App manages command routing and CLI dependencies.
type App struct {
	discoveryService *app.DiscoveryService
	recoveryService  *app.RecoveryService
	actor            domain.ActorContext
	prompter         Prompter
	directDefault    bool
	nowFn            func() time.Time
}

func (a *App) now() time.Time {
	if a.nowFn != nil {
		return a.nowFn().UTC()
	}
	return time.Now().UTC()
}

// WithRecoveryService configures a custom RecoveryService on App.
func WithRecoveryService(s *app.RecoveryService) AppOption {
	return func(a *App) {
		a.recoveryService = s
	}
}

// WithActor configures an authenticated local actor context on App.
func WithActor(act domain.ActorContext) AppOption {
	return func(a *App) {
		a.actor = act
	}
}

// WithPrompter configures an interactive prompter on App.
func WithPrompter(p Prompter) AppOption {
	return func(a *App) {
		a.prompter = p
	}
}

// WithDirectMode sets the default direct mode setting on App.
func WithDirectMode(direct bool) AppOption {
	return func(a *App) {
		a.directDefault = direct
	}
}

// NewApp creates a new App configured with the given DiscoveryService and options.
func NewApp(service *app.DiscoveryService, opts ...AppOption) *App {
	a := &App{
		discoveryService: service,
		prompter:         &DefaultPrompter{Stdin: os.Stdin, Stdout: os.Stderr},
		nowFn:            time.Now,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Run executes the command line entrypoint with the default Hyper-V backend adapter.
func Run(args []string, stdout, stderr io.Writer) int {
	norm, err := NormalizeGlobalFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "amc: %v\n", err)
		return ExitUsage
	}

	adapter := hyperv.New()
	discoveryService := app.NewDiscoveryService(adapter)

	if !isDirectMutatingCommand(norm) {
		readOnlyRecoverySvc := app.NewRecoveryService(adapter, nil, nil, nil, nil)
		appInstance := NewApp(
			discoveryService,
			WithRecoveryService(readOnlyRecoverySvc),
			WithPrompter(&DefaultPrompter{Stdin: os.Stdin, Stdout: stderr}),
			WithDirectMode(norm.Direct),
		)
		return appInstance.Run(args, stdout, stderr)
	}

	sd, err := statedir.Resolve(norm.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "amc: failed to resolve state directory: %v\n", err)
		return ExitBackendUnavailable
	}
	if err := sd.EnsureDirs(); err != nil {
		fmt.Fprintf(stderr, "amc: failed to initialize state directory: %v\n", err)
		return ExitBackendUnavailable
	}

	leaseMgr := lease.NewManager(sd.LeasesDir())
	auditStore := audit.NewStore(sd.AuditDir())
	receiptStore := receipt.NewStore(sd.ReceiptsDir())
	approvalStore := approval.NewStore(sd.ApprovalsDir())

	recoveryService := app.NewRecoveryService(
		adapter,
		leaseMgr,
		auditStore,
		receiptStore,
		approvalStore,
	)

	actorResolver := &actor.DefaultResolver{}
	actCtx, err := actorResolver.Resolve()
	if err != nil {
		fmt.Fprintf(stderr, "amc: failed to resolve actor identity: %v\n", err)
		return ExitBackendUnavailable
	}

	appInstance := NewApp(
		discoveryService,
		WithRecoveryService(recoveryService),
		WithActor(actCtx),
		WithPrompter(&DefaultPrompter{Stdin: os.Stdin, Stdout: stderr}),
		WithDirectMode(norm.Direct),
	)

	return appInstance.Run(args, stdout, stderr)
}

func isDirectMutatingCommand(norm NormalizedCLI) bool {
	if !norm.Direct || len(norm.CommandArgs) < 2 {
		return false
	}
	return isMutatingSubcommand(norm.CommandArgs[0], norm.CommandArgs[1])
}

func isMutatingSubcommand(cmd, sub string) bool {
	switch cmd {
	case "machine":
		return sub == "start" || sub == "stop"
	case "checkpoint":
		return sub == "create" || sub == "restore"
	default:
		return false
	}
}

// Run parses arguments and delegates execution to the appropriate command handler.
func (a *App) Run(args []string, stdout, stderr io.Writer) int {
	norm, err := NormalizeGlobalFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "amc: %v\n", err)
		return ExitUsage
	}

	if len(norm.CommandArgs) == 0 {
		printUsage(stderr)
		return ExitUsage
	}

	directMode := a.directDefault || norm.Direct
	cmdArgs := norm.CommandArgs[1:]
	if norm.JSON {
		cmdArgs = append(cmdArgs, "--json")
	}

	switch norm.CommandArgs[0] {
	case "--version", "-version", "-v":
		fmt.Fprintln(stdout, buildinfo.String("amc"))
		return ExitSuccess

	case "--help", "-help", "-h", "help":
		printUsage(stdout)
		return ExitSuccess

	case "doctor":
		return runDoctor(context.Background(), a.discoveryService, cmdArgs, stdout, stderr)

	case "machine":
		return runMachine(
			context.Background(),
			a.discoveryService,
			a.recoveryService,
			a.actor,
			a.prompter,
			a.now,
			directMode,
			cmdArgs,
			stdout,
			stderr,
		)

	case "checkpoint":
		return runCheckpoint(
			context.Background(),
			a.recoveryService,
			a.actor,
			a.prompter,
			a.now,
			directMode,
			cmdArgs,
			stdout,
			stderr,
		)

	default:
		fmt.Fprintf(stderr, "amc: unknown command %q\n", norm.CommandArgs[0])
		return ExitUsage
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: amc [--direct] <command> [subcommand] [flags] [args]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  doctor                                   Check Hyper-V and host readiness")
	fmt.Fprintln(w, "  machine list                             List discovered virtual machines")
	fmt.Fprintln(w, "  machine inspect <guid>                   Inspect virtual machine configuration and state")
	fmt.Fprintln(w, "  machine start <guid>                     Start virtual machine (requires --direct)")
	fmt.Fprintln(w, "  machine stop <guid>                      Stop virtual machine (requires --direct)")
	fmt.Fprintln(w, "  checkpoint list <guid>                   List virtual machine checkpoints")
	fmt.Fprintln(w, "  checkpoint create <guid> --name <name>   Create checkpoint (requires --direct)")
	fmt.Fprintln(w, "  checkpoint restore <guid> <chk-guid>     Restore checkpoint (requires --direct)")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Global Flags:")
	fmt.Fprintln(w, "  --direct                                 Execute in-process direct recovery mode")
	fmt.Fprintln(w, "  --state-dir <dir>                        Override state directory path")
	fmt.Fprintln(w, "  --json                                   Output structured JSON envelope")
	fmt.Fprintln(w, "  --help, -h                               Show help information")
	fmt.Fprintln(w, "  --version, -v                            Show version information")
}
