package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
)

func coordinationGrantFixture(t *testing.T) (*ConsoleService, ConsoleLabGrant, domain.Operation, domain.ActorContext) {
	t.Helper()
	h := newGuestHarness(t)
	h.actor.AuthenticatedCaller, h.actor.EffectiveActor = "agent:mcp-local", "agent:mcp-local"
	h.actor.CallerPermissions[domain.ScopeMachineWrite] = struct{}{}
	h.actor.EffectivePermissions[domain.ScopeMachineWrite] = struct{}{}
	authority, operator := persistentGuestAuthority(t, h)
	grant, _, err := authority.IssueLabGrant(t.Context(), operator, ConsoleLabGrantIssueRequest{Mode: ConsoleLabModePersistentVM, Target: "default", Reason: "exercise VM coordination", IdempotencyKey: "coordination-grant", Beneficiary: "agent:mcp-local", AcknowledgeExternalEffects: true})
	if err != nil {
		t.Fatal(err)
	}
	op := domain.Operation{Kind: "console.input", Target: grant.Target, Actor: h.actor, Reason: "exercise coordinated input", IdempotencyKey: "coordinated-input", Deadline: time.Now().Add(time.Minute), Classification: domain.ClassDestructivePrivileged, RequiredCapability: string(domain.CapabilityConsoleInput), RequiredScopes: []string{domain.ScopeMachineWrite}, EvidenceSensitivity: domain.EvidenceSensitivityStandard, Parameters: domain.ConsoleInputParameters(domain.ConsoleInput{Kind: "key", Key: "enter"})}
	return authority, grant, op, operator
}

func TestPersistentRecoveryCoordinationBindsApprovalAndFencesRevocation(t *testing.T) {
	authority, grant, op, operator := coordinationGrantFixture(t)
	recovery := authority.recovery
	WithRecoveryLabAuthority(authority)(recovery)
	req := MutationRequest{TargetID: string(op.Target), Actor: op.Actor, Reason: op.Reason, IdempotencyKey: op.IdempotencyKey, Deadline: op.Deadline}
	dispatched := false
	admitted, execute, release, err := recovery.coordinatePersistentRecovery(t.Context(), op, req, func(context.Context) error { dispatched = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })
	if admitted.Approval == nil || admitted.ApprovalID != string(admitted.Approval.ID) {
		t.Fatal("missing exact derived approval")
	}
	if err := admitted.Approval.Matches(op); err != nil {
		t.Fatalf("derived approval binding: %v", err)
	}
	revoke := ConsoleLabGrantRevokeRequest{GrantID: grant.GrantID, Reason: "end coordinated authority", IdempotencyKey: "revoke-coordination", Deadline: time.Now().Add(time.Minute).Format(time.RFC3339Nano)}
	if _, err := authority.RevokeLabGrant(t.Context(), operator, revoke); !errors.Is(err, lease.ErrLeaseConflict) {
		t.Fatalf("revocation crossed active grant lock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.RevokeLabGrant(t.Context(), operator, revoke); err != nil {
		t.Fatal(err)
	}
	if err := execute(t.Context()); !errors.Is(err, ErrInvalidConsoleLabGrant) || dispatched {
		t.Fatalf("revoked fenced callback dispatched=%v error=%v", dispatched, err)
	}
}

func TestPersistentRecoveryCoordinationRejectsBusyGrant(t *testing.T) {
	authority, grant, op, _ := coordinationGrantFixture(t)
	WithRecoveryLabAuthority(authority)(authority.recovery)
	held, err := authority.labLock(t.Context(), grant.GrantID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held() })
	_, _, release, err := authority.recovery.coordinatePersistentRecovery(t.Context(), op, MutationRequest{}, func(context.Context) error { t.Error("busy grant dispatched"); return nil })
	if !errors.Is(err, lease.ErrLeaseConflict) {
		t.Fatalf("busy grant admission: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentRecoveryCoordinationStorageFailureReleasesGrant(t *testing.T) {
	authority, grant, op, _ := coordinationGrantFixture(t)
	WithRecoveryLabAuthority(authority)(authority.recovery)
	blocked := filepath.Join(t.TempDir(), "blocked-approval-store")
	if err := os.WriteFile(blocked, []byte("synthetic storage conflict"), 0600); err != nil {
		t.Fatal(err)
	}
	authority.recovery.approvalStore = approval.NewStore(blocked)
	_, _, release, err := authority.recovery.coordinatePersistentRecovery(t.Context(), op, MutationRequest{}, func(context.Context) error { t.Error("failed storage admission dispatched"); return nil })
	if err == nil {
		t.Fatal("unwritable approval store admitted mutation")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release, err = authority.labLock(t.Context(), grant.GrantID)
	if err != nil {
		t.Fatalf("failed approval retained grant lock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}
