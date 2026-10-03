package daemon

import (
	"context"

	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

type desktopFailureBackend struct{ exactRetryBackend }

func (desktopFailureBackend) Capabilities(context.Context, string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(domain.CapabilityConsoleInput, domain.CapabilityConsoleScreenshot, domain.CapabilityDesktopAction), nil
}
func (desktopFailureBackend) CaptureConsole(context.Context, string, int, int) (domain.ConsoleFrame, error) {
	panic("unexpected capture")
}
func (desktopFailureBackend) SendConsoleInput(context.Context, string, domain.ConsoleInput) error {
	panic("unexpected input")
}

type desktopFailureProvider struct {
	calls atomic.Int32
	cause error
}

func (p *desktopFailureProvider) Execute(_ context.Context, _ domain.MachineRef, req domain.DesktopRequest) (domain.DesktopResponse, error) {
	p.calls.Add(1)
	return domain.DesktopResponse{RequestID: req.RequestID, Text: "synthetic-private-response", Elements: []domain.DesktopElement{{}}}, p.cause
}
func (*desktopFailureProvider) Provision(context.Context, domain.MachineRef) (domain.DesktopResponse, error) {
	panic("unexpected provision")
}
func (*desktopFailureProvider) Remove(context.Context, domain.MachineRef) error {
	panic("unexpected remove")
}

type desktopFailureSafety struct{}

func (desktopFailureSafety) ResolveSafety(context.Context, domain.MachineRef) (app.SafetyResolution, error) {
	return app.SafetyResolution{Contained: true, RollbackRef: "synthetic-checkpoint", RollbackState: policy.RollbackState{Available: true, Verified: true, CheckpointID: "synthetic-checkpoint"}}, nil
}

// DesktopFailureTestServer exposes a test-only application/provider seam to external
// transport tests without adding a production injection surface.
func DesktopFailureTestServer(t *testing.T, cause error) (string, string, string, string, func() int32, func(app.DesktopActionRequest) app.DesktopActionRequest) {
	t.Helper()
	fixture := newExactRetryFixture(t, t.TempDir()+"/state")
	now := time.Now().UTC()
	srv, err := NewServer(Config{Clock: func() time.Time { return now }, StateDir: fixture.sd.Root(), Backend: desktopFailureBackend{}, ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	app.WithRecoveryClock(func() time.Time { return now })(srv.recoveryService)
	provider := &desktopFailureProvider{cause: cause}
	app.WithConsoleLabSafetyResolver(desktopFailureSafety{})(srv.consoleService)
	srv.desktopService = app.NewDesktopService(provider, srv.consoleService)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	operator, err := auth.ReadTokenFile(fixture.sd.AuthDir(), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := auth.ReadTokenFile(fixture.sd.AuthDir(), auth.TokenTypeAgentMCP)
	if err != nil {
		t.Fatal(err)
	}
	approve := func(req app.DesktopActionRequest) app.DesktopActionRequest {
		t.Helper()
		req.Request.Deadline = now.Add(45 * time.Second).Format(time.RFC3339Nano)
		scopes := domain.NewScopeSet(domain.ScopeMachineRead, domain.ScopeMachineWrite, domain.ScopeOperationAdmin)
		actor, err := domain.NewActorContext("operator:synthetic-fixture", "operator:synthetic-fixture", scopes, scopes)
		if err != nil {
			t.Fatal(err)
		}
		grant, _, err := srv.recoveryService.IssueOperationApproval(t.Context(), app.OperationApprovalIssueParams{Kind: "desktop.action", Caller: actor, Target: req.Target, Reason: req.Reason, IdempotencyKey: req.IdempotencyKey, ValidFor: 45 * time.Second, Beneficiary: "agent:mcp-local", Parameters: domain.DesktopActionParameters(req.Request)})
		if err != nil {
			t.Fatal(err)
		}
		req.ApprovalID, req.Request.Deadline = grant.ApprovalID, grant.Deadline.Format(time.RFC3339Nano)
		return req
	}
	return srv.Endpoint(), operator, agent, fixture.sd.Root(), provider.calls.Load, approve
}
