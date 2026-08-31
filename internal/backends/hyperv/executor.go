package hyperv

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/wslruntime"
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
type DefaultExecutor struct {
	wslRuntime func() bool
}

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
		cmd.Env = commandEnvironment(os.Environ(), env, e.runningUnderWSL())
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

func (e *DefaultExecutor) runningUnderWSL() bool {
	if e.wslRuntime != nil {
		return e.wslRuntime()
	}
	return wslruntime.IsWSL()
}

func commandEnvironment(base, explicit []string, wslInterop bool) []string {
	env := mergeEnvironment(base, explicit)
	if !wslInterop {
		return env
	}

	forwardNames := explicitEnvironmentNames(explicit)
	if len(forwardNames) == 0 {
		return env
	}

	wslEnv := mergeWSLEnv(environmentEntryValue(env, "WSLENV"), forwardNames)
	return setEnvironmentEntry(env, "WSLENV", wslEnv)
}

func mergeEnvironment(base, explicit []string) []string {
	merged := append([]string(nil), base...)
	for _, entry := range explicit {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			merged = append(merged, entry)
			continue
		}
		merged = setEnvironmentRawEntry(merged, name, entry)
	}
	return merged
}

func explicitEnvironmentNames(entries []string) []string {
	names := make([]string, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "WSLENV" || !validEnvironmentName(name) {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func validEnvironmentName(name string) bool {
	if name == "" || !environmentNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !environmentNamePart(name[i]) {
			return false
		}
	}
	return true
}

func environmentNameStart(char byte) bool {
	return char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
}

func environmentNamePart(char byte) bool {
	return environmentNameStart(char) || char >= '0' && char <= '9'
}

func mergeWSLEnv(existing string, names []string) string {
	entries := make([]string, 0, len(names)+1)
	seen := make(map[string]struct{}, len(names)+1)
	for entry := range strings.SplitSeq(existing, ":") {
		if entry == "" {
			continue
		}
		entries = append(entries, entry)
		name, _, _ := strings.Cut(entry, "/")
		seen[name] = struct{}{}
	}
	for _, name := range names {
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, name)
	}
	return strings.Join(entries, ":")
}

func environmentEntryValue(env []string, name string) string {
	prefix := name + "="
	for _, entry := range slices.Backward(env) {
		if value, ok := strings.CutPrefix(entry, prefix); ok {
			return value
		}
	}
	return ""
}

func setEnvironmentEntry(env []string, name, value string) []string {
	return setEnvironmentRawEntry(env, name, name+"="+value)
}

func setEnvironmentRawEntry(env []string, name, replacement string) []string {
	prefix := name + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, replacement)
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
