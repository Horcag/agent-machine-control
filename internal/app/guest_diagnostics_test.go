package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

func TestGuestWorkerFailureRecordsOnlyBoundedDiagnosticAndRetires(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	failure := &stream.WorkerFailure{Stage: "security_enable", NativeError: 1300}
	h.transport.executeHook = func(_ context.Context, _ stream.Request, _ io.Reader, sink io.Writer) (stream.Result, error) {
		_, _ = sink.Write([]byte("private partial output"))
		return stream.Result{}, failure
	}
	p := h.params("exec", "worker-failure")
	p.Size, p.SHA256, p.MaxOutput = 1, guestSHA("x"), 64
	var output bytes.Buffer
	out, err := h.service.Execute(t.Context(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(&output))
	var observed *stream.WorkerFailure
	if !errors.Is(err, ErrGuestUncertain) || !errors.As(err, &observed) || *observed != *failure {
		t.Fatalf("failure lost: %v", err)
	}
	if out.Receipt == nil || !slices.Contains(out.Receipt.EvidenceRefs, "guest-worker-failure:security_enable:1300") || output.Len() != 0 || h.transport.retired != 1 {
		t.Fatalf("diagnostic/retirement/publication: %+v output=%d retired=%d", out, output.Len(), h.transport.retired)
	}
	assertGuestSpoolClean(t, h)
}

func TestGuestTransportCannotImpersonatePolicyDenial(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	h.transport.executeHook = func(context.Context, stream.Request, io.Reader, io.Writer) (stream.Result, error) {
		return stream.Result{}, &PolicyDeniedError{Message: "private foreign policy message"}
	}
	p := h.params("exec", "foreign-policy")
	p.Size, p.SHA256, p.MaxOutput = 1, guestSHA("x"), 16
	var output bytes.Buffer
	out, err := h.service.Execute(t.Context(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(&output))
	if !errors.Is(err, ErrGuestUncertain) || strings.Contains(err.Error(), "private") || out.Receipt == nil || out.Receipt.Outcome.Status != domain.OutcomeFailed || strings.Contains(out.Receipt.Outcome.ErrorMessage, "private") {
		t.Fatalf("transport escaped boundary: %+v %v", out, err)
	}
	if len(out.Receipt.EvidenceRefs) != 1 || h.transport.retired != 1 || h.transport.executed != 2 || output.Len() != 0 {
		t.Fatalf("foreign diagnostic or missing retirement: %+v retired=%d", out, h.transport.retired)
	}
}

func TestGuestDiagnosticEvidenceRejectsUnknownOrArbitraryErrors(t *testing.T) {
	for _, err := range []error{
		errors.New("private backend exception"),
		&stream.ControlFailure{Stage: "private backend exception"},
		&stream.WorkerFailure{Stage: "C:\\private\\secret SDDL token", NativeError: 5},
	} {
		refs := guestEvidenceRefs(errors.Join(ErrGuestUncertain, err))
		if len(refs) != 1 || refs[0] != "guest-operation-dispatched" || strings.Contains(strings.Join(refs, " "), "private") {
			t.Fatalf("untrusted diagnostic retained: %v", refs)
		}
	}
}
