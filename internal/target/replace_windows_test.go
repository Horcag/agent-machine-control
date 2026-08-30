//go:build windows

package target

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTargetFileRenameInfoExLayoutAndFlags(t *testing.T) {
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
	if fileRenameInfoExFlags != windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS {
		t.Fatalf("rename flags = %#x", fileRenameInfoExFlags)
	}
	if fileRenameSourceAccess != windows.DELETE|windows.SYNCHRONIZE|windows.GENERIC_WRITE {
		t.Fatalf("source access = %#x", fileRenameSourceAccess)
	}
	if fileRenameSourceShare != windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE {
		t.Fatalf("source share = %#x", fileRenameSourceShare)
	}
	wantFlags := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT | windows.FILE_FLAG_WRITE_THROUGH)
	if fileRenameSourceFlags != wantFlags {
		t.Fatalf("source flags = %#x, want %#x", fileRenameSourceFlags, wantFlags)
	}
}

func TestTargetFileRenameInfoExPerformsOneHandleBoundCommit(t *testing.T) {
	setCalls := 0
	flushCalls := 0
	operations := validTargetWindowsReplaceOperations()
	operations.createFile = func(_ *uint16, access, share uint32, _ *windows.SecurityAttributes, disposition, flags uint32, _ windows.Handle) (windows.Handle, error) {
		if access != fileRenameSourceAccess || share != fileRenameSourceShare || disposition != windows.OPEN_EXISTING || flags != fileRenameSourceFlags {
			t.Fatalf("CreateFile args = access %#x share %#x disposition %#x flags %#x", access, share, disposition, flags)
		}
		return windows.Handle(1), nil
	}
	operations.setFileInformation = func(_ windows.Handle, class uint32, buffer *byte, bufferLen uint32) error {
		setCalls++
		if class != windows.FileRenameInfoEx {
			t.Fatalf("information class = %d", class)
		}
		info := (*fileRenameInformation)(unsafe.Pointer(buffer))
		wantLen := uint32(unsafe.Offsetof(fileRenameInformation{}.FileName)) + info.FileNameLength + 2
		if info.Flags != fileRenameInfoExFlags || info.RootDirectory != 0 || bufferLen != wantLen {
			t.Fatalf("rename info = flags %#x root %d length %d, buffer %d", info.Flags, info.RootDirectory, info.FileNameLength, bufferLen)
		}
		return nil
	}
	operations.flushFileBuffers = func(windows.Handle) error {
		flushCalls++
		return nil
	}
	result := fileRenameInfoExReplaceWith(context.Background(), `C:\\source.tmp`, `C:\\target.json`, operations)
	if result.Err != nil || !result.Committed || setCalls != 1 || flushCalls != 1 {
		t.Fatalf("result = %+v, set %d flush %d", result, setCalls, flushCalls)
	}
}

func TestTargetFileRenameInfoExRejectsPreCommitFaults(t *testing.T) {
	injected := errors.New("injected pre-commit fault")
	tests := map[string]func(context.CancelFunc, *windowsReplaceOperations) (string, string){
		"canceled before open": func(cancel context.CancelFunc, _ *windowsReplaceOperations) (string, string) {
			cancel()
			return "source", "target"
		},
		"source path": func(_ context.CancelFunc, _ *windowsReplaceOperations) (string, string) {
			return "source\x00bad", "target"
		},
		"open": func(_ context.CancelFunc, operations *windowsReplaceOperations) (string, string) {
			operations.createFile = func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
				return 0, injected
			}
			return "source", "target"
		},
		"inspect": func(_ context.CancelFunc, operations *windowsReplaceOperations) (string, string) {
			operations.getFileInformation = func(windows.Handle, *windows.ByHandleFileInformation) error { return injected }
			return "source", "target"
		},
		"target path": func(_ context.CancelFunc, _ *windowsReplaceOperations) (string, string) {
			return "source", "target\x00bad"
		},
		"canceled before commit": func(cancel context.CancelFunc, operations *windowsReplaceOperations) (string, string) {
			operations.getFileInformation = func(_ windows.Handle, info *windows.ByHandleFileInformation) error {
				info.FileAttributes = windows.FILE_ATTRIBUTE_NORMAL
				cancel()
				return nil
			}
			return "source", "target"
		},
		"set information": func(_ context.CancelFunc, operations *windowsReplaceOperations) (string, string) {
			operations.setFileInformation = func(windows.Handle, uint32, *byte, uint32) error { return injected }
			return "source", "target"
		},
	}
	for name, configure := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			operations := validTargetWindowsReplaceOperations()
			setCalls := 0
			originalSet := operations.setFileInformation
			oldPath, newPath := configure(cancel, &operations)
			configuredSet := operations.setFileInformation
			operations.setFileInformation = func(handle windows.Handle, class uint32, buffer *byte, size uint32) error {
				setCalls++
				if configuredSet != nil {
					return configuredSet(handle, class, buffer, size)
				}
				return originalSet(handle, class, buffer, size)
			}
			result := fileRenameInfoExReplaceWith(ctx, oldPath, newPath, operations)
			if result.Committed || result.Err == nil {
				t.Fatalf("result = %+v, want pre-commit failure", result)
			}
			if name != "set information" && setCalls != 0 {
				t.Fatalf("set calls = %d, want zero", setCalls)
			}
		})
	}
}

