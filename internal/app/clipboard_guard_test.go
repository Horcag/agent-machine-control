package app_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func TestDesktopPartialClearRequiresReconciliation(t *testing.T) {
	for _, mode := range []string{"normal", "lab"} {
		t.Run(mode, func(t *testing.T) {
			f := newConsoleFixture(t)
			req := desktopRequest(f, "clipboard.set")
			zero := uint32(0)
			formats := []uint32{}
			req.Request.ExpectedSequence = &zero
			req.Request.ExpectedInventory = &formats
			actor, req := clipboardApprovalForMode(t, f, req, mode)
			p := desktopProvider(req)
			p.err = errors.Join(fmt.Errorf("%s: %w", desktopSecret, domain.ErrClipboardUncertain), context.Canceled)
			service := app.NewDesktopService(p, f.service)
			first, err := service.Action(context.Background(), actor, req)
			if !errors.Is(err, domain.ErrClipboardUncertain) || first.Receipt == nil || first.Receipt.Outcome.Status != domain.OutcomeFailed {
				t.Fatal("initial reconcile error lost", err, first)
			}
			message, _ := domain.CanonicalFailureMessage(domain.FailureCategoryClipboardUncertain)
			if first.Receipt.Outcome.ErrorCategory != domain.FailureCategoryClipboardUncertain || first.Receipt.Outcome.ErrorMessage != message {
				t.Fatal("uncertainty provenance lost", first.Receipt.Outcome)
			}
			state, _ := statedir.Resolve(f.root)
			stored, err := receipt.NewStore(state.ReceiptsDir()).GetContext(context.Background(), string(first.Receipt.ReceiptID))
			if err != nil || !reflect.DeepEqual(stored, first.Receipt) {
				t.Fatal("persisted receipt mismatch", err)
			}
			retry, err := app.NewDesktopService(p, f.service).Action(context.Background(), actor, req)
			if !errors.Is(err, domain.ErrClipboardUncertain) || len(p.targets) != 1 || retry.Receipt == nil || retry.Receipt.ReceiptID != first.Receipt.ReceiptID || retry.Response.Success {
				t.Fatal("cached reconciliation lost or dispatch repeated", err, retry, len(p.targets))
			}
			assertDesktopRedacted(t, f.root, first, err)
		})
	}
}

func clipboardApprovalForMode(t *testing.T, f consoleFixture, req app.DesktopActionRequest, mode string) (domain.ActorContext, app.DesktopActionRequest) {
	t.Helper()
	if mode == "lab" {
		configureLab(f)
		req.LabGrantID = issueLab(t, f).GrantID
		return labActor(t), req
	}
	return f.actor, approveDesktop(t, f, req)
}
