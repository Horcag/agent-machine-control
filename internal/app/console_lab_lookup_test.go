package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestPersistentGuestGrantReportsSecondInspectionFailureAndRecovers(t *testing.T) {
	h := newGuestHarness(t)
	h.safety.destructive = true
	h.actor.AuthenticatedCaller, h.actor.EffectiveActor = "agent:mcp-local", "agent:mcp-local"
	authority, operator := persistentGuestAuthority(t, h)
	grant, _, err := authority.IssueLabGrant(t.Context(), operator, ConsoleLabGrantIssueRequest{Mode: ConsoleLabModePersistentVM, Target: "default", Reason: "exercise provider recovery", IdempotencyKey: "lookup-grant", Beneficiary: "agent:mcp-local", AcknowledgeExternalEffects: true})
	if err != nil {
		t.Fatal(err)
	}
	h.service.session.labAuthority = authority
	resolution := h.service.resolver.(guestTestResolver).resolution
	observation := targetObservation(t, domain.LocalHostID, resolution.ProviderVMID, "synthetic-lab")
	lookupErr := &os.PathError{Op: "inspect", Path: "synthetic-provider", Err: os.ErrNotExist}
	observer := &exactTargetObserver{}
	observer.inspect = func(context.Context, string) (domain.MachineObservation, error) {
		if observer.calls == 2 {
			return domain.MachineObservation{}, lookupErr
		}
		return observation, nil
	}
	authority.target.observer = observer
	_, err = h.service.Execute(t.Context(), h.params("prepare", "failed-lookup"), nil, nil)
	if !errors.Is(err, lookupErr) || observer.calls != 2 || h.transport.prepared != 0 || h.transport.executed != 0 {
		t.Fatalf("lookup cause lost or dispatched: error=%v queries=%d prepare=%d execute=%d", err, observer.calls, h.transport.prepared, h.transport.executed)
	}
	result, err := h.service.Execute(t.Context(), h.params("prepare", "recovered-lookup"), nil, nil)
	if err != nil || result.Receipt == nil || h.transport.executed != 1 {
		t.Fatalf("recovery required new authority: %+v %v effects=%d", result, err, h.transport.executed)
	}
	status, err := authority.ActiveLabGrant(t.Context(), h.actor, "default")
	if err != nil || status.Grant.GrantID != grant.GrantID || !status.Grant.ExpiresAt.IsZero() {
		t.Fatalf("persistent authority changed: %+v %v", status, err)
	}
}

func TestPersistentGrantDiscoveryReportsEnrollmentReadFailure(t *testing.T) {
	authority, grant, op, _ := coordinationGrantFixture(t)
	lookupErr := errors.New("synthetic enrollment read failure")
	original := authority.labEnrollment
	authority.labEnrollment = func(context.Context, domain.MachineRef) (string, error) { return "", lookupErr }
	status, err := authority.ActiveLabGrant(t.Context(), op.Actor, "default")
	if !errors.Is(err, lookupErr) || status.State == "active" {
		t.Fatalf("enrollment cause lost: %+v %v", status, err)
	}
	authority.labEnrollment = original
	assertRecoveredLabGrant(t, authority, op.Actor, grant)
}

func TestPersistentGrantDiscoveryRejectsInvalidProtectedDocuments(t *testing.T) {
	for _, suffix := range []string{".revoked", ".active"} {
		t.Run(suffix, func(t *testing.T) {
			authority, grant, op, _ := coordinationGrantFixture(t)
			restore := replaceLabDocumentWithDirectory(t, authority, grant, suffix)
			status, err := authority.ActiveLabGrant(t.Context(), op.Actor, "default")
			if err != nil || status.State != "disabled" {
				t.Fatalf("invalid protected document conferred authority: %+v %v", status, err)
			}
			restore()
			assertRecoveredLabGrant(t, authority, op.Actor, grant)
		})
	}
}

func replaceLabDocumentWithDirectory(t *testing.T, authority *ConsoleService, grant ConsoleLabGrant, suffix string) func() {
	t.Helper()
	path := filepath.Join(authority.labDirectory(), grant.GrantID+suffix)
	if suffix == ".active" {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if suffix == ".active" {
			if err := authority.activateLabGrant(t.Context(), grant.GrantID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func assertRecoveredLabGrant(t *testing.T, authority *ConsoleService, actor domain.ActorContext, grant ConsoleLabGrant) {
	t.Helper()
	status, err := authority.ActiveLabGrant(t.Context(), actor, "default")
	if err != nil || status.State != "active" || status.Grant.GrantID != grant.GrantID {
		t.Fatalf("verification recovery required renewal: %+v %v", status, err)
	}
}
