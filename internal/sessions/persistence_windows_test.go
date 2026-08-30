//go:build windows

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"golang.org/x/sys/windows"
)

func TestFileRenameInformationExLayoutAndFlags(t *testing.T) {
	var info fileRenameInformation
	pointerSize := unsafe.Sizeof(windows.Handle(0))
	if got, want := unsafe.Offsetof(info.RootDirectory), pointerSize; got != want {
		t.Fatalf("RootDirectory offset = %d, want %d", got, want)
	}
	if got, want := unsafe.Offsetof(info.FileNameLength), 2*pointerSize; got != want {
		t.Fatalf("FileNameLength offset = %d, want %d", got, want)
	}
	if got, want := unsafe.Offsetof(info.FileName), 2*pointerSize+unsafe.Sizeof(uint32(0)); got != want {
		t.Fatalf("FileName offset = %d, want %d", got, want)
	}
	wantFlags := uint32(windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS)
	if fileRenameInfoExFlags != wantFlags {
		t.Fatalf("FileRenameInfoEx flags = %#x, want %#x", fileRenameInfoExFlags, wantFlags)
	}
}

func TestReplaceSessionFilePublishesCanonicalStateToConcurrentReaders(t *testing.T) {
	dir := t.TempDir()
	id := domain.SessionID("sess-00000000000000000000000000000001")
	path := sessionStatePathForTest(t, dir, id)
	createdAt := time.Now().UTC()
	observation := domain.SessionObservation{
		ID: id, Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		OwnerActor: "agent:windows-publication-test", State: domain.SessionStateActive,
		CreatedAt: createdAt, LastActivityAt: createdAt, Cols: 80, Rows: 24,
		TermType: "xterm-256color", ObservationType: domain.ObservationObserved,
	}

	writeObservation := func() {
		data, err := json.MarshalIndent(observation, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := replaceSessionFileContext(t.Context(), path, data); err != nil {
			t.Fatal(err)
		}
		published, err := readSessionState(dir, id)
		if err != nil {
			t.Fatalf("successful replacement was not immediately readable: %v", err)
		}
		if published.ID != id || published.BytesWritten != observation.BytesWritten {
			t.Fatalf("published state = id %q bytes %d, want id %q bytes %d", published.ID, published.BytesWritten, id, observation.BytesWritten)
		}
	}
	writeObservation()

	readerCtx, cancelReaders := context.WithCancel(t.Context())
	readerErr := make(chan error, 4)
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for readerCtx.Err() == nil {
				published, err := readSessionState(dir, id)
				if err != nil {
					readerErr <- err
					return
				}
				if published.ID != id {
					readerErr <- fmt.Errorf("published identity = %q, want %q", published.ID, id)
					return
				}
			}
		})
	}
	for iteration := range uint64(32) {
		observation.BytesWritten = iteration + 1
		observation.LastActivityAt = observation.LastActivityAt.Add(time.Millisecond)
		writeObservation()
	}
	cancelReaders()
	readers.Wait()
	close(readerErr)
	for err := range readerErr {
		t.Fatal(err)
	}
}

func sessionStatePathForTest(t *testing.T, dir string, id domain.SessionID) string {
	t.Helper()
	path, err := sessionStatePath(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
