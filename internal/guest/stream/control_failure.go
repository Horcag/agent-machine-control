package stream

import (
	"errors"
	"io"
	"reflect"
)

var ErrControlFailure = errors.New("guest stream: control input failed")

// ControlFailure is bound to a complete fatal pair and the original hello,
// before a request ID was decoded. It is never a business-operation result.
type ControlFailure struct {
	Stage string `json:"stage"`
}

func (f *ControlFailure) Valid() bool {
	if f == nil {
		return false
	}
	switch f.Stage {
	case "control_eof", "control_read", "control_decode", "control_header":
		return true
	}
	return false
}
func (f *ControlFailure) Error() string {
	if !f.Valid() {
		return ErrProtocol.Error()
	}
	return "guest stream: fatal " + f.Stage
}
func (*ControlFailure) Unwrap() error { return ErrControlFailure }

func validControlFailure(result Result, original WorkerIdentity) bool {
	return result.ControlFailure.Valid() && result.ID == "" && result.Worker == original &&
		result.Status == "failed" && result.ExitCode == 1 && result.Size == 0 && result.SHA256 == emptyDigest &&
		result.Root == nil && len(result.Files) == 0 && len(result.Removed) == 0 && result.Failure == nil
}
func receiveControlFailure(ch io.Reader, header Result, original WorkerIdentity) (Result, error) {
	if !validControlFailure(header, original) {
		return Result{}, ErrProtocol
	}
	var completion Result
	if err := readControl(ch, &completion); err != nil {
		return Result{}, err
	}
	if !validControlFailure(completion, original) || !reflect.DeepEqual(header, completion) {
		return Result{}, ErrProtocol
	}
	return Result{}, &ControlFailure{Stage: completion.ControlFailure.Stage}
}
