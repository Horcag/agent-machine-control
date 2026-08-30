package hyperv_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestAdapter_DefaultLocalRoutePreservesCommandPath(t *testing.T) {
	var capturedArgs []string
	var capturedEnv []string
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, args []string, env []string) ([]byte, []byte, error) {
			capturedArgs = append([]string(nil), args...)
			capturedEnv = append([]string(nil), env...)
			return []byte(`{"schema_version":"1","machines":[]}`), nil, nil
		},
	}
	adapter := hyperv.New(hyperv.WithExecutor(mock))
	if _, err := adapter.ListMachines(context.Background()); err != nil {
		t.Fatalf("ListMachines failed: %v", err)
	}
	if len(capturedArgs) != 7 || capturedArgs[0] != "-NoProfile" || capturedArgs[6] != hyperv.ScriptList {
		t.Fatalf("default local command path changed: args=%q", capturedArgs)
	}
	for _, entry := range capturedEnv {
		if strings.HasPrefix(entry, hyperv.HostAddressEnvVar+"=") {
			t.Fatalf("default local route should not set remote host env: %v", capturedEnv)
		}
	}
}

func TestAdapter_ExplicitRemoteRouteUsesEnvironmentAndStaticScript(t *testing.T) {
	route, err := hyperv.ExplicitRemoteHostRoute(app.HostEntry{ID: "host-a", Address: "trusted-host.example", Enabled: true})
	if err != nil {
		t.Fatalf("ExplicitRemoteHostRoute failed: %v", err)
	}
	var capturedArgs []string
	var capturedEnv []string
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, args []string, env []string) ([]byte, []byte, error) {
			capturedArgs = append([]string(nil), args...)
			capturedEnv = append([]string(nil), env...)
			return []byte(`{"schema_version":"1","machines":[{"id":"aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa","name":"vm-a","state":"Off","generation":2,"version":"10.0"}]}`), nil, nil
		},
	}
	adapter := hyperv.New(hyperv.WithExecutor(mock), hyperv.WithHostRoute(route))
	machines, err := adapter.ListMachines(context.Background())
	if err != nil {
		t.Fatalf("ListMachines failed: %v", err)
	}
	if machines[0].HostID != "host-a" || machines[0].Locator.String() != "host-a:aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa" {
		t.Fatalf("machine route not stamped: %+v", machines[0])
	}
	foundHostEnv := false
	for _, entry := range capturedEnv {
		if entry == hyperv.HostAddressEnvVar+"=trusted-host.example" {
			foundHostEnv = true
		}
	}
	if !foundHostEnv {
		t.Fatalf("remote host address not passed through env: %v", capturedEnv)
	}
	script := capturedArgs[len(capturedArgs)-1]
	if strings.Contains(script, "trusted-host.example") {
		t.Fatalf("remote address was interpolated into script source")
	}
	if !strings.Contains(script, "Get-VM -ComputerName $targetHost -ErrorAction Stop") {
		t.Fatalf("remote list script does not use bounded host parameter: %s", script)
	}
	if strings.Contains(script, "WindowsPrincipal") || strings.Contains(script, "S-1-5-32-544") || strings.Contains(script, "S-1-5-32-578") {
		t.Fatalf("remote read script must not depend on local Administrators/Hyper-V Administrators preflight")
	}
}

