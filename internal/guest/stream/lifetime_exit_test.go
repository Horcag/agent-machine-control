package stream

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type exitChannel struct {
	guestssh.Duplex
	code  int
	known bool
	block bool
	calls atomic.Int64
}

func (c *exitChannel) ExitStatus(ctx context.Context) (int, bool) {
	c.calls.Add(1)
	if c.block {
		<-ctx.Done()
		return 0, false
	}
	return c.code, c.known
}

type exitOpener struct {
	fixture *fixtureOpener
	channel *exitChannel
}

func (o *exitOpener) OpenDuplex(ctx context.Context, target domain.MachineRef, command string) (guestssh.Duplex, error) {
	channel, err := o.fixture.OpenDuplex(ctx, target, command)
	o.channel.Duplex = channel
	return o.channel, err
}

func TestLifetimeExitOnlyKnownEOFAndNoReplay(t *testing.T) {
	for _, tc := range []struct {
		name  string
		code  int
		known bool
		want  error
	}{
		{"query", 211, true, ErrLifetimeQuery}, {"information", 212, true, ErrLifetimeInformation}, {"disconnected", 213, true, ErrLifetimeDisconnected},
		{"pending", 214, true, ErrLifetimePending}, {"unknown", 215, true, nil}, {"missing", 211, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &fixtureOpener{handler: func(_ net.Conn, _ Request) {}}
			channel := &exitChannel{code: tc.code, known: tc.known}
			opener := &exitOpener{fixture: fixture, channel: channel}
			client := NewClient(opener)
			t.Cleanup(func() { _ = client.Close(); fixture.wg.Wait() })
			if _, err := client.Prepare(t.Context(), "target", "actor", "g"); err != nil {
				t.Fatal(err)
			}
			req := Request{ID: "observe-exit", Operation: "observe", Path: `C:\synthetic`, DeadlineUTC: time.Now().Add(10 * time.Second)}
			_, err := client.Execute(t.Context(), "target", "actor", "g", req, nil, nil)
			if !errors.Is(err, io.EOF) || (tc.want != nil && !errors.Is(err, tc.want)) || channel.calls.Load() != 1 {
				t.Fatal("exit marker", err)
			}
			if tc.want == nil && (errors.Is(err, ErrLifetimeQuery) || errors.Is(err, ErrLifetimeInformation) || errors.Is(err, ErrLifetimeDisconnected) || errors.Is(err, ErrLifetimePending)) {
				t.Fatal("unknown exit adopted", err)
			}
			if _, err = client.Execute(t.Context(), "target", "actor", "g", req, nil, nil); !errors.Is(err, ErrRequest) || fixture.count.Load() != 1 || fixture.requests.Load() != 1 {
				t.Fatal("uncertain worker replayed", err)
			}
		})
	}
}

func TestLifetimeExitTimeoutCancellationAndProtocol(t *testing.T) {
	channel := &exitChannel{block: true}
	started := time.Now()
	if err := observeLifetimeExit(t.Context(), channel, io.ErrUnexpectedEOF); err != io.ErrUnexpectedEOF || time.Since(started) > 2*time.Second {
		t.Fatal("unbounded or changed EOF", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	channel.block = false
	channel.code = 211
	channel.known = true
	if err := observeLifetimeExit(ctx, channel, io.EOF); err != io.EOF {
		t.Fatal("cancelled observation", err)
	}
	before := channel.calls.Load()
	if err := observeLifetimeExit(t.Context(), channel, ErrProtocol); err != ErrProtocol || channel.calls.Load() != before {
		t.Fatal("non EOF adopted exit", err)
	}
	if err := observeLifetimeExit(t.Context(), fixtureChannel{}, io.EOF); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func TestLifetimeExitCannotReplaceForeignWorkerProtocolFailure(t *testing.T) {
	fixture := &fixtureOpener{handler: func(conn net.Conn, req Request) {
		_ = writeControl(conn, Result{ID: req.ID, Worker: WorkerIdentity{PID: 999}, Status: "ok"})
	}}
	channel := &exitChannel{code: 214, known: true}
	client := NewClient(&exitOpener{fixture: fixture, channel: channel})
	t.Cleanup(func() { _ = client.Close(); fixture.wg.Wait() })
	if _, err := client.Prepare(t.Context(), "target", "actor", "g"); err != nil {
		t.Fatal(err)
	}
	req := Request{ID: "identity", Operation: "observe", Path: `C:\synthetic`, DeadlineUTC: time.Now().Add(time.Second)}
	if _, err := client.Execute(t.Context(), "target", "actor", "g", req, nil, nil); !errors.Is(err, ErrProtocol) || channel.calls.Load() != 0 || errors.Is(err, ErrLifetimePending) {
		t.Fatal("exit replaced original identity failure", err)
	}
}
