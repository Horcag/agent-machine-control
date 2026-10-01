package hyperv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAdapterPreservesExecutorDeadlineWithoutCallerDeadline(t *testing.T) {
	executor := &testMockExecutor{executeFn: func(context.Context, string, []string, []string) ([]byte, []byte, error) {
		return nil, nil, fmt.Errorf("%w: %w", ErrCommandTimeout, context.DeadlineExceeded)
	}}
	adapter := New(WithExecutor(executor))
	for name, operation := range map[string]func() error{
		"list": func() error { _, err := adapter.ListMachines(context.Background()); return err },
		"inspect": func() error {
			_, err := adapter.InspectMachine(context.Background(), "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa")
			return err
		},
		"checkpoints": func() error {
			_, err := adapter.ListCheckpoints(context.Background(), "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := operation()
			if !errors.Is(err, ErrCommandTimeout) || !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "%!w") {
				t.Fatalf("lost executor deadline cause: %v", err)
			}
		})
	}
}

func TestExecutorCancellationBoundsInheritedOutputPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX PID semantics required")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell required for inherited pipe integration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	stdout, _, err := (&DefaultExecutor{}).Execute(ctx, shell, []string{"-c", "sleep 30 & child=$!; echo $child; wait $child"}, nil)
	// The descendant belongs to this test: use only its recorded PID for cleanup.
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(stdout)))
	if parseErr == nil {
		process, findErr := os.FindProcess(pid)
		if findErr == nil {
			_ = process.Kill()
		}
	}
	if parseErr != nil {
		t.Fatalf("missing owned descendant PID: %q", stdout)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrCommandTimeout) {
		t.Fatalf("timeout error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("inherited pipe defeated deadline: %v", elapsed)
	}
}
