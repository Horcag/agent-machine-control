package app

import (
	"context"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

const guestShutdownTestBudget = 30 * time.Second

// Shutdown unit tests start from an already-admitted worker. Admission and
// receipt finalization have separate integration coverage; their fixed cleanup
// budget must not prevent the shutdown branch from being exercised under load.
func prepareGuestShutdownFixture(t *testing.T, h *guestHarness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), guestShutdownTestBudget)
	defer cancel()
	target := domain.MachineRef(h.service.resolver.(guestTestResolver).resolution.Locator.String())
	id := guestDigest("shutdown-fixture:" + t.Name())
	key := guestPoolKey(target, h.actor, h.config.generation)
	worker := guestWorkerRecord{RecordID: id, Target: target, Caller: h.actor.AuthenticatedCaller, Actor: h.actor.EffectiveActor, Generation: h.config.generation, Root: h.config.root}
	if err := h.service.recordLaunch(ctx, id, worker); err != nil {
		t.Fatal(err)
	}
	identity, err := h.transport.Prepare(ctx, target, key, worker.Generation)
	if err != nil {
		t.Fatal(err)
	}
	worker.Identity = identity
	if err := h.service.store.write(ctx, id, ".worker", worker); err != nil {
		t.Fatal(err)
	}
	if err := h.service.recordReady(ctx, id, worker); err != nil {
		t.Fatal(err)
	}
	h.service.mu.Lock()
	h.service.workers[key] = worker
	h.service.mu.Unlock()
}

func awaitGuestShutdownEntry(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(guestShutdownTestBudget):
		t.Fatal("retirement verifier did not start within operation budget")
	}
}
func awaitGuestShutdownResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(guestShutdownTestBudget):
		t.Fatal("shutdown did not return within operation budget")
		return nil
	}
}

func guestShutdownContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), guestShutdownTestBudget)
	t.Cleanup(cancel)
	return ctx
}
