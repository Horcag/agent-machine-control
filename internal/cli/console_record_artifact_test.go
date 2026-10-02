package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color/palette"
	"image/gif"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

type consoleRecordStub struct {
	calls     int
	recording app.ConsoleRecording
	err       error
	req       app.ConsoleRecordRequest
	onRecord  func()
}

func (s *consoleRecordStub) Screenshot(_ context.Context, _ domain.ActorContext, _ app.ConsoleScreenshotRequest) (domain.ConsoleFrame, error) {
	return domain.ConsoleFrame{}, errors.New("stub screenshot not implemented")
}

func (s *consoleRecordStub) Input(_ context.Context, _ domain.ActorContext, _ app.ConsoleInputRequest) (domain.Receipt, error) {
	return domain.Receipt{}, errors.New("stub input not implemented")
}

func (s *consoleRecordStub) Record(_ context.Context, _ domain.ActorContext, req app.ConsoleRecordRequest) (app.ConsoleRecording, error) {
	s.calls++
	s.req = req
	if s.onRecord != nil {
		s.onRecord()
	}
	return s.recording, s.err
}

func createSyntheticGIFBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), palette.Plan9)
	anim := &gif.GIF{
		Image: []*image.Paletted{img, img},
		Delay: []int{10, 10},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatalf("failed to encode synthetic GIF: %v", err)
	}
	return buf.Bytes()
}

func TestConsoleRecordNewPrivateGIFFileSucceeds(t *testing.T) {
	dir := privateConsoleTestDir(t)
	path := filepath.Join(dir, "recording.gif")
	gifData := createSyntheticGIFBytes(t, 16, 8)
	digest := sha256.Sum256(gifData)
	sha := hex.EncodeToString(digest[:])
	observed := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)

	svc := &consoleRecordStub{
		recording: app.ConsoleRecording{
			MIMEType:   "image/gif",
			Width:      16,
			Height:     8,
			SHA256:     sha,
			Data:       gifData,
			ObservedAt: []time.Time{observed, observed.Add(100 * time.Millisecond)},
		},
	}
	a := NewApp(nil, WithConsoleService(svc))
	var out, errOut bytes.Buffer

	code := a.Run([]string{
		"--direct", "console", "record", "test-vm",
		"--output", path,
		"--width", "16",
		"--height", "8",
		"--frames", "2",
		"--interval-ms", "100",
		"--json",
	}, &out, &errOut)

	if code != ExitSuccess {
		t.Fatalf("expected ExitSuccess (%d), got %d; stderr: %s", ExitSuccess, code, errOut.String())
	}
	if svc.calls != 1 {
		t.Fatalf("expected 1 record call, got %d", svc.calls)
	}
	wantRequest := app.ConsoleRecordRequest{Target: "test-vm", Width: 16, Height: 8, Frames: 2, IntervalMillis: 100}
	if svc.req != wantRequest {
		t.Fatalf("unexpected record request parameters: %+v", svc.req)
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read saved GIF file: %v", err)
	}
	if !bytes.Equal(saved, gifData) {
		t.Fatalf("saved GIF data does not match synthetic source bytes")
	}

	if err := target.NewPrivatePathSecurity().ValidateFile(context.Background(), path); err != nil {
		t.Fatalf("output file privacy validation failed: %v", err)
	}

	stdoutStr := out.String()
	if strings.Contains(stdoutStr, `"data"`) {
		t.Fatalf("metadata JSON must not include raw data field: %s", stdoutStr)
	}
	var metadata app.ConsoleRecording
	if err := json.Unmarshal(out.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	wantMetadata := svc.recording
	wantMetadata.Data = nil
	if !reflect.DeepEqual(metadata, wantMetadata) {
		t.Fatalf("metadata mismatch: %+v", metadata)
	}
}

