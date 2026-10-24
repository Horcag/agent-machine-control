package stream

import (
	"context"
	"errors"
	"fmt"
	"io"

	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

// StartupFailure contains closed local stage/category, optional closed remote
// observation and local elapsed milliseconds. Remote markers and exits never
// establish worker identity or native absence.
// Open includes SSH connection setup and command start, an opaque opener boundary.
type StartupFailure struct {
	Stage, Category                            string
	RemoteStage                                string
	OpenMillis, SourceWriteMillis, HelloMillis int64
	ExitObserved                               bool
	ExitCode                                   int
}

func (f *StartupFailure) Valid() bool {
	if f == nil || f.OpenMillis < 0 || f.SourceWriteMillis < 0 || f.HelloMillis < 0 {
		return false
	}
	if f.Stage != "open" && f.Stage != "source_write" && f.Stage != "hello" {
		return false
	}
	if f.Category != "transport" && startupCategoryError(f.Category) == nil {
		return false
	}
	if f.RemoteStage != "" && !guestssh.ValidStartupStage(f.RemoteStage) {
		return false
	}
	return (!f.ExitObserved && f.ExitCode == 0) || (f.ExitObserved && (&RemoteExit{Code: f.ExitCode}).Valid())
}

func (f *StartupFailure) Error() string {
	if !f.Valid() {
		return "guest stream: invalid startup diagnostic"
	}
	text := fmt.Sprintf("guest-startup-failure:%s:%s:open_ms=%d:source_write_ms=%d:hello_ms=%d", f.Stage, f.Category, f.OpenMillis, f.SourceWriteMillis, f.HelloMillis)
	if f.ExitObserved {
		text += fmt.Sprintf(":exit=%d", f.ExitCode)
	}
	if f.RemoteStage != "" {
		text += ":remote_stage=" + f.RemoteStage
	}
	return text
}

func (f *StartupFailure) Unwrap() []error {
	if !f.Valid() {
		return nil
	}
	var result []error
	if cause := startupCategoryError(f.Category); cause != nil {
		result = append(result, cause)
	}
	if f.ExitObserved {
		result = append(result, &RemoteExit{Code: f.ExitCode})
	}
	return result
}

func startupCategoryError(category string) error {
	switch category {
	case "deadline":
		return context.DeadlineExceeded
	case "cancelled":
		return context.Canceled
	case "eof":
		return io.EOF
	case "unexpected_eof":
		return io.ErrUnexpectedEOF
	case "short_write":
		return io.ErrShortWrite
	case "closed_pipe":
		return io.ErrClosedPipe
	case "protocol":
		return ErrProtocol
	default:
		return nil
	}
}

func startupFailure(ctx context.Context, channel io.ReadWriteCloser, failure StartupFailure, err error) error {
	failure.Category = "transport"
	if observer, ok := channel.(interface{ StartupStage() string }); ok {
		if stage := observer.StartupStage(); guestssh.ValidStartupStage(stage) {
			failure.RemoteStage = stage
		}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) {
		if code, known := observeExitStatus(ctx, channel); known && (&RemoteExit{Code: code}).Valid() {
			failure.ExitObserved, failure.ExitCode = true, code
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	for _, category := range []string{"deadline", "cancelled", "eof", "unexpected_eof", "short_write", "closed_pipe", "protocol"} {
		if errors.Is(err, startupCategoryError(category)) {
			failure.Category = category
			break
		}
	}
	return &failure
}
