package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func (a *App) runConsoleRecord(ctx context.Context, direct bool, stateDir string, args []string, stdout, stderr io.Writer) int {
	pos, flags := consoleArgs(args)
	fs := flag.NewFlagSet("console record", flag.ContinueOnError)
	fs.SetOutput(stderr)
	recordingID := fs.String("recording-id", "", "fresh 32 lowercase hex recording ID")
	output := fs.String("output", "", "new protected GIF path")
	width := fs.Int("width", 640, "frame width")
	height := fs.Int("height", 480, "frame height")
	frames := fs.Int("frames", 10, "2-30 frames")
	interval := fs.Int("interval-ms", 500, "frame interval (100-2000ms)")
	_ = fs.Bool("json", false, "emit metadata")
	if fs.Parse(flags) != nil || len(pos) > 1 || *output == "" {
		return ExitUsage
	}
	f, err := reserveConsoleOutput(ctx, *output)
	if err != nil {
		return ExitConflict
	}
	saved := false
	defer func() {
		if !saved {
			removeReservedConsoleOutput(f, *output)
		}
		_ = f.Close()
	}()
	req := app.ConsoleRecordRequest{RecordingID: *recordingID, Width: *width, Height: *height, Frames: *frames, IntervalMillis: *interval}
	if len(pos) == 1 {
		req.Target = pos[0]
	}
	var out app.ConsoleRecording
	if direct {
		service, ok := a.consoleService.(interface {
			Record(context.Context, domain.ActorContext, app.ConsoleRecordRequest) (app.ConsoleRecording, error)
		})
		if !ok {
			return ExitBackendUnavailable
		}
		out, err = service.Record(ctx, a.actor, req)
	} else {
		cl, discoverErr := client.Discover(stateDir, client.TokenTypeOperator)
		if discoverErr != nil {
			return mapClientError(discoverErr, stderr, "console record")
		}
		out, err = cl.ConsoleRecord(ctx, req)
	}
	if err != nil {
		return consoleError(err, direct, stderr, "console record")
	}
	if _, err = f.Write(out.Data); err != nil {
		return ExitMalformedProvider
	}
	if f.Close() != nil {
		return ExitMalformedProvider
	}
	saved = true
	out.Data = nil
	if writeJSON(stdout, out) != nil {
		fmt.Fprintln(stderr, "amc console: output failed")
		return ExitMalformedProvider
	}
	return ExitSuccess
}

func (a *App) runConsoleRecordStatus(ctx context.Context, direct bool, stateDir string, args []string, stdout, stderr io.Writer) int {
	pos, flags := consoleArgs(args)
	fs := flag.NewFlagSet("console record-status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("recording-id", "", "recording ID")
	_ = fs.Bool("json", false, "emit metadata")
	if fs.Parse(flags) != nil || len(pos) > 1 || *id == "" {
		return ExitUsage
	}
	req := app.ConsoleRecordStatusRequest{RecordingID: *id}
	if len(pos) == 1 {
		req.Target = pos[0]
	}
	var out app.ConsoleRecordStatus
	var err error
	if direct {
		service, ok := a.consoleService.(interface {
			RecordStatus(context.Context, domain.ActorContext, app.ConsoleRecordStatusRequest) (app.ConsoleRecordStatus, error)
		})
		if !ok {
			return ExitBackendUnavailable
		}
		out, err = service.RecordStatus(ctx, a.actor, req)
	} else {
		cl, discoverErr := client.Discover(stateDir, client.TokenTypeOperator)
		if discoverErr != nil {
			return mapClientError(discoverErr, stderr, "console record-status")
		}
		out, err = cl.ConsoleRecordStatus(ctx, req)
	}
	if err != nil {
		return consoleError(err, direct, stderr, "console record-status")
	}
	if writeJSON(stdout, out) != nil {
		return ExitMalformedProvider
	}
	return ExitSuccess
}
