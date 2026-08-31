package daemoncli_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemoncli"
)

func TestDaemonCLI_HelpAndVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := daemoncli.Run([]string{"--version"}, &stdout, &stderr)
	if code != daemoncli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d", code)
	}

	stdout.Reset()
	stderr.Reset()
	code = daemoncli.Run([]string{"--help"}, &stdout, &stderr)
	if code != daemoncli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Usage: amcd") {
		t.Errorf("expected usage output, got: %s", stdout.String())
	}
}

func TestDaemonCLI_StatusStopped(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	var stdout, stderr bytes.Buffer

	// Human output
	code := daemoncli.Run([]string{"status", "--state-dir", dir}, &stdout, &stderr)
	if code != daemoncli.ExitBackendUnavailable {
		t.Fatalf("expected ExitBackendUnavailable (4), got %d", code)
	}
	if !strings.Contains(stdout.String(), "stopped") {
		t.Errorf("expected stopped message, got %s", stdout.String())
	}

	// JSON output
	stdout.Reset()
	stderr.Reset()
	code = daemoncli.Run([]string{"status", "--state-dir", dir, "--json"}, &stdout, &stderr)
	if code != daemoncli.ExitBackendUnavailable {
		t.Fatalf("expected ExitBackendUnavailable (4), got %d", code)
	}
	if !strings.Contains(stdout.String(), `"status":"stopped"`) {
		t.Errorf("expected JSON status stopped, got %s", stdout.String())
	}
}

func TestDaemonCLI_RunStatusStop_Lifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")

	// Start daemon in background goroutine
	var runStdout, runStderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		code := daemoncli.Run([]string{"run", "--state-dir", dir, "--listen", "127.0.0.1:0", "--json"}, &runStdout, &runStderr)
		done <- code
	}()

	waitForDaemonReady(t, dir, &runStdout, &runStderr, done)

	// Stop daemon
	var stopStdout, stopStderr bytes.Buffer
	stopCode := daemoncli.Run([]string{"stop", "--state-dir", dir, "--json"}, &stopStdout, &stopStderr)
	if stopCode != daemoncli.ExitSuccess {
		t.Fatalf("stop returned code %d (stderr: %s)", stopCode, stopStderr.String())
	}

	select {
	case code := <-done:
		if code != daemoncli.ExitSuccess {
			t.Errorf("daemon run exited with code %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for daemon to exit after stop")
	}
}

func waitForDaemonReady(t *testing.T, dir string, runStdout, runStderr *bytes.Buffer, done <-chan int) {
	t.Helper()
	var statusStdout, statusStderr bytes.Buffer
	readyDeadline := time.NewTimer(30 * time.Second)
	defer readyDeadline.Stop()
	readyPoll := time.NewTicker(10 * time.Millisecond)
	defer readyPoll.Stop()
	for {
		select {
		case code := <-done:
			t.Fatalf("daemon run exited before becoming ready with code %d; run stdout: %s, stderr: %s, status output: %s", code, runStdout.String(), runStderr.String(), statusStdout.String())
		default:
		}

		statusStdout.Reset()
		statusStderr.Reset()
		code := daemoncli.Run([]string{"status", "--state-dir", dir, "--json"}, &statusStdout, &statusStderr)
		if code == daemoncli.ExitSuccess && strings.Contains(statusStdout.String(), `"status":"ok"`) {
			return
		}

		select {
		case code := <-done:
			t.Fatalf("daemon run exited before becoming ready with code %d; run stdout: %s, stderr: %s, status output: %s", code, runStdout.String(), runStderr.String(), statusStdout.String())
		case <-readyPoll.C:
		case <-readyDeadline.C:
			select {
			case code := <-done:
				t.Fatalf("daemon failed to start and exited with code %d; run stdout: %s, stderr: %s, status output: %s", code, runStdout.String(), runStderr.String(), statusStdout.String())
			default:
				t.Fatalf("daemon failed to start before deadline; run goroutine is still active, status output: %s", statusStdout.String())
			}
		}
	}
}

func TestDaemonCLI_UsageAndFlagErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// No args
	if code := daemoncli.Run([]string{}, &stdout, &stderr); code != daemoncli.ExitUsage {
		t.Errorf("expected ExitUsage for empty args, got %d", code)
	}

	// Unknown command
	if code := daemoncli.Run([]string{"unknown"}, &stdout, &stderr); code != daemoncli.ExitUsage {
		t.Errorf("expected ExitUsage for unknown command, got %d", code)
	}

	// Bad flags
	if code := daemoncli.Run([]string{"run", "--bad-flag"}, &stdout, &stderr); code != daemoncli.ExitUsage {
		t.Errorf("expected ExitUsage for bad flag in run, got %d", code)
	}
	if code := daemoncli.Run([]string{"status", "--bad-flag"}, &stdout, &stderr); code != daemoncli.ExitUsage {
		t.Errorf("expected ExitUsage for bad flag in status, got %d", code)
	}
	if code := daemoncli.Run([]string{"stop", "--bad-flag"}, &stdout, &stderr); code != daemoncli.ExitUsage {
		t.Errorf("expected ExitUsage for bad flag in stop, got %d", code)
	}

	// Stop non-running daemon
	dir := filepath.Join(t.TempDir(), "state")
	if code := daemoncli.Run([]string{"stop", "--state-dir", dir}, &stdout, &stderr); code != daemoncli.ExitBackendUnavailable {
		t.Errorf("expected ExitBackendUnavailable stopping inactive daemon, got %d", code)
	}
}
