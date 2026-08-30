package daemoncli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestShutdownFailureExitsNonZeroWithSanitizedMessage(t *testing.T) {
	var stderr bytes.Buffer
	code := reportShutdownFailure(&stderr, errors.New("sensitive synthetic detail"))
	if code == ExitSuccess {
		t.Fatal("shutdown failure returned success")
	}
	if got := stderr.String(); !strings.Contains(got, "shutdown failed") || strings.Contains(got, "sensitive synthetic detail") {
		t.Fatalf("shutdown message = %q, want sanitized operator-facing failure", got)
	}
}

type retryingShutdownStub struct {
	errors []error
	calls  int
}

func (s *retryingShutdownStub) Shutdown(context.Context) error {
	index := s.calls
	s.calls++
	if index >= len(s.errors) {
		return nil
	}
	return s.errors[index]
}

func TestShutdownUntilDrainedRetriesIncompleteOwnership(t *testing.T) {
	originalDelay := shutdownRetryDelay
	originalTimeout := shutdownAttemptTimeout
	shutdownRetryDelay = time.Millisecond
	shutdownAttemptTimeout = time.Second
	t.Cleanup(func() {
		shutdownRetryDelay = originalDelay
		shutdownAttemptTimeout = originalTimeout
	})
	stub := &retryingShutdownStub{errors: []error{
		errors.Join(daemon.ErrShutdownIncomplete, context.DeadlineExceeded),
		nil,
	}}
	var stderr bytes.Buffer
	if code := shutdownUntilDrained(stub, &stderr); code != ExitSuccess {
		t.Fatalf("exit code = %d, want success", code)
	}
	if stub.calls != 2 {
		t.Fatalf("shutdown calls = %d, want two", stub.calls)
	}
	if got := stderr.String(); !strings.Contains(got, "ownership retained") || !strings.Contains(got, "cleanup retries") {
		t.Fatalf("retry message = %q", got)
	}
}

func TestShutdownUntilDrainedDoesNotRetryPostDrainFailure(t *testing.T) {
	stub := &retryingShutdownStub{errors: []error{errors.New("synthetic finalization failure")}}
	var stderr bytes.Buffer
	if code := shutdownUntilDrained(stub, &stderr); code == ExitSuccess {
		t.Fatal("post-drain failure returned success")
	}
	if stub.calls != 1 {
		t.Fatalf("shutdown calls = %d, want one", stub.calls)
	}
	if got := stderr.String(); strings.Contains(got, "ownership retained") {
		t.Fatalf("post-drain message falsely claims retained ownership: %q", got)
	}
}
