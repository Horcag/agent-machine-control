package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

type guestReplacementTransport struct {
	*guestTestTransport
	events *[]string
	old    stream.WorkerIdentity
	closed bool
}

func (f *guestReplacementTransport) Retire(target domain.MachineRef, key, generation string) error {
	*f.events = append(*f.events, "retire")
	f.closed = true
	return f.guestTestTransport.Retire(target, key, generation)
}
func (f *guestReplacementTransport) Prepare(ctx context.Context, target domain.MachineRef, key, generation string) (stream.WorkerIdentity, error) {
	*f.events = append(*f.events, "prepare")
	if f.closed {
		f.worker.PID++
		f.worker.BirthTicks++
	}
	return f.guestTestTransport.Prepare(ctx, target, key, generation)
}

type guestReplacementConfig struct {
	*guestTestConfig
	transport *guestReplacementTransport
	absent    bool
}

func (c *guestReplacementConfig) ConfirmGuestRetirement(_ context.Context, _ domain.MachineRef, _ domain.ActorID, _ string, worker stream.WorkerIdentity) (bool, error) {
	*c.transport.events = append(*c.transport.events, "confirm")
	if worker != c.transport.old {
		return false, ErrGuestOwnership
	}
	return c.absent && c.transport.closed, nil
}
func replacementHarness(t *testing.T) (*guestHarness, *guestReplacementTransport, *guestReplacementConfig, *[]string) {
	t.Helper()
	h := newGuestHarness(t)
	events := []string{}
	transport := &guestReplacementTransport{guestTestTransport: h.transport, events: &events, old: h.transport.worker}
	config := &guestReplacementConfig{guestTestConfig: h.config, transport: transport, absent: true}
	h.service.transport, h.service.config = transport, config
	h.prepare(t)
	events = nil
	return h, transport, config, &events
}
func currentReplacementRecord(t *testing.T, h *guestHarness) (string, guestWorkerRecord) {
	t.Helper()
	for key, worker := range h.service.workers {
		return key, worker
	}
	t.Fatal("no admitted original")
	return "", guestWorkerRecord{}
}
func TestGuestHealthyReprepareRetiresOriginalBeforeReplacement(t *testing.T) {
	h, transport, _, events := replacementHarness(t)
	_, original := currentReplacementRecord(t, h)
	out, err := h.service.Execute(t.Context(), h.params("prepare", "replacement"), nil, nil)
	if err != nil || !slices.Equal(*events, []string{"retire", "confirm", "prepare"}) || transport.prepared != 2 || out.Worker == original.Identity {
		t.Fatalf("replacement crossed retirement barrier: %+v %v events=%v", out, err, *events)
	}
	var retired guestWorkerRecord
	if err = h.service.store.read(t.Context(), original.RecordID, ".retired", &retired); err != nil || retired != original {
		t.Fatalf("original retirement changed: %+v %v", retired, err)
	}
}
func TestGuestHealthyReprepareNativeRefusalPreventsNewPrepare(t *testing.T) {
	h, transport, config, events := replacementHarness(t)
	config.absent = false
	_, original := currentReplacementRecord(t, h)
	_, err := h.service.Execute(t.Context(), h.params("prepare", "refused"), nil, nil)
	if !errors.Is(err, ErrGuestUncertain) || !slices.Equal(*events, []string{"retire", "confirm"}) || transport.prepared != 1 {
		t.Fatalf("unconfirmed replacement: %v events=%v", err, *events)
	}
	var pending guestWorkerRecord
	if err = h.service.store.read(t.Context(), original.RecordID, ".retirement", &pending); err != nil || pending != original {
		t.Fatal("original obligation lost", err)
	}
}
func TestGuestHealthyReprepareRejectsOriginalDocumentDrift(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		alter        func(*guestWorkerRecord)
	}{
		{"missing_worker", ".worker", nil}, {"missing_ready", ".ready", nil},
		{"generation", ".ready", func(r *guestWorkerRecord) { r.Generation = "foreign" }},
		{"caller", ".worker", func(r *guestWorkerRecord) { r.Caller = "foreign" }},
		{"actor", ".ready", func(r *guestWorkerRecord) { r.Actor = "foreign" }},
		{"root", ".worker", func(r *guestWorkerRecord) { r.Root.FileID = "foreign" }},
		{"identity", ".ready", func(r *guestWorkerRecord) { r.Identity.BirthTicks++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, transport, _, events := replacementHarness(t)
			_, original := currentReplacementRecord(t, h)
			path := filepath.Join(h.service.store.directory, original.RecordID+tc.suffix)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if tc.alter != nil {
				changed := original
				tc.alter(&changed)
				if err := h.service.store.write(t.Context(), original.RecordID, tc.suffix, changed); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.service.Execute(t.Context(), h.params("prepare", "drift"), nil, nil); !errors.Is(err, ErrGuestUncertain) || transport.prepared != 1 || len(*events) != 0 {
				t.Fatalf("drift reached effect: %v events=%v", err, *events)
			}
		})
	}
}
func TestGuestHealthyRepreparePendingRetirementRemainsFirstBarrier(t *testing.T) {
	h, transport, config, events := replacementHarness(t)
	config.absent = false
	_, original := currentReplacementRecord(t, h)
	if err := h.service.store.write(t.Context(), original.RecordID, ".retirement", original); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Execute(t.Context(), h.params("prepare", "pending"), nil, nil); !errors.Is(err, ErrGuestUncertain) || transport.prepared != 1 || !slices.Equal(*events, []string{"confirm"}) {
		t.Fatalf("pending bypassed: %v events=%v", err, *events)
	}
}
