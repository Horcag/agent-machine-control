//go:build windows

package sessions_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"golang.org/x/sys/windows"
)

func TestManagerOpenPublishesCanonicalStateForImmediateRestart(t *testing.T) {
	const id = domain.SessionID("sess-00000000000000000000000000000008")
	mgr, server, actor, _, sessionsDir := setupTestManager(t, sessions.WithSessionIDGenerator(func() (domain.SessionID, error) {
		return id, nil
	}))
	defer server.Close()

	op := domain.Operation{
		Kind: "session.open", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor,
		Reason: "verify Windows durable publication", Deadline: time.Now().Add(time.Minute),
		IdempotencyKey: "windows-durable-publication", Classification: domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
	obs, err := mgr.Open(context.Background(), op, 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("Open failed: error={%s} diagnostic={%s}", publicationErrorDiagnostic(err), windowsSessionPublicationDiagnostic(sessionsDir, string(id)+".json"))
	}
	time.Sleep(50 * time.Millisecond)
	chunks, _, _, _, readObs, err := mgr.Read(context.Background(), obs.ID, actor, 0, 1024)
	if err != nil || len(chunks) == 0 || readObs == nil || readObs.ID != obs.ID {
		t.Fatalf(
			"prompt Read failed: chunks=%d id_match=%t err=%v diagnostic={%s}",
			len(chunks), readObs != nil && readObs.ID == obs.ID, err,
			windowsSessionPublicationDiagnostic(sessionsDir, string(id)+".json"),
		)
	}
	restarted := sessions.NewManager(
		sessionsDir,
		nil,
		time.Now,
		sessions.WithSanitizerConfig(ssh.SanitizerConfig{ExactSecrets: [][]byte{[]byte("synthetic")}}),
	)

	target, targetErr := restarted.MutationTarget(obs.ID, actor)
	loaded, getErr := restarted.Get(t.Context(), obs.ID, actor)
	if targetErr != nil || target != obs.Target || getErr != nil || loaded == nil || loaded.ID != obs.ID {
		t.Fatalf(
			"durable publication failed: target_match=%t target_err=%v get_id_match=%t get_err=%v diagnostic={%s}",
			target == obs.Target,
			targetErr,
			loaded != nil && loaded.ID == obs.ID,
			getErr,
			windowsSessionPublicationDiagnostic(sessionsDir, string(obs.ID)+".json"),
		)
	}
}

func windowsSessionPublicationDiagnostic(sessionsDir, basename string) string {
	path := filepath.Join(sessionsDir, basename)
	statResult := "present"
	if _, err := os.Stat(path); err != nil {
		statResult = windowsErrorDiagnostic(err)
	}

	canonicalOpen := "ok"
	path16, pathErr := windows.UTF16PtrFromString(path)
	if pathErr != nil {
		canonicalOpen = pathErr.Error()
	} else {
		handle, err := windows.CreateFile(
			path16,
			windows.GENERIC_READ,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
			0,
		)
		if err != nil {
			var errno syscall.Errno
			if errors.As(err, &errno) {
				canonicalOpen = fmt.Sprintf("error=%v win32=%d", err, uintptr(errno))
			} else {
				canonicalOpen = fmt.Sprintf("error=%v win32=unknown", err)
			}
		} else {
			_ = windows.CloseHandle(handle)
		}
	}

	entries, readDirErr := os.ReadDir(sessionsDir)
	entryNames := make([]string, 0, len(entries))
	tempNames := make([]string, 0)
	for _, entry := range entries {
		entryNames = append(entryNames, entry.Name())
		if matched, _ := filepath.Match(".session-*.tmp", entry.Name()); matched {
			tempNames = append(tempNames, entry.Name())
		}
	}
	readDirResult := "ok"
	if readDirErr != nil {
		readDirResult = windowsErrorDiagnostic(readDirErr)
	}
	return fmt.Sprintf(
		"expected=%q stat=%q canonical_open=%q entries=%q temp_residue=%q readdir=%q",
		basename,
		statResult,
		canonicalOpen,
		entryNames,
		tempNames,
		readDirResult,
	)
}

func publicationErrorDiagnostic(err error) string {
	return fmt.Sprintf("type=%T error=%q win32={%s}", err, err, windowsErrorDiagnostic(err))
}

func windowsErrorDiagnostic(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("error=%v win32=%d", errno, uintptr(errno))
	}
	return fmt.Sprintf("error_type=%T", err)
}
