package ssh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	gossh "golang.org/x/crypto/ssh"
)

var ErrCommandFailed = errors.New("ssh: command failed")
var ErrCommandOutputLimit = errors.New("ssh: command output exceeded limit")

// RunCommand uses enrolled credentials and host-key pinning without a PTY.
// Output and remote errors may be sensitive; only bounded stdout is returned.
func (t *NativeTransport) RunCommand(ctx context.Context, target domain.MachineRef, command string, stdin []byte, maxOutput int) ([]byte, error) {
	if command == "" || len(command) > 32768 || len(stdin) > 512*1024 || maxOutput < 1 || maxOutput > 1024*1024 {
		return nil, ErrCommandFailed
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	dc, err := t.resolveDialConfig(ctx, target, 0, 0, "")
	if err != nil {
		return nil, err
	}
	dial := t.dialContext
	if dial == nil {
		dialer := &net.Dialer{}
		dial = dialer.DialContext
	}
	conn, err := dial(ctx, "tcp", dc.endpoint)
	if err != nil {
		return nil, commandError(ctx)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	sshConn, channels, requests, err := gossh.NewClientConn(conn, dc.endpoint, dc.config)
	if err != nil {
		return nil, commandError(ctx)
	}
	client := gossh.NewClient(sshConn, channels, requests)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return nil, commandError(ctx)
	}
	defer session.Close()
	output := &boundedCommandOutput{limit: maxOutput, overflow: func() { _ = conn.Close() }}
	session.Stdin = bytes.NewReader(stdin)
	session.Stdout = output
	// Stderr is bounded and discarded, never included in provider errors.
	session.Stderr = &boundedCommandOutput{limit: 16 * 1024, overflow: func() { _ = conn.Close() }}
	err = session.Run(command)
	if output.exceeded() {
		return nil, ErrCommandOutputLimit
	}
	if err != nil {
		return nil, commandError(ctx)
	}
	return output.bytes(), nil
}

func commandError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrCommandFailed
}

type boundedCommandOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	full     bool
	overflow func()
}

func (w *boundedCommandOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.full || len(p) > w.limit-w.buffer.Len() {
		w.full = true
		w.overflow()
		return 0, io.ErrShortWrite
	}
	return w.buffer.Write(p)
}

func (w *boundedCommandOutput) exceeded() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.full }
func (w *boundedCommandOutput) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.buffer.Bytes())
}
