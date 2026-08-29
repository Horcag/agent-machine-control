package entrypoint

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersionContractIsSharedAcrossBinaries(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"amc", "amcd", "amc-mcp"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := Run(Config{Name: name, UnavailableMessage: "unavailable"}, []string{"--version"}, &stdout, &stderr)

			if exitCode != 0 {
				t.Fatalf("Run() exit code = %d, want 0", exitCode)
			}
			if !strings.HasPrefix(stdout.String(), name+" dev ") {
				t.Fatalf("Run() stdout = %q, want %q prefix", stdout.String(), name+" dev ")
			}
			if stderr.Len() != 0 {
				t.Fatalf("Run() stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRunUnavailableCommandReturnsUsageExitCode(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run(Config{Name: "amc", UnavailableMessage: "not ready"}, nil, &stdout, &stderr)

	if exitCode != 2 {
		t.Fatalf("Run() exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("Run() stdout = %q, want empty", stdout.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "not ready" {
		t.Fatalf("Run() stderr = %q, want %q", got, "not ready")
	}
}

func TestRunHelpIsSuccessful(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run(Config{Name: "amc", UnavailableMessage: "not ready"}, []string{"--help"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0", exitCode)
	}
	if !strings.Contains(stderr.String(), "-version") {
		t.Fatalf("Run() help = %q, want version flag", stderr.String())
	}
}
