//go:build windows

package sessions

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInformation struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

const fileRenameInfoExFlags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
const fileRenameSourceAccess = windows.DELETE | windows.SYNCHRONIZE | windows.GENERIC_WRITE
const fileRenameSourceShare = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
const fileRenameSourceFlags = windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT | windows.FILE_FLAG_WRITE_THROUGH

type windowsReplaceOperations struct {
	createFile         func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error)
	getFileInformation func(windows.Handle, *windows.ByHandleFileInformation) error
	setFileInformation func(windows.Handle, uint32, *byte, uint32) error
	flushFileBuffers   func(windows.Handle) error
	closeHandle        func(windows.Handle) error
}

func prepareAtomicReplace(oldPath, newPath string) (atomicReplacement, error) {
	return prepareAtomicReplaceWith(oldPath, newPath, windowsReplaceOperations{
		createFile:         windows.CreateFile,
		getFileInformation: windows.GetFileInformationByHandle,
		setFileInformation: windows.SetFileInformationByHandle,
		flushFileBuffers:   windows.FlushFileBuffers,
		closeHandle:        windows.CloseHandle,
	})
}

func syncSessionDirectory(string) error {
	// The source handle is opened write-through and flushed after the rename.
	// Windows does not expose a supported directory-handle flush equivalent.
	return nil
}

func prepareAtomicReplaceWith(oldPath, newPath string, operations windowsReplaceOperations) (atomicReplacement, error) {
	if operations.createFile == nil || operations.getFileInformation == nil || operations.setFileInformation == nil ||
		operations.flushFileBuffers == nil || operations.closeHandle == nil {
		return nil, errors.New("sessions: incomplete Windows replacement operations")
	}
	return func(ctx context.Context) publicationResult {
		return fileRenameInfoExReplaceWith(ctx, oldPath, newPath, operations)
	}, nil
}

func fileRenameInfoExReplaceWith(ctx context.Context, oldPath, newPath string, operations windowsReplaceOperations) publicationResult {
	oldPath16, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return publicationResult{Err: fmt.Errorf("sessions: FileRenameInfoEx source path: %w", err)}
	}
	if err := ctx.Err(); err != nil {
		return publicationResult{Err: err}
	}
	handle, err := operations.createFile(
		oldPath16,
		fileRenameSourceAccess,
		fileRenameSourceShare,
		nil,
		windows.OPEN_EXISTING,
		fileRenameSourceFlags,
		0,
	)
	if err != nil {
		return publicationResult{Err: fmt.Errorf("sessions: open FileRenameInfoEx source: %w", err)}
	}

	closeBeforeCommit := func(commitErr error) publicationResult {
		if closeErr := operations.closeHandle(handle); closeErr != nil {
			commitErr = errors.Join(commitErr, fmt.Errorf("sessions: close FileRenameInfoEx source before commit: %w", closeErr))
		}
		return publicationResult{Err: commitErr}
	}

	var info windows.ByHandleFileInformation
	if err := operations.getFileInformation(handle, &info); err != nil {
		return closeBeforeCommit(fmt.Errorf("sessions: inspect FileRenameInfoEx source: %w", err))
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return closeBeforeCommit(errors.New("sessions: FileRenameInfoEx source must be a regular non-reparse file"))
	}

	newPath16, err := windows.UTF16FromString(newPath)
	if err != nil {
		return closeBeforeCommit(fmt.Errorf("sessions: FileRenameInfoEx target path: %w", err))
	}
	fileNameLength := len(newPath16)*2 - 2
	var layout fileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.FileName)) + fileNameLength + 2
	buffer := make([]byte, bufferSize)
	renameInfo := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	renameInfo.Flags = fileRenameInfoExFlags
	renameInfo.FileNameLength = uint32(fileNameLength)
	copy(unsafe.Slice(&renameInfo.FileName[0], len(newPath16)), newPath16)

	if err := ctx.Err(); err != nil {
		return closeBeforeCommit(err)
	}
	if err := operations.setFileInformation(handle, windows.FileRenameInfoEx, &buffer[0], uint32(bufferSize)); err != nil {
		return closeBeforeCommit(fmt.Errorf("sessions: FileRenameInfoEx commit: %w", err))
	}

	// Successful SetFileInformationByHandle is the namespace commit point.
	// Caller cancellation and handle-close anomalies cannot erase that effect.
	if err := operations.flushFileBuffers(handle); err != nil {
		_ = operations.closeHandle(handle)
		return publicationResult{
			Committed: true,
			Err:       fmt.Errorf("sessions: FileRenameInfoEx committed but FlushFileBuffers failed: %w", err),
		}
	}
	_ = operations.closeHandle(handle)
	return publicationResult{Committed: true}
}
