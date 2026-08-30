package bootstrap

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func TestLocalDaemonVerifiesAndStopsOwnedEndpoint(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	controller := NewLocalDaemon()
	healthy, err := controller.Healthy(context.Background(), stateDir)
	if err != nil || healthy {
		t.Fatalf("Healthy() before start = %v, %v", healthy, err)
	}

	server, err := daemon.NewServer(daemon.Config{StateDir: stateDir, ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		server.TriggerShutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	healthy, err = controller.Healthy(context.Background(), stateDir)
	if err != nil || !healthy {
		t.Fatalf("Healthy() after start = %v, %v", healthy, err)
	}
	if err := controller.Stop(context.Background(), stateDir); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	server.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)

	healthy, err = controller.Healthy(context.Background(), stateDir)
	if err != nil || healthy {
		t.Fatalf("Healthy() after stop = %v, %v", healthy, err)
	}
}

func TestLocalDaemonRejectsCorruptEndpoint(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	server, err := daemon.NewServer(daemon.Config{StateDir: stateDir, ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	server.TriggerShutdown()
	server.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)

	// A stopped daemon removes its endpoint. Replacing it with malformed owned-state evidence
	// must report drift rather than treating the state as cleanly stopped.
	daemonDir := stateDir + "/daemon"
	if err := writeSyntheticFile(daemonDir+"/endpoint.json", []byte(`{"schema_version":"1","pid":1}`)); err != nil {
		t.Fatal(err)
	}
	_, err = NewLocalDaemon().Healthy(context.Background(), stateDir)
	if !errors.Is(err, app.ErrBootstrapDrift) {
		t.Fatalf("Healthy() error = %v, want drift", err)
	}
}

func TestLocalDaemonRejectsLiveEndpointWithoutAuthenticatedOwnership(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	sd, err := statedir.Resolve(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	runtimeID, pid, startTime := (&lease.DefaultIdentityProvider{}).CurrentIdentity()
	if err := daemon.WriteEndpointFile(sd.DaemonDir(), daemon.EndpointRecord{
		SchemaVersion: daemon.SchemaVersion, PID: pid, RuntimeID: runtimeID,
		ProcessStartTime: startTime, StartedAt: time.Now().UTC(), Endpoint: "http://127.0.0.1:1",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = NewLocalDaemon().Healthy(context.Background(), stateDir)
	if !errors.Is(err, app.ErrBootstrapDrift) {
		t.Fatalf("Healthy() error = %v, want drift", err)
	}
}

func TestLocalDaemonStopIsIdempotentWhenAbsent(t *testing.T) {
	t.Parallel()

	if err := NewLocalDaemon().Stop(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestLocalDaemonRejectsRuntimeAndProcessIdentityMismatch(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		runtimeID string
		pid       int
		startTime string
	}{
		{"runtime", "linux:synthetic:wrong-runtime", os.Getpid(), "1"},
		{"process", currentRuntimeID(t), 2147483000, "synthetic-start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stateDir := t.TempDir()
			sd, err := statedir.Resolve(stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := sd.EnsureDirs(); err != nil {
				t.Fatal(err)
			}
			if err := daemon.WriteEndpointFile(sd.DaemonDir(), daemon.EndpointRecord{
				SchemaVersion: daemon.SchemaVersion, PID: tc.pid, RuntimeID: tc.runtimeID,
				ProcessStartTime: tc.startTime, StartedAt: time.Now().UTC(), Endpoint: "http://127.0.0.1:1",
			}); err != nil {
				t.Fatal(err)
			}
			_, err = NewLocalDaemon().Healthy(context.Background(), stateDir)
			if !errors.Is(err, app.ErrBootstrapDrift) {
				t.Fatalf("Healthy() error = %v, want drift", err)
			}
		})
	}
}

func currentRuntimeID(t *testing.T) string {
	t.Helper()
	runtimeID, _, _ := (&lease.DefaultIdentityProvider{}).CurrentIdentity()
	return runtimeID
}

func writeSyntheticFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0600)
}
