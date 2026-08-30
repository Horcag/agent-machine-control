package audit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestStore_AdmissionAndTerminalOutcome(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	store := audit.NewStore(dir, audit.WithClock(func() time.Time { return now }))

	if err := store.CheckWritable(); err != nil {
		t.Fatalf("expected writable store, got %v", err)
	}

	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               actor,
		Reason:              "testing",
		Deadline:            now.Add(time.Minute),
		IdempotencyKey:      "key-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	if err := store.RecordAdmissionIntent(op); err != nil {
		t.Fatalf("RecordAdmissionIntent failed: %v", err)
	}

	fp, _ := op.Fingerprint()
	r := domain.Receipt{
		ReceiptID:        "rcpt-1",
		OperationKind:    op.Kind,
		Fingerprint:      fp,
		IdempotencyKey:   op.IdempotencyKey,
		Actor:            actor.EffectiveActor,
		Target:           op.Target,
		Class:            op.Classification,
		EffectiveBackend: "hyperv",
		StartedAt:        now,
		CompletedAt:      now.Add(time.Second),
		Outcome:          domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType:  domain.ObservationObserved,
		RollbackRef:      "chk-1",
		RedactionStatus:  domain.RedactionApplied,
	}

	if err := store.RecordTerminalOutcome(r); err != nil {
		t.Fatalf("RecordTerminalOutcome failed: %v", err)
	}

	// Read log file
	logFile := filepath.Join(dir, audit.AuditFileName)
	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read audit log: %v", err)
	}

	if len(content) == 0 {
		t.Fatalf("audit log is empty")
	}
}

func TestStore_Unwritable_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	unwritableDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(unwritableDir, 0500); err != nil {
		t.Fatalf("failed to create readonly dir: %v", err)
	}
	defer func() { _ = os.Chmod(unwritableDir, 0700) }()

	store := audit.NewStore(unwritableDir)
	if err := store.CheckWritable(); err == nil {
		t.Fatalf("expected error for unwritable store")
	}
}

type mockAuditIdentityProvider struct {
	runtimeID string
	pid       int
	startTime string
}

func (m *mockAuditIdentityProvider) CurrentIdentity() (string, int, string) {
	return m.runtimeID, m.pid, m.startTime
}

type mockAuditLivenessChecker struct {
	aliveMap map[int]bool
	errMap   map[int]error
}

func (m *mockAuditLivenessChecker) IsAlive(pid int, _ string) (bool, error) {
	if err, ok := m.errMap[pid]; ok {
		return false, err
	}
	return m.aliveMap[pid], nil
}

func TestStore_DeadLockOwner_Reclaimed(t *testing.T) {
	dir := t.TempDir()
	lockDir := filepath.Join(dir, ".audit.lock")
	_ = os.Mkdir(lockDir, 0700)
	ownerJSON := `{"schema_version":"1","runtime_id":"test-runtime","pid":8888,"process_start_time":"100","acquired_at":"2026-08-29T12:00:00Z"}`
	_ = os.WriteFile(filepath.Join(lockDir, "owner.json"), []byte(ownerJSON), 0600)

	checker := &mockAuditLivenessChecker{
		aliveMap: map[int]bool{8888: false}, // dead process!
	}
	ident := &mockAuditIdentityProvider{runtimeID: "test-runtime", pid: 9999, startTime: "200"}
	store := audit.NewStore(dir,
		audit.WithLivenessChecker(checker),
		audit.WithIdentityProvider(ident),
	)

	if err := store.CheckWritable(); err != nil {
		t.Fatalf("expected CheckWritable to reclaim dead owner lock, got: %v", err)
	}
}

func TestStore_LockReclaim_Blocked(t *testing.T) {
	cases := []struct {
		name      string
		ownerJSON string
		alive     bool
		callerRun string
	}{
		{
			name:      "live process",
			ownerJSON: `{"schema_version":"1","runtime_id":"test-runtime","pid":8888,"process_start_time":"100","acquired_at":"2026-08-29T12:00:00Z"}`,
			alive:     true,
			callerRun: "test-runtime",
		},
		{
			name:      "cross runtime",
			ownerJSON: `{"schema_version":"1","runtime_id":"other-runtime","pid":8888,"process_start_time":"100","acquired_at":"2026-08-29T12:00:00Z"}`,
			alive:     false,
			callerRun: "my-runtime",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			lockDir := filepath.Join(dir, ".audit.lock")
			_ = os.Mkdir(lockDir, 0700)
			_ = os.WriteFile(filepath.Join(lockDir, "owner.json"), []byte(tc.ownerJSON), 0600)

			checker := &mockAuditLivenessChecker{
				aliveMap: map[int]bool{8888: tc.alive},
			}
			ident := &mockAuditIdentityProvider{runtimeID: tc.callerRun, pid: 9999, startTime: "200"}
			store := audit.NewStore(dir,
				audit.WithLivenessChecker(checker),
				audit.WithIdentityProvider(ident),
				audit.WithLockTimeout(50*time.Millisecond),
			)

			if err := store.CheckWritable(); err == nil {
				t.Fatalf("expected CheckWritable to fail when audit lock is blocked by %s", tc.name)
			}
		})
	}
}

