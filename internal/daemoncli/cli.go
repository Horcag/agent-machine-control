package daemoncli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Horcag/agent-machine-control/internal/buildinfo"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
)

const (
	ExitSuccess            = 0
	ExitUsage              = 2
	ExitNotFound           = 3
	ExitBackendUnavailable = 4
	ExitMalformedProvider  = 5
	ExitTimeout            = 6
	ExitDenied             = 7
	ExitConflict           = 8
)

// Run executes the amcd CLI entrypoint.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printDaemonUsage(stderr)
		return ExitUsage
	}

	switch args[0] {
	case "--version", "-version", "-v":
		fmt.Fprintln(stdout, buildinfo.String("amcd"))
		return ExitSuccess
	case "--help", "-help", "-h", "help":
		printDaemonUsage(stdout)
		return ExitSuccess
	case "run":
		return runDaemon(args[1:], stdout, stderr)
	case "status":
		return statusDaemon(args[1:], stdout, stderr)
	case "stop":
		return stopDaemon(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "amcd: unknown command %q\n", args[0])
		return ExitUsage
	}
}

func runDaemon(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("amcd run", flag.ContinueOnError)
	fs.SetOutput(stderr)

	stateDir := fs.String("state-dir", "", "state directory path")
	listenAddr := fs.String("listen", "127.0.0.1:0", "HTTP listen address")
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON")

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	srv, err := daemon.NewServer(daemon.Config{
		StateDir:   *stateDir,
		ListenAddr: *listenAddr,
	})
	if err != nil {
		fmt.Fprintf(stderr, "amcd: initialization failed: %v\n", err)
		return ExitBackendUnavailable
	}

	if err := srv.Start(); err != nil {
		fmt.Fprintf(stderr, "amcd: failed to start: %v\n", err)
		return ExitBackendUnavailable
	}

	if *jsonOutput {
		fmt.Fprintf(stdout, `{"schema_version":"1","status":"running","endpoint":%q,"pid":%d}`+"\n", srv.Endpoint(), srv.PID())
	} else {
		fmt.Fprintf(stdout, "amcd running on %s (pid %d)\n", srv.Endpoint(), srv.PID())
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		srv.TriggerShutdown()
	}()

	srv.Wait()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)

	return ExitSuccess
}

func statusDaemon(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("amcd status", flag.ContinueOnError)
	fs.SetOutput(stderr)

	stateDir := fs.String("state-dir", "", "state directory path")
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON")

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	cl, err := client.Discover(*stateDir, client.TokenTypeOperator)
	if err != nil {
		if *jsonOutput {
			fmt.Fprintln(stdout, `{"schema_version":"1","status":"stopped"}`)
			return ExitBackendUnavailable
		}
		fmt.Fprintln(stdout, "amcd is stopped or unreachable")
		return ExitBackendUnavailable
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	health, err := cl.Health(ctx)
	if err != nil {
		if *jsonOutput {
			fmt.Fprintln(stdout, `{"schema_version":"1","status":"stopped"}`)
			return ExitBackendUnavailable
		}
		fmt.Fprintln(stdout, "amcd is stopped or unreachable")
		return ExitBackendUnavailable
	}

	if *jsonOutput {
		fmt.Fprintf(stdout, `{"schema_version":"1","status":"ok","endpoint":%q,"pid":%d,"version":%q}`+"\n", cl.Endpoint(), health.PID, health.Version)
		return ExitSuccess
	}

	fmt.Fprintf(stdout, "amcd is running on %s (pid %d, version %s)\n", cl.Endpoint(), health.PID, health.Version)
	return ExitSuccess
}

func stopDaemon(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("amcd stop", flag.ContinueOnError)
	fs.SetOutput(stderr)

	stateDir := fs.String("state-dir", "", "state directory path")
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON")

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	cl, err := client.Discover(*stateDir, client.TokenTypeOperator)
	if err != nil {
		fmt.Fprintf(stderr, "amcd stop: %v\n", err)
		return ExitBackendUnavailable
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := cl.StopDaemon(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "amcd stop: %v\n", err)
		return ExitBackendUnavailable
	}

	if *jsonOutput {
		fmt.Fprintf(stdout, `{"schema_version":"1","status":%q}`+"\n", resp.Status)
		return ExitSuccess
	}

	fmt.Fprintln(stdout, "amcd: stop signal sent successfully")
	return ExitSuccess
}

func printDaemonUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: amcd <command> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  run     Run the daemon in foreground")
	fmt.Fprintln(w, "  status  Query running daemon status")
	fmt.Fprintln(w, "  stop    Stop running daemon")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --state-dir <path>  Override state directory path")
	fmt.Fprintln(w, "  --listen <addr>     Listen address (default 127.0.0.1:0)")
	fmt.Fprintln(w, "  --json              Emit machine-readable JSON")
	fmt.Fprintln(w, "  --help, -h          Show help")
	fmt.Fprintln(w, "  --version, -v       Show version")
}
