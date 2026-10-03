package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestDesktopPreProviderAbortCannotEnterReceiptCache(t *testing.T) {
	now := time.Now().UTC()
	scopes := domain.NewScopeSet(domain.ScopeMachineRead, domain.ScopeMachineWrite)
	actor, err := domain.NewActorContext("agent:synthetic-admission", "agent:synthetic-admission", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	store := receipt.NewStore(t.TempDir())
	service := &RecoveryService{receiptStore: store, nowFn: func() time.Time { return now }}
	req := domain.DesktopRequest{RequestID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Action: "clipboard.set", Deadline: now.Add(time.Minute).Format(time.RFC3339Nano)}
	op, err := service.buildOperation("desktop.action", MutationRequest{TargetID: "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor, Reason: "synthetic admission failure", IdempotencyKey: "synthetic-admission-key", Deadline: now.Add(time.Minute)}, domain.ClassDestructivePrivileged, domain.CapabilityDesktopAction, domain.DesktopActionParameters(req))
	if err != nil {
		t.Fatal(err)
	}
	fp, err := op.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		out, err := service.preProviderFailure(t.Context(), op, fp, policy.Decision{}, now, cause, "", "")
		if !errors.Is(err, cause) || out.ReceiptID != "" {
			t.Fatal(out, err)
		}
		cached, err := store.LookupIdempotency(op)
		if err != nil || cached != nil {
			t.Fatal("nonadmitted desktop receipt entered cache", cached, err)
		}
	}
}
