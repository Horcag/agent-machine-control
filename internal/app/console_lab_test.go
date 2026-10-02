package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

type labSafetyFake struct {
	contained  bool
	unverified bool
}

func (f *labSafetyFake) ResolveSafety(context.Context, domain.MachineRef) (app.SafetyResolution, error) {
	return app.SafetyResolution{Contained: f.contained, RollbackRef: "synthetic-checkpoint", RollbackState: policy.RollbackState{Available: true, Verified: !f.unverified, CheckpointID: "synthetic-checkpoint"}}, nil
}

func configureLab(f consoleFixture) (*labSafetyFake, *string) {
	safety := &labSafetyFake{contained: true}
	epoch := strings.Repeat("a", 64)
	app.WithConsoleLabSafetyResolver(safety)(f.service)
	app.WithConsoleLabEnrollmentIdentity(func(context.Context, domain.MachineRef) (string, error) { return epoch, nil })(f.service)
	return safety, &epoch
}

func issueLab(t *testing.T, f consoleFixture) app.ConsoleLabGrant {
	t.Helper()
	grant, rcpt, err := f.service.IssueLabGrant(context.Background(), f.actor, app.ConsoleLabGrantIssueRequest{Target: "default", Reason: "operate disposable guest lab", IdempotencyKey: "lab-grant-test", Beneficiary: "agent:mcp-local", ValidForMillis: app.MaxConsoleLabGrantValidity.Milliseconds()})
	if err != nil || rcpt.Outcome.Status != domain.OutcomeSuccess {
		t.Fatalf("issue = %+v, %v", rcpt, err)
	}
	return grant
}