func TestConsoleRecordExistingPathPreservesBytes(t *testing.T) {
	dir := privateConsoleTestDir(t)
	path := filepath.Join(dir, "existing.gif")
	originalBytes := []byte("synthetic original private bytes that must remain intact")
	if err := os.WriteFile(path, originalBytes, 0600); err != nil {
		t.Fatal(err)
	}

	svc := &consoleRecordStub{}
	a := NewApp(nil, WithConsoleService(svc))
	var out, errOut bytes.Buffer

	code := a.Run([]string{"--direct", "console", "record", "--output", path}, &out, &errOut)
	if code != ExitConflict {
		t.Fatalf("expected ExitConflict (%d), got %d; stderr: %s", ExitConflict, code, errOut.String())
	}
	if svc.calls != 0 {
		t.Fatalf("expected 0 record calls when output exists, got %d", svc.calls)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read existing file: %v", err)
	}
	if !bytes.Equal(content, originalBytes) {
		t.Fatalf("existing file bytes were modified: got %q, want %q", content, originalBytes)
	}
}

func TestConsoleRecordProviderFailureRemovesOwnPartialFile(t *testing.T) {
	dir := privateConsoleTestDir(t)
	path := filepath.Join(dir, "failed.gif")
	svc := &consoleRecordStub{err: errors.New("synthetic provider record failure")}
	a := NewApp(nil, WithConsoleService(svc))
	var out, errOut bytes.Buffer

	code := a.Run([]string{"--direct", "console", "record", "--output", path}, &out, &errOut)
	if code == ExitSuccess {
		t.Fatal("expected failure exit code, got ExitSuccess")
	}
	if svc.calls != 1 {
		t.Fatalf("expected 1 record call, got %d", svc.calls)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed recording output file was not removed: %v", err)
	}
}

func TestConsoleRecordForeignReplacementNotDeleted(t *testing.T) {
	dir := privateConsoleTestDir(t)
	path := filepath.Join(dir, "replaced.gif")

	foreignContent := []byte("foreign process content that must not be deleted")
	svc := &consoleRecordStub{
		err: errors.New("synthetic provider failure after replacement"),
		onRecord: func() {
			if err := os.Rename(path, path+".owned"); err != nil {
				t.Skipf("platform prevents renaming open file: %v", err)
			}
			if err := os.WriteFile(path, foreignContent, 0600); err != nil {
				t.Fatal(err)
			}
		},
	}
	a := NewApp(nil, WithConsoleService(svc))
	var out bytes.Buffer
	if code := a.Run([]string{"--direct", "console", "record", "--output", path}, &out, &out); code == ExitSuccess || svc.calls != 1 {
		t.Fatalf("code=%d calls=%d", code, svc.calls)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read foreign file after provider failure: %v", err)
	}
	if !bytes.Equal(data, foreignContent) {
		t.Fatalf("foreign replacement file was altered or deleted: got %q, want %q", data, foreignContent)
	}
}

func TestConsoleRecordValidationAndUsageErrors(t *testing.T) {
	dir := privateConsoleTestDir(t)
	path := filepath.Join(dir, "test.gif")
	svc := &consoleRecordStub{}
	a := NewApp(nil, WithConsoleService(svc))
	var out, errOut bytes.Buffer

	// Missing --output
	code := a.Run([]string{"--direct", "console", "record"}, &out, &errOut)
	if code != ExitUsage || svc.calls != 0 {
		t.Fatalf("missing output code=%d, calls=%d", code, svc.calls)
	}

	// Multiple target positional arguments
	code = a.Run([]string{"--direct", "console", "record", "target1", "target2", "--output", path}, &out, &errOut)
	if code != ExitUsage || svc.calls != 0 {
		t.Fatalf("multiple targets code=%d, calls=%d", code, svc.calls)
	}

	// Direct service does not implement Record interface
	plainSvc := &consoleStub{}
	aPlain := NewApp(nil, WithConsoleService(plainSvc))
	code = aPlain.Run([]string{"--direct", "console", "record", "--output", path}, &out, &errOut)
	if code != ExitBackendUnavailable {
		t.Fatalf("unsupported record backend code=%d", code)
	}

	// Daemon route unavailable
	aDefault := NewApp(nil, WithConsoleService(svc), WithStateDir(dir))
	code = aDefault.Run([]string{"console", "record", "--output", path}, &out, &errOut)
	if code != ExitBackendUnavailable || svc.calls != 0 {
		t.Fatalf("default daemon route code=%d, calls=%d", code, svc.calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("output file left after daemon failure: %v", err)
	}
}
