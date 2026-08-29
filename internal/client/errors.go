package client

import "errors"

var (
	// ErrDaemonUnavailable indicates the daemon endpoint is unreachable or daemon is not running.
	ErrDaemonUnavailable = errors.New("client: daemon is unavailable or not running")

	// ErrNotFound indicates the requested resource was not found.
	ErrNotFound = errors.New("client: resource not found")

	// ErrDenied indicates access was denied by authentication or policy.
	ErrDenied = errors.New("client: access denied")

	// ErrConflict indicates an idempotency or state conflict.
	ErrConflict = errors.New("client: conflict with existing operation or state")

	// ErrTimeout indicates the client request or operation wait timed out.
	ErrTimeout = errors.New("client: operation timed out")

	// ErrMalformedResponse indicates the server returned an unparseable or invalid payload.
	ErrMalformedResponse = errors.New("client: malformed server response")

	// ErrInvalidArgument indicates bad request arguments.
	ErrInvalidArgument = errors.New("client: invalid argument")
)

// APIError wraps a structured error message and category from the daemon.
type APIError struct {
	StatusCode int
	Category   string
	Message    string
}

func (e *APIError) Error() string {
	return e.Message
}
