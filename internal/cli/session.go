package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type sessionSubcommandRunner func(ctx context.Context, cl *client.Client, args []string, stdout, stderr io.Writer) int

var sessionSubcommands = map[string]sessionSubcommandRunner{
	"open":    runSessionOpen,
	"read":    runSessionRead,
	"write":   runSessionWrite,
	"control": runSessionControl,
	"wait":    runSessionWait,
	"list":    runSessionList,
	"show":    runSessionShow,
	"close":   runSessionClose,
	"attach":  runSessionAttach,
}

func runSession(ctx context.Context, directMode bool, stateDir string, args []string, stdout, stderr io.Writer) int {
	if directMode {
		fmt.Fprintln(stderr, "amc: session commands cannot run in --direct mode; daemon amcd is required")
		return ExitBackendUnavailable
	}

	if len(args) == 0 {
		printSessionUsage(stderr)
		return ExitUsage
	}

	sub := args[0]
	subArgs := args[1:]

	if sub == "--help" || sub == "-h" || sub == "help" {
		printSessionUsage(stdout)
		return ExitSuccess
	}

	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		fmt.Fprintf(stderr, "amc: sessions require the background daemon (amcd); run 'amcd run' to start the daemon: %v\n", err)
		return ExitBackendUnavailable
	}

	runner, ok := sessionSubcommands[sub]
	if !ok {
		fmt.Fprintf(stderr, "amc: unknown session subcommand %q\n", sub)
		return ExitUsage
	}

	return runner(ctx, cl, subArgs, stdout, stderr)
}

func printSessionUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: amc session <subcommand> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  open <machine>     Open a new persistent SSH terminal session")
	fmt.Fprintln(w, "  read <session-id>  Read output chunks from a session")
	fmt.Fprintln(w, "  write <session-id> <data> Write input to a session")
	fmt.Fprintln(w, "  control <session-id> <key> Send a control key (ctrl-c, ctrl-d, enter, etc.)")
	fmt.Fprintln(w, "  wait <session-id>  Wait for output settle or regex match")
	fmt.Fprintln(w, "  list               List active and recent sessions")
	fmt.Fprintln(w, "  show <session-id>  Show details of a session")
	fmt.Fprintln(w, "  close <session-id> Close a persistent session")
	fmt.Fprintln(w, "  attach <session-id> Interactively attach to a session")
}

func runSessionOpen(ctx context.Context, cl *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session open", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var reason, term, idemKey, approvalFile string
	var cols, rows uint
	var jsonOutput bool

	fs.StringVar(&reason, "reason", "Interactive CLI terminal session", "Reason for opening session")
	fs.StringVar(&term, "term", "xterm-256color", "Terminal emulation type")
	fs.StringVar(&idemKey, "idempotency-key", "", "Idempotency key")
	fs.StringVar(&approvalFile, "approval-file", "", "Path to operator approval JSON file")
	fs.UintVar(&cols, "cols", 80, "Terminal columns")
	fs.UintVar(&rows, "rows", 24, "Terminal rows")
	fs.BoolVar(&jsonOutput, "json", false, "Output JSON format")

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	pos := fs.Args()
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "amc: target machine GUID is required")
		return ExitUsage
	}
	target := pos[0]

	var appObj *domain.Approval
	if approvalFile != "" {
		a, err := approval.LoadFromFile(approvalFile)
		if err != nil {
			fmt.Fprintf(stderr, "amc session open: invalid approval file: %v\n", err)
			return ExitDenied
		}
		appObj = a
	}

	if idemKey == "" {
		idemKey = fmt.Sprintf("cli-open-%d", time.Now().UnixNano())
	}

	req := daemon.SessionOpenRequest{
		Target:         target,
		Reason:         reason,
		IdempotencyKey: idemKey,
		Cols:           uint16(cols),
		Rows:           uint16(rows),
		Term:           term,
		Approval:       appObj,
	}

	resp, err := cl.OpenSession(ctx, req)
	if err != nil {
		return mapClientError(err, stderr, "session open")
	}

	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(resp)
		return ExitSuccess
	}

	fmt.Fprintf(stdout, "Session opened: %s (machine: %s, state: %s)\n", resp.Session.SessionID, resp.Session.Target, resp.Session.State)
	return ExitSuccess
}

