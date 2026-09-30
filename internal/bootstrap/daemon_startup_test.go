package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/lease"
)

func TestLocalDaemonMissingEndpointRequiresLiveExactStartupOwner(t *testing.T) {
	t.Parallel()
	runtimeID, pid, startTime := (&lease.DefaultIdentityProvider{}).CurrentIdentity()
	for _, tc := range []struct {
		name      string
		runtimeID string
		pid       int
		startTime string
	}{
		{"foreign runtime", "linux:synthetic:foreign", pid, startTime},
		{"missing process start", runtimeID, pid, ""},
		{"different process start", runtimeID, pid, "synthetic-wrong-start"},
		{"exited process", runtimeID, 2147483000, "synthetic-exited-process"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := filepath.Join(t.TempDir(), "state")
			writeSingletonOwner(t, stateDir, tc.runtimeID, tc.pid, tc.startTime)
			healthy, err := NewLocalDaemon().Healthy(context.Background(), stateDir)
			if healthy || !errors.Is(err, app.ErrBootstrapDrift) {
				t.Fatalf("Healthy() with unproven startup owner = %v, %v; want drift", healthy, err)
			}
		})
	}
}

func TestLocalDaemonLiveStartupOwnerDoesNotHideMalformedEndpoint(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "state")
	runtimeID, pid, startTime := (&lease.DefaultIdentityProvider{}).CurrentIdentity()
	writeSingletonOwner(t, stateDir, runtimeID, pid, startTime)
	path := filepath.Join(stateDir, "daemon", "endpoint.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"1","pid":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	healthy, err := NewLocalDaemon().Healthy(context.Background(), stateDir)
	if healthy || !errors.Is(err, app.ErrBootstrapDrift) {
		t.Fatalf("Healthy() with malformed endpoint and live owner = %v, %v; want drift", healthy, err)
	}
}
