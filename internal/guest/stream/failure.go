package stream

import (
	"errors"
	"fmt"
)

var ErrWorkerFailure = errors.New("guest stream: worker failed")

// WorkerFailure contains only a fixed stage and a native numeric error. It never
// carries exception text, a guest path, or a security descriptor.
type WorkerFailure struct {
	Stage       string `json:"stage"`
	NativeError uint32 `json:"nativeError"`
}

func (f *WorkerFailure) Error() string {
	if !f.Valid() {
		return ErrProtocol.Error()
	}
	return fmt.Sprintf("guest stream: worker failed at %s (native error %d)", f.Stage, f.NativeError)
}

func (*WorkerFailure) Unwrap() error { return ErrWorkerFailure }

// Valid allows trusted receipt adapters to reject diagnostics forged by an
// injected transport before recording the fixed stage and numeric error.
func (f *WorkerFailure) Valid() bool { return f != nil && validFailureStage(f.Stage) }

func validFailureStage(stage string) bool {
	switch stage {
	case "runspace_open", "request", "observe", "put", "get", "exec", "remove_owned",
		"security_token", "security_lookup", "security_enable", "security_restore",
		"ancestor_open", "workspace_open", "file_open", "metadata_read", "file_id_read",
		"security_size", "security_read", "security_parse", "file_commit", "file_delete":
		return true
	default:
		return false
	}
}

func validFailureResult(result Result) bool {
	return result.ControlFailure == nil && result.Failure.Valid() &&
		result.Status == "failed" && result.ExitCode == 1 && result.Size == 0 &&
		result.SHA256 == emptyDigest && result.Root == nil && len(result.Files) == 0 && len(result.Removed) == 0
}

const emptyDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func invalidFailureHeader(header Result) bool {
	return (header.Failure != nil || header.Status == "failed") && !validFailureResult(header)
}

// A failure is evidence only when both complete frames agree. An ordinary
// response cannot turn into an unannounced failure after payload publication.
func verifyWorkerFailure(header, result Result) error {
	if header.Failure != nil {
		if !validFailureResult(result) || *header.Failure != *result.Failure {
			return ErrProtocol
		}
		return result.Failure
	}
	if result.ControlFailure != nil || result.Failure != nil || result.Status == "failed" {
		return ErrProtocol
	}
	return nil
}