func TestStore_StrictJSON_TrailingData(t *testing.T) {
	dir := t.TempDir()
	lockDir := filepath.Join(dir, ".audit.lock")
	_ = os.Mkdir(lockDir, 0700)

	// Whitespace trailing is fine
	ownerJSONValid := "{\"schema_version\":\"1\",\"runtime_id\":\"test-runtime\",\"pid\":8888,\"process_start_time\":\"100\",\"acquired_at\":\"2026-08-29T12:00:00Z\"}\n  \n"
	_ = os.WriteFile(filepath.Join(lockDir, "owner.json"), []byte(ownerJSONValid), 0600)

	checker := &mockAuditLivenessChecker{
		aliveMap: map[int]bool{8888: false},
	}
	ident := &mockAuditIdentityProvider{runtimeID: "test-runtime", pid: 9999, startTime: "200"}
	store := audit.NewStore(dir,
		audit.WithLivenessChecker(checker),
		audit.WithIdentityProvider(ident),
		audit.WithLockTimeout(50*time.Millisecond),
	)

	if err := store.CheckWritable(); err != nil {
		t.Fatalf("expected CheckWritable to succeed with whitespace trailing data, got: %v", err)
	}

	// Extra object trailing is rejected (cannot reclaim)
	_ = os.Mkdir(lockDir, 0700)
	ownerJSONTrailing := "{\"schema_version\":\"1\",\"runtime_id\":\"test-runtime\",\"pid\":8888,\"process_start_time\":\"100\",\"acquired_at\":\"2026-08-29T12:00:00Z\"}\n{\"extra\":1}"
	_ = os.WriteFile(filepath.Join(lockDir, "owner.json"), []byte(ownerJSONTrailing), 0600)

	if err := store.CheckWritable(); err == nil {
		t.Fatalf("expected CheckWritable to fail when owner.json has trailing JSON")
	}
}

func TestStore_AdmissionAndTerminalValidation(t *testing.T) {
	dir := t.TempDir()
	store := audit.NewStore(dir)

	// RecordAdmissionIntent invalid op
	if err := store.RecordAdmissionIntent(domain.Operation{}); err == nil {
		t.Errorf("expected error for invalid operation in RecordAdmissionIntent")
	}

	// CheckWritable on unwritable path
	unwritableStore := audit.NewStore("/dev/null/impossible/audit/dir")
	if err := unwritableStore.CheckWritable(); err == nil {
		t.Errorf("expected error for unwritable audit path")
	}
}

func TestStore_LockCleanupFailuresAreReturnedAndJoined(t *testing.T) {
	cleanupErr := errors.New("synthetic audit cleanup failure")
	protectedErr := errors.New("synthetic protected audit failure")
	for _, tc := range []struct {
		name      string
		failBase  string
		operation error
	}{
		{name: "owner removal", failBase: "owner.json"},
		{name: "lock directory removal", failBase: ".audit.lock"},
		{name: "joined protected error", failBase: "owner.json", operation: protectedErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := audit.NewStore(dir,
				audit.WithEnsureHook(func(context.Context, audit.Event) error { return tc.operation }),
				audit.WithRemoveFunc(func(path string) error {
					if filepath.Base(path) == tc.failBase {
						return cleanupErr
					}
					return os.Remove(path)
				}),
			)
			err := store.EnsureTerminalOutcome(terminalReceipt())
			if !errors.Is(err, cleanupErr) {
				t.Fatalf("cleanup error = %v, want injected removal failure", err)
			}
			if tc.operation != nil && !errors.Is(err, protectedErr) {
				t.Fatalf("joined error = %v, want protected operation failure", err)
			}
		})
	}
}
