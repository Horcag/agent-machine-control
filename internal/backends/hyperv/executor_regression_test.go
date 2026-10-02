package hyperv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Linux or macOS process identity is required for safe descendant cleanup")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell required for inherited pipe integration")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	marker := t.TempDir() + "/descendant.pid"
	markerTemp := marker + ".tmp"
	command := fmt.Sprintf("sleep 30 & child=$!; if ! printf '%%s %%s\\n' \"$$\" \"$child\" > %q || ! mv %q %q; then kill \"$child\"; wait \"$child\"; exit 1; fi; wait \"$child\"", markerTemp, markerTemp, marker)
	done := make(chan executorResult, 1)
	go func() {
		stdout, stderr, err := (&DefaultExecutor{}).Execute(ctx, shell, []string{"-c", command}, nil)
		done <- executorResult{stdout: stdout, stderr: stderr, err: err}
	}()

	var descendant *processIdentity
	var shellPID, childPID int
	t.Cleanup(func() {
		cancel()
		if descendant == nil {
			return
		}
		if err := killOwnedProcess(*descendant); err != nil {
			t.Errorf("clean up owned descendant pid %d: %v", descendant.pid, err)
		}
	})

	deadline := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	shellPID, childPID = waitForDescendantMarker(t, marker, done, ticker.C, deadline.C)

	parentIdentity, err := readProcessIdentity(shellPID)
	if err != nil {
		t.Fatalf("read test-owned shell identity pid %d: %v", shellPID, err)
	}
	descendant = waitForReadyDescendant(t, childPID, parentIdentity.pid, done, ticker.C, deadline.C, &descendant)

	started := time.Now()
	cancel()
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) || !errors.Is(result.err, ErrCommandTimeout) {
			t.Fatalf("cancellation error = %v, want context.Canceled and ErrCommandTimeout", result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inherited output pipe prevented executor return within 3s of cancellation")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("inherited pipe defeated cancellation bound: %v", elapsed)
	}
	t.Logf("executor returned after cancellation; test-owned descendant pid=%d start=%s", descendant.pid, descendant.startIdentity)
}

type executorResult struct {
	stdout []byte
	stderr []byte
	err    error
}

func waitForDescendantMarker(t *testing.T, marker string, done <-chan executorResult, tick <-chan time.Time, deadline <-chan time.Time) (int, int) {
	t.Helper()
	for {
		contents, err := os.ReadFile(marker)
		if err == nil {
			var shellPID, childPID int
			if _, err := fmt.Sscanf(string(contents), "%d %d", &shellPID, &childPID); err != nil {
				t.Fatalf("invalid descendant readiness marker %q: %v", contents, err)
			}
			return shellPID, childPID
		}
		select {
		case result := <-done:
			t.Fatalf("executor returned before descendant readiness: stdout=%q stderr=%q err=%v", result.stdout, result.stderr, result.err)
		case <-tick:
		case <-deadline:
			t.Fatal("descendant did not publish readiness within 5s")
		}
	}
}

func waitForReadyDescendant(t *testing.T, pid, parentPID int, done <-chan executorResult, tick <-chan time.Time, deadline <-chan time.Time, owned **processIdentity) *processIdentity {
	t.Helper()
	for {
		identity, err := readProcessIdentity(pid)
		if err == nil && identity.parentPID == parentPID {
			*owned = &identity
			if filepath.Base(identity.command) == "sleep" {
				return *owned
			}
		}
		select {
		case result := <-done:
			t.Fatalf("executor returned before descendant became ready: stdout=%q stderr=%q err=%v", result.stdout, result.stderr, result.err)
		case <-tick:
		case <-deadline:
			t.Fatalf("test-owned descendant pid %d did not become ready: identity=%+v err=%v", pid, identity, err)
		}
	}
}

type processIdentity struct {
	pid           int
	parentPID     int
	startIdentity string
	command       string
	executable    string
}

func readProcessIdentity(pid int) (processIdentity, error) {
	if runtime.GOOS == "linux" {
		return readLinuxProcessIdentity(pid)
	}
	return readPSProcessIdentity(pid)
}

func readLinuxProcessIdentity(pid int) (processIdentity, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return processIdentity{}, err
	}
	text := string(stat)
	openParen := strings.IndexByte(text, '(')
	closeParen := strings.LastIndexByte(text, ')')
	if openParen < 0 || closeParen <= openParen {
		return processIdentity{}, fmt.Errorf("malformed /proc stat")
	}
	fields := strings.Fields(text[closeParen+1:])
	if len(fields) <= 19 {
		return processIdentity{}, fmt.Errorf("short /proc stat")
	}
	parentPID, err := strconv.Atoi(fields[1])
	if err != nil {
		return processIdentity{}, fmt.Errorf("parse parent PID: %w", err)
	}
	executable, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return processIdentity{}, err
	}
	return processIdentity{
		pid:           pid,
		parentPID:     parentPID,
		startIdentity: fields[19],
		command:       text[openParen+1 : closeParen],
		executable:    executable,
	}, nil
}

func readPSProcessIdentity(pid int) (processIdentity, error) {
	// #nosec G204 -- pid comes from the test-owned marker as a positive integer.
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=", "-o", "lstart=", "-o", "comm=").Output()
	if err != nil {
		if len(output) == 0 {
			return processIdentity{}, os.ErrNotExist
		}
		return processIdentity{}, err
	}
	fields := strings.Fields(string(output))
	if len(fields) < 7 {
		return processIdentity{}, fmt.Errorf("malformed ps identity %q", output)
	}
	parentPID, err := strconv.Atoi(fields[0])
	if err != nil {
		return processIdentity{}, fmt.Errorf("parse parent PID: %w", err)
	}
	return processIdentity{
		pid:           pid,
		parentPID:     parentPID,
		startIdentity: strings.Join(fields[1:6], " "),
		command:       strings.Join(fields[6:], " "),
		executable:    strings.Join(fields[6:], " "),
	}, nil
}

func killOwnedProcess(identity processIdentity) error {
	current, err := readProcessIdentity(identity.pid)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	identityMatches := current.startIdentity == identity.startIdentity && current.command == identity.command && current.executable == identity.executable
	childExecMatches := current.startIdentity == identity.startIdentity && filepath.Base(current.command) == "sleep" && filepath.Base(current.executable) == "sleep"
	if !identityMatches && !childExecMatches {
		return fmt.Errorf("pid %d no longer matches recorded process identity; leaving it untouched", identity.pid)
	}
	process, err := os.FindProcess(identity.pid)
	if err != nil {
		return err
	}
	return process.Kill()
}
