package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordingStatusFinalizationFailureCannotPublishTerminal(t *testing.T) {
	now := time.Now().UTC()
	service := &ConsoleService{framesDir: filepath.Join(t.TempDir(), "console-frames"), recovery: &RecoveryService{nowFn: func() time.Time { return now }}}
	id := "a123456789abcdef0123456789abcdef"
	dir := filepath.Join(service.recordingDirectory(), id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	progress := &recordingProgress{service: service, document: recordingStatusDocument{SchemaVersion: 1, Caller: "operator:test", Actor: "operator:test", Status: ConsoleRecordStatus{SchemaVersion: "1", RecordingID: id, VMID: "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001", RequestedFrames: 2, AttemptedCaptures: 2, CompletedCaptures: 2, StartedAt: now, UpdatedAt: now, ExpiresAt: now.Add(recordingStatusTTL)}}}
	if err := service.writeRecordingStatus(t.Context(), progress.document); err != nil {
		t.Fatal(err)
	}
	// A final publication cannot overwrite an unknown pending writer/object.
	if err := os.Mkdir(filepath.Join(dir, "pending"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := progress.finish(nil); !errors.Is(err, ErrRecordingStatusInconclusive) {
		t.Fatalf("final write failure %v", err)
	}
	if _, err := service.readRecordingStatus(t.Context(), id); !errors.Is(err, ErrRecordingStatusInconclusive) {
		t.Fatalf("terminal fabricated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "01.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal published: %v", err)
	}
}

func TestRecordingStatusFinalizationCancellationAndDeadline(t *testing.T) {
	for _, reason := range []struct {
		err  error
		want string
	}{{context.Canceled, "canceled"}, {context.DeadlineExceeded, "deadline_exceeded"}} {
		t.Run(reason.want, func(t *testing.T) {
			now := time.Now().UTC()
			service := &ConsoleService{framesDir: filepath.Join(t.TempDir(), "console-frames"), recovery: &RecoveryService{nowFn: func() time.Time { return now }}}
			id := "a123456789abcdef0123456789abcdef"
			if err := os.MkdirAll(filepath.Join(service.recordingDirectory(), id), 0700); err != nil {
				t.Fatal(err)
			}
			progress := &recordingProgress{service: service, document: recordingStatusDocument{SchemaVersion: 1, Caller: "operator:test", Actor: "operator:test", Status: ConsoleRecordStatus{SchemaVersion: "1", RecordingID: id, VMID: "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001", RequestedFrames: 2, AttemptedCaptures: 2, CompletedCaptures: 2, StartedAt: now, UpdatedAt: now, ExpiresAt: now.Add(recordingStatusTTL)}}}
			if err := progress.finish(reason.err); err != nil {
				t.Fatal(err)
			}
			got, err := service.readRecordingStatus(t.Context(), id)
			if err != nil || !got.Status.Terminal || got.Status.TerminalReason != reason.want || got.Status.CaptureInFlight || got.Status.CompletedCaptures != 2 {
				t.Fatalf("finalization %+v %v", got, err)
			}
		})
	}
}
