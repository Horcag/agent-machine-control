package operations_test

import (
	"context"
	"errors"
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

type mockBackend struct {
	startCount        atomic.Int64
	blockStart        chan struct{}
	capabilitiesFn    func(context.Context, string) (domain.CapabilitySet, error)
	listCheckpointsFn func(context.Context, string) ([]domain.CheckpointObservation, error)
}

func (m *mockBackend) Doctor(_ context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}

func (m *mockBackend) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	return nil, nil
}

func (m *mockBackend) InspectMachine(_ context.Context, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func (m *mockBackend) Capabilities(ctx context.Context, target string) (domain.CapabilitySet, error) {
	if m.capabilitiesFn != nil {
		return m.capabilitiesFn(ctx, target)
	}
	return domain.NewCapabilitySet(domain.CapabilityMachineStart, domain.CapabilityMachineStop), nil
}

func (m *mockBackend) StartMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	m.startCount.Add(1)
	if m.blockStart != nil {
		select {
		case <-m.blockStart:
		case <-ctx.Done():
			return domain.MachineObservation{}, ctx.Err()
		}
	}
	return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
}

func (m *mockBackend) StopMachine(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func (m *mockBackend) ListCheckpoints(ctx context.Context, id string) ([]domain.CheckpointObservation, error) {
	if m.listCheckpointsFn != nil {
		return m.listCheckpointsFn(ctx, id)
	}
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

func (m *mockBackend) CreateCheckpoint(_ context.Context, _ string, _ string) (domain.CheckpointObservation, error) {
	return domain.CheckpointObservation{}, nil
}

func (m *mockBackend) RestoreCheckpoint(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func setupTestManager(t *testing.T, backend app.Backend, opts ...operations.Option) (*operations.Manager, *events.Hub, string) {
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
	mgr := operations.NewManager(dir, recoverySvc, rcptStore, auditStore, eventHub, opts...)
	t.Cleanup(func() {
		_ = mgr.Shutdown(context.Background())
	})
	return mgr, eventHub, dir
}

func TestManager_SubmitAndComplete(t *testing.T) {
	b := &mockBackend{}
	mgr, hub, _ := setupTestManager(t, b)

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	act := domain.ActorContext{
		AuthenticatedCaller:  "operator:local",
		EffectiveActor:       "operator:local",
		CallerPermissions:    domain.NewScopeSet("machine:write"),
		EffectivePermissions: domain.NewScopeSet("machine:write"),
	}

	op := domain.Operation{
		Kind:                "machine.start",
		Target:              domain.MachineRef(targetID),
		Actor:               act,
		Reason:              "test start",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      "key-1",
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	rec, wasExisting, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	if wasExisting {
		t.Errorf("expected new operation, got wasExisting=true")
	}

	// Wait for terminal completion
	ch, unsub, err := hub.Subscribe(context.Background(), rec.ID, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer unsub()

	for ev := range ch {
		if ev.State == domain.OpStateCompleted {
			break
		}
	}

	// Verify on disk
	finalRec, err := mgr.Get(rec.ID, act)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if finalRec.State != domain.OpStateCompleted {
		t.Errorf("expected completed state, got %s", finalRec.State)
	}
	if finalRec.ReceiptID == "" {
		t.Errorf("expected receipt ID to be populated on completion")
	}
}

func TestManager_Concurrent50Submissions_Deduplicated(t *testing.T) {
	b := &mockBackend{blockStart: make(chan struct{})}
	mgr, _, _ := setupTestManager(t, b)

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	act := domain.ActorContext{
		AuthenticatedCaller:  "operator:local",
		EffectiveActor:       "operator:local",
		CallerPermissions:    domain.NewScopeSet("machine:write"),
		EffectivePermissions: domain.NewScopeSet("machine:write"),
	}

	op := domain.Operation{
		Kind:                "machine.start",
		Target:              domain.MachineRef(targetID),
		Actor:               act,
		Reason:              "test concurrent",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      "key-concurrent-50",
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	const concurrency = 50
	var wg sync.WaitGroup
	results := make([]*domain.OperationRecord, concurrency)
	errs := make([]error, concurrency)

	for i := range concurrency {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec, _, err := mgr.Submit(context.Background(), op, 30*time.Second)
			results[idx] = rec
			errs[idx] = err
		}(i)
	}

	wg.Wait()

	// Unblock execution
	close(b.blockStart)

	firstID := results[0].ID
	for i := range concurrency {
		if errs[i] != nil {
			t.Fatalf("submit %d failed: %v", i, errs[i])
		}
		if results[i].ID != firstID {
			t.Errorf("expected all submitters to get same op ID %s, got %s at index %d", firstID, results[i].ID, i)
		}
	}

	var reached bool
	for range 40 {
		if b.startCount.Load() == 1 {
			reached = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !reached {
		t.Errorf("expected exactly 1 backend execution, got %d", b.startCount.Load())
	}
}

func TestManager_CancelOperation(t *testing.T) {
	blockChan := make(chan struct{})
	b := &mockBackend{blockStart: blockChan}
	mgr, hub, _ := setupTestManager(t, b)

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	act := domain.ActorContext{
		AuthenticatedCaller:  "agent:mcp-local",
		EffectiveActor:       "agent:mcp-local",
		CallerPermissions:    domain.NewScopeSet("machine:write"),
		EffectivePermissions: domain.NewScopeSet("machine:write"),
	}

	op := domain.Operation{
		Kind:                "machine.start",
		Target:              domain.MachineRef(targetID),
		Actor:               act,
		Reason:              "test cancel",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      "key-cancel",
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	rec, _, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	ch, unsub, err := hub.Subscribe(context.Background(), rec.ID, 0)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer unsub()

	// Wait until running
	for ev := range ch {
		if ev.State == domain.OpStateRunning {
			break
		}
	}

	// Cancel
	if err := mgr.Cancel(rec.ID, act, "user requested cancel"); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	for ev := range ch {
		if ev.State == domain.OpStateCancelled {
			break
		}
	}

	finalRec, err := mgr.Get(rec.ID, act)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if finalRec.State != domain.OpStateCancelled {
		t.Errorf("expected cancelled state, got %s", finalRec.State)
	}
}

func TestManager_ActorIsolation(t *testing.T) {
	b := &mockBackend{}
	mgr, _, _ := setupTestManager(t, b)

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	act1 := domain.ActorContext{
		AuthenticatedCaller:  "agent:mcp-1",
		EffectiveActor:       "agent:mcp-1",
		CallerPermissions:    domain.NewScopeSet("machine:write"),
		EffectivePermissions: domain.NewScopeSet("machine:write"),
	}

	op := domain.Operation{
		Kind:                "machine.start",
		Target:              domain.MachineRef(targetID),
		Actor:               act1,
		Reason:              "test isolation",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      "key-iso",
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	rec, _, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	act2 := domain.ActorContext{
		AuthenticatedCaller:  "agent:mcp-2",
		EffectiveActor:       "agent:mcp-2",
		CallerPermissions:    domain.NewScopeSet("machine:write"),
		EffectivePermissions: domain.NewScopeSet("machine:write"),
	}

	// act2 query should fail with ErrOperationNotFound
	_, err = mgr.Get(rec.ID, act2)
	if !errors.Is(err, operations.ErrOperationNotFound) {
		t.Fatalf("expected ErrOperationNotFound for cross-actor query, got: %v", err)
	}

	// act2 cancel should fail with ErrOperationNotFound
	err = mgr.Cancel(rec.ID, act2, "malicious cancel")
	if !errors.Is(err, operations.ErrOperationNotFound) {
		t.Fatalf("expected ErrOperationNotFound for cross-actor cancel, got: %v", err)
	}

	// operator query should succeed
	opAct := domain.ActorContext{
		AuthenticatedCaller:  "operator:admin",
		EffectiveActor:       "operator:admin",
		CallerPermissions:    domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"),
		EffectivePermissions: domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"),
	}
	fetched, err := mgr.Get(rec.ID, opAct)
	if err != nil || fetched == nil {
		t.Fatalf("expected operator to access operation, got: %v", err)
	}
}

func TestManager_ListFiltersAndTerminalRetry(t *testing.T) {
	b := &mockBackend{}
	mgr, hub, dir := setupTestManager(t, b)

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	act := domain.ActorContext{
		AuthenticatedCaller:  "operator:local",
		EffectiveActor:       "operator:local",
		CallerPermissions:    domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"),
		EffectivePermissions: domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"),
	}

	op := domain.Operation{
		Kind:                "machine.start",
		Target:              domain.MachineRef(targetID),
		Actor:               act,
		Reason:              "test list and retry",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      "key-list-1",
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	rec, _, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	// Wait for completion
	ch, unsub, _ := hub.Subscribe(context.Background(), rec.ID, 0)
	defer unsub()
	for ev := range ch {
		if ev.State == domain.OpStateCompleted {
			break
		}
	}

	// 1. Terminal retry with exact parameters returns existing completed record
	recRetry, wasExisting, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil {
		t.Fatalf("Retry failed: %v", err)
	}
	if !wasExisting || recRetry.ID != rec.ID {
		t.Errorf("expected existing completed record, got ID %s wasExisting %v", recRetry.ID, wasExisting)
	}

	// 2. Conflict on different target with same idempotency key
	conflictOp := op
	conflictOp.Target = "c4a523d4-6b99-4d62-a5e2-4752c0f20002"
	_, _, err = mgr.Submit(context.Background(), conflictOp, 30*time.Second)
	if !errors.Is(err, operations.ErrOperationConflict) {
		t.Errorf("expected ErrOperationConflict, got %v", err)
	}

	// 3. Cancel already terminal operation returns ErrOperationTerminal
	err = mgr.Cancel(rec.ID, act, "cancel terminal")
	if !errors.Is(err, operations.ErrOperationTerminal) {
		t.Errorf("expected ErrOperationTerminal, got %v", err)
	}

	// 4. List with State filter
	list, err := mgr.List(operations.ListOptions{
		State: domain.OpStateCompleted,
	}, act)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) == 0 {
		t.Errorf("expected at least 1 completed operation")
	}

	// 5. List with Machine filter
	list, err = mgr.List(operations.ListOptions{
		Machine: domain.MachineRef(targetID),
		Limit:   10,
	}, act)
	if err != nil {
		t.Fatalf("List by machine failed: %v", err)
	}
	if len(list) == 0 {
		t.Errorf("expected at least 1 operation for machine")
	}

	// 6. Read non-existent record
	_, err = operations.ReadRecord(dir, "op-00000000000000000000000000000000")
	if !errors.Is(err, operations.ErrOperationNotFound) {
		t.Errorf("expected ErrOperationNotFound, got %v", err)
	}

	// 7. Read invalid record ID format
	_, err = operations.ReadRecord(dir, "op-nonexistent")
	if !errors.Is(err, domain.ErrInvalidOperationID) {
		t.Errorf("expected ErrInvalidOperationID, got %v", err)
	}
}

type failingBackend struct{}

func (f *failingBackend) Doctor(_ context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}
func (f *failingBackend) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	return nil, nil
}
func (f *failingBackend) InspectMachine(_ context.Context, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}
func (f *failingBackend) Capabilities(_ context.Context, _ string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(domain.CapabilityMachineStart), nil
}
func (f *failingBackend) StartMachine(_ context.Context, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, errors.New("hyperv start failure error")
}
func (f *failingBackend) StopMachine(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}
func (f *failingBackend) ListCheckpoints(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
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
func (f *failingBackend) CreateCheckpoint(_ context.Context, _ string, _ string) (domain.CheckpointObservation, error) {
	return domain.CheckpointObservation{}, nil
}
func (f *failingBackend) RestoreCheckpoint(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func TestManager_ExecutionFailureAndStoreEdgeCases(t *testing.T) {
	b := &failingBackend{}
	mgr, hub, dir := setupTestManager(t, b)

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	act := domain.ActorContext{
		AuthenticatedCaller:  "operator:local",
		EffectiveActor:       "operator:local",
		CallerPermissions:    domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"),
		EffectivePermissions: domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"),
	}

	op := domain.Operation{
		Kind:                "machine.start",
		Target:              domain.MachineRef(targetID),
		Actor:               act,
		Reason:              "test failure",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      "key-fail-1",
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	rec, _, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	ch, unsub, _ := hub.Subscribe(context.Background(), rec.ID, 0)
	defer unsub()
	for ev := range ch {
		if ev.State == domain.OpStateFailed {
			break
		}
	}

	finalRec, err := mgr.Get(rec.ID, act)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if finalRec.State != domain.OpStateFailed {
		t.Errorf("expected failed state, got %s", finalRec.State)
	}

	// Store edge cases: empty ID
	if err := operations.SaveRecord(dir, domain.OperationRecord{}); err == nil {
		t.Errorf("expected error saving empty record")
	}
	if _, err := operations.ReadRecord(dir, ""); err == nil {
		t.Errorf("expected error reading empty record ID")
	}
}

type blockingBackend struct {
	started chan struct{}
	unblock chan struct{}
}

func (b *blockingBackend) Doctor(_ context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}
func (b *blockingBackend) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	return nil, nil
}
func (b *blockingBackend) InspectMachine(_ context.Context, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}
func (b *blockingBackend) Capabilities(_ context.Context, _ string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(domain.CapabilityMachineStart, domain.CapabilityMachineStop), nil
}
func (b *blockingBackend) StartMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	close(b.started)
	select {
	case <-b.unblock:
		return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
	case <-ctx.Done():
		return domain.MachineObservation{}, ctx.Err()
	}
}
func (b *blockingBackend) StopMachine(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}
func (b *blockingBackend) ListCheckpoints(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
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
func (b *blockingBackend) CreateCheckpoint(_ context.Context, _ string, _ string) (domain.CheckpointObservation, error) {
	return domain.CheckpointObservation{}, nil
}
func (b *blockingBackend) RestoreCheckpoint(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func TestManager_CancelActiveInFlight(t *testing.T) {
	bb := &blockingBackend{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
	mgr, hub, _ := setupTestManager(t, bb)

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	act := domain.ActorContext{
		AuthenticatedCaller:  "operator:local",
		EffectiveActor:       "operator:local",
		CallerPermissions:    domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"),
		EffectivePermissions: domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"),
	}

	op := domain.Operation{
		Kind:                "machine.start",
		Target:              domain.MachineRef(targetID),
		Actor:               act,
		Reason:              "test cancel active",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      "key-cancel-active-1",
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	rec, _, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	// Wait for execution to start
	<-bb.started

	// Cancel active operation
	err = mgr.Cancel(rec.ID, act, "user requested cancellation")
	if err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	// Subscribe to verify terminal cancelled event
	ch, unsub, _ := hub.Subscribe(context.Background(), rec.ID, 0)
	defer unsub()
	for ev := range ch {
		if ev.State == domain.OpStateCancelled {
			break
		}
	}

	finalRec, err := mgr.Get(rec.ID, act)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if finalRec.State != domain.OpStateCancelled {
		t.Errorf("expected cancelled state, got %s", finalRec.State)
	}
}

func TestManager_WithClockOption(_ *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	_ = operations.NewManager("", nil, nil, nil, nil, operations.WithClock(func() time.Time { return now }))
}
