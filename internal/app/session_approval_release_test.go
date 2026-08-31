package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type cancelledDialTransport struct{ calls atomic.Int32 }

func (t *cancelledDialTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	t.calls.Add(1)
	return nil, context.Canceled
}

func TestCancelledZeroEffectMutationDoesNotConsumeIssuedApproval(t *testing.T) {
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	transport := &cancelledDialTransport{}
	manager := sessions.NewManager(sd.SessionsDir(), transport, func() time.Time { return now })
	approvalStore := approval.NewStore(sd.ApprovalsDir())
	service := app.NewSessionService(manager, diagnosticDestructiveSafety{}, nil, audit.NewStore(sd.AuditDir()), receipt.NewStore(sd.ReceiptsDir()), approvalStore, app.WithSessionClock(func() time.Time { return now }))
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionAdmin)
	actor, err := domain.NewActorContext("operator:approval-release", "operator:approval-release", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	params := app.SessionOpenParams{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Caller: actor,
		Reason: "cancel before guest effect", IdempotencyKey: "approval-release-open", Timeout: 30 * time.Second,
	}
	op := domain.Operation{
		Kind: "session.open", Target: domain.MachineRef(params.Target), Actor: actor,
		Reason: params.Reason, Deadline: now.Add(params.Timeout), IdempotencyKey: params.IdempotencyKey,
		RequiredCapability: domain.CapabilitySessionOpen, RequiredScopes: []string{domain.ScopeSessionOpen},
		Classification: domain.ClassDestructivePrivileged, EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{"cols": uint16(domain.DefaultCols), "rows": uint16(domain.DefaultRows), "term": domain.DefaultTermType},
	}
	fingerprint, err := op.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	issued := domain.Approval{
		ID: "app-release-session-1", Actor: actor.EffectiveActor, Target: domain.MachineRef(params.Target),
		AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fingerprint, IdempotencyKey: params.IdempotencyKey,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	if err := approvalStore.Issue(issued); err != nil {
		t.Fatal(err)
	}
	params.Approval = &issued
	_, _, err = service.OpenSession(context.Background(), params)
	if !errors.Is(err, context.Canceled) || transport.calls.Load() != 1 {
		t.Fatalf("cancelled open error=%v calls=%d", err, transport.calls.Load())
	}
	if consumed, err := approvalStore.IsConsumed(string(issued.ID)); err != nil || consumed {
		t.Fatalf("zero-effect approval consumed=%v err=%v", consumed, err)
	}
}
