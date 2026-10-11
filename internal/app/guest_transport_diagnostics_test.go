package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

func TestGuestTransportSentinelsRecordNoWrappedSecretsOrOutput(t *testing.T) {
	cases := []struct {
		category string
		cause    error
	}{
		{"protocol", stream.ErrProtocol}, {"integrity", stream.ErrIntegrity}, {"eof", io.EOF},
		{"unexpected_eof", io.ErrUnexpectedEOF}, {"short_write", io.ErrShortWrite},
	}
	for _, tc := range cases {
		t.Run(tc.category, func(t *testing.T) {
			h := newGuestHarness(t)
			h.prepare(t)
			h.transport.executeHook = func(_ context.Context, _ stream.Request, _ io.Reader, sink io.Writer) (stream.Result, error) {
				_, _ = sink.Write([]byte("private output"))
				return stream.Result{}, fmt.Errorf("private path credentials: %w", tc.cause)
			}
			p := h.params("exec", "transport-diagnostic")
			p.Size, p.SHA256, p.MaxOutput = 1, guestSHA("x"), 64
			var output bytes.Buffer
			out, err := h.service.Execute(t.Context(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(&output))
			if !errors.Is(err, ErrGuestUncertain) || !errors.Is(err, tc.cause) || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe error: %v", err)
			}
			assertGuestFailedDiagnostic(t, h, out, "guest-transport-failure:"+tc.category)
			if output.Len() != 0 {
				t.Fatal("failed output published")
			}
			assertGuestSpoolClean(t, h)
		})
	}
}

func assertGuestFailedDiagnostic(t *testing.T, h *guestHarness, out GuestOperationResult, ref string) {
	t.Helper()
	if out.Receipt == nil || out.Receipt.Outcome.Status != domain.OutcomeFailed || !slices.Equal(out.Receipt.EvidenceRefs, []string{"guest-operation-dispatched", ref}) || h.transport.retired != 1 {
		t.Fatalf("failure/retirement contract: %+v retired=%d", out, h.transport.retired)
	}
	raw, err := json.Marshal(out)
	if err != nil || bytes.Contains(raw, []byte("private")) {
		t.Fatalf("result leaked metadata: %s %v", raw, err)
	}
	id := strings.TrimPrefix(string(out.Receipt.IdempotencyFingerprint), "sha256:")
	if _, err := os.Stat(filepath.Join(h.service.store.directory, id+".result")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed result committed", err)
	}
	assertGuestOriginalRetirement(t, h, out.Receipt)
}

func assertGuestOriginalRetirement(t *testing.T, h *guestHarness, receipt *domain.Receipt) {
	t.Helper()
	entries, err := os.ReadDir(h.service.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".retirement") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".retirement")
		var record guestWorkerRecord
		if err := h.service.store.read(t.Context(), id, ".retirement", &record); err != nil || record.RecordID != id || record.Identity != h.transport.worker {
			t.Fatalf("wrong original retirement: %+v %v", record, err)
		}
		var original guestWorkerRecord
		if err := h.service.store.read(t.Context(), id, ".worker", &original); err != nil || original != record || record.Target != receipt.Target || record.Actor != receipt.Actor || record.Caller != h.actor.AuthenticatedCaller {
			t.Fatalf("retirement did not preserve immutable original: %+v %v", record, err)
		}
		found++
	}
	if found != 1 {
		t.Fatalf("expected one original retirement, got %d", found)
	}
}

func TestGuestRootDiagnosticSurvivesRetirementCloseEOF(t *testing.T) {
	h := newGuestHarness(t)
	h.transport.alter = func(r *stream.Result) { r.Root.SDDLHash = strings.Repeat("e", 64) }
	h.transport.retireErr = io.EOF
	out, err := h.service.Execute(t.Context(), h.params("prepare", "root-and-close-failure"), nil, nil)
	if !errors.Is(err, ErrGuestOwnership) || !errors.Is(err, ErrGuestUncertain) || !errors.Is(err, io.EOF) {
		t.Fatalf("original failure lost: %v", err)
	}
	assertGuestFailedDiagnostic(t, h, out, "guest-result-failure:root_sddl")
}

func TestGuestPrepareRootDiagnosticsKeepEveryOriginalFieldExact(t *testing.T) {
	cases := map[string]func(*stream.Result){
		"missing":  func(r *stream.Result) { r.Root = nil },
		"path":     func(r *stream.Result) { r.Root.Path = `C:\private\foreign` },
		"volume":   func(r *stream.Result) { r.Root.Volume++ },
		"id":       func(r *stream.Result) { r.Root.FileID = strings.Repeat("f", 32) },
		"creation": func(r *stream.Result) { r.Root.CreationTime++ },
		"owner":    func(r *stream.Result) { r.Root.Owner = "private-owner" },
		"sddl":     func(r *stream.Result) { r.Root.SDDLHash = strings.Repeat("e", 64) },
	}
	for field, alter := range cases {
		t.Run(field, func(t *testing.T) {
			h := newGuestHarness(t)
			h.transport.alter = alter
			p := h.params("prepare", "root-diagnostic")
			out, err := h.service.Execute(t.Context(), p, nil, nil)
			if !errors.Is(err, ErrGuestOwnership) || !errors.Is(err, ErrGuestUncertain) {
				t.Fatalf("root admitted: %+v %v", out, err)
			}
			assertGuestFailedDiagnostic(t, h, out, "guest-result-failure:root_"+field)
			if strings.Contains(err.Error(), "private") {
				t.Fatal("root value exposed", err)
			}
			if _, err := h.service.Execute(t.Context(), p, nil, nil); err == nil || h.transport.executed != 1 || h.transport.prepared != 1 {
				t.Fatal("uncertain prepare replayed", err)
			}
		})
	}
}
