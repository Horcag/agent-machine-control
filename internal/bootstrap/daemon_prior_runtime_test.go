//go:build linux

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/daemon"
)

const (
	priorBootstrapRuntime   = "linux:synthetic-host:11111111-1111-1111-1111-111111111111"
	currentBootstrapRuntime = "linux:synthetic-host:22222222-2222-2222-2222-222222222222"
)

type bootstrapRuntimeIdentity string

func (id bootstrapRuntimeIdentity) CurrentIdentity() (string, int, string) {
	return string(id), 200, "current-start"
}

type bootstrapRuntimeLiveness func(int, string) (bool, error)

func (check bootstrapRuntimeLiveness) IsAlive(pid int, start string) (bool, error) {
	return check(pid, start)
}

func TestLocalDaemonPriorRuntimeEndpointReadiness(t *testing.T) {
	t.Parallel()
	livenessError := errors.New("synthetic liveness failure")
	for _, tc := range []bootstrapEndpointCase{
		{name: "retained dead prior boot", ownerRuntime: priorBootstrapRuntime, ownerPID: 100, ownerStart: "old-start"},
		{name: "replacement singleton initializing", ownerRuntime: currentBootstrapRuntime, ownerPID: 200, ownerStart: "current-start", currentAlive: true},
		{name: "foreign host", runtimeID: "linux:foreign-host:11111111-1111-1111-1111-111111111111", wantErr: app.ErrBootstrapDrift},
		{name: "unknown host", runtimeID: "linux:unknown-host:11111111-1111-1111-1111-111111111111", wantErr: app.ErrBootstrapDrift},
		{name: "malformed boot", runtimeID: "linux:synthetic-host:11111111-1111-1111-1111-11111111111g", wantErr: app.ErrBootstrapDrift},
		{name: "missing boot", runtimeID: "linux:synthetic-host", wantErr: app.ErrBootstrapDrift},
		{name: "foreign OS", runtimeID: "windows:synthetic-host:11111111-1111-1111-1111-111111111111", wantErr: app.ErrBootstrapDrift},
		{name: "missing endpoint start", endpointStart: "missing", wantErr: app.ErrBootstrapDrift},
		{name: "missing singleton", missingOwner: true, wantErr: app.ErrBootstrapDrift},
		{name: "corrupt singleton", corruptOwner: true, wantErr: app.ErrBootstrapDrift},
		{name: "different prior singleton PID", ownerRuntime: priorBootstrapRuntime, ownerPID: 101, ownerStart: "old-start", wantErr: app.ErrBootstrapDrift},
		{name: "different prior singleton start", ownerRuntime: priorBootstrapRuntime, ownerPID: 100, ownerStart: "different-start", wantErr: app.ErrBootstrapDrift},
		{name: "foreign singleton", ownerRuntime: "linux:foreign-host:11111111-1111-1111-1111-111111111111", ownerPID: 100, ownerStart: "old-start", wantErr: app.ErrBootstrapDrift},
		{name: "live prior process", oldAlive: true, wantErr: app.ErrBootstrapDrift},
		{name: "liveness failure", livenessErr: livenessError, wantErr: livenessError},
		{name: "dead replacement singleton", ownerRuntime: currentBootstrapRuntime, ownerPID: 200, ownerStart: "current-start", wantErr: app.ErrBootstrapDrift},
		{name: "missing singleton start", ownerRuntime: priorBootstrapRuntime, ownerPID: 100, wantErr: app.ErrBootstrapDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stateDir := tc.stateDir(t)
			controller := &LocalDaemon{identity: bootstrapRuntimeIdentity(currentBootstrapRuntime), liveness: tc}
			healthy, err := controller.Healthy(context.Background(), stateDir)
			if healthy || !errors.Is(err, tc.wantErr) {
				t.Fatalf("Healthy() = %v, %v; want unavailable with %v", healthy, err, tc.wantErr)
			}
			// Readiness never widens release classification or task-stop authority.
			observation, err := controller.ObserveRelease(context.Background(), stateDir)
			if err != nil || observation.State != app.BootstrapDaemonReleaseDrift {
				t.Fatalf("ObserveRelease() = %#v, %v; want strict prior-runtime drift", observation, err)
			}
		})
	}
}

// Each endpoint scenario owns its generated persisted state and process evidence.
type bootstrapEndpointCase struct {
	name          string
	runtimeID     string
	endpointStart string
	ownerRuntime  string
	ownerPID      int
	ownerStart    string
	missingOwner  bool
	corruptOwner  bool
	oldAlive      bool
	currentAlive  bool
	livenessErr   error
	wantErr       error
}

func (tc bootstrapEndpointCase) stateDir(t *testing.T) string {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	daemonDir := filepath.Join(stateDir, "daemon")
	if err := os.MkdirAll(daemonDir, 0700); err != nil {
		t.Fatal(err)
	}
	endpointRuntime := tc.runtimeID
	if endpointRuntime == "" {
		endpointRuntime = priorBootstrapRuntime
	}
	endpointStart := "old-start"
	if tc.endpointStart == "missing" {
		endpointStart = ""
	}
	if err := daemon.WriteEndpointFile(daemonDir, daemon.EndpointRecord{
		SchemaVersion: daemon.SchemaVersion, PID: 100, RuntimeID: endpointRuntime,
		ProcessStartTime: endpointStart, Endpoint: "http://127.0.0.1:1",
	}); err != nil {
		t.Fatal(err)
	}
	if !tc.missingOwner {
		writeSingletonOwner(t, stateDir, tc.ownerRuntime, tc.ownerPID, tc.ownerStart)
	}
	if tc.corruptOwner {
		if err := os.WriteFile(filepath.Join(daemonDir, "singleton.lock", "owner.json"), []byte(`{`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return stateDir
}

func (tc bootstrapEndpointCase) IsAlive(pid int, start string) (bool, error) {
	if tc.livenessErr != nil {
		return false, tc.livenessErr
	}
	if pid == 100 && start == "old-start" {
		return tc.oldAlive, nil
	}
	return pid == 200 && start == "current-start" && tc.currentAlive, nil
}
