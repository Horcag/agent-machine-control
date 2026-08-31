package lease_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/lease"
)

func TestManager_ReclaimStaleLeases_Batch(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ident1 := &mockIdentityProvider{runtimeID: "runtime-local", pid: 1001, startTime: "100"}
	checker := &mockLivenessChecker{
		aliveMap: map[int]bool{
			1001: false, // Dead
			1002: true,  // Alive
		},
	}

	mgr1 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now }),
		lease.WithIdentityProvider(ident1),
		lease.WithLivenessChecker(checker),
	)

	ctx := context.Background()
	m1 := "11111111-1111-1111-1111-111111111111"
	m2 := "22222222-2222-2222-2222-222222222222"
	m3 := "33333333-3333-3333-3333-333333333333"

	// Lease 1: owned by PID 1001 (dead)
	_, err := mgr1.Acquire(ctx, m1, "machine.start", "fp-1", 10*time.Second)
	if err != nil {
		t.Fatalf("Acquire m1 failed: %v", err)
	}

	// Lease 2: owned by PID 1002 (alive)
	ident2 := &mockIdentityProvider{runtimeID: "runtime-local", pid: 1002, startTime: "200"}
	mgr2 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now }),
		lease.WithIdentityProvider(ident2),
		lease.WithLivenessChecker(checker),
	)
	_, err = mgr2.Acquire(ctx, m2, "machine.start", "fp-2", 10*time.Second)
	if err != nil {
		t.Fatalf("Acquire m2 failed: %v", err)
	}

	// Lease 3: cross-runtime
	ident3 := &mockIdentityProvider{runtimeID: "runtime-remote", pid: 1003, startTime: "300"}
	mgr3 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now }),
		lease.WithIdentityProvider(ident3),
		lease.WithLivenessChecker(checker),
	)
	_, err = mgr3.Acquire(ctx, m3, "machine.start", "fp-3", 10*time.Second)
	if err != nil {
		t.Fatalf("Acquire m3 failed: %v", err)
	}

	// Now run batch reclaim under runtime-local
	identLocal := &mockIdentityProvider{runtimeID: "runtime-local", pid: 2000, startTime: "400"}
	mgrLocal := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now.Add(20 * time.Second) }),
		lease.WithIdentityProvider(identLocal),
		lease.WithLivenessChecker(checker),
	)

	reclaimed, err := mgrLocal.ReclaimStaleLeases(ctx)
	if err != nil {
		t.Fatalf("ReclaimStaleLeases failed: %v", err)
	}

	if len(reclaimed) != 1 || reclaimed[0] != m1 {
		t.Fatalf("expected only m1 to be reclaimed, got: %v", reclaimed)
	}
}

func TestManager_ReclaimStaleLeases_IgnoresUnsafeDerivedMachineIDs(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	validMachineID := "11111111-1111-1111-1111-111111111111"
	unsafeMachineID := `nested\\child`
	checker := &mockLivenessChecker{aliveMap: map[int]bool{1001: false}}
	mgr := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now }),
		lease.WithIdentityProvider(&mockIdentityProvider{runtimeID: "runtime-local", pid: 1001, startTime: "100"}),
		lease.WithLivenessChecker(checker),
	)

	if _, err := mgr.Acquire(context.Background(), validMachineID, "machine.start", "fp-1", time.Minute); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	unsafeLeasePath := filepath.Join(dir, unsafeMachineID+".lease.json")
	if err := os.WriteFile(unsafeLeasePath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	reclaimed, err := mgr.ReclaimStaleLeases(context.Background())
	if err != nil {
		t.Fatalf("ReclaimStaleLeases() error = %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0] != validMachineID {
		t.Fatalf("ReclaimStaleLeases() = %v, want [%s]", reclaimed, validMachineID)
	}
	if _, err := os.Stat(unsafeLeasePath); err != nil {
		t.Fatalf("unsafe lease fixture = %v, want unchanged", err)
	}
	if _, err := os.Stat(filepath.Join(dir, unsafeMachineID+".lock")); !os.IsNotExist(err) {
		t.Fatalf("unsafe machine ID created a transition lock: %v", err)
	}
}
