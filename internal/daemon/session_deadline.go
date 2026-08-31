package daemon

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidSessionDeadline indicates a missing or non-canonical approval-bound deadline.
var ErrInvalidSessionDeadline = errors.New("daemon: invalid session deadline")

// ResolveSessionDeadline parses the exact canonical UTC deadline required by approval references.
func ResolveSessionDeadline(approvalID, raw string) (time.Time, error) {
	if raw == "" {
		if approvalID != "" {
			return time.Time{}, fmt.Errorf("%w: approval_id requires deadline", ErrInvalidSessionDeadline)
		}
		return time.Time{}, nil
	}
	deadline, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || deadline.UTC().Format(time.RFC3339Nano) != raw {
		return time.Time{}, fmt.Errorf("%w: deadline must be canonical RFC3339Nano UTC", ErrInvalidSessionDeadline)
	}
	return deadline.UTC(), nil
}
