//go:build linux

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestLocalDaemonPriorRuntimeReadinessWaitsForSingletonTransition(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "state")
	daemonDir := filepath.Join(stateDir, "daemon")
	writeSingletonOwner(t, stateDir, priorBootstrapRuntime, 100, "old-start")
	if err := daemon.WriteEndpointFile(daemonDir, daemon.EndpointRecord{
		SchemaVersion: daemon.SchemaVersion, PID: 100, RuntimeID: priorBootstrapRuntime,
		ProcessStartTime: "old-start", Endpoint: "http://127.0.0.1:1",
	}); err != nil {
		t.Fatal(err)
	}
	guardPath := filepath.Join(daemonDir, "singleton.guard")
	if err := os.WriteFile(guardPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	guard, err := daemon.LockSingletonObservation(context.Background(), daemonDir)
	if err != nil || guard == nil {
		t.Fatalf("writer guard = %v, %v", guard, err)
	}
	t.Cleanup(func() { _ = guard.Close() })
	// Model the exact guarded takeover interval after removing the old owner and
	// before publishing the replacement owner. No real process is started.
	ownerPath := filepath.Join(daemonDir, "singleton.lock", "owner.json")
	if err := os.Remove(ownerPath); err != nil {
		t.Fatal(err)
	}
	controller := &LocalDaemon{
		identity: bootstrapRuntimeIdentity(currentBootstrapRuntime),
		liveness: bootstrapRuntimeLiveness(func(pid int, start string) (bool, error) {
			return pid == 200 && start == "current-start", nil
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type result struct {
		healthy bool
		err     error
	}
	results := make(chan result, 1)
	go func() { healthy, err := controller.Healthy(ctx, stateDir); results <- result{healthy, err} }()
	select {
	case got := <-results:
		t.Fatalf("readiness observed incomplete owner while writer held guard: %#v", got)
	case <-time.After(30 * time.Millisecond):
	}
	writeSingletonOwner(t, stateDir, currentBootstrapRuntime, 200, "current-start")
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-results:
		if got.healthy || got.err != nil {
			t.Fatalf("readiness after publication = %#v", got)
		}
	case <-ctx.Done():
		t.Fatal("readiness did not finish after ownership publication")
	}
}

func TestLocalDaemonPriorRuntimeReadinessCancellationPreservesIncompleteOwner(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "state")
	daemonDir := filepath.Join(stateDir, "daemon")
	writeSingletonOwner(t, stateDir, priorBootstrapRuntime, 100, "old-start")
	guardPath := filepath.Join(daemonDir, "singleton.guard")
	if err := os.WriteFile(guardPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	guard, err := daemon.LockSingletonObservation(context.Background(), daemonDir)
	if err != nil || guard == nil {
		t.Fatalf("writer guard = %v, %v", guard, err)
	}
	defer guard.Close()
	ownerPath := filepath.Join(daemonDir, "singleton.lock", "owner.json")
	if err := os.WriteFile(ownerPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	controller := &LocalDaemon{
		identity: bootstrapRuntimeIdentity(currentBootstrapRuntime),
		liveness: bootstrapRuntimeLiveness(func(int, string) (bool, error) { t.Error("canceled reader checked process"); return false, nil }),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	healthy, err := controller.Healthy(ctx, stateDir)
	if healthy || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled readiness = %v, %v", healthy, err)
	}
	data, err := os.ReadFile(ownerPath)
	if err != nil || len(data) != 0 {
		t.Fatalf("canceled reader changed incomplete owner: %q, %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(daemonDir, "endpoint.json")); !os.IsNotExist(err) {
		t.Fatalf("canceled reader published endpoint: %v", err)
	}
}
