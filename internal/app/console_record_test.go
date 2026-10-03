package app_test

import (
	"bytes"
	"context"
	"errors"
	"image/gif"
	"os"
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
)

func TestConsoleRecordingFinalizesFramesAndTiming(t *testing.T) {
	f := newConsoleFixture(t)
	out, err := f.service.Record(t.Context(), f.actor, app.ConsoleRecordRequest{Width: 8, Height: 4, Frames: 2, IntervalMillis: 100})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(out.Data))
	if err != nil || len(decoded.Image) != 2 || len(out.ObservedAt) != 2 || decoded.Image[0].Bounds().Dx() != 8 || decoded.Delay[0] < 1 || out.MIMEType != "image/gif" || len(out.SHA256) != 64 {
		t.Fatalf("invalid finalized recording: %+v %v", out, err)
	}
}

func TestConsoleRecordingCancellationAfterFinalCaptureRefusesArtifact(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	provider := &boundsRecordingProviderFake{}
	removeLock := func(path string) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		if provider.captures == 2 && filepath.Base(path) == "console-frame-metadata.lock" {
			leasePath := filepath.Join(filepath.Dir(path), "console-frame-metadata.lease.json")
			if _, err := os.Stat(leasePath); os.IsNotExist(err) {
				// The final valid screenshot has saved metadata and released its lease.
				cancel()
			}
		}
		return nil
	}
	f := newBoundsRecordingFixture(t, provider, lease.WithRemoveFunc(removeLock))

	out, err := f.service.Record(ctx, f.actor, app.ConsoleRecordRequest{Width: 8, Height: 4, Frames: 2, IntervalMillis: 100})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("final-capture cancellation = %v, want context.Canceled", err)
	}
	if len(out.Data) != 0 || out.MIMEType != "" || out.SHA256 != "" {
		t.Fatalf("canceled recording published artifact: %+v", out)
	}
	if provider.captures != 2 {
		t.Fatalf("captures = %d, want 2", provider.captures)
	}
}

func TestConsoleRecordingBoundsCancellationAndSensitiveAdmission(t *testing.T) {
	f := newConsoleFixture(t)
	for _, req := range []app.ConsoleRecordRequest{{Width: 8, Height: 4, Frames: 1, IntervalMillis: 100}, {Width: 8, Height: 4, Frames: 31, IntervalMillis: 100}, {Width: 65535, Height: 65535, Frames: 2, IntervalMillis: 100}, {Width: 8, Height: 4, Frames: 30, IntervalMillis: 2000}} {
		if _, err := f.service.Record(t.Context(), f.actor, req); err == nil {
			t.Fatal("unbounded recording accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := app.ConsoleRecordRequest{Width: 8, Height: 4, Frames: 2, IntervalMillis: 100}
	if _, err := f.service.Record(ctx, f.actor, req); err == nil {
		t.Fatal("canceled recording accepted")
	}
	actor, _ := domain.NewActorContext("agent:test", "agent:test", domain.NewScopeSet(domain.ScopeMachineRead), domain.NewScopeSet(domain.ScopeMachineRead))
	if _, err := f.service.Record(t.Context(), actor, req); err == nil || f.provider.captures != 0 {
		t.Fatal("unauthorized recording reached provider")
	}
	f.provider.badPNG = true
	if _, err := f.service.Record(t.Context(), f.actor, req); err == nil {
		t.Fatal("bad frame accepted")
	}
}
