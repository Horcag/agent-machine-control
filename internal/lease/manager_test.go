package lease_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/lease"
)

type mockIdentityProvider struct {
	runtimeID string
	pid       int
	startTime string
}

func (m *mockIdentityProvider) CurrentIdentity() (string, int, string) {
	return m.runtimeID, m.pid, m.startTime
}

type mockLivenessChecker struct {
	aliveMap map[int]bool
	errMap   map[int]error
}

func (m *mockLivenessChecker) IsAlive(pid int, _ string) (bool, error) {
	if err, ok := m.errMap[pid]; ok {
		return false, err
	}
	return m.aliveMap[pid], nil
}

func TestManager_CanceledAcquireCreatesNoLockOrLeaseState(t *testing.T) {
	dir := t.TempDir()
	mgr := lease.NewManager(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	acquired, err := mgr.Acquire(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", "session.write", "sha256:test", time.Minute)
	if acquired != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire() = lease %+v err %v, want canceled with no lease", acquired, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled acquire created state: %v", entries)
	}
}

func TestManager_TransitionLockCleanupFailuresAreReturnedAndJoined(t *testing.T) {
	cleanupErr := errors.New("synthetic lease cleanup failure")
	protectedMachine := "c4a523d4-6b99-4d62-a5e2-4752c0f20009"
	for _, tc := range []struct {
		name      string
		failBase  string
		protected bool
	}{
		{name: "owner removal", failBase: "owner.json"},
		{name: "lock directory removal", failBase: protectedMachine + ".lock"},
		{name: "joined protected error", failBase: "owner.json", protected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			machineID := protectedMachine
			if tc.protected {
				if err := os.WriteFile(filepath.Join(dir, machineID+".gen.json"), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			mgr := lease.NewManager(dir, lease.WithRemoveFunc(func(path string) error {
				if filepath.Base(path) == tc.failBase {
					return cleanupErr
				}
				return os.Remove(path)
			}))
			_, err := mgr.Acquire(context.Background(), machineID, "session.write", "sha256:test", time.Minute)
			if !errors.Is(err, cleanupErr) {
				t.Fatalf("cleanup error = %v, want injected removal failure", err)
			}
			if tc.protected && !errors.Is(err, lease.ErrInvalidLeaseData) {
				t.Fatalf("joined error = %v, want protected operation failure", err)
			}
		})
	}
}

func TestManager_AcquireAndRelease_Success(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ident := &mockIdentityProvider{runtimeID: "test-runtime", pid: 1001, startTime: "12345"}
	mgr := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now }),
		lease.WithIdentityProvider(ident),
	)

	ctx := context.Background()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"

	l, err := mgr.Acquire(ctx, machineID, "machine.start", "fp-1", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	if l.MachineID != machineID {
		t.Errorf("expected machine ID %s, got %s", machineID, l.MachineID)
	}
	if l.FencingGeneration != 1 {
		t.Errorf("expected initial fencing generation 1, got %d", l.FencingGeneration)
	}
	if l.ExpiresAt != now.Add(30*time.Second) {
		t.Errorf("expected ExpiresAt %v, got %v", now.Add(30*time.Second), l.ExpiresAt)
	}

	// Release
	if err := mgr.Release(ctx, l); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// Can acquire again after release: generation must monotonically increment
	l2, err := mgr.Acquire(ctx, machineID, "machine.start", "fp-2", 30*time.Second)
	if err != nil {
		t.Fatalf("second Acquire failed: %v", err)
	}
	if l2.FencingGeneration != 2 {
		t.Errorf("expected fencing generation 2 after clean release, got %d", l2.FencingGeneration)
	}

	if err := mgr.Release(ctx, l2); err != nil {
		t.Fatalf("second Release failed: %v", err)
	}

	l3, err := mgr.Acquire(ctx, machineID, "machine.start", "fp-3", 30*time.Second)
	if err != nil {
		t.Fatalf("third Acquire failed: %v", err)
	}
	if l3.FencingGeneration != 3 {
		t.Errorf("expected fencing generation 3 after second release, got %d", l3.FencingGeneration)
	}
}

