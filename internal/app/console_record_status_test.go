package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

const statusRecordingID = "a123456789abcdef0123456789abcdef"

func statusRecordRequest() app.ConsoleRecordRequest {
	return app.ConsoleRecordRequest{Target: "default", RecordingID: statusRecordingID, Width: 8, Height: 4, Frames: 2, IntervalMillis: 100}
}

func readRecordingStatus(t *testing.T, f boundsRecordingFixture) app.ConsoleRecordStatus {
	t.Helper()
	got, err := f.service.RecordStatus(t.Context(), f.actor, app.ConsoleRecordStatusRequest{Target: "default", RecordingID: statusRecordingID})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRecordingStatusBlockedCaptureCancellationAndCollision(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	releaseCapture := func() { releaseOnce.Do(func() { close(release) }) }
	provider := &boundsRecordingProviderFake{onCapture: func(int) error { enteredOnce.Do(func() { close(entered) }); <-release; return nil }}
	f := newBoundsRecordingFixture(t, provider)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	finished := make(chan struct{})
	t.Cleanup(func() { cancel(); releaseCapture(); awaitRecordingCleanup(t, finished) })
	go func() {
		defer close(finished)
		out, err := f.service.Record(ctx, f.actor, statusRecordRequest())
		if len(out.Data) != 0 {
			done <- errors.New("artifact returned on cancellation")
			return
		}
		done <- err
	}()
	awaitStatusRecordingStart(t, entered, done)
	assert := func() {
		t.Helper()
		s := readRecordingStatus(t, f)
		if s.Terminal || !s.CaptureInFlight || s.AttemptedCaptures != 1 || s.CompletedCaptures != 0 {
			t.Fatalf("in-flight status %+v", s)
		}
	}
	assert()
	cancel()
	assert()
	if _, err := f.service.Record(t.Context(), f.actor, statusRecordRequest()); !errors.Is(err, app.ErrRecordingIDUnavailable) {
		t.Fatalf("duplicate: %v", err)
	}
	provider.mu.Lock()
	captures := provider.captures
	provider.mu.Unlock()
	if captures != 1 {
		t.Fatalf("duplicate dispatched: %d", captures)
	}
	releaseCapture()
	if err := awaitStatusRecordingResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result: %v", err)
	}
	s := readRecordingStatus(t, f)
	if !s.Terminal || s.CaptureInFlight || s.TerminalReason != "canceled" || s.AttemptedCaptures != 1 || s.CompletedCaptures != 1 {
		t.Fatalf("terminal status %+v", s)
	}
	if next := readRecordingStatus(t, f); next != s {
		t.Fatal("terminal counts changed")
	}
}

func TestRecordingStatusBeforeCaptureAndAfterCapture(t *testing.T) {
	for _, phase := range []string{"admission", "after-capture", "last-capture"} {
		t.Run(phase, func(t *testing.T) {
			testRecordingCancellationPhase(t, phase)
		})
	}
}

func TestRecordingStatusCompletedMetadataAdmissionAndExpiry(t *testing.T) {
	provider := &boundsRecordingProviderFake{}
	f := newBoundsRecordingFixture(t, provider)
	out, err := f.service.Record(t.Context(), f.actor, statusRecordRequest())
	if err != nil || len(out.Data) == 0 {
		t.Fatalf("record: %v", err)
	}
	s := readRecordingStatus(t, f)
	if !s.Terminal || s.TerminalReason != "completed" || s.CompletedCaptures != 2 || s.AttemptedCaptures != 2 || s.CaptureInFlight {
		t.Fatalf("status %+v", s)
	}
	f.backend.capabilitiesFn = func(context.Context, string) (domain.CapabilitySet, error) {
		t.Fatal("status performed provider admission")
		return nil, nil
	}
	if got := readRecordingStatus(t, f); got != s {
		t.Fatal("read changed status")
	}
	foreign := f.actor.Clone()
	foreign.AuthenticatedCaller = "foreign"
	foreign.EffectiveActor = "foreign"
	cases := []struct {
		actor  domain.ActorContext
		target string
	}{{foreign, "default"}, {f.actor, "local:c4a523d4-6b99-4d62-a5e2-4752c0f20002"}}
	missingScope := f.actor.Clone()
	missingScope.EffectivePermissions = domain.NewScopeSet(domain.ScopeMachineRead)
	cases = append(cases, struct {
		actor  domain.ActorContext
		target string
	}{missingScope, "default"})
	for _, c := range cases {
		if _, err := f.service.RecordStatus(t.Context(), c.actor, app.ConsoleRecordStatusRequest{Target: c.target, RecordingID: statusRecordingID}); err == nil {
			t.Fatal("foreign admission accepted")
		}
	}
	*f.now = f.now.Add(15 * time.Minute)
	if _, err := f.service.RecordStatus(t.Context(), f.actor, app.ConsoleRecordStatusRequest{Target: "default", RecordingID: statusRecordingID}); !errors.Is(err, app.ErrRecordingStatusInconclusive) {
		t.Fatalf("expiry: %v", err)
	}
}

