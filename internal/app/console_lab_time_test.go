package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestConsoleLabEquivalentDeadlineDispatchesOnce(t *testing.T) {
	for _, entrypoint := range []string{"native input", "lab admission"} {
		t.Run(entrypoint, func(t *testing.T) {
			f := newConsoleFixture(t)
			configureLab(f)
			grant := issueLab(t, f)
			deadline := grant.ExpiresAt.In(time.FixedZone("+00:00", 0))
			var execute func() (domain.Receipt, error)
			if entrypoint == "native input" {
				req := labRequest(grant)
				req.Deadline = deadline.Format("2006-01-02T15:04:05.999999999-07:00")
				execute = func() (domain.Receipt, error) {
					return f.service.Input(context.Background(), labActor(t), req)
				}
			} else {
				// Preserve the non-UTC representation on every host, including hosts
				// where time.Parse reuses time.Local for a +00:00 input deadline.
				op, req, providerID := labOperation(t, grant)
				op.Deadline, req.Deadline = deadline, deadline
				execute = func() (domain.Receipt, error) {
					return f.service.ExecuteLabMutation(context.Background(), req.Actor, grant.GrantID, op, req, providerID, func(ctx context.Context) error {
						return f.provider.SendConsoleInput(ctx, providerID, domain.ConsoleInput{Kind: "key", Key: "enter"})
					})
				}
			}
			first, err := execute()
			if err != nil || first.Outcome.Status != domain.OutcomeSuccess || first.ReceiptID == "" || len(f.provider.inputs) != 1 {
				t.Fatalf("equal-instant dispatch = %+v, error = %v, inputs = %d", first, err, len(f.provider.inputs))
			}
			second, err := execute()
			if err != nil || first.ReceiptID != second.ReceiptID || len(f.provider.inputs) != 1 {
				t.Fatalf("cached retry = %+v, error = %v, inputs = %d", second, err, len(f.provider.inputs))
			}
		})
	}
}

func TestNativeLabAdmissionSurvivesBackwardClockStep(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	grant := issueLab(t, f)
	op, req, providerID := labOperation(t, grant)
	digest := sha256.Sum256([]byte(grant.GrantID + "\x00" + string(op.Actor.EffectiveActor) + "\x00" + op.IdempotencyKey))
	id := app.ConsoleLabApprovalPrefix + hex.EncodeToString(digest[:16])
	store := approval.NewStore(filepath.Join(f.root, "approvals"))
	*f.now = f.now.Add(time.Second)
	admittedAt := *f.now
	app.WithRecoveryClock(func() time.Time {
		if issued, _ := store.LoadIssuedContext(context.Background(), id); issued != nil {
			return admittedAt.Add(-50 * time.Millisecond)
		}
		return admittedAt
	})(f.recovery)
	dispatched := 0
	result, err := f.service.ExecuteLabMutation(t.Context(), req.Actor, grant.GrantID, op, req, providerID, func(context.Context) error { dispatched++; return nil })
	if err != nil || result.Outcome.Status != domain.OutcomeSuccess || dispatched != 1 {
		t.Fatalf("native lab clock step: result=%+v err=%v dispatches=%d", result, err, dispatched)
	}
	loaded, err := approval.LoadFromFile(filepath.Join(f.root, "approvals", id+".json"))
	if err != nil || !loaded.Consumed || loaded.ConsumedAt.Before(loaded.IssuedAt) {
		t.Fatalf("native consumption is invalid: approval=%+v err=%v", loaded, err)
	}
}
