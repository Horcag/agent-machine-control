package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func runSessionWait(ctx context.Context, cl *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session wait", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var settleMs int
	var regex string
	var afterSeq uint64
	var timeoutSec int
	var jsonOutput bool

	fs.IntVar(&settleMs, "settle-ms", 500, "Quiet settle time in milliseconds")
	fs.StringVar(&regex, "regex", "", "Regex pattern to match against output")
	fs.Uint64Var(&afterSeq, "after-seq", 0, "Sequence number to match from")
	fs.IntVar(&timeoutSec, "timeout", 30, "Wait timeout in seconds")
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

	req := daemon.SessionWaitRequest{
		SettleMs:       settleMs,
		Regex:          regex,
		AfterSeq:       afterSeq,
		TimeoutSeconds: timeoutSec,
	}

	resp, err := cl.WaitSession(ctx, sessID, req)
	if err != nil {
		return mapClientError(err, stderr, "session wait")
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

func runSessionList(ctx context.Context, cl *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session list", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var machineRef string
	var jsonOutput bool

	fs.StringVar(&machineRef, "machine", "", "Filter by machine GUID")
	fs.BoolVar(&jsonOutput, "json", false, "Output JSON format")

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	sessions, err := cl.ListSessions(ctx, machineRef)
	if err != nil {
		return mapClientError(err, stderr, "session list")
	}

	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(sessions)
		return ExitSuccess
	}

	if len(sessions) == 0 {
		fmt.Fprintln(stdout, "No active sessions found.")
		return ExitSuccess
	}

	fmt.Fprintf(stdout, "%-38s %-38s %-10s %-10s\n", "SESSION ID", "MACHINE", "STATE", "COLSxROWS")
	for _, s := range sessions {
		fmt.Fprintf(stdout, "%-38s %-38s %-10s %dx%d\n", s.SessionID, s.Target, s.State, s.Cols, s.Rows)
	}
	return ExitSuccess
}

func runSessionShow(ctx context.Context, cl *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session show", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var jsonOutput bool
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

	sess, err := cl.GetSession(ctx, sessID)
	if err != nil {
		return mapClientError(err, stderr, "session show")
	}

	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(sess)
		return ExitSuccess
	}

	fmt.Fprintf(stdout, "Session ID:        %s\n", sess.SessionID)
	fmt.Fprintf(stdout, "Target Machine:    %s\n", sess.Target)
	fmt.Fprintf(stdout, "Owner Actor:       %s\n", sess.OwnerActor)
	fmt.Fprintf(stdout, "State:             %s\n", sess.State)
	fmt.Fprintf(stdout, "Dimensions:        %dx%d (%s)\n", sess.Cols, sess.Rows, sess.TermType)
	fmt.Fprintf(stdout, "Bytes Read/Write:  %d / %d\n", sess.BytesRead, sess.BytesWritten)
	fmt.Fprintf(stdout, "Created At:        %s\n", sess.CreatedAt)
	if sess.ExitCode != nil {
		fmt.Fprintf(stdout, "Exit Code:         %d\n", *sess.ExitCode)
	}
	return ExitSuccess
}

func runSessionClose(ctx context.Context, cl *client.Client, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session close", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var reason, idemKey, approvalFile string
	var force, jsonOutput bool

	fs.StringVar(&reason, "reason", "CLI user session close", "Reason for closing session")
	fs.StringVar(&idemKey, "idempotency-key", "", "Idempotency key")
	fs.StringVar(&approvalFile, "approval-file", "", "Path to operator approval JSON file")
	fs.BoolVar(&force, "force", false, "Force terminate immediately")
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

	var appObj *domain.Approval
	if approvalFile != "" {
		a, err := approval.LoadFromFile(approvalFile)
		if err != nil {
			fmt.Fprintf(stderr, "amc session close: invalid approval file: %v\n", err)
			return ExitDenied
		}
		appObj = a
	}

	if idemKey == "" {
		idemKey = fmt.Sprintf("cli-close-%d", time.Now().UnixNano())
	}

	resp, err := cl.CloseSession(ctx, sessID, reason, idemKey, force, appObj)
	if err != nil {
		return mapClientError(err, stderr, "session close")
	}

	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(resp)
		return ExitSuccess
	}

	fmt.Fprintf(stdout, "Session %s closed.\n", sessID)
	return ExitSuccess
}

func runSessionAttach(ctx context.Context, cl *client.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "amc: session ID is required")
		return ExitUsage
	}
	sessID := args[0]

	// Read loop with settle
	afterSeq := uint64(0)
	for {
		select {
		case <-ctx.Done():
			return ExitSuccess
		default:
		}

		resp, err := cl.ReadSession(ctx, sessID, afterSeq, 64*1024, 2*time.Second)
		if err != nil {
			if errors.Is(err, client.ErrNotFound) || errors.Is(err, domain.ErrSessionClosed) {
				return ExitSuccess
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		for _, c := range resp.Chunks {
			fmt.Fprint(stdout, c.Data)
		}
		afterSeq = resp.NextSeq

		if resp.Closed {
			return ExitSuccess
		}

		time.Sleep(50 * time.Millisecond)
	}
}