func TestTargetFileRenameInfoExCloseFailureBeforeCommitIsNotCommitted(t *testing.T) {
	inspectErr := errors.New("injected inspect failure")
	closeErr := errors.New("injected close failure")
	operations := validTargetWindowsReplaceOperations()
	operations.getFileInformation = func(windows.Handle, *windows.ByHandleFileInformation) error { return inspectErr }
	operations.closeHandle = func(windows.Handle) error { return closeErr }
	result := fileRenameInfoExReplaceWith(context.Background(), "source", "target", operations)
	if result.Committed || !errors.Is(result.Err, inspectErr) || !errors.Is(result.Err, closeErr) {
		t.Fatalf("result = %+v, want joined pre-commit inspect and close errors", result)
	}
}

func TestTargetFileRenameInfoExRejectsDirectoryAndReparseSources(t *testing.T) {
	for _, attributes := range []uint32{windows.FILE_ATTRIBUTE_DIRECTORY, windows.FILE_ATTRIBUTE_REPARSE_POINT} {
		t.Run(fmt.Sprintf("attributes-%#x", attributes), func(t *testing.T) {
			operations := validTargetWindowsReplaceOperations()
			operations.getFileInformation = func(_ windows.Handle, info *windows.ByHandleFileInformation) error {
				info.FileAttributes = attributes
				return nil
			}
			setCalls := 0
			operations.setFileInformation = func(windows.Handle, uint32, *byte, uint32) error {
				setCalls++
				return nil
			}
			result := fileRenameInfoExReplaceWith(context.Background(), "source", "target", operations)
			if result.Committed || result.Err == nil || setCalls != 0 {
				t.Fatalf("result = %+v, set calls %d", result, setCalls)
			}
		})
	}
}

func TestTargetFileRenameInfoExPostCommitFlushFailurePreservesEffectTruth(t *testing.T) {
	flushErr := errors.New("injected post-commit flush failure")
	operations := validTargetWindowsReplaceOperations()
	operations.flushFileBuffers = func(windows.Handle) error { return flushErr }
	result := fileRenameInfoExReplaceWith(context.Background(), "source", "target", operations)
	if !result.Committed || !errors.Is(result.Err, flushErr) {
		t.Fatalf("result = %+v, want committed flush anomaly", result)
	}
}

func validTargetWindowsReplaceOperations() windowsReplaceOperations {
	return windowsReplaceOperations{
		createFile: func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
			return windows.Handle(1), nil
		},
		getFileInformation: func(_ windows.Handle, info *windows.ByHandleFileInformation) error {
			info.FileAttributes = windows.FILE_ATTRIBUTE_NORMAL
			return nil
		},
		setFileInformation: func(windows.Handle, uint32, *byte, uint32) error { return nil },
		flushFileBuffers:   func(windows.Handle) error { return nil },
		closeHandle:        func(windows.Handle) error { return nil },
	}
}
