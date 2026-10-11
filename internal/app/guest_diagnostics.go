package app

import (
	"errors"
	"fmt"
	"io"

	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

// Copy only recognized diagnostics. The original error may contain guest paths,
// exception text or credentials even when it wraps a safe protocol sentinel.
func safeGuestTransportError(err error) error {
	var failure *stream.WorkerFailure
	if errors.As(err, &failure) && failure.Valid() {
		return errors.Join(ErrGuestUncertain, &stream.WorkerFailure{Stage: failure.Stage, NativeError: failure.NativeError})
	}
	for _, known := range guestTransportDiagnostics {
		if errors.Is(err, known.err) {
			return errors.Join(ErrGuestUncertain, known.err)
		}
	}
	return ErrGuestUncertain
}

var guestTransportDiagnostics = []struct {
	err      error
	category string
}{
	{stream.ErrProtocol, "protocol"}, {stream.ErrIntegrity, "integrity"},
	{io.ErrUnexpectedEOF, "unexpected_eof"}, {io.EOF, "eof"}, {io.ErrShortWrite, "short_write"},
}

type guestRootFailure struct{ field string }

func (f guestRootFailure) Error() string { return "app: invalid guest root" }
func (f guestRootFailure) Unwrap() error { return ErrGuestOwnership }

func verifyGuestRoot(actual *stream.RootIdentity, original stream.RootIdentity) error {
	switch {
	case actual == nil:
		return guestRootFailure{"missing"}
	case actual.Path != original.Path:
		return guestRootFailure{"path"}
	case actual.Volume != original.Volume:
		return guestRootFailure{"volume"}
	case actual.FileID != original.FileID:
		return guestRootFailure{"id"}
	case actual.CreationTime != original.CreationTime:
		return guestRootFailure{"creation"}
	case actual.Owner != original.Owner:
		return guestRootFailure{"owner"}
	case actual.SDDLHash != original.SDDLHash:
		return guestRootFailure{"sddl"}
	case *actual == original:
		return nil
	default:
		return guestProtocolError()
	}
}

func guestFailureEvidence(err error) string {
	var failure *stream.WorkerFailure
	if errors.As(err, &failure) && failure.Valid() {
		return fmt.Sprintf("guest-worker-failure:%s:%d", failure.Stage, failure.NativeError)
	}
	if root, ok := errors.AsType[guestRootFailure](err); ok {
		switch root.field {
		case "missing", "path", "volume", "id", "creation", "owner", "sddl":
			return "guest-result-failure:root_" + root.field
		}
	}
	for _, known := range guestTransportDiagnostics {
		if errors.Is(err, known.err) {
			return "guest-transport-failure:" + known.category
		}
	}
	if errors.Is(err, ErrGuestOwnership) {
		return "guest-result-failure:ownership"
	}
	return ""
}
