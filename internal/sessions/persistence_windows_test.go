//go:build windows

package sessions

import (
	"context"
	"encoding/json"
	"errors"
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
	if fileRenameSourceAccess != windows.DELETE|windows.SYNCHRONIZE|windows.GENERIC_WRITE {
		t.Fatalf("source access = %#x", fileRenameSourceAccess)
	}
	if fileRenameSourceShare != windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE {
		t.Fatalf("source share = %#x", fileRenameSourceShare)
	}
	wantSourceFlags := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT | windows.FILE_FLAG_WRITE_THROUGH)
	if fileRenameSourceFlags != wantSourceFlags {
		t.Fatalf("source flags = %#x, want %#x", fileRenameSourceFlags, wantSourceFlags)
	}
}

func TestPrepareAtomicReplaceUsesOneFileRenameInfoExCallForAbsentAndExistingTargets(t *testing.T) {
	commitErr := errors.New("injected commit failure")
	for _, name := range []string{"absent target", "existing target"} {
		t.Run(name, func(t *testing.T) {
			createCalls := 0
			renameCalls := 0
			flushCalls := 0
			replace, err := prepareAtomicReplaceWith("source", "target", windowsReplaceOperations{
				createFile: func(_ *uint16, access, share uint32, _ *windows.SecurityAttributes, disposition, flags uint32, _ windows.Handle) (windows.Handle, error) {
					createCalls++
					if access != fileRenameSourceAccess || share != fileRenameSourceShare || disposition != windows.OPEN_EXISTING || flags != fileRenameSourceFlags {
						t.Fatalf("CreateFile args = access %#x share %#x disposition %#x flags %#x", access, share, disposition, flags)
					}
					return windows.Handle(1), nil
				},
				getFileInformation: func(_ windows.Handle, info *windows.ByHandleFileInformation) error {
					info.FileAttributes = windows.FILE_ATTRIBUTE_NORMAL
					return nil
				},
				setFileInformation: func(_ windows.Handle, class uint32, buffer *byte, bufferLen uint32) error {
					renameCalls++
					if class != windows.FileRenameInfoEx {
						t.Fatalf("information class = %d, want FileRenameInfoEx", class)
					}
					info := (*fileRenameInformation)(unsafe.Pointer(buffer))
					if info.Flags != fileRenameInfoExFlags {
						t.Fatalf("rename flags = %#x, want %#x", info.Flags, fileRenameInfoExFlags)
					}
					if info.RootDirectory != 0 {
						t.Fatalf("rename root directory = %d, want 0", info.RootDirectory)
					}
					wantBufferLen := uint32(unsafe.Offsetof(fileRenameInformation{}.FileName)) + info.FileNameLength + 2
					if bufferLen != wantBufferLen {
						t.Fatalf("rename buffer length = %d, want %d with terminator", bufferLen, wantBufferLen)
					}
					nameUnits := unsafe.Slice(&info.FileName[0], int(info.FileNameLength/2)+1)
					if nameUnits[len(nameUnits)-1] != 0 {
						t.Fatal("rename buffer omitted NUL terminator")
					}
					return commitErr
				},
				flushFileBuffers: func(windows.Handle) error {
					flushCalls++
					return nil
				},
				closeHandle: func(windows.Handle) error { return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			result := replace(t.Context())
			if result.Committed || !errors.Is(result.Err, commitErr) {
				t.Fatalf("replacement result = %+v, want pre-commit injected failure", result)
			}
			if createCalls != 1 || renameCalls != 1 || flushCalls != 0 {
				t.Fatalf("calls = create:%d rename:%d flush:%d, want 1/1/0", createCalls, renameCalls, flushCalls)
			}
		})
	}
}

func TestFileRenameInfoExPostCommitFlushFailurePreservesCommit(t *testing.T) {
	flushErr := errors.New("injected FlushFileBuffers failure")
	closeErr := errors.New("injected close anomaly")
	operations := windowsReplaceOperations{
		createFile: func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
			return windows.Handle(1), nil
		},
		getFileInformation: func(_ windows.Handle, info *windows.ByHandleFileInformation) error {
			info.FileAttributes = windows.FILE_ATTRIBUTE_NORMAL
			return nil
		},
		setFileInformation: func(windows.Handle, uint32, *byte, uint32) error { return nil },
		flushFileBuffers:   func(windows.Handle) error { return flushErr },
		closeHandle:        func(windows.Handle) error { return closeErr },
	}
	result := fileRenameInfoExReplaceWith(t.Context(), "source", "target", operations)
	if !result.Committed || result.Durable || !errors.Is(result.Err, flushErr) || errors.Is(result.Err, closeErr) {
		t.Fatalf("flush failure result = %+v, want committed durability error only", result)
	}
}

func TestFileRenameInfoExRetriesTransientCommitFailures(t *testing.T) {
	renameCalls := 0
	operations := windowsReplaceOperations{
		createFile: func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
			return windows.Handle(1), nil
		},
		getFileInformation: func(_ windows.Handle, info *windows.ByHandleFileInformation) error {
			info.FileAttributes = windows.FILE_ATTRIBUTE_NORMAL
			return nil
		},
		setFileInformation: func(windows.Handle, uint32, *byte, uint32) error {
			renameCalls++
			if renameCalls < 3 {
				return windows.ERROR_ACCESS_DENIED
			}
			return nil
		},
		flushFileBuffers: func(windows.Handle) error { return nil },
		closeHandle:      func(windows.Handle) error { return nil },
	}
	result := fileRenameInfoExReplaceWith(t.Context(), "source", "target", operations)
	if result.Err != nil || !result.Committed || renameCalls != 3 {
		t.Fatalf("transient rename result = %+v calls = %d, want committed after three attempts", result, renameCalls)
	}
}

func TestFileRenameInfoExRetryStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	renameCalls := 0
	operations := windowsReplaceOperations{
		createFile: func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
			return windows.Handle(1), nil
		},
		getFileInformation: func(_ windows.Handle, info *windows.ByHandleFileInformation) error {
			info.FileAttributes = windows.FILE_ATTRIBUTE_NORMAL
			return nil
		},
		setFileInformation: func(windows.Handle, uint32, *byte, uint32) error {
			renameCalls++
			cancel()
			return windows.ERROR_SHARING_VIOLATION
		},
		flushFileBuffers: func(windows.Handle) error { return nil },
		closeHandle:      func(windows.Handle) error { return nil },
	}
	result := fileRenameInfoExReplaceWith(ctx, "source", "target", operations)
	if result.Committed || !errors.Is(result.Err, context.Canceled) || renameCalls != 1 {
		t.Fatalf("cancelled rename result = %+v calls = %d, want one pre-commit attempt", result, renameCalls)
	}
}