func TestAdapter_RemoteInspectNormalizesTargetEnvironment(t *testing.T) {
	route, err := hyperv.ExplicitRemoteHostRoute(app.HostEntry{ID: "host-a", Address: "trusted-host.example", Enabled: true})
	if err != nil {
		t.Fatalf("ExplicitRemoteHostRoute failed: %v", err)
	}
	var capturedEnv []string
	var capturedScript string
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, args []string, env []string) ([]byte, []byte, error) {
			capturedEnv = append([]string(nil), env...)
			capturedScript = args[len(args)-1]
			return []byte(`{"schema_version":"1","machine":{"id":"aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa","name":"vm-a","state":"Off","generation":2,"version":"10.0"}}`), nil, nil
		},
	}
	adapter := hyperv.New(hyperv.WithExecutor(mock), hyperv.WithHostRoute(route))
	if _, err := adapter.InspectMachine(context.Background(), "AAAAAAAA-AAAA-4AAA-AAAA-AAAAAAAAAAAA"); err != nil {
		t.Fatalf("InspectMachine failed: %v", err)
	}
	if !containsEnv(capturedEnv, hyperv.TargetVMIDEnvVar+"=aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa") {
		t.Fatalf("target GUID not normalized through env: %v", capturedEnv)
	}
	if strings.Contains(capturedScript, "AAAAAAAA-AAAA") || strings.Contains(capturedScript, "trusted-host.example") {
		t.Fatalf("target or host was interpolated into script source")
	}
	if !strings.Contains(capturedScript, "Get-VM -ComputerName $targetHost -Id $vmGuid -ErrorAction Stop") {
		t.Fatalf("remote inspect script missing ComputerName parameter")
	}
}

func containsEnv(env []string, want string) bool {
	return slices.Contains(env, want)
}

func TestExplicitRemoteHostRouteRejectsLocalID(t *testing.T) {
	_, err := hyperv.ExplicitRemoteHostRoute(app.HostEntry{ID: domain.LocalHostID, Address: "trusted-host.example", Enabled: true})
	if err == nil {
		t.Fatal("expected local host ID to be rejected for explicit remote route")
	}
}

func TestZeroHostRouteDefaultsToLocalCapabilities(t *testing.T) {
	adapter := hyperv.New(hyperv.WithHostRoute(hyperv.HostRoute{}))
	caps, err := adapter.Capabilities(context.Background(), "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("zero local route capabilities: %v", err)
	}
	if !slices.Equal(caps.Slice(), domain.DirectMachineCapabilities().Slice()) {
		t.Fatalf("zero local route capabilities = %v, want direct capabilities", caps.Slice())
	}
}

func TestLocalRouteRejectsRemoteAddressBeforeCapabilities(t *testing.T) {
	adapter := hyperv.New(hyperv.WithHostRoute(hyperv.HostRoute{
		HostID:  domain.LocalHostID,
		Address: "trusted-host.example",
	}))
	caps, err := adapter.Capabilities(context.Background(), "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa")
	if !errors.Is(err, domain.ErrInvalidHostAddress) || caps != nil {
		t.Fatalf("local route with remote address capabilities = %v, %v; want nil, %v", caps, err, domain.ErrInvalidHostAddress)
	}
}

