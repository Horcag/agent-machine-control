package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestConsoleCorruptOrUnsafeMetadataCannotDrivePointer(t *testing.T) {
	for _, variant := range []string{"wrong-target", "trailing", "unknown", "oversized", "symlink", "public", "future", "frame-id"} {
		t.Run(variant, func(t *testing.T) {
			if variant == "public" && runtime.GOOS == "windows" {
				t.Skip("POSIX permission mutation; native Windows ACL guards have separate coverage")
			}
			f := newConsoleFixture(t)
			frame, err := f.service.Screenshot(context.Background(), f.actor, app.ConsoleScreenshotRequest{Width: 100, Height: 50})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.root, "console-frames", frame.FrameID)
			corruptConsoleMetadata(t, path, frame, variant, *f.now)
			input := domain.ConsoleInput{Kind: "move", FrameID: filepath.Base(path), X: 50, Y: 25}
			req := f.approved(t, input, "metadata-"+variant)
			if _, err := f.service.Input(context.Background(), f.actor, req); err == nil || len(f.provider.inputs) != 0 {
				t.Fatal("unsafe metadata executed pointer input")
			}
		})
	}
}

func TestConsolePruningPreservesUnownedFilesAndEnforcesCapacity(t *testing.T) {
	f := newConsoleFixture(t)
	frame, err := f.service.Screenshot(context.Background(), f.actor, app.ConsoleScreenshotRequest{Width: 8, Height: 8})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.root, "console-frames")
	unowned := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(unowned, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, frame.FrameID)
	old := f.now.Add(-3 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Screenshot(context.Background(), f.actor, app.ConsoleScreenshotRequest{Width: 8, Height: 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired metadata remains: %v", err)
	}
	if _, err := os.Stat(unowned); err != nil {
		t.Fatal("unowned entry removed")
	}
	for i := range 512 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%032x", i)), []byte("synthetic"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.Screenshot(context.Background(), f.actor, app.ConsoleScreenshotRequest{Width: 8, Height: 8}); err == nil {
		t.Fatal("unbounded metadata retention")
	}
}

func TestConsoleFailedInputRetryRemainsFailed(t *testing.T) {
	f := newConsoleFixture(t)
	f.provider.failInput = true
	req := f.approved(t, domain.ConsoleInput{Kind: "type", Text: "synthetic secret"}, "failed-retry")
	for range 2 {
		if _, err := f.service.Input(context.Background(), f.actor, req); err == nil {
			t.Fatal("failed input reported success")
		}
	}
	if len(f.provider.inputs) != 1 {
		t.Fatal("failed idempotent input repeated")
	}
}

func corruptConsoleMetadata(t *testing.T, path string, frame domain.ConsoleFrame, variant string, now time.Time) {
	t.Helper()
	if variant == "symlink" {
		if err := os.Rename(path, path+".owned"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path+".owned", path); err != nil {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		return
	}
	if variant == "public" {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data := corruptConsoleMetadataBytes(t, frame, variant, now)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func corruptConsoleMetadataBytes(t *testing.T, frame domain.ConsoleFrame, variant string, now time.Time) []byte {
	t.Helper()
	frame.Data = nil
	switch variant {
	case "wrong-target":
		frame.VMID = "local:bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	case "future":
		frame.ObservedAt = now.Add(time.Minute)
	case "frame-id":
		frame.FrameID = strings.Repeat("a", 32)
	case "unknown":
		return []byte(`{"unexpected":true}`)
	case "oversized":
		return []byte(strings.Repeat("x", 4097))
	}
	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if variant == "trailing" {
		data = append(data, []byte(" {}")...)
	}
	return data
}
