package bootstrap

import (
	"context"
	_ "embed"
	"os/exec"
	"strings"
	"testing"
	"time"
)

//go:embed task_fingerprint_test.ps1
var taskFingerprintTestScript string

const powershellTestTimeout = time.Minute

func TestPowerShellFingerprintRegressions(t *testing.T) {
	t.Parallel()

	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("powershell.exe is required for executable fingerprint regressions")
	}
	ctx, cancel := context.WithTimeout(context.Background(), powershellTestTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	cmd.Stdin = strings.NewReader(taskFingerprintScript + "\n" + taskFingerprintTestScript)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell fingerprint regressions failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "bootstrap fingerprint regressions: passed") {
		t.Fatalf("PowerShell fingerprint regressions did not report success:\n%s", out)
	}
}

func TestPowerShellSchedulerScriptParses(t *testing.T) {
	t.Parallel()

	assertPowerShellParses(t, taskSchedulerScript, "scheduler")
}

func TestPowerShellHostContextScriptParses(t *testing.T) {
	t.Parallel()

	assertPowerShellParses(t, hostContextScript, "host context")
}

func assertPowerShellParses(t *testing.T, script, name string) {
	t.Helper()

	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skipf("powershell.exe is required for %s parser validation", name)
	}
	ctx, cancel := context.WithTimeout(t.Context(), powershellTestTimeout)
	defer cancel()
	command := "$source = [Console]::In.ReadToEnd(); [scriptblock]::Create($source) | Out-Null; 'bootstrap parser: passed'"
	cmd := exec.CommandContext(ctx, path, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell %s parser failed: %v\n%s", name, err, out)
	}
	if !strings.Contains(string(out), "bootstrap parser: passed") {
		t.Fatalf("PowerShell %s parser did not report success:\n%s", name, out)
	}
}
