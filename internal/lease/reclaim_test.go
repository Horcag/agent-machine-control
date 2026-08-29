package lease_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/lease"
)

func TestManager_Reclaim_SameRuntimeDeadOwner_IncrementsFencing(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ident1 := &mockIdentityProvider{runtimeID: "runtime-alpha", pid: 1001, startTime: "100"}
	checker := &mockLivenessChecker{
		aliveMap: map[int]bool{1001: false}, // PID 1001 is positively dead
	}

	mgr1 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now }),
		lease.WithIdentityProvider(ident1),
		lease.WithLivenessChecker(checker),
	)

	ctx := context.Background()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"

	l1, err := mgr1.Acquire(ctx, machineID, "machine.start", "fp-1", 10*time.Second)
	if err != nil {
		t.Fatalf("Acquire l1 failed: %v", err)
	}
	if l1.FencingGeneration != 1 {
		t.Fatalf("expected fencing gen 1, got %d", l1.FencingGeneration)
	}

	// Advance time past expiry (now + 20s > now + 10s)
	ident2 := &mockIdentityProvider{runtimeID: "runtime-alpha", pid: 1002, startTime: "200"}
	mgr2 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now.Add(20 * time.Second) }),
		lease.WithIdentityProvider(ident2),
		lease.WithLivenessChecker(checker),
	)

	l2, err := mgr2.Acquire(ctx, machineID, "machine.stop", "fp-2", 10*time.Second)
	if err != nil {
		t.Fatalf("Acquire l2 reclaim failed: %v", err)
	}
	if l2.FencingGeneration != 2 {
		t.Errorf("expected reclaimed lease to increment fencing generation to 2, got %d", l2.FencingGeneration)
	}
}

func TestManager_Reclaim_CrossRuntimeOwner_NeverReclaimed(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ident1 := &mockIdentityProvider{runtimeID: "runtime-alpha", pid: 1001, startTime: "100"}
	mgr1 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now }),
		lease.WithIdentityProvider(ident1),
	)

	ctx := context.Background()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"

	_, err := mgr1.Acquire(ctx, machineID, "machine.start", "fp-1", 10*time.Second)
	if err != nil {
		t.Fatalf("Acquire l1 failed: %v", err)
	}

	// Advance time past expiry, but with different runtimeID (e.g. windows vs wsl or different host)
	ident2 := &mockIdentityProvider{runtimeID: "runtime-beta", pid: 2002, startTime: "200"}
	mgr2 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now.Add(20 * time.Second) }),
		lease.WithIdentityProvider(ident2),
	)

	_, err = mgr2.Acquire(ctx, machineID, "machine.stop", "fp-2", 10*time.Second)
	if err == nil || !errors.Is(err, lease.ErrLeaseUnverifiableOwner) {
		t.Fatalf("expected ErrLeaseUnverifiableOwner, got %v", err)
	}
}

func TestManager_Reclaim_LiveOwner_NeverReclaimed(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ident1 := &mockIdentityProvider{runtimeID: "runtime-alpha", pid: 1001, startTime: "100"}
	checker := &mockLivenessChecker{
		aliveMap: map[int]bool{1001: true}, // PID 1001 is STILL ALIVE even though TTL expired
	}

	mgr1 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now }),
		lease.WithIdentityProvider(ident1),
		lease.WithLivenessChecker(checker),
	)

	ctx := context.Background()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"

	_, err := mgr1.Acquire(ctx, machineID, "machine.start", "fp-1", 10*time.Second)
	if err != nil {
		t.Fatalf("Acquire l1 failed: %v", err)
	}

	// Advance time past expiry
	ident2 := &mockIdentityProvider{runtimeID: "runtime-alpha", pid: 1002, startTime: "200"}
	mgr2 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now.Add(20 * time.Second) }),
		lease.WithIdentityProvider(ident2),
		lease.WithLivenessChecker(checker),
	)

	_, err = mgr2.Acquire(ctx, machineID, "machine.stop", "fp-2", 10*time.Second)
	if err == nil || !errors.Is(err, lease.ErrLeaseConflict) {
		t.Fatalf("expected ErrLeaseConflict when owner is alive, got %v", err)
	}
}

func TestManager_Reclaim_LivenessCheckError_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ident1 := &mockIdentityProvider{runtimeID: "runtime-alpha", pid: 1001, startTime: "100"}
	checker := &mockLivenessChecker{
		errMap: map[int]error{1001: errors.New("procfs read error")},
	}

	mgr1 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now }),
		lease.WithIdentityProvider(ident1),
		lease.WithLivenessChecker(checker),
	)

	ctx := context.Background()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"

	_, err := mgr1.Acquire(ctx, machineID, "machine.start", "fp-1", 10*time.Second)
	if err != nil {
		t.Fatalf("Acquire l1 failed: %v", err)
	}

	ident2 := &mockIdentityProvider{runtimeID: "runtime-alpha", pid: 1002, startTime: "200"}
	mgr2 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now.Add(20 * time.Second) }),
		lease.WithIdentityProvider(ident2),
		lease.WithLivenessChecker(checker),
	)

	_, err = mgr2.Acquire(ctx, machineID, "machine.stop", "fp-2", 10*time.Second)
	if err == nil || !errors.Is(err, lease.ErrLeaseConflict) {
		t.Fatalf("expected ErrLeaseConflict on liveness check error, got %v", err)
	}
}
