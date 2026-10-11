package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

type startupFailureTransport struct {
	*guestTestTransport
	err error
}

func (f *startupFailureTransport) Prepare(context.Context, domain.MachineRef, string, string) (stream.WorkerIdentity, error) {
	f.prepared++
	return stream.WorkerIdentity{}, f.err
}

func TestGuestPrepareStartupDiagnosticPreservesSanitizedReceipt(t *testing.T) {
	h := newGuestHarness(t)
	failure := &stream.StartupFailure{Stage: "hello", Category: "eof", OpenMillis: 3, SourceWriteMillis: 4, HelloMillis: 5, ExitObserved: true, ExitCode: 1}
	h.service.transport = &startupFailureTransport{guestTestTransport: h.transport, err: fmt.Errorf("private path and credentials: %w", failure)}
	out, err := h.service.Execute(t.Context(), h.params("prepare", "startup-failed"), nil, nil)
	copied, ok := errors.AsType[*stream.StartupFailure](err)
	if !ok || copied == failure || *copied != *failure || !errors.Is(err, ErrGuestUncertain) || strings.Contains(err.Error(), "private") || h.transport.executed != 0 {
		t.Fatalf("startup diagnostic lost/leaked: %v", err)
	}
	if out.Receipt == nil || out.Receipt.Outcome.Status != domain.OutcomeFailed || !slices.Equal(out.Receipt.EvidenceRefs, []string{"guest-operation-dispatched", failure.Error()}) || h.transport.retired != 1 {
		t.Fatalf("startup receipt/close obligation lost: %+v", out)
	}
}

func TestGuestPrepareStartupDiagnosticKeepsUnknownLaunchAndBlocksReplay(t *testing.T) {
	h := newGuestHarness(t)
	h.service.transport = &startupFailureTransport{guestTestTransport: h.transport, err: &stream.StartupFailure{Stage: "hello", Category: "eof", ExitObserved: true, ExitCode: 1}}
	_, err := h.service.Execute(t.Context(), h.params("prepare", "startup-failed"), nil, nil)
	if !errors.Is(err, ErrGuestUncertain) || h.transport.executed != 0 {
		t.Fatal("unknown startup crossed effect boundary", err)
	}
	records := startupObligationRecords(t, h)
	if len(records[".launch"]) != 1 || len(records[".retirement"]) != 1 {
		t.Fatalf("expected one unknown launch and retirement: %+v", records)
	}
	launch, pending := records[".launch"][0], records[".retirement"][0]
	if launch != pending || pending.Identity != (stream.WorkerIdentity{}) {
		t.Fatalf("unknown obligation lost: launch=%+v pending=%+v", launch, pending)
	}
	_, err = h.service.Execute(t.Context(), h.params("prepare", "startup-new-attempt"), nil, nil)
	if !errors.Is(err, ErrGuestUncertain) || h.transport.prepared != 1 {
		t.Fatal("unknown startup replayed", err)
	}
}

// Read the durable obligation inventory through the typed original store.
// Unknown startup must have no admitted-ready or completed-retirement document.
func startupObligationRecords(t *testing.T, h *guestHarness) map[string][]guestWorkerRecord {
	t.Helper()
	entries, err := os.ReadDir(h.service.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	records := make(map[string][]guestWorkerRecord)
	for _, entry := range entries {
		suffix := filepath.Ext(entry.Name())
		switch suffix {
		case ".ready", ".retired":
			t.Fatal("unknown startup falsely admitted/retired")
		case ".launch", ".retirement":
			id, _ := strings.CutSuffix(entry.Name(), suffix)
			var record guestWorkerRecord
			if err := h.service.store.read(t.Context(), id, suffix, &record); err != nil {
				t.Fatal(err)
			}
			records[suffix] = append(records[suffix], record)
		}
	}
	return records
}

func TestGuestStartupDiagnosticRejectsUntrustedFields(t *testing.T) {
	for _, failure := range []*stream.StartupFailure{
		{Stage: "private", Category: "eof"}, {Stage: "hello", Category: "private"},
		{Stage: "hello", Category: "eof", HelloMillis: -1}, {Stage: "hello", Category: "eof", ExitObserved: true, ExitCode: -1},
	} {
		err := safeGuestTransportError(fmt.Errorf("private: %w", failure))
		if err != ErrGuestUncertain || guestFailureEvidence(err) != "" {
			t.Fatal("invalid startup leaked", err)
		}
	}
}
