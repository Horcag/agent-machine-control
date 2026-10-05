package app_test

import (
	"context"
	"testing"
	"time"

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