func TestRecordingStatusCorruptMissingAndWriteFailure(t *testing.T) {
	provider := &boundsRecordingProviderFake{}
	f := newBoundsRecordingFixture(t, provider)
	req := app.ConsoleRecordStatusRequest{Target: "default", RecordingID: statusRecordingID}
	if _, err := f.service.RecordStatus(t.Context(), f.actor, req); !errors.Is(err, app.ErrRecordingStatusInconclusive) {
		t.Fatalf("missing: %v", err)
	}
	dir := filepath.Join(f.root, "console-recordings", statusRecordingID)
	provider.onCapture = func(int) error { return os.Mkdir(filepath.Join(dir, "pending"), 0700) }
	out, err := f.service.Record(t.Context(), f.actor, statusRecordRequest())
	if err == nil || len(out.Data) != 0 || strings.Contains(err.Error(), f.root) {
		t.Fatalf("write failure leaked or succeeded: %v", err)
	}
	if _, err := f.service.RecordStatus(t.Context(), f.actor, req); !errors.Is(err, app.ErrRecordingStatusInconclusive) {
		t.Fatalf("write failure fabricated conclusive status: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01.json"), []byte(`{"status":"guest secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RecordStatus(t.Context(), f.actor, req); !errors.Is(err, app.ErrRecordingStatusInconclusive) || strings.Contains(err.Error(), "guest secret") {
		t.Fatalf("corrupt: %v", err)
	}
}

func TestRecordingStatusProviderFailureAndIDValidation(t *testing.T) {
	provider := &boundsRecordingProviderFake{onCapture: func(int) error { return errors.New("guest secret") }}
	f := newBoundsRecordingFixture(t, provider)
	for _, id := range []string{"../escape", strings.ToUpper(statusRecordingID), "short"} {
		req := statusRecordRequest()
		req.RecordingID = id
		if _, err := f.service.Record(t.Context(), f.actor, req); err == nil {
			t.Fatal("invalid ID accepted")
		}
	}
	out, err := f.service.Record(t.Context(), f.actor, statusRecordRequest())
	if err == nil || len(out.Data) != 0 || strings.Contains(err.Error(), "guest secret") {
		t.Fatalf("failure result: %v", err)
	}
	s := readRecordingStatus(t, f)
	if !s.Terminal || s.TerminalReason != "failed" || s.CompletedCaptures != 0 || s.AttemptedCaptures != 1 || s.CaptureInFlight {
		t.Fatalf("failed status %+v", s)
	}
}

func testRecordingCancellationPhase(t *testing.T, phase string) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	provider := &boundsRecordingProviderFake{}
	f := newBoundsRecordingFixture(t, provider)
	if phase == "admission" {
		f.backend.capabilitiesFn = func(context.Context, string) (domain.CapabilitySet, error) {
			cancel()
			return domain.NewCapabilitySet(domain.CapabilityConsoleScreenshot), nil
		}
	}
	if phase == "after-capture" {
		provider.onCapture = func(int) error { cancel(); return nil }
	}
	if phase == "last-capture" {
		provider.onCapture = func(i int) error {
			if i == 1 {
				cancel()
			}
			return nil
		}
	}
	out, err := f.service.Record(ctx, f.actor, statusRecordRequest())
	if !errors.Is(err, context.Canceled) || len(out.Data) != 0 {
		t.Fatalf("cancel artifact/result: %d %v", len(out.Data), err)
	}
	s := readRecordingStatus(t, f)
	want := 0
	if phase == "after-capture" {
		want = 1
	}
	if phase == "last-capture" {
		want = 2
	}
	if s.AttemptedCaptures != want || s.CompletedCaptures != want || s.CaptureInFlight || !s.Terminal || s.TerminalReason != "canceled" {
		t.Fatalf("status %+v", s)
	}
}

func awaitRecordingCleanup(t *testing.T, finished <-chan struct{}) {
	t.Helper()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Error("owned recording producer did not stop during cleanup")
	}
}

func awaitStatusRecordingStart(t *testing.T, entered <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("producer ended before capture: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("capture did not begin")
	}
}

func awaitStatusRecordingResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("producer did not return after release")
		return nil
	}
}
