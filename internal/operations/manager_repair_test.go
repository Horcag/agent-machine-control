package operations_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/operations"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

type repairBlockingBackend struct {
	started      atomic.Int64
	current      atomic.Int64
	maxCurrent   atomic.Int64
	blockChannel chan struct{}
	entered      chan struct{}
}

func (b *repairBlockingBackend) Doctor(_ context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}

func (b *repairBlockingBackend) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	return nil, nil
}

func (b *repairBlockingBackend) InspectMachine(_ context.Context, id string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func (b *repairBlockingBackend) Capabilities(_ context.Context, _ string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(domain.CapabilityMachineStart, domain.CapabilityMachineStop), nil
}

func (b *repairBlockingBackend) StartMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	b.started.Add(1)
	current := b.current.Add(1)
	defer b.current.Add(-1)
	for {
		observed := b.maxCurrent.Load()
		if current <= observed || b.maxCurrent.CompareAndSwap(observed, current) {
			break
		}
	}
	if b.entered != nil {
		select {
		case b.entered <- struct{}{}:
		default:
		}
	}
	if b.blockChannel != nil {
		select {
		case <-b.blockChannel:
		case <-ctx.Done():
			return domain.MachineObservation{}, ctx.Err()
		}
	}
	return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
}

func (b *repairBlockingBackend) StopMachine(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func (b *repairBlockingBackend) ListCheckpoints(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
	return []domain.CheckpointObservation{
		{
			ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			Name:            "base",
			VMID:            id,
			CheckpointType:  "Standard",
			CreatedAt:       time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
			ObservedAt:      time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
			ObservationType: domain.ObservationObserved,
		},
	}, nil
}

func (b *repairBlockingBackend) CreateCheckpoint(_ context.Context, _ string, _ string) (domain.CheckpointObservation, error) {
	return domain.CheckpointObservation{}, nil
}

func (b *repairBlockingBackend) RestoreCheckpoint(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func TestManager_PendingAdmittedRunningObservationAndExactRetry(t *testing.T) {
	blockCh := make(chan struct{})
	backend := &repairBlockingBackend{blockChannel: blockCh}

	dir := t.TempDir()
	leasesDir := t.TempDir()
	auditDir := t.TempDir()
	rcptDir := t.TempDir()
	apprDir := t.TempDir()

	leaseMgr := lease.NewManager(leasesDir)
	auditStore := audit.NewStore(auditDir)
	rcptStore := receipt.NewStore(rcptDir)
	apprStore := approval.NewStore(apprDir)
	eventHub := events.NewHub(dir)

	recoverySvc := app.NewRecoveryService(backend, leaseMgr, auditStore, rcptStore, apprStore)
	mgr := operations.NewManager(dir, recoverySvc, rcptStore, auditStore, eventHub)
	defer func() {
		close(blockCh)
		_ = mgr.Shutdown(context.Background())
	}()

	actor := makeTestActor("operator:test-runner")
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               actor,
		Reason:              "test lifecycle freshness",
		Deadline:            time.Now().Add(30 * time.Second),
		IdempotencyKey:      "idemp-lifecycle-1",
		RequiredScopes:      []string{"machine:write"},
		RequiredCapability:  "machine:start",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	// Initial submission
	rec, wasExisting, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil || wasExisting {
		t.Fatalf("initial Submit failed: err=%v, wasExisting=%v", err, wasExisting)
	}
	if rec.State != domain.OpStatePending {
		t.Fatalf("expected initial state pending, got %s", rec.State)
	}

	// Wait until operation transitions to running
	var runningRec *domain.OperationRecord
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := mgr.Get(rec.ID, actor)
		if err == nil && r.State == domain.OpStateRunning {
			runningRec = r
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if runningRec == nil {
		t.Fatalf("timed out waiting for operation to reach running state")
	}

	// Exact retry while running must return fresh running record
	retryRec, isRetry, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil {
		t.Fatalf("retry while running failed: %v", err)
	}
	if !isRetry {
		t.Errorf("expected isRetry=true for exact retry")
	}
	if retryRec.State != domain.OpStateRunning {
		t.Errorf("expected retry to return running state, got %s", retryRec.State)
	}
	if retryRec.ID != rec.ID {
		t.Errorf("expected retry ID %s, got %s", rec.ID, retryRec.ID)
	}
}

func TestManager_Over100ConcurrentBlockedBackendCapacity(t *testing.T) {
	blockCh := make(chan struct{})
	backend := &repairBlockingBackend{blockChannel: blockCh, entered: make(chan struct{}, 100)}

	dir := t.TempDir()
	leasesDir := t.TempDir()
	auditDir := t.TempDir()
	rcptDir := t.TempDir()
	apprDir := t.TempDir()

	leaseMgr := lease.NewManager(leasesDir)
	auditStore := audit.NewStore(auditDir)
	rcptStore := receipt.NewStore(rcptDir)
	apprStore := approval.NewStore(apprDir)
	eventHub := events.NewHub(dir)

	recoverySvc := app.NewRecoveryService(backend, leaseMgr, auditStore, rcptStore, apprStore)
	mgr := operations.NewManager(dir, recoverySvc, rcptStore, auditStore, eventHub)
	defer func() {
		close(blockCh)
		_ = mgr.Shutdown(context.Background())
	}()

	actor := makeTestActor("operator:capacity-tester")

	totalSubmitters := 150
	var wg sync.WaitGroup
	var acceptedCount atomic.Int64
	var busyCount atomic.Int64
	var otherErrCount atomic.Int64

	for i := range totalSubmitters {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			op := domain.Operation{
				Kind:                "machine.start",
				Target:              domain.MachineRef(fmt.Sprintf("c4a523d4-6b99-4d62-a5e2-%012x", idx+1)),
				Actor:               actor,
				Reason:              "capacity stress test",
				Deadline:            time.Now().Add(5 * time.Minute),
				IdempotencyKey:      fmt.Sprintf("idemp-cap-%04d", idx),
				RequiredScopes:      []string{"machine:write"},
				RequiredCapability:  "machine:start",
				Classification:      domain.ClassReversibleMutation,
				EvidenceSensitivity: domain.EvidenceSensitivityStandard,
			}
			_, _, err := mgr.Submit(context.Background(), op, 5*time.Minute)
			switch {
			case err == nil:
				acceptedCount.Add(1)
			case errors.Is(err, operations.ErrManagerBusy):
				busyCount.Add(1)
			default:
				otherErrCount.Add(1)
			}
		}(i)
	}

	wg.Wait()

	if otherErrCount.Load() > 0 {
		t.Errorf("unexpected errors during concurrent submit: %d", otherErrCount.Load())
	}
	if got := acceptedCount.Load(); got > 100 {
		t.Fatalf("accepted backend calls = %d, capacity must not exceed 100", got)
	}
	if acceptedCount.Load() != 100 || busyCount.Load() != int64(totalSubmitters-100) {
		t.Fatalf("capacity outcomes = accepted %d busy %d, want 100/%d", acceptedCount.Load(), busyCount.Load(), totalSubmitters-100)
	}
	for range 100 {
		select {
		case <-backend.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("backend operations did not reach simultaneous blocking barrier")
		}
	}
	if got := backend.current.Load(); got != 100 {
		t.Errorf("simultaneous backend concurrency = %d, want 100", got)
	}
	if got := backend.maxCurrent.Load(); got != 100 {
		t.Errorf("maximum backend concurrency = %d, want 100", got)
	}
	if acceptedCount.Load()+busyCount.Load() != int64(totalSubmitters) {
		t.Errorf("expected accepted + busy == %d, got %d + %d", totalSubmitters, acceptedCount.Load(), busyCount.Load())
	}
}

func TestManager_InjectedFinalizationSaveRecordFailure(t *testing.T) {
	backend := &repairBlockingBackend{} // completes immediately
	dir := t.TempDir()
	leasesDir := t.TempDir()
	auditDir := t.TempDir()
	rcptDir := t.TempDir()
	apprDir := t.TempDir()

	leaseMgr := lease.NewManager(leasesDir)
	auditStore := audit.NewStore(auditDir)
	rcptStore := receipt.NewStore(rcptDir)
	apprStore := approval.NewStore(apprDir)
	eventHub := events.NewHub(dir)

	recoverySvc := app.NewRecoveryService(backend, leaseMgr, auditStore, rcptStore, apprStore)
	mgr := operations.NewManager(dir, recoverySvc, rcptStore, auditStore, eventHub)

	actor := makeTestActor("operator:injected-failure")
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               actor,
		Reason:              "injected persistence failure test",
		Deadline:            time.Now().Add(30 * time.Second),
		IdempotencyKey:      "idemp-inject-save-err",
		RequiredScopes:      []string{"machine:write"},
		RequiredCapability:  "machine:start",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	// Subscribe to event stream before submitting to ensure waiters receive terminal event
	// Submit operation
	rec, _, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	ch, unsub, err := eventHub.Subscribe(context.Background(), rec.ID, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer unsub()

	// Wait for terminal event from eventHub
	var sawTerminal bool
	timer := time.After(3 * time.Second)
	for !sawTerminal {
		select {
		case ev, ok := <-ch:
			if !ok {
				sawTerminal = true
				break
			}
			if ev.EventType == "terminal" || ev.State.IsTerminal() {
				sawTerminal = true
			}
		case <-timer:
			t.Fatalf("timed out waiting for terminal event on event hub stream")
		}
	}

	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Logf("Shutdown returned expected aggregation or nil: %v", err)
	}
}

func TestManager_FailClosedListRecordsError(t *testing.T) {
	backend := &repairBlockingBackend{}
	dir := t.TempDir()
	leasesDir := t.TempDir()
	auditDir := t.TempDir()
	rcptDir := t.TempDir()
	apprDir := t.TempDir()

	leaseMgr := lease.NewManager(leasesDir)
	auditStore := audit.NewStore(auditDir)
	rcptStore := receipt.NewStore(rcptDir)
	apprStore := approval.NewStore(apprDir)
	eventHub := events.NewHub(dir)

	recoverySvc := app.NewRecoveryService(backend, leaseMgr, auditStore, rcptStore, apprStore)
	mgr := operations.NewManager(dir, recoverySvc, rcptStore, auditStore, eventHub)

	// Create a corrupted unparseable operation record JSON file in dir
	_ = os.WriteFile(filepath.Join(dir, "op-corrupt.json"), []byte("not-valid-json"), 0600)

	actor := makeTestActor("operator:fail-closed-test")
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               actor,
		Reason:              "fail closed idempotency test",
		Deadline:            time.Now().Add(30 * time.Second),
		IdempotencyKey:      "idemp-corrupt-storage",
		RequiredScopes:      []string{"machine:write"},
		RequiredCapability:  "machine:start",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	_, _, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err == nil {
		t.Errorf("expected Submit to fail closed when disk storage has corrupted records")
	}
}

func TestManager_CachedReceiptLookup_MatchesAndConflicts(t *testing.T) {
	backend := &repairBlockingBackend{}
	dir := t.TempDir()
	leasesDir := t.TempDir()
	auditDir := t.TempDir()
	rcptDir := t.TempDir()
	apprDir := t.TempDir()

	leaseMgr := lease.NewManager(leasesDir)
	auditStore := audit.NewStore(auditDir)
	rcptStore := receipt.NewStore(rcptDir)
	apprStore := approval.NewStore(apprDir)
	eventHub := events.NewHub(dir)

	recoverySvc := app.NewRecoveryService(backend, leaseMgr, auditStore, rcptStore, apprStore)
	mgr := operations.NewManager(dir, recoverySvc, rcptStore, auditStore, eventHub)
	t.Cleanup(func() { _ = mgr.Shutdown(context.Background()) })

	actor := makeTestActor("operator:cached-rcpt-test")
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               actor,
		Reason:              "test cached receipt lookup",
		Deadline:            time.Now().Add(30 * time.Second),
		IdempotencyKey:      "idemp-cached-test-1",
		RequiredScopes:      []string{"machine:write"},
		RequiredCapability:  "machine:start",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	fp, err := op.Fingerprint()
	if err != nil {
		t.Fatalf("op.Fingerprint failed: %v", err)
	}

	// Persist matching receipt in rcptStore
	rcpt := domain.Receipt{
		ReceiptID:        "rcpt-00000000000000000000000000000123",
		OperationKind:    op.Kind,
		Target:           op.Target,
		Actor:            actor.EffectiveActor,
		Fingerprint:      fp,
		IdempotencyKey:   op.IdempotencyKey,
		StartedAt:        time.Now().Add(-10 * time.Second),
		CompletedAt:      time.Now().Add(-5 * time.Second),
		Class:            op.Classification,
		EffectiveBackend: "fake",
		RollbackRef:      "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Outcome:          domain.ExecutionOutcome{Status: domain.OutcomeSuccess},
		ObservationType:  domain.ObservationObserved,
		RedactionStatus:  domain.RedactionNotApplicable,
	}
	if err := rcptStore.Save(rcpt); err != nil {
		t.Fatalf("rcptStore.Save failed: %v", err)
	}

	// 1. Matching receipt lookup
	rec, isCached, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil || !isCached {
		t.Fatalf("expected cached receipt hit: err=%v, isCached=%v", err, isCached)
	}
	if rec.State != domain.OpStateCompleted || rec.ReceiptID != rcpt.ReceiptID {
		t.Errorf("expected completed record with receipt %s, got state %s, receipt %s", rcpt.ReceiptID, rec.State, rec.ReceiptID)
	}

	// 2. Conflict: different actor with same idempotency key in cached receipt
	otherActor := makeTestActor("operator:different-actor")
	opDiffActor := op
	opDiffActor.Actor = otherActor
	_, _, err = mgr.Submit(context.Background(), opDiffActor, 30*time.Second)
	if !errors.Is(err, operations.ErrOperationConflict) {
		t.Errorf("expected ErrOperationConflict on cached receipt actor mismatch, got: %v", err)
	}

	// 3. Conflict: different target with same idempotency key in cached receipt
	opDiffTarget := op
	opDiffTarget.Target = "c4a523d4-6b99-4d62-a5e2-4752c0f20002"
	_, _, err = mgr.Submit(context.Background(), opDiffTarget, 30*time.Second)
	if !errors.Is(err, operations.ErrOperationConflict) {
		t.Errorf("expected ErrOperationConflict on cached receipt target mismatch, got: %v", err)
	}
}

func TestOperationsStore_SaveReadEdgeCases(t *testing.T) {
	dir := t.TempDir()

	// 1. SaveRecord with empty dir is no-op
	rec := domain.OperationRecord{
		SchemaVersion:  "1",
		ID:             "op-00000000000000000000000000000001",
		Actor:          "operator:test",
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Kind:           "machine.start",
		State:          domain.OpStateCompleted,
		RequestedClass: domain.ClassReversibleMutation,
		EffectiveClass: domain.ClassReversibleMutation,
		CreatedAt:      time.Now(),
	}
	if err := operations.SaveRecord("", rec); err != nil {
		t.Errorf("expected nil for empty dir, got: %v", err)
	}

	// 2. SaveRecord with invalid record fails validation
	invalidRec := rec
	invalidRec.SchemaVersion = "99"
	if err := operations.SaveRecord(dir, invalidRec); err == nil {
		t.Errorf("expected validation error for invalid ID")
	}

	// 3. Save valid record and read back
	if err := operations.SaveRecord(dir, rec); err != nil {
		t.Fatalf("SaveRecord failed: %v", err)
	}
	readRec, err := operations.ReadRecord(dir, rec.ID)
	if err != nil || readRec.ID != rec.ID {
		t.Fatalf("ReadRecord failed: %v", err)
	}

	// 4. ReadRecord invalid opID
	if _, err := operations.ReadRecord(dir, "bad-id"); err == nil {
		t.Errorf("expected error reading invalid ID")
	}

	// 5. ReadRecord non-existent
	if _, err := operations.ReadRecord(dir, "op-00000000000000000000000000000999"); !errors.Is(err, operations.ErrOperationNotFound) {
		t.Errorf("expected ErrOperationNotFound, got: %v", err)
	}

	// 6. ReadRecord symlink detection
	targetFile := filepath.Join(dir, rec.ID+".json")
	symlinkPath := filepath.Join(dir, "op-00000000000000000000000000000002.json")
	_ = os.Symlink(targetFile, symlinkPath)
	if _, err := operations.ReadRecord(dir, "op-00000000000000000000000000000002"); err == nil {
		t.Errorf("expected error on symlinked record")
	}
}

func TestOperationsStore_ListOptionsAndClamping(t *testing.T) {
	dir := t.TempDir()

	rec1 := domain.OperationRecord{
		SchemaVersion:  "1",
		ID:             "op-00000000000000000000000000000010",
		Actor:          "operator:test",
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Kind:           "machine.start",
		State:          domain.OpStateCompleted,
		RequestedClass: domain.ClassReversibleMutation,
		EffectiveClass: domain.ClassReversibleMutation,
		CreatedAt:      time.Now().Add(-10 * time.Minute),
	}
	rec2 := domain.OperationRecord{
		SchemaVersion:  "1",
		ID:             "op-00000000000000000000000000000020",
		Actor:          "operator:test",
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20002",
		Kind:           "machine.stop",
		State:          domain.OpStateFailed,
		RequestedClass: domain.ClassReversibleMutation,
		EffectiveClass: domain.ClassReversibleMutation,
		CreatedAt:      time.Now().Add(-5 * time.Minute),
	}

	_ = operations.SaveRecord(dir, rec1)
	_ = operations.SaveRecord(dir, rec2)

	// Filter by machine target
	listTarget, err := operations.ListRecords(dir, operations.ListOptions{Machine: "c4a523d4-6b99-4d62-a5e2-4752c0f20001"})
	if err != nil || len(listTarget) != 1 {
		t.Errorf("expected 1 result filtered by machine, got %d (err=%v)", len(listTarget), err)
	}

	// Filter by state
	listState, err := operations.ListRecords(dir, operations.ListOptions{State: domain.OpStateFailed})
	if err != nil || len(listState) != 1 {
		t.Errorf("expected 1 result filtered by state, got %d (err=%v)", len(listState), err)
	}

	// Non-existent dir returns empty list
	listEmpty, err := operations.ListRecords(filepath.Join(dir, "non-existent"), operations.ListOptions{})
	if err != nil || len(listEmpty) != 0 {
		t.Errorf("expected 0 results for non-existent dir, got %d (err=%v)", len(listEmpty), err)
	}
}

func makeTestActor(actorID string) domain.ActorContext {
	scopes := domain.NewScopeSet("machine:read", "machine:write", "audit:read", "operation:cancel")
	act, _ := domain.NewActorContext(domain.ActorID(actorID), domain.ActorID(actorID), scopes, scopes)
	return act
}
