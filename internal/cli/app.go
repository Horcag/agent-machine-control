package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/buildinfo"
)

// App manages command routing and CLI dependencies.
type App struct {
	service *app.DiscoveryService
}

// NewApp creates a new App configured with the given DiscoveryService.
func NewApp(service *app.DiscoveryService) *App {
	return &App{service: service}
}

// Run executes the command line entrypoint with the default Hyper-V backend adapter.
func Run(args []string, stdout, stderr io.Writer) int {
	adapter := hyperv.New()
	service := app.NewDiscoveryService(adapter)
	return NewApp(service).Run(args, stdout, stderr)
}

// Run parses arguments and delegates execution to the appropriate command handler.
func (a *App) Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return ExitUsage
	}

	switch args[0] {
	case "--version", "-version", "-v":
		fmt.Fprintln(stdout, buildinfo.String("amc"))
		return ExitSuccess

	case "--help", "-help", "-h", "help":
		printUsage(stdout)
		return ExitSuccess

	case "doctor":
		return runDoctor(context.Background(), a.service, args[1:], stdout, stderr)

	case "machine":
		return runMachine(context.Background(), a.service, args[1:], stdout, stderr)

	default:
		fmt.Fprintf(stderr, "amc: unknown command %q\n", args[0])
		return ExitUsage
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: amc <command> [subcommand] [flags] [args]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  doctor                     Check Hyper-V and host readiness")
	fmt.Fprintln(w, "  machine list               List discovered virtual machines")
	fmt.Fprintln(w, "  machine inspect <guid>     Inspect virtual machine configuration and state")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --help, -h                 Show help information")
	fmt.Fprintln(w, "  --version, -v              Show version information")
	fmt.Fprintln(w, "  --json                     Output structured JSON envelope")
}
