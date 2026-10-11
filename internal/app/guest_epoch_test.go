package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

type guestVerifiedConfig struct {
	*guestTestConfig
	absent  bool
	checked []stream.WorkerIdentity
}

func (c *guestVerifiedConfig) ConfirmGuestRetirement(_ context.Context, _ domain.MachineRef, _ domain.ActorID, _ string, worker stream.WorkerIdentity) (bool, error) {
	c.checked = append(c.checked, worker)
	return c.absent, nil
}

func TestGuestConfirmedRetirementAllowsFreshSameGenerationEpoch(t *testing.T) {
	h := newGuestHarness(t)
	verified := &guestVerifiedConfig{guestTestConfig: h.config}
	h.service.config = verified
	h.prepare(t)
	original := h.transport.worker
	h.transport.fail = true
	p := h.params("exec", "failed")
	p.Size = 1
	p.SHA256 = guestSHA("x")
	p.MaxOutput = 10
	if _, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(new(bytes.Buffer))); !errors.Is(err, ErrGuestUncertain) {
		t.Fatal(err)
	}
	h.transport.fail = false
	if _, err := h.service.Execute(context.Background(), h.params("prepare", "before-absence"), nil, nil); !errors.Is(err, ErrGuestUncertain) || h.transport.prepared != 1 {
		t.Fatalf("unconfirmed replacement: %v", err)
	}
	verified.absent = true
	h.transport.worker.PID++
	h.transport.worker.BirthTicks++
	h.prepareWithKey(t, "after-absence")
	if h.transport.prepared != 2 {
		t.Fatalf("fresh epoch not launched: %d", h.transport.prepared)
	}
	for _, identity := range verified.checked {
		if identity != original {
			t.Fatalf("retirement adopted current PID: %+v", identity)
		}
	}
	h.put(t, "new-epoch-put")
}

func TestGuestShutdownRetainsUnverifiedOwnWorker(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	if err := h.service.Shutdown(context.Background()); !errors.Is(err, ErrGuestUncertain) {
		t.Fatalf("shutdown claimed absence: %v", err)
	}
	if h.transport.retired != 1 {
		t.Fatalf("worker not closed exactly once: %d", h.transport.retired)
	}
	if _, err := h.service.Execute(context.Background(), h.params("prepare", "closed"), nil, nil); !errors.Is(err, ErrGuestAuthority) {
		t.Fatalf("closed service admitted request: %v", err)
	}
	h.service = NewGuestService(h.service.session, h.service.resolver, h.config, h.transport, h.service.store)
	if _, err := h.service.Execute(context.Background(), h.params("prepare", "restart"), nil, nil); !errors.Is(err, ErrGuestUncertain) {
		t.Fatalf("lost shutdown obligation: %v", err)
	}
}
func TestGuestVerifiedShutdownAllowsNewServiceSameGeneration(t *testing.T) {
	h := newGuestHarness(t)
	verified := &guestVerifiedConfig{guestTestConfig: h.config, absent: true}
	h.service.config = verified
	h.prepare(t)
	if err := h.service.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.transport.worker.PID++
	h.transport.worker.BirthTicks++
	h.service = NewGuestService(h.service.session, h.service.resolver, verified, h.transport, h.service.store)
	h.prepareWithKey(t, "new-service")
}

func TestGuestSelfApprovalPrepareAndCommand(t *testing.T) {
	h := newGuestHarness(t)
	h.safety.destructive = true
	operator := h.actor.Clone()
	operator.CallerPermissions[domain.ScopeSessionAdmin] = struct{}{}
	operator.EffectivePermissions[domain.ScopeSessionAdmin] = struct{}{}
	h.actor = operator
	for _, operation := range []string{"prepare", "exec"} {
		p := h.params(operation, "self-"+operation)
		var input *bytes.Reader
		if operation == "exec" {
			p.Size = 1
			p.SHA256 = guestSHA("x")
			p.MaxOutput = 10
			input = bytes.NewReader([]byte("x"))
		}
		grant, _, err := h.service.IssueApprovalFor(context.Background(), p, time.Minute, "self")
		if err != nil {
			t.Fatal(err)
		}
		p.Deadline = grant.Deadline
		p.ApprovalID = grant.ApprovalID
		var out GuestOperationResult
		if input != nil {
			out, err = h.service.Execute(context.Background(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(new(bytes.Buffer)))
		} else {
			out, err = h.service.Execute(context.Background(), p, nil, nil)
		}
		if err != nil || out.Receipt == nil || out.Receipt.Actor != operator.EffectiveActor || out.Receipt.Validate() != nil {
			t.Fatalf("self %s %+v %v", operation, out, err)
		}
	}
}
func TestGuestApprovalBeneficiaryFailsClosed(t *testing.T) {
	h := newGuestHarness(t)
	h.safety.destructive = true
	p := h.params("prepare", "beneficiary")
	p.Caller.CallerPermissions[domain.ScopeSessionAdmin] = struct{}{}
	p.Caller.EffectivePermissions[domain.ScopeSessionAdmin] = struct{}{}
	for _, beneficiary := range []string{"", "other-operator", "agent:foreign"} {
		if _, _, err := h.service.IssueApprovalFor(context.Background(), p, time.Minute, beneficiary); err == nil {
			t.Fatalf("foreign beneficiary %q approved", beneficiary)
		}
	}
	if _, _, err := h.service.IssueApprovalFor(context.Background(), p, time.Minute, "self"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.service.IssueApprovalFor(context.Background(), p, time.Minute, string(sessionApprovalBeneficiary)); err == nil {
		t.Fatal("beneficiary changed under same idempotency key")
	}
	p.IdempotencyKey = "missing-admin"
	delete(p.Caller.EffectivePermissions, domain.ScopeSessionAdmin)
	if _, _, err := h.service.IssueApprovalFor(context.Background(), p, time.Minute, "self"); err == nil {
		t.Fatal("issuer without admin approved")
	}
	p.Caller.EffectiveActor = domain.ActorID(strings.Repeat("x", 257))
	if _, _, err := h.service.IssueApprovalFor(context.Background(), p, time.Minute, "self"); err == nil {
		t.Fatal("invalid issuer approved")
	}
}

func TestGuestShutdownWaitsOwnedCallAndHonorsCallerDeadline(t *testing.T) {
	h := newGuestHarness(t)
	verified := &guestVerifiedConfig{guestTestConfig: h.config, absent: true}
	h.service.config = verified
	h.prepare(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	h.transport.executeHook = func(ctx context.Context, request stream.Request, input io.Reader, output io.Writer) (stream.Result, error) {
		_, err := io.Copy(io.Discard, input)
		if err != nil {
			return stream.Result{}, err
		}
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return stream.Result{}, ctx.Err()
		}
		_, err = output.(stream.ContextWriter).WriteContext(ctx, []byte("ok"))
		return stream.Result{ID: request.ID, Status: "ok", Worker: h.transport.worker, Size: 2, SHA256: guestSHA("ok")}, err
	}
	p := h.params("exec", "during-shutdown")
	p.Size = 1
	p.SHA256 = guestSHA("x")
	p.MaxOutput = 10
	go func() {
		_, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(new(bytes.Buffer)))
		done <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := h.service.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown ignored caller deadline: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := h.service.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertGuestSpoolClean(t, h)
}
