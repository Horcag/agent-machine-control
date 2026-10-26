package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestPersistentVMGrantCoordinatesUncontainedGuestAndSurvivesRestart(t *testing.T) {
	h := newGuestHarness(t)
	h.safety.destructive = true
	h.actor.AuthenticatedCaller, h.actor.EffectiveActor = "agent:mcp-local", "agent:mcp-local"
	coordinator := h.service.session
	authority, operator := persistentGuestAuthority(t, h)
	grant, _, err := authority.IssueLabGrant(t.Context(), operator, ConsoleLabGrantIssueRequest{Mode: ConsoleLabModePersistentVM, Target: "default", Reason: "operate disposable VM", IdempotencyKey: "persistent-guest", Beneficiary: "agent:mcp-local", AcknowledgeExternalEffects: true})
	if err != nil {
		t.Fatal(err)
	}
	// A new service must discover protected on-disk authority without a console backend.
	coordinator.labAuthority = NewConsoleService(nil, authority.recovery, authority.target, authority.framesDir, WithConsoleLabEnrollmentIdentity(authority.labEnrollment))
	if !grant.ExpiresAt.IsZero() {
		t.Fatal("persistent guest authority acquired a TTL")
	}
	var artifact GuestArtifact
	for i, operation := range []string{"prepare", "put", "get", "exec", "remove-owned"} {
		t.Run(operation, func(t *testing.T) {
			artifact = exercisePersistentGuestOperation(t, h, operation, artifact)
			if h.transport.executed != i+1 {
				t.Fatalf("guest dispatched %d times; expected %d", h.transport.executed, i+1)
			}
		})
		if t.Failed() {
			t.FailNow()
		}
	}
	if _, err := authority.RevokeLabGrant(t.Context(), operator, ConsoleLabGrantRevokeRequest{GrantID: grant.GrantID, Reason: "end guest lab", IdempotencyKey: "revoke-guest", Deadline: time.Now().Add(time.Minute).Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Execute(t.Context(), h.params("prepare", "revoked-prepare"), nil, nil); err == nil || h.transport.executed != 5 {
		t.Fatalf("revoked guest dispatched: %v effects=%d", err, h.transport.executed)
	}
}

func exercisePersistentGuestOperation(t *testing.T, h *guestHarness, operation string, artifact GuestArtifact) GuestArtifact {
	t.Helper()
	p := h.params(operation, "persistent-"+operation)
	var output bytes.Buffer
	payload := stream.BytesInput([]byte("data"))
	sink := stream.BufferOutput(&output)
	fixtures := map[string]struct {
		size      int64
		sha       string
		artifacts []string
		maxOutput int64
		input     stream.ContextReader
		output    stream.ContextWriter
	}{
		"prepare":      {},
		"put":          {size: 4, sha: guestSHA("data"), input: payload},
		"get":          {artifacts: []string{artifact.ID}, maxOutput: 10, output: sink},
		"exec":         {size: 4, sha: guestSHA("data"), maxOutput: 10, input: payload, output: sink},
		"remove-owned": {artifacts: []string{artifact.ID}},
	}
	fixture := fixtures[operation]
	p.Size, p.SHA256, p.ArtifactIDs, p.MaxOutput = fixture.size, fixture.sha, fixture.artifacts, fixture.maxOutput
	result, err := h.service.Execute(t.Context(), p, fixture.input, fixture.output)
	if err != nil || result.Receipt == nil || result.Receipt.Outcome.Status != domain.OutcomeSuccess {
		t.Fatalf("persistent guest: %+v %v", result, err)
	}
	switch operation {
	case "put":
		if len(result.Artifacts) != 1 {
			t.Fatalf("put artifacts: %+v", result.Artifacts)
		}
		return result.Artifacts[0]
	case "get":
		if output.String() != "data" || result.SHA256 != artifact.SHA256 {
			t.Fatalf("get payload: %+v %q", result, output.String())
		}
	case "exec":
		if output.String() != "ok" {
			t.Fatalf("exec output: %q", output.String())
		}
	case "remove-owned":
		if len(result.Removed) != 1 || !result.Removed[0].Absent || !result.Removed[0].Accepted {
			t.Fatalf("remove outcome: %+v", result.Removed)
		}
	}
	return artifact
}

func persistentGuestAuthority(t *testing.T, h *guestHarness) (*ConsoleService, domain.ActorContext) {
	t.Helper()
	coordinator := h.service.session
	resolution := h.service.resolver.(guestTestResolver).resolution
	now := time.Now().UTC()
	inventory, err := NewTrustedInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	observed := domain.MachineObservation{HostID: domain.LocalHostID, Locator: resolution.Locator, ID: resolution.ProviderVMID, Name: "synthetic-lab", State: domain.MachineStateOff, RawState: "Off", Generation: 2, Version: "10.0", MemoryAssignedBytes: 1024, Capabilities: domain.ReadOnlyMachineCapabilities(), ObservedAt: now, ObservationType: domain.ObservationObserved}
	if err := inventory.ApplySnapshot(HostSnapshot{HostID: domain.LocalHostID, Health: HostHealthObserved, Machines: []domain.MachineObservation{observed}}); err != nil {
		t.Fatal(err)
	}
	state, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	// Use the canonical state initializer so Windows creates the directory with
	// the current user owner and protected DACL from the start.
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	store, err := target.NewStore(state.TargetsDir())
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := target.NewDefault(resolution.Locator, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(t.Context(), enrolled); err != nil {
		t.Fatal(err)
	}
	targets, err := NewTargetService(inventory, store)
	if err != nil {
		t.Fatal(err)
	}
	recovery := &RecoveryService{backend: &guestApprovalBackend{}, leaseManager: coordinator.leaseMgr, auditStore: coordinator.auditStore, receiptStore: coordinator.receiptStore, approvalStore: coordinator.approvalStore}
	epoch := strings.Repeat("a", 64)
	options := []ConsoleOption{WithConsoleLabEnrollmentIdentity(func(_ context.Context, _ domain.MachineRef) (string, error) { return epoch, nil })}
	authority := NewConsoleService(nil, recovery, targets, filepath.Join(state.Root(), "console-frames"), options...)
	scopes := domain.NewScopeSet(domain.ScopeOperationAdmin)
	operator, err := domain.NewActorContext("operator:synthetic", "operator:synthetic", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}

	return authority, operator
}
