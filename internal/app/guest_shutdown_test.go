package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

type guestBlockingVerifier struct {
	*guestTestConfig
	entered            chan struct{}
	release            chan struct{}
	mu                 sync.Mutex
	calls              int
	ignoreCancellation bool
	observedDeadline   time.Time
	observedWitness    any
}

func (c *guestBlockingVerifier) ConfirmGuestRetirement(ctx context.Context, _ domain.MachineRef, _ domain.ActorID, _ string, _ stream.WorkerIdentity) (bool, error) {
	c.mu.Lock()
	c.calls++
	c.observedDeadline, _ = ctx.Deadline()
	c.observedWitness = ctx.Value(guestShutdownWitnessKey{})
	if c.calls == 1 {
		close(c.entered)
	}
	c.mu.Unlock()
	if c.ignoreCancellation {
		<-c.release
		return true, nil
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-c.release:
		return true, nil
	}
}

func TestGuestShutdownRetirementHonorsCallerContext(t *testing.T) {
	h := newGuestHarness(t)
	verifier := &guestBlockingVerifier{guestTestConfig: h.config, entered: make(chan struct{}), release: make(chan struct{})}
	h.service.config = verifier
	prepareGuestShutdownFixture(t, h)
	witness := new(int)
	parent := context.WithValue(t.Context(), guestShutdownWitnessKey{}, witness)
	ctx, cancel := context.WithTimeout(parent, guestShutdownTestBudget)
	defer cancel()
	requestedDeadline, _ := ctx.Deadline()
	result := make(chan error, 1)
	go func() { result <- h.service.Shutdown(ctx) }()
	awaitGuestShutdownEntry(t, verifier.entered)
	verifier.mu.Lock()
	observedDeadline := verifier.observedDeadline
	observedWitness := verifier.observedWitness
	verifier.mu.Unlock()
	cancel()
	if err := awaitGuestShutdownResult(t, result); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrGuestUncertain) {
		t.Fatalf("caller cancellation lost: %v", err)
	}
	if !observedDeadline.Equal(requestedDeadline) || observedWitness != witness {
		t.Fatalf("caller context detached: deadline=%v witness=%v", observedDeadline, observedWitness)
	}
	worker := originalGuestEpoch(t, h)
	var pending guestWorkerRecord
	if err := h.service.store.read(t.Context(), worker.RecordID, ".retirement", &pending); err != nil || pending.Identity != worker.Identity {
		t.Fatalf("pending original identity lost: %v", err)
	}
	close(verifier.release)
	if err := h.service.Shutdown(guestShutdownContext(t)); err != nil {
		t.Fatalf("pending retirement could not be confirmed: %v", err)
	}
}

func TestGuestConcurrentShutdownHasOneRetirementEffect(t *testing.T) {
	h := newGuestHarness(t)
	verifier := &guestBlockingVerifier{guestTestConfig: h.config, entered: make(chan struct{}), release: make(chan struct{})}
	h.service.config = verifier
	prepareGuestShutdownFixture(t, h)
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- h.service.Shutdown(guestShutdownContext(t)) }()
	awaitGuestShutdownEntry(t, verifier.entered)
	go func() { second <- h.service.Shutdown(guestShutdownContext(t)) }()
	close(verifier.release)
	for _, result := range []chan error{first, second} {
		if err := awaitGuestShutdownResult(t, result); err != nil {
			t.Fatalf("identical shutdown failed: %v", err)
		}
	}
	verifier.mu.Lock()
	calls := verifier.calls
	verifier.mu.Unlock()
	if calls != 1 || h.transport.retired != 1 {
		t.Fatalf("duplicate retirement effects: verify=%d close=%d", calls, h.transport.retired)
	}
}

func TestGuestShutdownNeverReportsSuccessAfterDeadline(t *testing.T) {
	h := newGuestHarness(t)
	verifier := &guestBlockingVerifier{guestTestConfig: h.config, entered: make(chan struct{}), release: make(chan struct{}), ignoreCancellation: true}
	h.service.config = verifier
	prepareGuestShutdownFixture(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- h.service.Shutdown(ctx) }()
	awaitGuestShutdownEntry(t, verifier.entered)
	cancel()
	close(verifier.release)
	if err := awaitGuestShutdownResult(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("expired shutdown reported success: %v", err)
	}
}

func TestGuestShutdownDoesNotCloseBeforeDurableIntent(t *testing.T) {
	h := newGuestHarness(t)
	prepareGuestShutdownFixture(t, h)
	original := h.service.store.directory
	h.service.store.directory = filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(h.service.store.directory, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Shutdown(guestShutdownContext(t)); !errors.Is(err, ErrGuestUncertain) {
		t.Fatalf("missing intent accepted: %v", err)
	}
	if h.transport.retired != 0 {
		t.Fatal("worker closed without durable retirement intent")
	}
	h.service.store.directory = original
	verified := &guestVerifiedConfig{guestTestConfig: h.config, absent: true}
	h.service.config = verified
	if err := h.service.Shutdown(guestShutdownContext(t)); err != nil {
		t.Fatal(err)
	}
}

type guestShutdownWitnessKey struct{}
