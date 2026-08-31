package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

type exactRetryBackend struct{}

func (exactRetryBackend) Doctor(context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}
func (exactRetryBackend) ListMachines(context.Context) ([]domain.MachineObservation, error) {
	locator, _ := domain.NewMachineLocator(domain.LocalHostID, exactRetryVMID)
	return []domain.MachineObservation{{
		HostID: domain.LocalHostID, Locator: locator, ID: exactRetryVMID, Name: "synthetic-exact-retry-vm",
		State: domain.MachineStateRunning, RawState: "Running", Generation: 2, Version: "10.0",
		MemoryAssignedBytes: 1024, Capabilities: domain.DirectMachineCapabilities(),
		ObservedAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC), ObservationType: domain.ObservationObserved,
	}}, nil
}
func (exactRetryBackend) InspectMachine(context.Context, string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}
func (exactRetryBackend) Capabilities(context.Context, string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(domain.CapabilityMachineStart), nil
}
func (exactRetryBackend) StartMachine(context.Context, string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}
func (exactRetryBackend) StopMachine(context.Context, string, string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}
func (exactRetryBackend) ListCheckpoints(context.Context, string) ([]domain.CheckpointObservation, error) {
	return nil, nil
}
func (exactRetryBackend) CreateCheckpoint(context.Context, string, string) (domain.CheckpointObservation, error) {
	return domain.CheckpointObservation{}, nil
}
func (exactRetryBackend) RestoreCheckpoint(context.Context, string, string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

type exactRetryTransport struct{ dials atomic.Int32 }

func (t *exactRetryTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	t.dials.Add(1)
	return nil, errors.New("guest transport must not run for an exact retry")
}

type exactRetryFixture struct {
	sd          *statedir.StateDir
	now         time.Time
	principal   domain.ActorID
	target      domain.MachineRef
	op          domain.Operation
	observation *domain.SessionObservation
	receipt     domain.Receipt
	journal     *sessions.MutationJournal
}

const exactRetryVMID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

func newExactRetryFixture(t *testing.T, root string) exactRetryFixture {
	t.Helper()
	sd, err := statedir.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	principal := domain.ActorID("agent:daemon-exact-retry")
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen)
	actor, err := domain.NewActorContext(principal, principal, scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := domain.NewMachineLocator(domain.LocalHostID, exactRetryVMID)
	if err != nil {
		t.Fatal(err)
	}
	targetStore, err := target.NewStore(sd.TargetsDir())
	if err != nil {
		t.Fatal(err)
	}
	defaultTarget, err := target.NewDefault(locator, []string{"primary"})
	if err != nil {
		t.Fatal(err)
	}
	if publication, saveErr := targetStore.Save(context.Background(), defaultTarget); saveErr != nil || !publication.Durable {
		t.Fatalf("seed target: publication=%+v err=%v", publication, saveErr)
	}
	target := domain.MachineRef(locator.String())
	op := domain.Operation{
		Kind: "session.open", Target: target, Actor: actor,
		Reason: "daemon exact retry", Deadline: now.Add(time.Minute), IdempotencyKey: "idem-daemon-finalizing-open",
		RequiredCapability: domain.CapabilitySessionOpen, RequiredScopes: []string{domain.ScopeSessionOpen},
		Classification: domain.ClassReversibleMutation, EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{"cols": uint16(80), "rows": uint16(24), "term": domain.DefaultTermType},
	}
	journal := sessions.NewMutationJournal(filepath.Join(sd.SessionsDir(), "mutations"))
	if _, err := journal.Reserve(op, now); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := op.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	idempotencyFingerprint, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		t.Fatal(err)
	}
	observation := &domain.SessionObservation{
		ID: "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4", Target: target, OwnerActor: principal,
		State: domain.SessionStateActive, CreatedAt: now, LastActivityAt: now,
		Cols: 80, Rows: 24, TermType: domain.DefaultTermType, ObservationType: domain.ObservationObserved,
	}
	receipt := domain.Receipt{
		ReceiptID: "rcpt-0123456789abcdef0123456789abcdef", OperationKind: op.Kind,
		Fingerprint: fingerprint, IdempotencyFingerprint: idempotencyFingerprint, IdempotencyKey: op.IdempotencyKey,
		Actor: principal, Target: target, Class: op.Classification, EffectiveBackend: "amcd",
		StartedAt: now, CompletedAt: now.Add(time.Second), Outcome: domain.ExecutionOutcome{Status: domain.OutcomeSuccess},
		ObservationType: domain.ObservationObserved, RollbackRef: "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		RedactionStatus: domain.RedactionApplied, EvidenceRefs: []string{string(observation.ID)},
	}
	effectApplied := true
	if err := journal.RecordFinalizationIntent(op, receipt, sessions.MutationResult{Observation: observation, EffectApplied: &effectApplied}, now); err != nil {
		t.Fatal(err)
	}
	return exactRetryFixture{
		sd: sd, now: now, principal: principal, target: target, op: op,
		observation: observation, receipt: receipt, journal: journal,
	}
}

func newExactRetryServer(t *testing.T, root string, fixture exactRetryFixture, transport *exactRetryTransport) *Server {
	t.Helper()
	srv, err := NewServer(Config{
		StateDir: root, Backend: exactRetryBackend{}, Transport: transport, Clock: func() time.Time { return fixture.now.Add(2 * time.Second) },
		PrincipalResolver: func() (string, error) { return string(fixture.principal), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func serveExactRetryOpen(t *testing.T, srv *Server, fixture exactRetryFixture) SessionOpenResponse {
	t.Helper()
	token, err := auth.ReadTokenFile(fixture.sd.AuthDir(), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(SessionOpenRequest{Target: exactRetryVMID, Reason: fixture.op.Reason, IdempotencyKey: fixture.op.IdempotencyKey})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response SessionOpenResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestDaemonHTTPExactRetryReconcilesFinalizingOpenWithoutGuestTransport(t *testing.T) {
	root := t.TempDir()
	fixture := newExactRetryFixture(t, root)
	transport := &exactRetryTransport{}
	srv := newExactRetryServer(t, root, fixture, transport)
	defer func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()
	response := serveExactRetryOpen(t, srv, fixture)
	if response.Receipt == nil || response.Receipt.ReceiptID != string(fixture.receipt.ReceiptID) || response.Session.SessionID != string(fixture.observation.ID) {
		t.Fatalf("reconciled response = %+v", response)
	}
	if got := transport.dials.Load(); got != 0 {
		t.Fatalf("guest transport dials = %d, want 0", got)
	}
	record, err := fixture.journal.Lookup(fixture.op)
	if err != nil || record == nil || record.State != sessions.MutationReservationFinalized {
		t.Fatalf("reservation after route = %+v err %v", record, err)
	}
}
