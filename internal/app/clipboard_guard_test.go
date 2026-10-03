package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestDesktopPartialClearRequiresReconciliation(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	req := desktopRequest(f, "clipboard.set")
	req.LabGrantID = issueLab(t, f).GrantID
	zero := uint32(0)
	formats := []uint32{}
	req.Request.ExpectedSequence = &zero
	req.Request.ExpectedInventory = &formats
	p := desktopProvider(req)
	p.err = domain.ErrClipboardUncertain
	result, err := app.NewDesktopService(p, f.service).Action(context.Background(), labActor(t), req)
	if !errors.Is(err, domain.ErrClipboardUncertain) || len(p.targets) != 1 {
		t.Fatal("reconcile error lost or retried", err)
	}
	assertDesktopRedacted(t, f.root, result, err)
}
