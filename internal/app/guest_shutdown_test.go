package app

import (
	"context"
	"errors"
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
}

func (c *guestBlockingVerifier) ConfirmGuestRetirement(ctx context.Context, _ domain.MachineRef, _ domain.ActorID, _ string, _ stream.WorkerIdentity) (bool, error) {
	c.mu.Lock()
	c.calls++
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
	h.prepare(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- h.service.Shutdown(ctx) }()
	select {
	case <-verifier.entered:
	case <-time.After(time.Second):
		t.Fatal("retirement verifier was not called")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrGuestUncertain) {
			t.Fatalf("deadline lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown discarded caller deadline")
	}
	close(verifier.release)
	if err := h.service.Shutdown(context.Background()); err != nil {
		t.Fatalf("pending retirement could not be confirmed: %v", err)
	}
}

func TestGuestConcurrentShutdownHasOneRetirementEffect(t *testing.T) {
	h := newGuestHarness(t)
	verifier := &guestBlockingVerifier{guestTestConfig: h.config, entered: make(chan struct{}), release: make(chan struct{})}
	h.service.config = verifier
	h.prepare(t)
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- h.service.Shutdown(context.Background()) }()
	<-verifier.entered
	go func() { second <- h.service.Shutdown(context.Background()) }()
	close(verifier.release)
	for _, result := range []chan error{first, second} {
		if err := <-result; err != nil {
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
	h.prepare(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- h.service.Shutdown(ctx) }()
	<-verifier.entered
	cancel()
	close(verifier.release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("expired shutdown reported success: %v", err)
	}
}
