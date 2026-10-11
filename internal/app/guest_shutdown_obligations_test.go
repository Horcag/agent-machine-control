package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func restartGuestService(h *guestHarness, config GuestConfigProvider) {
	h.service = NewGuestService(h.service.session, h.service.resolver, config, h.transport, h.service.store)
}
func originalGuestEpoch(t *testing.T, h *guestHarness) guestWorkerRecord {
	t.Helper()
	for _, worker := range h.service.workers {
		return worker
	}
	t.Fatal("missing fixture worker")
	return guestWorkerRecord{}
}

func TestGuestRestartShutdownPreservesLiveObligation(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	worker := originalGuestEpoch(t, h)
	verifier := &guestVerifiedConfig{guestTestConfig: h.config}
	restartGuestService(h, verifier)
	if err := h.service.Shutdown(context.Background()); !errors.Is(err, ErrGuestUncertain) || h.service.shutdownDone {
		t.Fatalf("live restart claimed shutdown: %v done=%v", err, h.service.shutdownDone)
	}
	if h.transport.retired != 0 || len(h.service.workers) != 0 {
		t.Fatal("durable evidence was adopted as channel ownership")
	}
	var intent guestWorkerRecord
	if err := h.service.store.read(context.Background(), worker.RecordID, ".retirement", &intent); err != nil || intent.Identity != worker.Identity {
		t.Fatalf("original obligation not durable: %+v %v", intent, err)
	}
}

func TestGuestRestartShutdownVerifiesOriginalAbsence(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	worker := originalGuestEpoch(t, h)
	verifier := &guestVerifiedConfig{guestTestConfig: h.config, absent: true}
	h.transport.worker.PID++
	h.transport.worker.BirthTicks++
	restartGuestService(h, verifier)
	if err := h.service.Shutdown(context.Background()); err != nil || !h.service.shutdownDone {
		t.Fatalf("verified restart shutdown: %v", err)
	}
	if h.transport.retired != 0 || len(verifier.checked) != 1 || verifier.checked[0] != worker.Identity {
		t.Fatalf("original identity replaced or retired: %+v", verifier.checked)
	}
	var retired guestWorkerRecord
	if err := h.service.store.read(context.Background(), worker.RecordID, ".retired", &retired); err != nil || retired.Identity != worker.Identity {
		t.Fatalf("absence not reconciled: %v", err)
	}
}

func TestGuestRestartShutdownFailsClosedOnDamagedRecords(t *testing.T) {
	for _, damage := range []string{"corrupt-launch", "missing-launch", "missing-identity", "changed-ready-identity", "unknown-name"} {
		t.Run(damage, func(t *testing.T) {
			h := newGuestHarness(t)
			h.prepare(t)
			worker := originalGuestEpoch(t, h)
			if err := damageGuestObligation(h, worker, damage); err != nil {
				t.Fatal(err)
			}
			restartGuestService(h, &guestVerifiedConfig{guestTestConfig: h.config, absent: true})
			if err := h.service.Shutdown(context.Background()); !errors.Is(err, ErrGuestUncertain) || h.service.shutdownDone {
				t.Fatalf("damaged obligation accepted: %v", err)
			}
			if h.transport.retired != 0 {
				t.Fatal("damaged evidence caused channel retirement")
			}
		})
	}
}

func restartShutdownTest(t *testing.T) (*guestHarness, *guestBlockingVerifier, chan error, context.CancelFunc) {
	t.Helper()
	h := newGuestHarness(t)
	h.prepare(t)
	verifier := &guestBlockingVerifier{guestTestConfig: h.config, entered: make(chan struct{}), release: make(chan struct{})}
	restartGuestService(h, verifier)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- h.service.Shutdown(ctx) }()
	select {
	case <-verifier.entered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("original absence verification did not start")
	}
	return h, verifier, first, cancel
}
func TestGuestRestartShutdownCancellation(t *testing.T) {
	h, verifier, first, cancel := restartShutdownTest(t)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) || h.service.shutdownDone {
		t.Fatalf("cancellation discarded: %v", err)
	}
	close(verifier.release)
	if h.transport.retired != 0 {
		t.Fatal("restart retired a foreign channel")
	}
}
func TestGuestRestartShutdownConcurrency(t *testing.T) {
	h, verifier, first, cancel := restartShutdownTest(t)
	defer cancel()
	second := make(chan error, 1)
	go func() { second <- h.service.Shutdown(context.Background()) }()
	close(verifier.release)
	for _, result := range []chan error{first, second} {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	verifier.mu.Lock()
	calls := verifier.calls
	verifier.mu.Unlock()
	if calls != 1 || h.transport.retired != 0 {
		t.Fatalf("duplicate or foreign effects: confirms=%d retired=%d", calls, h.transport.retired)
	}
}

func TestGuestShutdownRescansAfterEarlierSuccess(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	verified := &guestVerifiedConfig{guestTestConfig: h.config, absent: true}
	h.service.config = verified
	if err := h.service.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	worker := guestWorkerRecord{RecordID: strings.Repeat("f", 64), Target: domain.MachineRef(h.service.resolver.(guestTestResolver).resolution.Locator.String()), Caller: "other-caller", Actor: "other-actor", Generation: "other-generation", Root: fixtureGuestRoot()}
	if err := h.service.store.write(context.Background(), worker.RecordID, ".launch", worker); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Shutdown(context.Background()); !errors.Is(err, ErrGuestUncertain) || h.service.shutdownDone {
		t.Fatalf("new obligation ignored: %v", err)
	}
	if h.transport.retired != 1 {
		t.Fatal("a previously closed or foreign channel was retired again")
	}
}

func TestGuestShutdownObligationScanBounds(t *testing.T) {
	records := map[string][]string{}
	for i := range maxGuestShutdownObligations {
		id := guestDigest(i)
		if err := collectGuestObligationEntry(records, id+".launch"); err != nil {
			t.Fatal(err)
		}
	}
	if err := collectGuestObligationEntry(records, guestDigest("overflow")+".launch"); !errors.Is(err, ErrGuestUncertain) {
		t.Fatal("unbounded obligation scan")
	}
}

func damageGuestObligation(h *guestHarness, worker guestWorkerRecord, damage string) error {
	path := func(suffix string) string { return filepath.Join(h.service.store.directory, worker.RecordID+suffix) }
	switch damage {
	case "corrupt-launch":
		return os.WriteFile(path(".launch"), []byte("{broken"), 0600)
	case "missing-launch":
		return os.Remove(path(".launch"))
	case "missing-identity":
		return errors.Join(os.Remove(path(".worker")), os.Remove(path(".ready")))
	case "changed-ready-identity":
		var altered guestWorkerRecord
		if err := h.service.store.read(context.Background(), worker.RecordID, ".ready", &altered); err != nil {
			return err
		}
		altered.Identity.PID++
		if err := os.Remove(path(".ready")); err != nil {
			return err
		}
		return h.service.store.write(context.Background(), worker.RecordID, ".ready", altered)
	case "unknown-name":
		return os.WriteFile(filepath.Join(h.service.store.directory, "foreign.launch"), []byte("{}"), 0600)
	default:
		return errors.New("unknown synthetic damage")
	}
}
