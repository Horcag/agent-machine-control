package sessions

import (
	"context"
	"errors"
	"io"
	"time"

	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

// OpenFailure records transport-effect truth when session publication fails after channel creation.
type OpenFailure struct {
	Cause           error
	ChannelCreated  bool
	CleanupComplete bool
	CleanupErr      error
}

func (e *OpenFailure) Error() string {
	return errors.Join(e.Cause, e.CleanupErr).Error()
}

// Unwrap preserves both the publication failure and cleanup outcome for callers.
func (e *OpenFailure) Unwrap() []error {
	return []error{e.Cause, e.CleanupErr}
}

func cleanupFailedOpen(parent context.Context, channel guestssh.Channel, cause error) error {
	cleanupCtx, cancel := openCleanupContext(parent)
	defer cancel()
	complete, cleanupErr := closeChannel(cleanupCtx, channel)
	if cleanupErr == io.EOF {
		cleanupErr = nil
	}
	return &OpenFailure{
		Cause:           cause,
		ChannelCreated:  true,
		CleanupComplete: complete,
		CleanupErr:      cleanupErr,
	}
}

func openCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := sessionCleanupTimeout
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.WithDeadline(context.Background(), deadline)
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	return context.WithTimeout(context.Background(), timeout)
}
