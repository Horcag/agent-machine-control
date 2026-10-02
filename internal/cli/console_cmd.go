package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

// ConsoleService is the shared application boundary used by direct recovery.
type ConsoleService interface {
	Screenshot(context.Context, domain.ActorContext, app.ConsoleScreenshotRequest) (domain.ConsoleFrame, error)
	Input(context.Context, domain.ActorContext, app.ConsoleInputRequest) (domain.Receipt, error)
}

// WithConsoleService configures direct VM-console recovery.
func WithConsoleService(s ConsoleService) AppOption { return func(a *App) { a.consoleService = s } }

func consoleArgs(args []string) ([]string, []string) {
	var pos, flags []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			pos = append(pos, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if !strings.Contains(name, "=") && name != "json" && name != "async" && name != "for-mcp" && name != "acknowledge-external-effects" && i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return pos, flags
}

func (a *App) runConsole(ctx context.Context, direct bool, stateDir string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "amc console: requires screenshot, key, type, move, click, drag, or scroll")
		return ExitUsage
	}
	if args[0] == "screenshot" {
		return a.runConsoleScreenshot(ctx, direct, stateDir, args[1:], stdout, stderr)
	}
	if args[0] == "record" {
		return a.runConsoleRecord(ctx, direct, stateDir, args[1:], stdout, stderr)
	}
	return a.runConsoleInput(ctx, direct, stateDir, args, stdout, stderr)
}

func (a *App) runConsoleScreenshot(ctx context.Context, direct bool, stateDir string, args []string, stdout, stderr io.Writer) int {
	pos, flags := consoleArgs(args)
	fs := flag.NewFlagSet("console screenshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	width := fs.Int("width", 1024, "maximum image width")
	height := fs.Int("height", 768, "maximum image height")
	output := fs.String("output", "", "new protected PNG path (required)")
	jsonOutput := fs.Bool("json", false, "emit frame metadata")
	if err := fs.Parse(flags); err != nil {
		return ExitUsage
	}
	if len(pos) > 1 || *output == "" || *width <= 0 || *height <= 0 || *width > domain.MaxConsolePixels / *height {
		fmt.Fprintln(stderr, "amc console screenshot: requires --output and bounded positive dimensions with at most one target")
		return ExitUsage
	}
	// Reserve the path before capture: existing files and symlinks are never overwritten.
	file, err := reserveConsoleOutput(ctx, *output)
	if err != nil {
		fmt.Fprintln(stderr, "amc console screenshot: output path is unavailable")
		return ExitConflict
	}
	saved := false
	defer func() {
		if !saved {
			removeReservedConsoleOutput(file, *output)
		}
		_ = file.Close()
	}()
	req := app.ConsoleScreenshotRequest{Width: *width, Height: *height}
	if len(pos) == 1 {
		req.Target = pos[0]
	}
	frame, captureErr := a.captureConsole(ctx, direct, stateDir, req)
	if captureErr != nil {
		return consoleError(captureErr, direct, stderr, "console screenshot")
	}
	if _, err = file.Write(frame.Data); err != nil {
		fmt.Fprintln(stderr, "amc console screenshot: failed to save PNG")
		return ExitMalformedProvider
	}
	if err = file.Close(); err != nil {
		return ExitMalformedProvider
	}
	saved = true
	frame.Data = nil
	if *jsonOutput {
		if err = writeJSON(stdout, struct {
			SchemaVersion string              `json:"schema_version"`
			Output        string              `json:"output"`
			Frame         domain.ConsoleFrame `json:"frame"`
		}{SchemaVersion, *output, frame}); err != nil {
			return ExitMalformedProvider
		}
	} else {
		fmt.Fprintf(stdout, "PNG saved to %s (%dx%d), frame %s\n", *output, frame.Width, frame.Height, frame.FrameID)
	}
	return ExitSuccess
}

func (a *App) captureConsole(ctx context.Context, direct bool, stateDir string, req app.ConsoleScreenshotRequest) (domain.ConsoleFrame, error) {
	if direct {
		if a.consoleService == nil {
			return domain.ConsoleFrame{}, client.ErrDaemonUnavailable
		}
		return a.consoleService.Screenshot(ctx, a.actor, req)
	}
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		return domain.ConsoleFrame{}, err
	}
	return cl.ConsoleScreenshot(ctx, req)
}

func consoleError(err error, direct bool, stderr io.Writer, action string) int {
	if direct {
		return mapMutationError(err, stderr, action)
	}
	return mapClientError(err, stderr, action)
}

func reserveConsoleOutput(ctx context.Context, path string) (*os.File, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	security := target.NewPrivatePathSecurity()
	if err := security.ValidateDir(ctx, filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if err := security.ProtectNewFile(ctx, path); err != nil {
		removeReservedConsoleOutput(file, path)
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func removeReservedConsoleOutput(file *os.File, path string) {
	owned, err := file.Stat()
	_ = file.Close()
	if err != nil {
		return
	}
	current, err := os.Lstat(path)
	if err == nil && os.SameFile(owned, current) {
		_ = os.Remove(path)
	}
}
