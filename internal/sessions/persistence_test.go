package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestPersistSessionConcurrentReadersSeeAtomicMonotonicJSON(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, nil, time.Now)
	createdAt := time.Now().UTC()
	s := &Session{obs: domain.SessionObservation{
		ID: "sess-00000000000000000000000000000001", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		OwnerActor: "agent:persistence-test", State: domain.SessionStateActive,
		CreatedAt: createdAt, LastActivityAt: createdAt, Cols: 80, Rows: 24,
		TermType: "xterm-256color", ObservationType: domain.ObservationObserved,
	}}
	if result := mgr.persistSession(s); result.Err != nil {
		t.Fatal(result.Err)
	}

	path := filepath.Join(dir, string(s.obs.ID)+".json")
	readerDone := make(chan struct{})
	readerErr := make(chan error, 17)
	go func() {
		defer close(readerDone)
		if err := readValidSessionJSON(path, 500); err != nil {
			readerErr <- err
		}
	}()

	var writers sync.WaitGroup
	for range 16 {
		writers.Go(func() {
			for range 25 {
				s.mu.Lock()
				s.obs.BytesWritten++
				s.mu.Unlock()
				if result := mgr.persistSession(s); result.Err != nil {
					readerErr <- result.Err
					return
				}
			}
		})
	}
	writers.Wait()
	<-readerDone
	select {
	case err := <-readerErr:
		t.Fatal(err)
	default:
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("session file mode = %04o, want 0600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".session-") {
			t.Fatalf("temporary persistence residue remains: %s", entry.Name())
		}
	}
}

func readValidSessionJSON(path string, count int) error {
	for range count {
		file, err := openSessionStateFile(filepath.Dir(path), filepath.Base(path))
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		var obs domain.SessionObservation
		if err := json.Unmarshal(data, &obs); err != nil {
			return err
		}
	}
	return nil
}

func TestReplaceSessionFileCleansTempAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "target")
	if err := os.Mkdir(targetDir, 0700); err != nil {
		t.Fatal(err)
	}
	if result := replaceSessionFile(targetDir, []byte("{}")); result.Err == nil {
		t.Fatal("rename over directory unexpectedly succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".session-") {
			t.Fatalf("failed replacement left temporary file %s", entry.Name())
		}
	}
}

func TestReplaceSessionFileCancellationBeforeCommitHasNoEffect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	ctx, cancel := context.WithCancel(t.Context())
	commits := 0
	prepare := func(_, _ string) (atomicReplacement, error) {
		cancel()
		return func(context.Context) publicationResult {
			commits++
			return publicationResult{Committed: true}
		}, nil
	}

	result := replaceSessionFileContextWithPreparer(ctx, path, []byte("{}"), prepare, syncSessionDirectory)
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("replace error = %v, want context cancellation", result.Err)
	}
	if commits != 0 {
		t.Fatalf("replacement commits = %d, want 0", commits)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical target exists after pre-commit cancellation: %v", err)
	}
}

func TestReplaceSessionFileCancellationAfterCommitPreservesSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	ctx, cancel := context.WithCancel(t.Context())
	prepare := func(oldPath, newPath string) (atomicReplacement, error) {
		return func(context.Context) publicationResult {
			if err := os.Rename(oldPath, newPath); err != nil {
				return publicationResult{Err: err}
			}
			cancel()
			return publicationResult{Committed: true}
		}, nil
	}

	result := replaceSessionFileContextWithPreparer(ctx, path, []byte("{}"), prepare, syncSessionDirectory)
	if result.Err != nil || !result.Committed || !result.Durable {
		t.Fatalf("committed replacement result = %+v, want durable success", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}" {
		t.Fatalf("canonical data = %q, want %q", data, "{}")
	}
}

func TestReplaceSessionFilePostCommitSyncFailurePreservesPublicationTruth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	syncErr := errors.New("synthetic directory sync failure")
	prepare := func(oldPath, newPath string) (atomicReplacement, error) {
		return func(context.Context) publicationResult {
			if err := os.Rename(oldPath, newPath); err != nil {
				return publicationResult{Err: err}
			}
			return publicationResult{Committed: true}
		}, nil
	}

	result := replaceSessionFileContextWithPreparer(t.Context(), path, []byte("{}"), prepare, func(string) error {
		return syncErr
	})
	if !result.Committed || result.Durable || !errors.Is(result.Err, syncErr) {
		t.Fatalf("post-commit sync result = %+v, want committed non-durable error", result)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{}" {
		t.Fatalf("canonical publication = %q err %v, want visible committed data", data, err)
	}
}

func TestReconcileSessionFilePostCommitSyncFailureReturnsReconciledIdentity(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	id := domain.SessionID("sess-d2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5")
	createdAt := now.Add(-time.Hour)
	obs := domain.SessionObservation{
		ID: id, Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		OwnerActor: "agent:reconcile-publication-test", State: domain.SessionStateActive,
		CreatedAt: createdAt, LastActivityAt: createdAt, Cols: 80, Rows: 24,
		TermType: "xterm-256color", ObservationType: domain.ObservationObserved,
	}
	data, err := json.MarshalIndent(obs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, string(id)+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("synthetic reconcile directory sync failure")

	reconciledID, err := reconcileSessionFileWithSync(t.Context(), dir, id, now, func(string) error {
		return syncErr
	})
	if reconciledID == nil || *reconciledID != id || !errors.Is(err, syncErr) {
		t.Fatalf("reconcile result = id %v error %v, want committed identity and durability error", reconciledID, err)
	}
	reconciled, err := readSessionState(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.State != domain.SessionStateCrashed || reconciled.ClosedAt == nil || !reconciled.ClosedAt.Equal(now) {
		t.Fatalf("visible reconciled state = %+v", reconciled)
	}
}
