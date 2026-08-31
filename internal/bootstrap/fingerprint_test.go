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

func TestPowerShellFingerprintRegressions(t *testing.T) {
	t.Parallel()

	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("powershell.exe is required for executable fingerprint regressions")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("powershell.exe is required for scheduler parser validation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := "$source = [Console]::In.ReadToEnd(); [scriptblock]::Create($source) | Out-Null; 'bootstrap scheduler parser: passed'"
	cmd := exec.CommandContext(ctx, path, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	cmd.Stdin = strings.NewReader(taskSchedulerScript)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PowerShell scheduler parser failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "bootstrap scheduler parser: passed") {
		t.Fatalf("PowerShell scheduler parser did not report success:\n%s", out)
	}
}
