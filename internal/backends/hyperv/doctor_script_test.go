package hyperv

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDoctorProbesMachineAndAdapterQueries(t *testing.T) {
	executable, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("native PowerShell unavailable")
	}
	for _, test := range []struct {
		name, query, adapter string
		ready                bool
	}{
		{"healthy queries", "[pscustomobject]@{ Name='synthetic' }", "@()", true},
		{"enumeration failure", "throw 'synthetic inventory failure'", "@()", false},
		{"adapter failure", "[pscustomobject]@{ Name='synthetic' }", "throw 'synthetic adapter failure'", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Generated provider fixtures exercise the real script without host state.
			prefix := "function Import-Module {}\nfunction Get-VMHost {}\nfunction Get-VM { " + test.query + " }\nfunction Get-VMNetworkAdapter { " + test.adapter + " }\n"
			script := prefix + strings.ReplaceAll(ScriptDoctor, scriptAccessPreflightDoctor, "")
			stdout, stderr, runErr := (&DefaultExecutor{}).Execute(t.Context(), executable, []string{"-NoProfile", "-NonInteractive", "-Command", script}, nil)
			if runErr != nil {
				t.Fatalf("fixture script: %v (%s)", runErr, stderr)
			}
			report, parseErr := parseDoctorResponse(stdout, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			if parseErr != nil || report.Ready != test.ready {
				t.Fatalf("query readiness: %+v, %v", report, parseErr)
			}
		})
	}
}
