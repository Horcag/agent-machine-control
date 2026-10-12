package stream

import (
	"bytes"
	"errors"
	"testing"
)

func TestFileOwnerFailureRequiresExactPairedMetadata(t *testing.T) {
	worker := WorkerIdentity{PID: 12, BirthTicks: 123, Image: "synthetic-worker", SID: "synthetic-user"}
	request := Request{ID: "owned-put", Operation: "put"}
	header := failureResult(request.ID, worker)
	header.Failure = &WorkerFailure{Stage: "file_owner_set", NativeError: 5}
	var output bytes.Buffer
	result, err := receiveResult(failureWire(header, header), request, &output, worker)
	var failure *WorkerFailure
	if !errors.As(err, &failure) || failure.Stage != "file_owner_set" || failure.NativeError != 5 || result.Failure == nil || output.Len() != 0 {
		t.Fatalf("owner refusal contract: result=%+v error=%v output=%d", result, err, output.Len())
	}
	completion := header
	completion.Failure = &WorkerFailure{Stage: "file_owner_set", NativeError: 1307}
	if _, err := receiveResult(failureWire(header, completion), request, &output, worker); !errors.Is(err, ErrProtocol) || output.Len() != 0 {
		t.Fatal("inconsistent owner refusal accepted", err)
	}
}
