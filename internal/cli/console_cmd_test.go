package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type consoleStub struct {
	calls int
	frame domain.ConsoleFrame
	err   error
	input app.ConsoleInputRequest
	actor domain.ActorContext
}

func (s *consoleStub) Screenshot(_ context.Context, actor domain.ActorContext, _ app.ConsoleScreenshotRequest) (domain.ConsoleFrame, error) {
	s.calls++
	s.actor = actor
	return s.frame, s.err
}
func (s *consoleStub) Input(_ context.Context, actor domain.ActorContext, in app.ConsoleInputRequest) (domain.Receipt, error) {
	s.calls++
	s.actor = actor
	s.input = in
	return domain.Receipt{}, s.err
}

func TestConsoleScreenshotProtectedOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frame.png")
	data := []byte("synthetic PNG bytes")
	svc := &consoleStub{frame: domain.ConsoleFrame{Data: data, FrameID: "frame-1", Width: 8, Height: 4}}
	a := NewApp(nil, WithConsoleService(svc))
	var out, errOut bytes.Buffer
	code := a.Run([]string{"--direct", "console", "screenshot", "--output", path, "--json"}, &out, &errOut)
	if code != ExitSuccess {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("saved bytes %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("file mode %v, %v", info, err)
	}
	if strings.Contains(out.String(), "data") || !strings.Contains(out.String(), "frame-1") {
		t.Fatalf("metadata %s", out.String())
	}
	code = a.Run([]string{"--direct", "console", "screenshot", "--output", path}, &out, &errOut)
	if code != ExitConflict || svc.calls != 1 {
		t.Fatalf("overwrite code=%d calls=%d", code, svc.calls)
	}
}

func TestConsoleScreenshotFailureCleanupAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frame.png")
	svc := &consoleStub{err: errors.New("capture failed")}
	a := NewApp(nil, WithConsoleService(svc))
	var out bytes.Buffer
	if code := a.Run([]string{"--direct", "console", "screenshot", "--output", path}, &out, &out); code == ExitSuccess {
		t.Fatal("failure reported success")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed output remains: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), path); err != nil {
		t.Fatal(err)
	}
	if code := a.Run([]string{"--direct", "console", "screenshot", "--output", path}, &out, &out); code != ExitConflict || svc.calls != 1 {
		t.Fatalf("symlink code=%d calls=%d", code, svc.calls)
	}
}

func TestConsoleScreenshotRequiresOutputAndDefaultDaemon(t *testing.T) {
	svc := &consoleStub{}
	a := NewApp(nil, WithConsoleService(svc), WithStateDir(t.TempDir()))
	var out bytes.Buffer
	if code := a.Run([]string{"--direct", "console", "screenshot"}, &out, &out); code != ExitUsage || svc.calls != 0 {
		t.Fatalf("missing output code=%d calls=%d", code, svc.calls)
	}
	path := filepath.Join(t.TempDir(), "frame.png")
	if code := a.Run([]string{"console", "screenshot", "--output", path}, &out, &out); code != ExitBackendUnavailable || svc.calls != 0 {
		t.Fatalf("default route code=%d calls=%d", code, svc.calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("output remains: %v", err)
	}
}

func TestConsoleTypeFileAndInputMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "text")
	if err := os.WriteFile(path, []byte("private text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	svc := &consoleStub{}
	a := NewApp(nil, WithConsoleService(svc))
	var out bytes.Buffer
	code := a.Run([]string{"--direct", "console", "type", "default", "--text-file", path, "--reason", "exercise console", "--idempotency-key", "console-test"}, &out, &out)
	if code != ExitSuccess || svc.input.Input.Text != "private text\n" || svc.input.Target != "default" || svc.input.Deadline == "" || svc.input.Reason != "exercise console" {
		t.Fatalf("code=%d request=%+v", code, svc.input)
	}
	if strings.Contains(out.String(), "private text") {
		t.Fatal("typed text leaked")
	}
}