func runSessionRead(ctx context.Context, cl *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session read", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var afterSeq uint64
	var limitBytes int
	var jsonOutput bool

	fs.Uint64Var(&afterSeq, "after-seq", 0, "Read chunks after sequence number")
	fs.IntVar(&limitBytes, "limit", 64*1024, "Maximum bytes to read")
	fs.BoolVar(&jsonOutput, "json", false, "Output JSON format")

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	pos := fs.Args()
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "amc: session ID is required")
		return ExitUsage
	}
	sessID := pos[0]

	resp, err := cl.ReadSession(ctx, sessID, afterSeq, limitBytes, 10*time.Second)
	if err != nil {
		return mapClientError(err, stderr, "session read")
	}

	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(resp)
		return ExitSuccess
	}

	for _, c := range resp.Chunks {
		fmt.Fprint(stdout, c.Data)
	}
	return ExitSuccess
}

func runSessionWrite(ctx context.Context, cl *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session write", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var reason, idemKey, data, approvalFile string
	var jsonOutput bool

	fs.StringVar(&reason, "reason", "CLI session write", "Reason for writing data")
	fs.StringVar(&idemKey, "idempotency-key", "", "Idempotency key")
	fs.StringVar(&data, "data", "", "Data to write")
	fs.StringVar(&approvalFile, "approval-file", "", "Path to operator approval JSON file")
	fs.BoolVar(&jsonOutput, "json", false, "Output JSON format")

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	pos := fs.Args()
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "amc: session ID is required")
		return ExitUsage
	}
	sessID := pos[0]

	if data == "" && len(pos) > 1 {
		data = strings.Join(pos[1:], " ") + "\n"
	}
	if data == "" {
		fmt.Fprintln(stderr, "amc: data to write is required")
		return ExitUsage
	}

	var appObj *domain.Approval
	if approvalFile != "" {
		a, err := approval.LoadFromFile(approvalFile)
		if err != nil {
			fmt.Fprintf(stderr, "amc session write: invalid approval file: %v\n", err)
			return ExitDenied
		}
		appObj = a
	}

	if idemKey == "" {
		idemKey = fmt.Sprintf("cli-write-%d", time.Now().UnixNano())
	}

	resp, err := cl.WriteSession(ctx, sessID, data, reason, idemKey, appObj)
	if err != nil {
		return mapClientError(err, stderr, "session write")
	}

	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(resp)
		return ExitSuccess
	}

	return ExitSuccess
}

func runSessionControl(ctx context.Context, cl *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session control", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var reason, idemKey, approvalFile string
	var jsonOutput bool

	fs.StringVar(&reason, "reason", "CLI session control", "Reason for sending control key")
	fs.StringVar(&idemKey, "idempotency-key", "", "Idempotency key")
	fs.StringVar(&approvalFile, "approval-file", "", "Path to operator approval JSON file")
	fs.BoolVar(&jsonOutput, "json", false, "Output JSON format")

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	pos := fs.Args()
	if len(pos) < 2 {
		fmt.Fprintln(stderr, "amc: session ID and control key are required (e.g. amc session control <id> ctrl-c)")
		return ExitUsage
	}
	sessID := pos[0]
	ctrlKey := domain.ControlKey(pos[1])

	var appObj *domain.Approval
	if approvalFile != "" {
		a, err := approval.LoadFromFile(approvalFile)
		if err != nil {
			fmt.Fprintf(stderr, "amc session control: invalid approval file: %v\n", err)
			return ExitDenied
		}
		appObj = a
	}

	if idemKey == "" {
		idemKey = fmt.Sprintf("cli-ctrl-%d", time.Now().UnixNano())
	}

	resp, err := cl.SendControlKey(ctx, sessID, ctrlKey, reason, idemKey, appObj)
	if err != nil {
		return mapClientError(err, stderr, "session control")
	}

	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(resp)
		return ExitSuccess
	}

	return ExitSuccess
}