func labActor(t *testing.T) domain.ActorContext {
	t.Helper()
	scopes := domain.NewScopeSet(domain.ScopeMachineRead, domain.ScopeMachineWrite, domain.ScopeEvidenceCapture)
	actor, err := domain.NewActorContext("agent:mcp-local", "agent:mcp-local", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func labRequest(grant app.ConsoleLabGrant) app.ConsoleInputRequest {
	return app.ConsoleInputRequest{Target: "default", Input: domain.ConsoleInput{Kind: "type", Text: "synthetic-private-text"}, Reason: "type into disposable guest", IdempotencyKey: "lab-input-test", Deadline: grant.ExpiresAt.Format(time.RFC3339Nano), LabGrantID: grant.GrantID}
}

func TestConsoleLabGrantExactInputIdempotencyAndRedaction(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	grant := issueLab(t, f)
	actor := labActor(t)
	req := labRequest(grant)
	first, err := f.service.Input(context.Background(), actor, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.Input(context.Background(), actor, req)
	if err != nil || first.ReceiptID != second.ReceiptID || len(f.provider.inputs) != 1 {
		t.Fatalf("retry = %+v, %v, inputs %d", second, err, len(f.provider.inputs))
	}
	req.Input.Text = "different text"
	if _, err := f.service.Input(context.Background(), actor, req); err == nil || len(f.provider.inputs) != 1 {
		t.Fatal("changed payload reused approval")
	}
	root, err := os.OpenRoot(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := filepath.WalkDir(f.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative, err := filepath.Rel(f.root, path)
		if err != nil {
			return err
		}
		data, err := root.ReadFile(relative)
		if err == nil && strings.Contains(string(data), "synthetic-private-text") {
			return errors.New("typed text persisted")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleLabGrantRequiresOperatorAndVerifiedContainment(t *testing.T) {
	f := newConsoleFixture(t)
	safety, _ := configureLab(f)
	req := app.ConsoleLabGrantIssueRequest{Target: "default", Reason: "operate disposable guest lab", IdempotencyKey: "lab-create", ValidForMillis: 60000}
	if _, _, err := f.service.IssueLabGrant(context.Background(), labActor(t), req); !errors.Is(err, app.ErrOperationApprovalForbidden) {
		t.Fatalf("agent minted grant: %v", err)
	}
	safety.contained = false
	if _, _, err := f.service.IssueLabGrant(context.Background(), f.actor, req); err == nil {
		t.Fatal("uncontained grant issued")
	}
	safety.contained = true
	req.Target = "foreign-machine"
	if _, _, err := f.service.IssueLabGrant(context.Background(), f.actor, req); err == nil {
		t.Fatal("foreign target accepted")
	}
	req.Target = "default"
	req.ValidForMillis = app.MaxConsoleLabGrantValidity.Milliseconds() + 1
	if _, _, err := f.service.IssueLabGrant(context.Background(), f.actor, req); err == nil {
		t.Fatal("unbounded grant accepted")
	}
}

func TestConsoleLabGrantLifecycleAndActorBinding(t *testing.T) {
	for _, variant := range []string{"expiry", "revoke", "enrollment", "containment", "actor", "deadline", "explicit-derived"} {
		t.Run(variant, func(t *testing.T) {
			f := newConsoleFixture(t)
			safety, epoch := configureLab(f)
			grant := issueLab(t, f)
			actor := labActor(t)
			req := labRequest(grant)
			actor, req = mutateLabLifecycle(t, f, safety, epoch, grant, variant, actor, req)
			if _, err := f.service.Input(context.Background(), actor, req); err == nil || len(f.provider.inputs) != 0 {
				t.Fatalf("%s dispatched input: %v", variant, err)
			}
			if variant == "expiry" || variant == "revoke" || variant == "enrollment" {
				status, err := f.service.LabGrantStatus(context.Background(), labActor(t), grant.GrantID)
				if err != nil || status.State == "active" {
					t.Fatalf("inactive status = %+v, %v", status, err)
				}
			}
		})
	}
}

func TestConsoleLabGrantAcknowledgmentNeverForgesContainmentOrRollback(t *testing.T) {
	f := newConsoleFixture(t)
	safety, _ := configureLab(f)
	safety.contained = false
	req := app.ConsoleLabGrantIssueRequest{Target: "default", Reason: "acknowledge guest external effects", IdempotencyKey: "lab-ack", Beneficiary: "agent:mcp-local", ValidForMillis: 60000, AcknowledgeExternalEffects: true}
	grant, first, err := f.service.IssueLabGrant(context.Background(), f.actor, req)
	if err != nil || !grant.AcknowledgeExternalEffects {
		t.Fatalf("acknowledged issue: %v", err)
	}
	*f.now = f.now.Add(time.Second)
	same, second, err := f.service.IssueLabGrant(context.Background(), f.actor, req)
	if err != nil || same != grant || first.ReceiptID != second.ReceiptID {
		t.Fatalf("issue retry: %v", err)
	}
	req.AcknowledgeExternalEffects = false
	if _, _, err := f.service.IssueLabGrant(context.Background(), f.actor, req); err == nil {
		t.Fatal("acknowledgment change accepted")
	}
	receipt, err := f.service.Input(context.Background(), labActor(t), labRequest(grant))
	if err != nil || receipt.Class != domain.ClassDestructivePrivileged || safety.contained {
		t.Fatalf("ack changed safety: %+v, %v", receipt, err)
	}
	safety.unverified = true
	req.AcknowledgeExternalEffects = true
	req.IdempotencyKey = "no-rollback"
	if _, _, err := f.service.IssueLabGrant(context.Background(), f.actor, req); err == nil {
		t.Fatal("ack substituted for verified rollback")
	}
	input := labRequest(grant)
	input.IdempotencyKey = "unverified-input"
	if _, err := f.service.Input(context.Background(), labActor(t), input); err == nil || len(f.provider.inputs) != 1 {
		t.Fatal("revoked rollback evidence admitted input")
	}
}

func mutateLabLifecycle(t *testing.T, f consoleFixture, safety *labSafetyFake, epoch *string, grant app.ConsoleLabGrant, variant string, actor domain.ActorContext, req app.ConsoleInputRequest) (domain.ActorContext, app.ConsoleInputRequest) {
	t.Helper()
	switch variant {
	case "expiry":
		*f.now = grant.ExpiresAt
	case "revoke":
		r := app.ConsoleLabGrantRevokeRequest{GrantID: grant.GrantID, Reason: "end lab authority", IdempotencyKey: "lab-revoke", Deadline: f.now.Add(time.Minute).Format(time.RFC3339Nano)}
		first, err := f.service.RevokeLabGrant(context.Background(), f.actor, r)
		if err != nil {
			t.Fatal(err)
		}
		second, err := f.service.RevokeLabGrant(context.Background(), f.actor, r)
		if err != nil || first.ReceiptID != second.ReceiptID {
			t.Fatalf("revoke retry: %v", err)
		}
	case "enrollment":
		*epoch = strings.Repeat("b", 64)
	case "containment":
		safety.contained = false
	case "actor":
		actor = f.actor
	case "deadline":
		req.Deadline = grant.ExpiresAt.Add(time.Second).Format(time.RFC3339Nano)
	case "explicit-derived":
		req.LabGrantID = ""
		req.ApprovalID = app.ConsoleLabApprovalPrefix + strings.Repeat("a", 32)
	}
	return actor, req
}

func TestConsoleLabGrantIncompleteIssuanceCannotDispatchBeforeRepair(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	grant := issueLab(t, f)
	// Simulate interruption after authority publication but before activation.
	if err := os.Remove(filepath.Join(f.root, "console-lab-grants", grant.GrantID+".active")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Input(context.Background(), labActor(t), labRequest(grant)); err == nil || len(f.provider.inputs) != 0 {
		t.Fatal("incomplete grant dispatched input")
	}
	same := issueLab(t, f)
	if same != grant {
		t.Fatal("repair changed immutable grant")
	}
	if _, err := f.service.Input(context.Background(), labActor(t), labRequest(grant)); err != nil || len(f.provider.inputs) != 1 {
		t.Fatalf("repaired grant input: %v", err)
	}
}
