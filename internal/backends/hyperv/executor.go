package hyperv

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

const (
	// MaxStdoutBytes bounds captured stdout from PowerShell processes (4 MB).
	MaxStdoutBytes = 4 * 1024 * 1024

	// MaxStderrBytes bounds captured stderr from PowerShell processes (64 KB).
	MaxStderrBytes = 64 * 1024

	// DefaultCommandTimeout defines the fallback deadline when context has none.
	DefaultCommandTimeout = 15 * time.Second
)

// Executor defines the low-level process execution interface used by the Hyper-V adapter.
type Executor interface {
	LookPath(file string) (string, error)
	Execute(ctx context.Context, name string, args []string, env []string) (stdout []byte, stderr []byte, err error)
}

// DefaultExecutor executes commands using the operating system's process runner.
type DefaultExecutor struct{}

// LookPath searches for an executable in the system PATH.
func (e *DefaultExecutor) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

// Execute runs a process under the provided context with bounded output buffers.
func (e *DefaultExecutor) Execute(ctx context.Context, name string, args []string, env []string) ([]byte, []byte, error) {
	execCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		execCtx, cancel = context.WithTimeout(ctx, DefaultCommandTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(execCtx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	stdoutBuf := newBoundedBuffer(MaxStdoutBytes)
	stderrBuf := newBoundedBuffer(MaxStderrBytes)
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	runErr := cmd.Run()

	if stdoutBuf.exceeded || stderrBuf.exceeded {
		return stdoutBuf.Bytes(), stderrBuf.Bytes(), ErrOutputExceededLimit
	}
	if execCtx.Err() != nil {
		return stdoutBuf.Bytes(), stderrBuf.Bytes(), fmt.Errorf("%w: %w", ErrCommandTimeout, execCtx.Err())
	}

	return stdoutBuf.Bytes(), stderrBuf.Bytes(), runErr
}

type boundedBuffer struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (n int, err error) {
	if b.buf.Len()+len(p) > b.limit {
		b.exceeded = true
		remaining := b.limit - b.buf.Len()
		if remaining > 0 {
			b.buf.Write(p[:remaining])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *boundedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}