func TestManager_Conflict_WhenActive(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"

	mgr1 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now }),
		lease.WithIdentityProvider(&mockIdentityProvider{runtimeID: "run-A", pid: 501, startTime: "501-t"}),
	)

	l1, err := mgr1.Acquire(ctx, machineID, "machine.start", "fp-init", 1*time.Minute)
	if err != nil || l1 == nil {
		t.Fatalf("initial acquire failed: %v", err)
	}

	// Concurrent worker on same or different PID within TTL gets ErrLeaseConflict
	mgr2 := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now.Add(5 * time.Second) }),
		lease.WithIdentityProvider(&mockIdentityProvider{runtimeID: "run-A", pid: 502, startTime: "502-t"}),
	)

	if _, err := mgr2.Acquire(ctx, machineID, "machine.stop", "fp-other", 1*time.Minute); !errors.Is(err, lease.ErrLeaseConflict) {
		t.Fatalf("expected ErrLeaseConflict during active TTL, got: %v", err)
	}
}

func TestManager_ReleaseFencingProtection(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ident := &mockIdentityProvider{runtimeID: "test-runtime", pid: 1001, startTime: "12345"}
	mgr := lease.NewManager(dir,
		lease.WithClock(func() time.Time { return now }),
		lease.WithIdentityProvider(ident),
	)

	ctx := context.Background()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"

	l, err := mgr.Acquire(ctx, machineID, "machine.start", "fp-1", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// Tamper with lease owner / fencing gen
	tampered := *l
	tampered.FencingGeneration = 999

	err = mgr.Release(ctx, &tampered)
	if err == nil || !errors.Is(err, lease.ErrLeaseFencingViolation) {
		t.Fatalf("expected ErrLeaseFencingViolation, got %v", err)
	}

	tamperedOwner := *l
	tamperedOwner.OwnerID = "other-owner"
	err = mgr.Release(ctx, &tamperedOwner)
	if err == nil || !errors.Is(err, lease.ErrLeaseFencingViolation) {
		t.Fatalf("expected ErrLeaseFencingViolation, got %v", err)
	}
}

func TestManager_ActiveLock_NeverBrokenByTimeAlone(t *testing.T) {
	dir := t.TempDir()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"
	lockDir := dir + "/" + machineID + ".lock"

	// Create active lock directory held by living process in test-runtime
	if err := os.Mkdir(lockDir, 0700); err != nil {
		t.Fatalf("failed to create lock directory: %v", err)
	}
	ownerJSON := `{"schema_version":"1","runtime_id":"test-runtime","pid":8888,"process_start_time":"100","acquired_at":"2026-08-29T12:00:00Z"}`
	if err := os.WriteFile(lockDir+"/owner.json", []byte(ownerJSON), 0600); err != nil {
		t.Fatalf("failed to write owner.json: %v", err)
	}

	// Set mtime to 10 seconds ago (> 5 seconds)
	pastTime := time.Now().Add(-10 * time.Second)
	_ = os.Chtimes(lockDir, pastTime, pastTime)

	checker := &mockLivenessChecker{
		aliveMap: map[int]bool{8888: true}, // PID 8888 IS ALIVE
	}
	ident := &mockIdentityProvider{runtimeID: "test-runtime", pid: 9999, startTime: "200"}
	mgr := lease.NewManager(dir,
		lease.WithLivenessChecker(checker),
		lease.WithIdentityProvider(ident),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := mgr.Acquire(ctx, machineID, "machine.start", "fp-1", 30*time.Second)
	if err == nil {
		t.Fatalf("expected acquire to fail on active lock")
	}

	// Verify lock directory was NOT deleted
	if _, statErr := os.Stat(lockDir); statErr != nil {
		t.Fatalf("active lock directory must NEVER be broken by time alone: %v", statErr)
	}
}

func TestManager_CorruptGeneration_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"
	genFile := dir + "/" + machineID + ".gen.json"

	_ = os.WriteFile(genFile, []byte(`corrupt json`), 0600)

	mgr := lease.NewManager(dir)
	ctx := context.Background()

	_, err := mgr.Acquire(ctx, machineID, "machine.start", "fp-1", 30*time.Second)
	if err == nil || !errors.Is(err, lease.ErrInvalidLeaseData) {
		t.Fatalf("expected ErrInvalidLeaseData for corrupt gen file, got: %v", err)
	}
}

func TestManager_EdgeCases(t *testing.T) {
	dir := t.TempDir()
	mgr := lease.NewManager(dir, lease.WithOwnerPrefix("test-prefix"))

	ctx := context.Background()

	// Empty machine ID
	if _, err := mgr.Acquire(ctx, "", "op", "fp", time.Minute); err == nil {
		t.Errorf("expected error for empty machine ID")
	}

	// Release nil
	if err := mgr.Release(ctx, nil); err != nil {
		t.Errorf("expected nil for Release(nil)")
	}

	// Acquire with default TTL (0)
	l, err := mgr.Acquire(ctx, "a0b1c2d3-e4f5-6789-abcd-ef0123456789", "op", "fp", 0)
	if err != nil {
		t.Fatalf("Acquire with 0 TTL failed: %v", err)
	}
	if l == nil {
		t.Fatalf("expected non-nil lease")
	}
	_ = mgr.Release(ctx, l)
}

func TestManager_DeadLockOwner_Reclaimed(t *testing.T) {
	dir := t.TempDir()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"
	lockDir := dir + "/" + machineID + ".lock"

	_ = os.Mkdir(lockDir, 0700)
	ownerJSON := `{"schema_version":"1","runtime_id":"test-runtime","pid":7777,"process_start_time":"100","acquired_at":"2026-08-29T12:00:00Z"}`
	_ = os.WriteFile(lockDir+"/owner.json", []byte(ownerJSON), 0600)

	checker := &mockLivenessChecker{
		aliveMap: map[int]bool{7777: false}, // PID 7777 IS DEAD
	}
	ident := &mockIdentityProvider{runtimeID: "test-runtime", pid: 9999, startTime: "200"}
	mgr := lease.NewManager(dir,
		lease.WithLivenessChecker(checker),
		lease.WithIdentityProvider(ident),
	)

	l, err := mgr.Acquire(context.Background(), machineID, "machine.start", "fp-1", 30*time.Second)
	if err != nil {
		t.Fatalf("expected acquire to reclaim dead transition lock, got: %v", err)
	}
	if l == nil {
		t.Fatalf("expected acquired lease")
	}
}

func TestManager_SymlinkAndOversized_Rejected(t *testing.T) {
	dir := t.TempDir()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"

	// Symlink on lease file
	realFile := dir + "/real.json"
	_ = os.WriteFile(realFile, []byte(`{}`), 0600)
	leaseFile := dir + "/" + machineID + ".lease.json"
	if err := os.Symlink(realFile, leaseFile); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	mgr := lease.NewManager(dir)
	_, err := mgr.Acquire(context.Background(), machineID, "machine.start", "fp-1", 30*time.Second)
	if err == nil || !errors.Is(err, lease.ErrInvalidLeaseData) {
		t.Fatalf("expected ErrInvalidLeaseData for symlinked lease file, got: %v", err)
	}

	// Symlink on gen file
	_ = os.Remove(leaseFile)
	genFile := dir + "/" + machineID + ".gen.json"
	if err := os.Symlink(realFile, genFile); err != nil {
		t.Skipf("generation symlink creation unavailable: %v", err)
	}

	_, err = mgr.Acquire(context.Background(), machineID, "machine.start", "fp-1", 30*time.Second)
	if err == nil || !errors.Is(err, lease.ErrInvalidLeaseData) {
		t.Fatalf("expected ErrInvalidLeaseData for symlinked gen file, got: %v", err)
	}
}

func TestManager_DefaultClock(t *testing.T) {
	dir := t.TempDir()
	mgr := lease.NewManager(dir)
	l, err := mgr.Acquire(context.Background(), "a0b1c2d3-e4f5-6789-abcd-ef0123456789", "machine.start", "fp-1", time.Minute)
	if err != nil {
		t.Fatalf("Acquire with default clock failed: %v", err)
	}
	if err := mgr.Release(context.Background(), l); err != nil {
		t.Fatalf("Release with default clock failed: %v", err)
	}
}

func TestManager_StrictJSON_TrailingData(t *testing.T) {
	dir := t.TempDir()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"
	genFile := dir + "/" + machineID + ".gen.json"

	// 1. Whitespace-only trailing data is valid
	validWithWhitespace := "{\n  \"schema_version\": \"1\",\n  \"machine_id\": \"" + machineID + "\",\n  \"last_generation\": 5,\n  \"updated_at\": \"2026-08-29T12:00:00Z\"\n}\n  \t\n"
	if err := os.WriteFile(genFile, []byte(validWithWhitespace), 0600); err != nil {
		t.Fatalf("failed to write gen file: %v", err)
	}

	mgr := lease.NewManager(dir)
	l, err := mgr.Acquire(context.Background(), machineID, "machine.start", "fp-1", 30*time.Second)
	if err != nil {
		t.Fatalf("expected Acquire to succeed with whitespace trailing data, got: %v", err)
	}
	if l.FencingGeneration != 6 {
		t.Errorf("expected fencing generation 6, got %d", l.FencingGeneration)
	}
	_ = mgr.Release(context.Background(), l)

	// 2. Second JSON object trailing data is rejected
	trailingObject := "{\n  \"schema_version\": \"1\",\n  \"machine_id\": \"" + machineID + "\",\n  \"last_generation\": 10,\n  \"updated_at\": \"2026-08-29T12:00:00Z\"\n}\n{\"extra\": 123}"
	if err := os.WriteFile(genFile, []byte(trailingObject), 0600); err != nil {
		t.Fatalf("failed to write gen file: %v", err)
	}
	if _, err := mgr.Acquire(context.Background(), machineID, "machine.start", "fp-1", 30*time.Second); err == nil || !errors.Is(err, lease.ErrInvalidLeaseData) {
		t.Fatalf("expected ErrInvalidLeaseData for second object trailing data, got: %v", err)
	}

	// 3. Second JSON scalar trailing data is rejected
	trailingScalar := "{\n  \"schema_version\": \"1\",\n  \"machine_id\": \"" + machineID + "\",\n  \"last_generation\": 10,\n  \"updated_at\": \"2026-08-29T12:00:00Z\"\n}\n123"
	if err := os.WriteFile(genFile, []byte(trailingScalar), 0600); err != nil {
		t.Fatalf("failed to write gen file: %v", err)
	}
	if _, err := mgr.Acquire(context.Background(), machineID, "machine.start", "fp-1", 30*time.Second); err == nil || !errors.Is(err, lease.ErrInvalidLeaseData) {
		t.Fatalf("expected ErrInvalidLeaseData for second scalar trailing data, got: %v", err)
	}
}

func TestManager_Release_DoesNotDeleteOtherProcessLock(t *testing.T) {
	dir := t.TempDir()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"
	lockDir := dir + "/" + machineID + ".lock"
	ownerPath := lockDir + "/owner.json"

	_ = os.Mkdir(lockDir, 0700)
	otherOwnerJSON := `{"schema_version":"1","runtime_id":"other-runtime","pid":9999,"process_start_time":"555","acquired_at":"2026-08-29T12:00:00Z"}`
	_ = os.WriteFile(ownerPath, []byte(otherOwnerJSON), 0600)

	// If manager tries to release a lock that now belongs to another process, it must NOT delete other's lockDir or owner.json
	mgr := lease.NewManager(dir,
		lease.WithIdentityProvider(&mockIdentityProvider{runtimeID: "my-runtime", pid: 1000, startTime: "100"}),
	)

	// Try release for a lease
	fakeLease := &lease.Lease{
		SchemaVersion:     lease.SchemaVersion,
		MachineID:         machineID,
		OwnerID:           "direct:1000:abc",
		FencingGeneration: 1,
	}

	// This should fail or time out because lockDir is locked by other process
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = mgr.Release(ctx, fakeLease)

	// Check that other process's lockDir and owner.json are still present
	if _, err := os.Stat(lockDir); os.IsNotExist(err) {
		t.Fatalf("lockDir was erroneously deleted")
	}
	if _, err := os.Stat(ownerPath); os.IsNotExist(err) {
		t.Fatalf("ownerPath was erroneously deleted")
	}
}
