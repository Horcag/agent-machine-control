package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func recordingStoreFixture(t *testing.T) (*ConsoleService, recordingStatusDocument, string) {
	t.Helper()
	now := time.Now().UTC()
	s := &ConsoleService{framesDir: filepath.Join(t.TempDir(), "console-frames"), recovery: &RecoveryService{nowFn: func() time.Time { return now }}}
	id := "a123456789abcdef0123456789abcdef"
	dir := filepath.Join(s.recordingDirectory(), id)
	if err := statedir.EnsurePrivateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	doc := recordingStatusDocument{SchemaVersion: 1, Caller: "operator:test", Actor: "operator:test", Status: ConsoleRecordStatus{SchemaVersion: "1", RecordingID: id, VMID: "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001", RequestedFrames: 2, StartedAt: now, UpdatedAt: now, ExpiresAt: now.Add(recordingStatusTTL)}}
	return s, doc, dir
}

func TestRecordingStatusRejectsCorruptSnapshots(t *testing.T) {
	cases := []struct {
		name   string
		change func(*recordingStatusDocument) []byte
	}{
		{"actor", func(d *recordingStatusDocument) []byte { d.Actor = ""; return nil }},
		{"schema", func(d *recordingStatusDocument) []byte { d.SchemaVersion = 2; return nil }},
		{"vm", func(d *recordingStatusDocument) []byte { d.Status.VMID = "invalid"; return nil }},
		{"counts", func(d *recordingStatusDocument) []byte { d.Status.CompletedCaptures = 3; return nil }},
		{"inflight", func(d *recordingStatusDocument) []byte { d.Status.CaptureInFlight = true; return nil }},
		{"time", func(d *recordingStatusDocument) []byte {
			d.Status.ExpiresAt = d.Status.ExpiresAt.Add(time.Hour)
			return nil
		}},
		{"running-reason", func(d *recordingStatusDocument) []byte { d.Status.TerminalReason = "failed"; return nil }},
		{"terminal-inflight", func(d *recordingStatusDocument) []byte {
			d.Status.Terminal = true
			d.Status.TerminalReason = "failed"
			d.Status.AttemptedCaptures = 1
			d.Status.CaptureInFlight = true
			return nil
		}},
		{"terminal-reason", func(d *recordingStatusDocument) []byte {
			d.Status.Terminal = true
			d.Status.TerminalReason = "guest secret"
			return nil
		}},
		{"terminal-counts", func(d *recordingStatusDocument) []byte {
			d.Status.Terminal = true
			d.Status.TerminalReason = "completed"
			return nil
		}},
		{"sequence", func(d *recordingStatusDocument) []byte { d.Sequence = 1; return nil }},
		{"duplicate", func(*recordingStatusDocument) []byte { return []byte(`{"schema_version":1,"schema_version":1}`) }},
		{"trailing", func(*recordingStatusDocument) []byte { return []byte(`{} {}`) }},
		{"oversized", func(*recordingStatusDocument) []byte { return make([]byte, 4097) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, doc, dir := recordingStoreFixture(t)
			data := tc.change(&doc)
			if data == nil {
				data, _ = json.Marshal(doc)
			}
			if err := os.WriteFile(filepath.Join(dir, "00.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := target.NewPrivatePathSecurity().ProtectNewFile(t.Context(), filepath.Join(dir, "00.json")); err != nil {
				t.Fatal(err)
			}
			if _, err := s.readRecordingStatus(t.Context(), doc.Status.RecordingID); !errors.Is(err, ErrRecordingStatusInconclusive) {
				t.Fatalf("invalid accepted %v", err)
			}
		})
	}
}

func TestRecordingStatusRejectsInsecureSnapshots(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "unknown-name", "publishing", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS == "windows" && kind == "permissions" {
				t.Skip("POSIX mode test; Windows privacy is ACL-based")
			}
			s, doc, dir := recordingStoreFixture(t)
			if err := s.writeRecordingStatus(t.Context(), doc); err != nil {
				t.Fatal(err)
			}
			corruptRecordingSnapshotPath(t, dir, kind)
			if _, err := s.readRecordingStatus(t.Context(), doc.Status.RecordingID); !errors.Is(err, ErrRecordingStatusInconclusive) {
				t.Fatalf("insecure accepted %v", err)
			}
		})
	}
}

func TestRecordingStatusStorePublicationBoundsAndCancellation(t *testing.T) {
	s, doc, dir := recordingStoreFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.writeRecordingStatus(ctx, doc); err == nil {
		t.Fatal("canceled publication succeeded")
	}
	if _, err := s.readRecordingStatus(ctx, doc.Status.RecordingID); err == nil {
		t.Fatal("canceled read succeeded")
	}
	if err := s.writeRecordingStatus(t.Context(), doc); err != nil {
		t.Fatal(err)
	}
	if err := s.writeRecordingStatus(t.Context(), doc); err == nil {
		t.Fatal("snapshot overwritten")
	}
	doc.Sequence = 62
	if err := s.writeRecordingStatus(t.Context(), doc); err == nil {
		t.Fatal("snapshot capacity exceeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "62.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overflow file: %v", err)
	}
}

func corruptRecordingSnapshotPath(t *testing.T, dir, kind string) {
	t.Helper()
	path := filepath.Join(dir, "00.json")
	switch kind {
	case "symlink":
		if err := os.Rename(path, path+".owned"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path+".owned", path); err != nil {
			t.Skip(err)
		}
	case "directory":
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	case "unknown-name":
		if err := os.WriteFile(filepath.Join(dir, "unknown"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	case "publishing":
		if err := os.WriteFile(filepath.Join(dir, "publishing"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	case "permissions":
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecordingStatusPublicationMarkerAppearingAfterInitialCheckIsInconclusive(t *testing.T) {
	s, doc, dir := recordingStoreFixture(t)
	if err := s.writeRecordingStatus(t.Context(), doc); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// Simulate a reader past its initial check while a writer publishes a
	// terminal snapshot but fails its directory sync, leaving the marker.
	if _, err := root.Lstat("publishing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	doc.Sequence = 1
	doc.Status.Terminal = true
	doc.Status.TerminalReason = "completed"
	doc.Status.AttemptedCaptures = 2
	doc.Status.CompletedCaptures = 2
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := target.NewPrivatePathSecurity().ProtectNewFile(t.Context(), filepath.Join(dir, "01.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "publishing"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readRecordingSnapshot(t.Context(), root, dir, "01.json", doc.Status.RecordingID, target.NewPrivatePathSecurity()); !errors.Is(err, ErrRecordingStatusInconclusive) || got.Status.Terminal {
		t.Fatalf("partial publication trusted: %+v %v", got, err)
	}
}
