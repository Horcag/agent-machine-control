package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

func TestGuestRemoteExitRetiresOriginalAndDoesNotPublishOutput(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	h.transport.executeHook = func(_ context.Context, _ stream.Request, _ io.Reader, sink io.Writer) (stream.Result, error) {
		_, _ = sink.Write([]byte("private output"))
		return stream.Result{}, fmt.Errorf("private guest path: %w", &stream.RemoteExit{Code: 1})
	}
	p := h.params("exec", "remote-exit")
	p.Size, p.SHA256, p.MaxOutput = 1, guestSHA("x"), 64
	var output bytes.Buffer
	out, err := h.service.Execute(t.Context(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(&output))
	if !errors.Is(err, ErrGuestUncertain) || output.Len() != 0 {
		t.Fatal("remote exit published output or lost uncertainty", err)
	}
	assertGuestFailedDiagnostic(t, h, out, "guest-remote-exit:1")
	assertGuestSpoolClean(t, h)
}

func TestGuestRemoteExitCopiesOnlyNumericStatusAndKeepsUncertainty(t *testing.T) {
	for _, code := range []int{0, 1, 215, 0xc0000005} {
		original := &stream.RemoteExit{Code: code}
		err := safeGuestTransportError(fmt.Errorf("private guest path: %w", original))
		copied, ok := errors.AsType[*stream.RemoteExit](err)
		if !ok || copied == original || copied.Code != code || !errors.Is(err, ErrGuestUncertain) || strings.Contains(err.Error(), "private") {
			t.Fatal("unsafe exit diagnostic", err)
		}
		if guestFailureEvidence(err) != fmt.Sprintf("guest-remote-exit:%d", code) {
			t.Fatal("numeric evidence missing", err)
		}
	}
	for _, code := range []int{-1, 0x100000000} {
		err := safeGuestTransportError(fmt.Errorf("private: %w", &stream.RemoteExit{Code: code}))
		if err != ErrGuestUncertain || guestFailureEvidence(err) != "" {
			t.Fatal("invalid exit leaked", err)
		}
	}
}
