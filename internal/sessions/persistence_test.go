package sessions

import (
	"encoding/json"
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
	if err := mgr.persistSession(s); err != nil {
		t.Fatal(err)
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
				if err := mgr.persistSession(s); err != nil {
					readerErr <- err
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
	if err := replaceSessionFile(targetDir, []byte("{}")); err == nil {
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
