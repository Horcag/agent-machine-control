package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type startupChannel struct {
	io.Reader
	writeErr error
	code     int
	known    bool
	closed   bool
	cancel   context.CancelFunc
}

func (c *startupChannel) Write(p []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return len(p), nil
}
func (c *startupChannel) Close() error { c.closed = true; return nil }
func (c *startupChannel) Wait() error  { panic("startup must not wait for an unbounded remote exit") }
func (c *startupChannel) ExitStatus(context.Context) (int, bool) {
	if c.cancel != nil {
		c.cancel()
	}
	return c.code, c.known
}

type startupOpener struct {
	channel guestssh.Duplex
	err     error
	calls   int
}

func (o *startupOpener) OpenDuplex(context.Context, domain.MachineRef, string) (guestssh.Duplex, error) {
	o.calls++
	return o.channel, o.err
}

func TestStartupFailuresAreSanitizedAndNeverReplayOrAdmitIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, stage, category string
		openErr, writeErr     error
		hello                 []byte
		code                  int
		known                 bool
	}{
		{name: "open", stage: "open", category: "transport", openErr: errors.New("private credentials and path")},
		{name: "source", stage: "source_write", category: "closed_pipe", writeErr: io.ErrClosedPipe, code: 1, known: true},
		{name: "hello_exit", stage: "hello", category: "eof", code: 214, known: true},
		{name: "hello_unknown_exit", stage: "hello", category: "eof"},
		{name: "malformed_hello", stage: "hello", category: "protocol", hello: []byte{0, 0, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &startupChannel{Reader: bytes.NewReader(tc.hello), writeErr: tc.writeErr, code: tc.code, known: tc.known}
			opener := &startupOpener{channel: channel, err: tc.openErr}
			client := NewClient(opener)
			defer client.Close()
			identity, err := client.Prepare(t.Context(), "synthetic", "actor", "generation")
			failure, ok := errors.AsType[*StartupFailure](err)
			if !ok || !failure.Valid() || failure.Stage != tc.stage || failure.Category != tc.category || identity != (WorkerIdentity{}) || opener.calls != 1 || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe startup: identity=%+v err=%v calls=%d", identity, err, opener.calls)
			}
			if failure.ExitObserved != tc.known || failure.ExitCode != tc.code || channel.closed != (tc.openErr == nil) {
				t.Fatalf("exit/owned close lost: %+v closed=%v", failure, channel.closed)
			}
			if _, err := client.Execute(t.Context(), "synthetic", "actor", "generation", request(), nil, nil); !errors.Is(err, ErrRequest) || opener.calls != 1 {
				t.Fatalf("failed startup adopted/replayed: %v", err)
			}
		})
	}
}

type cancelledStartupChannel struct {
	started, closed chan struct{}
	once            sync.Once
}

func (c *cancelledStartupChannel) Write(p []byte) (int, error) { return len(p), nil }
func (c *cancelledStartupChannel) Read([]byte) (int, error) {
	close(c.started)
	<-c.closed
	return 0, io.EOF
}
func (c *cancelledStartupChannel) Close() error { c.once.Do(func() { close(c.closed) }); return nil }
func (c *cancelledStartupChannel) Wait() error  { panic("unbounded wait") }

func TestStartupHelloCancellationKeepsCategoryAndClosesOwnedChannel(t *testing.T) {
	channel := &cancelledStartupChannel{started: make(chan struct{}), closed: make(chan struct{})}
	client := NewClient(&startupOpener{channel: channel})
	defer client.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := client.Prepare(ctx, "synthetic", "actor", "generation"); done <- err }()
	<-channel.started
	cancel()
	err := <-done
	failure, ok := errors.AsType[*StartupFailure](err)
	if !ok || failure.Stage != "hello" || failure.Category != "cancelled" || failure.ExitObserved || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation diagnostic lost", err)
	}
}

func TestStartupFailureRejectsUnsafeFieldsAndInvalidRemoteStatus(t *testing.T) {
	for _, failure := range []*StartupFailure{
		nil, {Stage: "private", Category: "eof"}, {Stage: "hello", Category: "private"},
		{Stage: "hello", Category: "eof", OpenMillis: -1}, {Stage: "hello", Category: "eof", ExitObserved: true, ExitCode: -1},
	} {
		if failure.Valid() {
			t.Fatal("unsafe startup accepted", failure)
		}
	}
	channel := &startupChannel{code: -1, known: true}
	failure := startupFailure(t.Context(), channel, StartupFailure{Stage: "hello"}, io.EOF).(*StartupFailure)
	if failure.ExitObserved || !failure.Valid() || !errors.Is(failure, io.EOF) {
		t.Fatal(failure)
	}
}

func TestStartupCancellationDuringExitObservationTakesPrecedence(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	channel := &startupChannel{code: 1, known: true, cancel: cancel}
	err := startupFailure(ctx, channel, StartupFailure{Stage: "hello"}, io.EOF)
	failure := err.(*StartupFailure)
	if failure.Category != "cancelled" || failure.ExitObserved || !errors.Is(err, context.Canceled) {
		t.Fatal("exit observation hid cancellation", err)
	}
}

type observedStartupChannel struct {
	*startupChannel
	stage string
}

func (c *observedStartupChannel) StartupStage() string { return c.stage }

func TestStartupFailureRemoteStageIsClosedObservationOnly(t *testing.T) {
	for _, stage := range []string{"before_compile", "private/path/data"} {
		channel := &observedStartupChannel{startupChannel: &startupChannel{Reader: bytes.NewReader(nil)}, stage: stage}
		opener := &startupOpener{channel: channel}
		client := NewClient(opener)
		identity, err := client.Prepare(t.Context(), "synthetic", "actor", "generation")
		_ = client.Close()
		failure, ok := errors.AsType[*StartupFailure](err)
		if !ok || !failure.Valid() || identity != (WorkerIdentity{}) || opener.calls != 1 {
			t.Fatal(identity, err)
		}
		if stage == "before_compile" {
			if failure.RemoteStage != stage || !strings.Contains(failure.Error(), ":remote_stage=before_compile") {
				t.Fatal(failure)
			}
		} else if failure.RemoteStage != "" || strings.Contains(failure.Error(), "private") {
			t.Fatal("unsafe remote stage retained", failure)
		}
	}
	invalid := &StartupFailure{Stage: "hello", Category: "deadline", RemoteStage: "private/path/data"}
	if invalid.Valid() || strings.Contains(invalid.Error(), "private") || invalid.Unwrap() != nil {
		t.Fatal("invalid field escaped", invalid)
	}
}
