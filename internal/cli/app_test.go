package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type mockObserver struct {
	doctorReport app.DoctorReport
	doctorErr    error
	listResult   []domain.MachineObservation
	listErr      error
	inspectFn    func(ctx context.Context, id string) (domain.MachineObservation, error)
}

func (m *mockObserver) Doctor(_ context.Context) (app.DoctorReport, error) {
	return m.doctorReport, m.doctorErr
}

func (m *mockObserver) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	return m.listResult, m.listErr
}

func (m *mockObserver) InspectMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	if m.inspectFn != nil {
		return m.inspectFn(ctx, id)
	}
	return domain.MachineObservation{}, errors.New("not implemented")
}

func TestApp_Version(t *testing.T) {
	for _, flag := range []string{"--version", "-version", "-v"} {
		t.Run(flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.Run([]string{flag}, &stdout, &stderr)

			if code != cli.ExitSuccess {
				t.Fatalf("expected ExitSuccess (0), got %d", code)
			}
			if !strings.HasPrefix(stdout.String(), "amc dev ") {
				t.Errorf("expected version output with prefix 'amc dev ', got %q", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Errorf("expected empty stderr, got %q", stderr.String())
			}
		})
	}
}

func TestApp_Help(t *testing.T) {
	for _, flag := range []string{"--help", "-help", "-h", "help"} {
		t.Run(flag, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.Run([]string{flag}, &stdout, &stderr)

			if code != cli.ExitSuccess {
				t.Fatalf("expected ExitSuccess (0), got %d", code)
			}
			if !strings.Contains(stdout.String(), "Usage: amc") {
				t.Errorf("expected usage output, got %q", stdout.String())
			}
		})
	}
}

func TestApp_EmptyArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run(nil, &stdout, &stderr)

	if code != cli.ExitUsage {
		t.Fatalf("expected ExitUsage (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "Usage: amc") {
		t.Errorf("expected usage output on stderr, got %q", stderr.String())
	}
}

func TestApp_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"unknowncmd"}, &stdout, &stderr)

	if code != cli.ExitUsage {
		t.Fatalf("expected ExitUsage (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("expected unknown command error, got %q", stderr.String())
	}
}

func assertDoctorHumanOutput(t *testing.T, cliApp *cli.App, expectedSubstrings ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cliApp.Run([]string{"doctor"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess (0), got %d", code)
	}
	out := stdout.String()
	for _, exp := range expectedSubstrings {
		if !strings.Contains(out, exp) {
			t.Errorf("expected substring %q in doctor output, got %q", exp, out)
		}
	}
}

func TestApp_Doctor_Ready_HumanAndJSON(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	mock := &mockObserver{
		doctorReport: app.NewReadyReport(domain.ReadOnlyMachineCapabilities(), now),
	}
	service := app.NewDiscoveryService(mock)
	cliApp := cli.NewApp(service)

	t.Run("human output", func(t *testing.T) {
		assertDoctorHumanOutput(t, cliApp,
			"Status: ready",
			"Hyper-V Module: available",
			"Capabilities: host.diagnostics, machine.inspect, machine.list, network_adapter.observe",
		)
	})

	t.Run("JSON output", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"doctor", "--json"}, &stdout, &stderr)

		if code != cli.ExitSuccess {
			t.Fatalf("expected ExitSuccess (0), got %d", code)
		}
		var env cli.DoctorOutputEnvelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to decode JSON envelope: %v", err)
		}
		if env.SchemaVersion != "1" || !env.Ready || env.Status != app.DoctorReady {
			t.Errorf("unexpected doctor envelope: %+v", env)
		}
		if len(env.Capabilities) != 4 {
			t.Errorf("expected 4 capabilities, got %d", len(env.Capabilities))
		}
	})
}

func TestApp_Doctor_Unavailable_HumanAndJSON(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	mock := &mockObserver{
		doctorReport: app.NewUnavailableReport(
			app.DoctorReasonExecutableMissing,
			"PowerShell executable (powershell.exe) was not found in PATH",
			now,
		),
	}
	service := app.NewDiscoveryService(mock)
	cliApp := cli.NewApp(service)

	t.Run("human output", func(t *testing.T) {
		assertDoctorHumanOutput(t, cliApp,
			"Status: unavailable",
			"Reason: executable_missing",
			"Capabilities: none",
		)
	})

	t.Run("JSON output", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"doctor", "--json"}, &stdout, &stderr)

		if code != cli.ExitSuccess {
			t.Fatalf("expected ExitSuccess (0), got %d", code)
		}
		var env cli.DoctorOutputEnvelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to decode JSON envelope: %v", err)
		}
		if env.SchemaVersion != "1" || env.Ready || env.Status != app.DoctorUnavailable {
			t.Errorf("unexpected doctor envelope: %+v", env)
		}
		if env.Reason != app.DoctorReasonExecutableMissing {
			t.Errorf("expected Reason executable_missing, got %q", env.Reason)
		}
	})
}

func TestApp_Doctor_UnexpectedArgsAndFlags(t *testing.T) {
	mock := &mockObserver{doctorReport: app.NewReadyReport(nil, time.Now())}
	service := app.NewDiscoveryService(mock)
	cliApp := cli.NewApp(service)

	var stdout, stderr bytes.Buffer
	code := cliApp.Run([]string{"doctor", "unexpected-arg"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage (2) for unexpected arg, got %d", code)
	}

	stdout.Reset()
	stderr.Reset()
	code = cliApp.Run([]string{"doctor", "--invalid-flag"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage (2) for invalid flag, got %d", code)
	}
}

func TestCli_DefaultRun_ExecutableMissing_Doctor(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"doctor"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess (0), got %d (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "executable_missing") {
		t.Errorf("expected executable_missing in doctor output, got %q", stdout.String())
	}
}

func TestCli_DefaultRun_ExecutableMissing_DoctorJSON(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"doctor", "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess (0), got %d (stderr: %s)", code, stderr.String())
	}
	var env cli.DoctorOutputEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to decode JSON envelope: %v", err)
	}
	if env.SchemaVersion != "1" || env.Ready || env.Reason != app.DoctorReasonExecutableMissing {
		t.Errorf("unexpected doctor envelope: %+v", env)
	}
}

func TestCli_DefaultRun_ExecutableMissing_MachineList(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"machine", "list"}, &stdout, &stderr)
	if code != cli.ExitBackendUnavailable {
		t.Fatalf("expected ExitBackendUnavailable (4), got %d", code)
	}
	if !strings.Contains(stderr.String(), "Hyper-V host management is unavailable") {
		t.Errorf("expected sanitized provider message on stderr, got %q", stderr.String())
	}
}

func TestCli_DefaultRun_ExecutableMissing_MachineInspect(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"machine", "inspect", "c4a523d4-6b99-4d62-a5e2-4752c0f20001"}, &stdout, &stderr)
	if code != cli.ExitBackendUnavailable {
		t.Fatalf("expected ExitBackendUnavailable (4), got %d", code)
	}
	if !strings.Contains(stderr.String(), "Hyper-V host management is unavailable") {
		t.Errorf("expected sanitized provider message on stderr, got %q", stderr.String())
	}
}
