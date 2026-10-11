package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	gossh "golang.org/x/crypto/ssh"
)

// Duplex is a binary non-PTY session. It owns its SSH connection; closing it
// never changes another session's socket deadlines.
type Duplex interface {
	io.ReadWriteCloser
	Wait() error
}

type duplex struct {
	reader   io.Reader
	writer   io.WriteCloser
	session  *gossh.Session
	client   *gossh.Client
	once     sync.Once
	err      error
	exitDone chan struct{}
	waitErr  error
}

func (d *duplex) Read(p []byte) (int, error)  { return d.reader.Read(p) }
func (d *duplex) Write(p []byte) (int, error) { return d.writer.Write(p) }
func (d *duplex) Close() error {
	d.once.Do(func() { d.err = errors.Join(d.session.Close(), d.client.Close()) })
	return d.err
}
func (d *duplex) Wait() error { <-d.exitDone; return d.waitErr }

// ExitStatus observes only this channel's cached remote exit, never stderr text.
func (d *duplex) ExitStatus(ctx context.Context) (int, bool) {
	select {
	case <-ctx.Done():
		return 0, false
	case <-d.exitDone:
		if ctx.Err() != nil {
			return 0, false
		}
		if exited, ok := errors.AsType[*gossh.ExitError](d.waitErr); ok {
			return exited.ExitStatus(), true
		}
		if d.waitErr == nil {
			return 0, true
		}
		return 0, false
	}
}

// OpenDuplex resolves enrolled credentials and pinned host identity on each
// worker creation. The caller owns lifetime after startup and must close it.
func (t *NativeTransport) OpenDuplex(ctx context.Context, target domain.MachineRef, command string) (Duplex, error) {
	if command == "" || len(command) > 8192 {
		return nil, ErrCommandFailed
	}
	dc, err := t.resolveDialConfig(ctx, target, 0, 0, "")
	if err != nil {
		return nil, err
	}
	dial := t.dialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "tcp", dc.endpoint)
	if err != nil {
		return nil, commandError(ctx)
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stop := context.AfterFunc(handshakeCtx, func() { _ = conn.Close() })
	defer stop()
	sc, ch, rq, err := gossh.NewClientConn(conn, dc.endpoint, dc.config)
	if err != nil {
		_ = conn.Close()
		return nil, commandError(handshakeCtx)
	}
	client := gossh.NewClient(sc, ch, rq)
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, commandError(ctx)
	}
	failed := true
	defer func() {
		if failed {
			_ = session.Close()
			_ = client.Close()
		}
	}()
	stdin, err := session.StdinPipe()
	if err != nil {
		return nil, ErrCommandFailed
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return nil, ErrCommandFailed
	}
	// Draining stderr prevents SSH flow-control deadlock without retaining secrets.
	session.Stderr = io.Discard
	if err = session.Start(command); err != nil {
		return nil, commandError(ctx)
	}
	if err = handshakeCtx.Err(); err != nil {
		return nil, err
	}
	// Synchronize cancellation before transferring connection ownership.
	if !stop() {
		return nil, commandError(handshakeCtx)
	}
	failed = false
	d := &duplex{reader: stdout, writer: stdin, session: session, client: client, exitDone: make(chan struct{})}
	go func() { d.waitErr = session.Wait(); close(d.exitDone) }()
	return d, nil
}
