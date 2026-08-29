package lease_test

import (
	"os"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/lease"
)

func TestDefaultIdentityProvider_CurrentIdentity(t *testing.T) {
	provider := &lease.DefaultIdentityProvider{}
	runtimeID, pid, _ := provider.CurrentIdentity()

	if runtimeID == "" {
		t.Errorf("expected non-empty runtimeID")
	}
	if pid != os.Getpid() {
		t.Errorf("expected pid %d, got %d", os.Getpid(), pid)
	}
}

func TestDefaultLivenessChecker_IsAlive(t *testing.T) {
	checker := &lease.DefaultLivenessChecker{}

	// Current running process should be alive
	alive, err := checker.IsAlive(os.Getpid(), "")
	if err != nil {
		t.Fatalf("unexpected error checking self liveness: %v", err)
	}
	if !alive {
		t.Errorf("expected current process to be alive")
	}

	// Invalid PID should be dead
	dead, err := checker.IsAlive(-1, "")
	if err != nil || dead {
		t.Errorf("expected invalid pid -1 to be dead without error, got alive=%v err=%v", dead, err)
	}

	// Non-existent large PID should be dead
	dead, err = checker.IsAlive(999999999, "")
	if err != nil || dead {
		t.Errorf("expected non-existent pid to be dead without error, got alive=%v err=%v", dead, err)
	}

	// PID recycling test: provide impossible start time
	dead, err = checker.IsAlive(os.Getpid(), "impossible-start-time-999999")
	if err != nil {
		t.Errorf("unexpected error on recycled pid check: %v", err)
	}
	if dead {
		t.Logf("recycled start time correctly detected as dead")
	}
}

func TestDefaultLivenessChecker_IsAliveWithStartTime(t *testing.T) {
	provider := &lease.DefaultIdentityProvider{}
	_, pid, startTime := provider.CurrentIdentity()
	checker := &lease.DefaultLivenessChecker{}

	alive, err := checker.IsAlive(pid, startTime)
	if err != nil || !alive {
		t.Errorf("expected current process with recorded startTime to be alive, got alive=%v err=%v", alive, err)
	}

	// Mismatched start time
	alive, err = checker.IsAlive(pid, "9999999999")
	if err != nil || alive {
		t.Errorf("expected mismatched startTime to be considered dead, got alive=%v err=%v", alive, err)
	}
}
