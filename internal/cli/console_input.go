package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func (a *App) runConsoleInput(ctx context.Context, direct bool, stateDir string, args []string, stdout, stderr io.Writer) int {
	switch args[0] {
	case "key", "type", "move", "click", "drag", "scroll":
	default:
		fmt.Fprintln(stderr, "amc console: unknown action")
		return ExitUsage
	}
	pos, flags := consoleArgs(args[1:])
	fs := flag.NewFlagSet("console "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var input domain.ConsoleInput
	input.Kind = args[0]
	fs.StringVar(&input.FrameID, "frame-id", "", "captured frame for pointer coordinates")
	fs.IntVar(&input.X, "x", 0, "frame x coordinate")
	fs.IntVar(&input.Y, "y", 0, "frame y coordinate")
	fs.IntVar(&input.ToX, "to-x", 0, "drag destination x")
	fs.IntVar(&input.ToY, "to-y", 0, "drag destination y")
	fs.StringVar(&input.Button, "button", "", "left, right, or middle")
	fs.StringVar(&input.Key, "key", "", "console key")
	fs.StringVar(&input.Text, "text", "", "text (prefer --text-file for sensitive input)")
	textFile := fs.String("text-file", "", "read typed text from a file")
	fs.IntVar(&input.Delta, "delta", 0, "bounded scroll amount")
	common, err := parseCommonFlags(fs, flags, stderr, "console "+args[0])
	if err != nil || len(pos) > 1 {
		return ExitUsage
	}
	if common.Async {
		fmt.Fprintln(stderr, "amc console: --async is unsupported")
		return ExitUsage
	}
	if err = readConsoleTextFile(&input, *textFile); err != nil {
		fmt.Fprintln(stderr, "amc console type:", err)
		return ExitUsage
	}
	deadline := common.Deadline
	if deadline.IsZero() {
		deadline = a.now().Add(common.Timeout)
	}
	req := app.ConsoleInputRequest{Input: input, Reason: common.Reason, IdempotencyKey: common.IdempotencyKey, Deadline: deadline.UTC().Format(time.RFC3339Nano), ApprovalID: common.ApprovalID}
	if len(pos) == 1 {
		req.Target = pos[0]
	}
	rcpt, inputErr := a.sendConsoleInput(ctx, direct, stateDir, req)
	if inputErr != nil {
		return consoleError(inputErr, direct, stderr, "console input")
	}
	if common.JSON {
		if err = writeJSON(stdout, receipt.ConvertToDTO(rcpt)); err != nil {
			return ExitMalformedProvider
		}
	} else {
		fmt.Fprintln(stdout, "Console input completed.")
	}
	return ExitSuccess
}

func readConsoleTextFile(input *domain.ConsoleInput, path string) error {
	if path == "" {
		return nil
	}
	if input.Kind != "type" || input.Text != "" {
		return fmt.Errorf("--text-file requires type without --text")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("text file unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(data) > 65536 {
		return fmt.Errorf("text file exceeds limit or is unreadable")
	}
	input.Text = string(data)
	return nil
}

func (a *App) sendConsoleInput(ctx context.Context, direct bool, stateDir string, req app.ConsoleInputRequest) (domain.Receipt, error) {
	if direct {
		if a.consoleService == nil {
			return domain.Receipt{}, client.ErrDaemonUnavailable
		}
		return a.consoleService.Input(ctx, a.actor, req)
	}
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		return domain.Receipt{}, err
	}
	return cl.ConsoleInput(ctx, req)
}
