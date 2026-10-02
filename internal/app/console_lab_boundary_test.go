package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
)

func labOperation(t *testing.T, grant app.ConsoleLabGrant) (domain.Operation, app.MutationRequest, string) {
	t.Helper()
	actor := labActor(t)
	req := app.MutationRequest{TargetID: string(grant.Target), Actor: actor, Reason: "control disposable guest", IdempotencyKey: "desktop-test", Deadline: grant.ExpiresAt}
	op := domain.Operation{Kind: "desktop.action", Target: grant.Target, Actor: actor, Reason: req.Reason, IdempotencyKey: req.IdempotencyKey, Deadline: req.Deadline,
		Classification: domain.ClassDestructivePrivileged, RequiredCapability: domain.CapabilityDesktopAction, RequiredScopes: []string{domain.ScopeMachineWrite}, EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{"action": "window.focus", "payload_sha256": strings.Repeat("a", 64)}}
	// Fixture backend advertises only console, so use a console operation for actual dispatch.
	op.Kind, op.RequiredCapability = "console.input", string(domain.CapabilityConsoleInput)
	op.Parameters = domain.ConsoleInputParameters(domain.ConsoleInput{Kind: "key", Key: "enter"})
	locator, err := domain.ParseMachineLocator(string(grant.Target))
	if err != nil {
		t.Fatal(err)
	}
	return op, req, locator.VMID
}

func TestConsoleLabGrantSerializesActionsAndRevocation(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	grant := issueLab(t, f)
	op, req, providerID := labOperation(t, grant)
	started, unblock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := f.service.ExecuteLabMutation(context.Background(), req.Actor, grant.GrantID, op, req, providerID, func(context.Context) error { close(started); <-unblock; return nil })
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("action failed before dispatch: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("action did not start")
	}
	r := app.ConsoleLabGrantRevokeRequest{GrantID: grant.GrantID, Reason: "revoke running lab", IdempotencyKey: "revoke-running", Deadline: f.now.Add(time.Minute).Format(time.RFC3339Nano)}
	_, revokeErr := f.service.RevokeLabGrant(context.Background(), f.actor, r)
	if !errors.Is(revokeErr, lease.ErrLeaseConflict) {
		close(unblock)
		<-done
		t.Fatalf("concurrent revoke = %v", revokeErr)
	}
	_, concurrentErr := f.service.ExecuteLabMutation(context.Background(), req.Actor, grant.GrantID, op, req, providerID, func(context.Context) error { t.Error("parallel action dispatched"); return nil })
	close(unblock)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(concurrentErr, lease.ErrLeaseConflict) {
		t.Fatalf("parallel action = %v", concurrentErr)
	}
	if _, err := f.service.RevokeLabGrant(context.Background(), f.actor, r); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ExecuteLabMutation(context.Background(), req.Actor, grant.GrantID, op, req, providerID, func(context.Context) error { t.Error("revoked action dispatched"); return nil }); err == nil {
		t.Fatal("revoked grant accepted")
	}
}

func TestConsoleLabGrantCannotAuthorizePowerOrSpoofOperationIdentity(t *testing.T) {
	for _, variant := range []string{"power", "actor", "provider", "reason", "approval", "delegation"} {
		t.Run(variant, func(t *testing.T) {
			f := newConsoleFixture(t)
			configureLab(f)
			grant := issueLab(t, f)
			op, req, providerID := labOperation(t, grant)
			actor := req.Actor
			switch variant {
			case "power":
				op.Kind = "machine.start"
				op.RequiredCapability = string(domain.CapabilityMachineStart)
				op.Parameters = nil
			case "actor":
				op.Actor = f.actor
			case "provider":
				providerID = "c4a523d4-6b99-4d62-a5e2-4752c0f20002"
			case "reason":
				req.Reason = "different request reason"
			case "approval":
				req.ApprovalID = "app-other"
			case "delegation":
				actor.AuthenticatedCaller = f.actor.AuthenticatedCaller
			}
			called := false
			if _, err := f.service.ExecuteLabMutation(context.Background(), actor, grant.GrantID, op, req, providerID, func(context.Context) error { called = true; return nil }); err == nil || called {
				t.Fatalf("%s authority accepted: %v", variant, err)
			}
		})
	}
}