func TestFileRenameInfoExRejectsDirectoryAndReparseSourcesBeforeCommit(t *testing.T) {
	for _, attributes := range []uint32{windows.FILE_ATTRIBUTE_DIRECTORY, windows.FILE_ATTRIBUTE_REPARSE_POINT} {
		t.Run(fmt.Sprintf("attributes-%#x", attributes), func(t *testing.T) {
			renameCalls := 0
			operations := windowsReplaceOperations{
				createFile: func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
					return windows.Handle(1), nil
				},
				getFileInformation: func(_ windows.Handle, info *windows.ByHandleFileInformation) error {
					info.FileAttributes = attributes
					return nil
				},
				setFileInformation: func(windows.Handle, uint32, *byte, uint32) error {
					renameCalls++
					return nil
				},
				flushFileBuffers: func(windows.Handle) error { return nil },
				closeHandle:      func(windows.Handle) error { return nil },
			}
			result := fileRenameInfoExReplaceWith(t.Context(), "source", "target", operations)
			if result.Committed || result.Err == nil || renameCalls != 0 {
				t.Fatalf("rejected source result = %+v rename calls %d", result, renameCalls)
			}
		})
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
		if result := replaceSessionFileContext(t.Context(), path, data); result.Err != nil || !result.Committed || !result.Durable {
			t.Fatalf("replacement result = %+v", result)
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
