package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recordingWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (ctx *recordingWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.entered) })
	return ctx.Context.Done()
}

func TestRecordingStatusCancellationWhileFrameWaitIsActive(t *testing.T) {
	service, doc, _ := recordingStoreFixture(t)
	doc.Status.AttemptedCaptures = 1
	doc.Status.CompletedCaptures = 1
	if err := service.writeRecordingStatus(t.Context(), doc); err != nil {
		t.Fatal(err)
	}
	progress := &recordingProgress{service: service, document: doc}
	ctx, cancel := context.WithCancel(t.Context())
	waiting := &recordingWaitContext{Context: ctx, entered: make(chan struct{})}
	result := make(chan error, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("owned frame wait did not stop during cleanup")
		}
	})
	go func() { defer close(finished); result <- waitRecordingFrame(waiting, 2000) }()
	select {
	case <-waiting.entered:
	case err := <-result:
		t.Fatalf("wait ended before cancellation: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("frame wait did not begin")
	}
	cancel()
	assertRecordingWaitStatus(t, service, doc.Status.RecordingID, false)
	var waitErr error
	select {
	case waitErr = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("frame wait ignored cancellation")
	}
	if !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("wait error %v", waitErr)
	}
	if err := progress.finish(waitErr); err != nil {
		t.Fatal(err)
	}
	assertRecordingWaitStatus(t, service, doc.Status.RecordingID, true)
}

func assertRecordingWaitStatus(t *testing.T, service *ConsoleService, id string, terminal bool) {
	t.Helper()
	got, err := service.readRecordingStatus(t.Context(), id)
	reason := ""
	if terminal {
		reason = "canceled"
	}
	if err != nil || got.Status.Terminal != terminal || got.Status.CaptureInFlight || got.Status.TerminalReason != reason || got.Status.AttemptedCaptures != 1 || got.Status.CompletedCaptures != 1 {
		t.Fatalf("frame wait status %+v %v", got, err)
	}
}