func TestAdapterRejectsInvalidRemoteRouteBeforeExecutor(t *testing.T) {
	tests := []struct {
		name    string
		host    app.HostEntry
		route   hyperv.HostRoute
		wantErr error
	}{
		{
			name:    "local host ID",
			host:    app.HostEntry{ID: domain.LocalHostID, Address: "local", Enabled: true},
			route:   hyperv.HostRoute{HostID: domain.LocalHostID, Address: "trusted-host.example", Remote: true},
			wantErr: domain.ErrInvalidHostID,
		},
		{
			name:    "empty host ID",
			host:    app.HostEntry{Address: "trusted-host.example", Enabled: true},
			route:   hyperv.HostRoute{Address: "trusted-host.example", Remote: true},
			wantErr: domain.ErrInvalidHostID,
		},
		{
			name:    "padded host ID",
			host:    app.HostEntry{ID: " host-a ", Address: "trusted-host.example", Enabled: true},
			route:   hyperv.HostRoute{HostID: " host-a ", Address: "trusted-host.example", Remote: true},
			wantErr: domain.ErrInvalidHostID,
		},
		{
			name:    "padded local ID",
			host:    app.HostEntry{ID: " local ", Address: "trusted-host.example", Enabled: true},
			route:   hyperv.HostRoute{HostID: " local ", Address: "trusted-host.example", Remote: true},
			wantErr: domain.ErrInvalidHostID,
		},
		{
			name:    "padded host address",
			host:    app.HostEntry{ID: "host-a", Address: " trusted-host.example ", Enabled: true},
			route:   hyperv.HostRoute{HostID: "host-a", Address: " trusted-host.example ", Remote: true},
			wantErr: domain.ErrInvalidHostAddress,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := hyperv.ExplicitRemoteHostRoute(tt.host); !errors.Is(err, tt.wantErr) {
				t.Fatalf("ExplicitRemoteHostRoute expected %v, got %v", tt.wantErr, err)
			}
			adapter := hyperv.New(
				hyperv.WithExecutor(noCallExecutor{t: t}),
				hyperv.WithHostRoute(tt.route),
			)
			if _, err := adapter.ListMachines(context.Background()); !errors.Is(err, tt.wantErr) {
				t.Fatalf("ListMachines expected %v, got %v", tt.wantErr, err)
			}
			if caps, err := adapter.Capabilities(context.Background(), "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"); !errors.Is(err, tt.wantErr) || caps != nil {
				t.Fatalf("Capabilities = %v, %v; want nil, %v", caps, err, tt.wantErr)
			}
		})
	}
}

func TestRemoteRoutePrivilegedMethodsFailClosedBeforeExecutor(t *testing.T) {
	route, err := hyperv.ExplicitRemoteHostRoute(app.HostEntry{ID: "host-a", Address: "trusted-host.example", Enabled: true})
	if err != nil {
		t.Fatalf("ExplicitRemoteHostRoute failed: %v", err)
	}
	adapter := hyperv.New(hyperv.WithExecutor(noCallExecutor{t: t}), hyperv.WithHostRoute(route))
	id := "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	checkpointID := "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	if caps, err := adapter.Capabilities(context.Background(), id); err != nil || !slices.Equal(caps.Slice(), domain.ReadOnlyMachineCapabilities().Slice()) {
		t.Fatalf("remote capabilities = %+v, %v; want read-only", caps, err)
	}
	assertRemoteReadOnly(t, func() error {
		_, err := adapter.StartMachine(context.Background(), id)
		return err
	})
	assertRemoteReadOnly(t, func() error {
		_, err := adapter.StopMachine(context.Background(), id, "save")
		return err
	})
	assertRemoteReadOnly(t, func() error {
		_, err := adapter.ListCheckpoints(context.Background(), id)
		return err
	})
	assertRemoteReadOnly(t, func() error {
		_, err := adapter.CreateCheckpoint(context.Background(), id, "checkpoint-a")
		return err
	})
	assertRemoteReadOnly(t, func() error {
		_, err := adapter.RestoreCheckpoint(context.Background(), id, checkpointID)
		return err
	})
}

func assertRemoteReadOnly(t *testing.T, fn func() error) {
	t.Helper()
	if err := fn(); !errors.Is(err, hyperv.ErrRemoteRouteReadOnly) {
		t.Fatalf("expected ErrRemoteRouteReadOnly, got %v", err)
	}
}

type noCallExecutor struct {
	t *testing.T
}

func (e noCallExecutor) LookPath(string) (string, error) {
	e.t.Fatal("remote privileged method should fail before LookPath")
	return "", nil
}

func (e noCallExecutor) Execute(context.Context, string, []string, []string) ([]byte, []byte, error) {
	e.t.Fatal("remote privileged method should fail before Execute")
	return nil, nil, nil
}

func TestNilExecutorOptionFallsBackToDefault(t *testing.T) {
	adapter := hyperv.New(hyperv.WithExecutor(nil), hyperv.WithExecutablePath("powershell.exe"))
	if _, err := adapter.ListMachines(context.Background()); err == nil {
		t.Fatal("expected execution error, not nil executor panic")
	}
}
