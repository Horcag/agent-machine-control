package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestTerminalBatchScansThousandsOfFinalizedReceiptsOnce(t *testing.T) {
	dir := t.TempDir()
	requests := make([]TerminalOutcomeRequest, 2400)
	f, err := os.Create(filepath.Join(dir, AuditFileName))
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(f)
	for i := range requests {
		r := testTerminalReceipt()
		r.ReceiptID = domainReceiptID(i)
		requests[i] = TerminalOutcomeRequest{Receipt: r, RequireExisting: i%2 == 0}
		if err := encoder.Encode(terminalOutcomeEvent(r)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	reads, syncs := 0, 0
	store := NewStore(dir, WithSyncDir(func(string) error { syncs++; return nil }))
	store.openReadFn = func(path string) (*os.File, error) { reads++; return os.Open(path) }
	if err := store.EnsureTerminalOutcomesContext(context.Background(), requests); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || syncs != 1 {
		t.Fatalf("audit reads = %d, directory syncs = %d; want one each", reads, syncs)
	}
	requireAuditEventCount(t, dir, len(requests))
}

func TestTerminalBatchRejectsInvalidEvidence(t *testing.T) {
	for _, kind := range []string{"duplicate", "identity", "outcome", "envelope", "corrupt", "missing legacy", "request collision"} {
		t.Run(kind, func(t *testing.T) { runTerminalBatchInvalidEvidence(t, kind) })
	}
}

func runTerminalBatchInvalidEvidence(t *testing.T, kind string) {
	t.Helper()
	dir := t.TempDir()
	r := testTerminalReceipt()
	event := terminalOutcomeEvent(r)
	requests := []TerminalOutcomeRequest{{Receipt: r}}
	switch kind {
	case "identity":
		event.Target = "different-target"
	case "outcome":
		event.ExitCode = 9
	case "envelope":
		event.SchemaVersion = "0"
	case "missing legacy":
		requests[0].RequireExisting = true
		event.ReceiptID = "different-receipt"
	case "request collision":
		conflict := r
		conflict.IdempotencyKey = "conflict"
		requests = append(requests, TerminalOutcomeRequest{Receipt: conflict})
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if kind == "duplicate" {
		data = append(data, data...)
	}
	if kind == "corrupt" {
		data = []byte("{corrupt\n")
	}
	if err := os.WriteFile(filepath.Join(dir, AuditFileName), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(dir).EnsureTerminalOutcomesContext(context.Background(), requests); !errors.Is(err, ErrTerminalEvidenceInvalid) {
		t.Fatalf("error = %v, want invalid terminal evidence", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, AuditFileName))
	if err != nil || string(got) != string(data) {
		t.Fatalf("rejection changed audit log: %v", err)
	}
}

func TestTerminalBatchConcurrentRecoveryDoesNotDuplicate(t *testing.T) {
	dir := t.TempDir()
	r := testTerminalReceipt()
	requests := []TerminalOutcomeRequest{{Receipt: r}, {Receipt: r}}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := NewStore(dir).EnsureTerminalOutcomesContext(context.Background(), requests); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	requireAuditEventCount(t, dir, 1)
}

func TestTerminalBatchCancellationRetainsDurablePrefix(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	store := NewStore(dir, WithPostAppendHook(cancel))
	first := testTerminalReceipt()
	second := first
	second.ReceiptID = domainReceiptID(2)
	requests := []TerminalOutcomeRequest{{Receipt: first}, {Receipt: second}}
	if err := store.EnsureTerminalOutcomesContext(ctx, requests); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want canceled", err)
	}
	requireAuditEventCount(t, dir, 1)
	if err := NewStore(dir).EnsureTerminalOutcomesContext(context.Background(), requests); err != nil {
		t.Fatal(err)
	}
	requireAuditEventCount(t, dir, 2)
}

func domainReceiptID(i int) domain.ReceiptID {
	return domain.ReceiptID(fmt.Sprintf("rcpt-%032x", i+1))
}

func TestTerminalBatchStorageFailureLeavesRecoverableIntent(t *testing.T) {
	for _, boundary := range []string{"ensure", "append", "close", "sync"} {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			injected := errors.New("synthetic storage failure")
			store := NewStore(dir)
			switch boundary {
			case "ensure":
				store.ensureHook = func(context.Context, Event) error { return injected }
			case "append":
				store.appendHook = func(Event) error { return injected }
			case "close":
				store.closeFn = func(f *os.File) error { return errors.Join(f.Close(), injected) }
			case "sync":
				store.syncDirFn = func(string) error { return injected }
			}
			r := testTerminalReceipt()
			requests := []TerminalOutcomeRequest{{Receipt: r}}
			if err := store.EnsureTerminalOutcomesContext(context.Background(), requests); !errors.Is(err, injected) {
				t.Fatalf("error = %v, want injected failure", err)
			}
			if err := NewStore(dir).EnsureTerminalOutcomesContext(context.Background(), requests); err != nil {
				t.Fatal(err)
			}
			requireAuditEventCount(t, dir, 1)
			if _, err := os.Stat(filepath.Join(dir, ".audit.lock")); !os.IsNotExist(err) {
				t.Fatalf("audit lock remains after recovery: %v", err)
			}
		})
	}
}

func TestTerminalBatchRejectsInvalidReceiptAndCanceledContext(t *testing.T) {
	r := testTerminalReceipt()
	var unavailable *Store
	if err := unavailable.EnsureTerminalOutcomesContext(context.Background(), nil); !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("nil store error = %v", err)
	}
	store := NewStore(t.TempDir())
	if err := store.EnsureTerminalOutcomesContext(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	invalid := r
	invalid.ReceiptID = "invalid"
	if err := store.EnsureTerminalOutcomesContext(context.Background(), []TerminalOutcomeRequest{{Receipt: invalid}}); err == nil {
		t.Fatal("invalid receipt accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.EnsureTerminalOutcomesContext(ctx, []TerminalOutcomeRequest{{Receipt: r}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v", err)
	}
}