func TestConsoleLabGrantUnsafeDocumentsFailClosed(t *testing.T) {
	for _, variant := range []string{"public", "symlink", "trailing", "unknown", "duplicate", "oversized"} {
		t.Run(variant, func(t *testing.T) {
			if variant == "public" && runtime.GOOS == "windows" {
				t.Skip("POSIX permission test; Windows privacy is enforced by DACL validation")
			}
			f := newConsoleFixture(t)
			configureLab(f)
			grant := issueLab(t, f)
			path := filepath.Join(f.root, "console-lab-grants", grant.GrantID+".json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			corruptLabGrant(t, path, data, variant)
			if _, err := f.service.Input(context.Background(), labActor(t), labRequest(grant)); err == nil || len(f.provider.inputs) != 0 {
				t.Fatalf("unsafe grant %s accepted: %v", variant, err)
			}
		})
	}
}

func corruptLabGrant(t *testing.T, path string, data []byte, variant string) {
	t.Helper()
	switch variant {
	case "public":
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
	case "symlink":
		if err := os.Rename(path, path+".fixture"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path+".fixture", path); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	case "trailing":
		data = append(data, []byte(" {}")...)
	case "unknown":
		data = append([]byte(`{"unexpected":true,`), data[1:]...)
	case "duplicate":
		data = append([]byte(`{"schema_version":1,`), data[1:]...)
	case "oversized":
		data = []byte(strings.Repeat("x", 8193))
	}
	if variant != "public" && variant != "symlink" {
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		file, err := root.OpenFile(filepath.Base(path), os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConsoleLabEnrollmentChangeDuringAdmissionSkipsGuestDispatch(t *testing.T) {
	f := newConsoleFixture(t)
	_, epoch := configureLab(f)
	grant := issueLab(t, f)
	op, req, providerID := labOperation(t, grant)
	req.OnRunning = func(context.Context) error { *epoch = strings.Repeat("b", 64); return nil }
	called := false
	result, err := f.service.ExecuteLabMutation(context.Background(), req.Actor, grant.GrantID, op, req, providerID, func(context.Context) error { called = true; return nil })
	if err == nil || called || result.Outcome.Status != domain.OutcomeFailed {
		t.Fatalf("changed enrollment dispatched: %+v, %v", result, err)
	}
}

func TestConsoleLabFailedActionRetryDoesNotDispatchAgain(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	grant := issueLab(t, f)
	f.provider.failInput = true
	for range 2 {
		result, err := f.service.Input(context.Background(), labActor(t), labRequest(grant))
		if err == nil || result.Outcome.Status != domain.OutcomeFailed {
			t.Fatalf("failed action treated as success: %+v, %v", result, err)
		}
	}
	if len(f.provider.inputs) != 1 {
		t.Fatalf("failed action replayed %d times", len(f.provider.inputs))
	}
}

func TestConsoleLabAgentCannotReadForeignGrantOrRevoke(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	grant := issueLab(t, f)
	agent := labActor(t)
	foreign, err := domain.NewActorContext("agent:foreign", "agent:foreign", agent.CallerPermissions, agent.EffectivePermissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.LabGrantStatus(context.Background(), foreign, grant.GrantID); err == nil {
		t.Fatal("foreign agent read grant metadata")
	}
	req := app.ConsoleLabGrantRevokeRequest{GrantID: grant.GrantID, Reason: "revoke lab", IdempotencyKey: "agent-revoke", Deadline: f.now.Add(time.Minute).Format(time.RFC3339Nano)}
	if _, err := f.service.RevokeLabGrant(context.Background(), agent, req); !errors.Is(err, app.ErrOperationApprovalForbidden) {
		t.Fatalf("agent revoked authority: %v", err)
	}
	status, err := f.service.LabGrantStatus(context.Background(), agent, grant.GrantID)
	if err != nil || status.State != "active" {
		t.Fatalf("refused revoke changed grant: %+v, %v", status, err)
	}
}

type labBoundedSafety struct {
	labSafetyFake
	t *testing.T
}

func (s labBoundedSafety) ResolveSafety(ctx context.Context, target domain.MachineRef) (app.SafetyResolution, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Minute {
		s.t.Fatal("grant preparation outlives its lease execution budget")
	}
	return s.labSafetyFake.ResolveSafety(ctx, target)
}

func TestConsoleLabAdmissionAndExecutionShareBoundedLeaseBudget(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	app.WithConsoleLabSafetyResolver(labBoundedSafety{labSafetyFake: labSafetyFake{contained: true}, t: t})(f.service)
	grant := issueLab(t, f)
	if _, err := f.service.Input(context.Background(), labActor(t), labRequest(grant)); err != nil {
		t.Fatal(err)
	}
	if len(f.provider.inputs) != 1 {
		t.Fatal("bounded action did not dispatch")
	}
}
