package daemon

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidSessionTimeout indicates a malformed, conflicting, or unrepresentable timeout.
var ErrInvalidSessionTimeout = errors.New("daemon: invalid session timeout")

// ResolveSessionTimeout decodes the compatible seconds/milliseconds wire fields without ambiguity.
func ResolveSessionTimeout(seconds, millis int64, fallback time.Duration) (time.Duration, error) {
	if seconds < 0 || millis < 0 {
		return 0, fmt.Errorf("%w: timeout fields must not be negative", ErrInvalidSessionTimeout)
	}
	if seconds != 0 && millis != 0 {
		return 0, fmt.Errorf("%w: timeout_seconds and timeout_ms are mutually exclusive", ErrInvalidSessionTimeout)
	}
	if seconds > int64((time.Duration(1<<63-1))/time.Second) {
		return 0, fmt.Errorf("%w: timeout_seconds overflows time.Duration", ErrInvalidSessionTimeout)
	}
	if millis > int64((time.Duration(1<<63-1))/time.Millisecond) {
		return 0, fmt.Errorf("%w: timeout_ms overflows time.Duration", ErrInvalidSessionTimeout)
	}
	if seconds != 0 {
		return time.Duration(seconds) * time.Second, nil
	}
	if millis != 0 {
		return time.Duration(millis) * time.Millisecond, nil
	}
	return fallback, nil
}

// EncodeSessionTimeout preserves whole seconds for compatibility and uses milliseconds otherwise.
func EncodeSessionTimeout(timeout time.Duration) (seconds, millis int64, err error) {
	if timeout < 0 {
		return 0, 0, fmt.Errorf("%w: timeout must not be negative", ErrInvalidSessionTimeout)
	}
	if timeout == 0 {
		return 0, 0, nil
	}
	if timeout%time.Millisecond != 0 {
		return 0, 0, fmt.Errorf("%w: timeout must use millisecond precision", ErrInvalidSessionTimeout)
	}
	if timeout%time.Second == 0 {
		return int64(timeout / time.Second), 0, nil
	}
	return 0, int64(timeout / time.Millisecond), nil
}
