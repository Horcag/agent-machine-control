//go:build windows

package target

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

func atomicReplace(ctx context.Context, oldPath, newPath string) CommitResult {
	return fileRenameInfoExReplaceWith(ctx, oldPath, newPath, windowsReplaceOperations{
		createFile:         windows.CreateFile,
		getFileInformation: windows.GetFileInformationByHandle,
		setFileInformation: windows.SetFileInformationByHandle,
		flushFileBuffers:   windows.FlushFileBuffers,
		closeHandle:        windows.CloseHandle,
	})
}

func fileRenameInfoExReplaceWith(ctx context.Context, oldPath, newPath string, operations windowsReplaceOperations) CommitResult {
	oldPath16, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return CommitResult{Err: fmt.Errorf("target: FileRenameInfoEx source path: %w", err)}
	}
	if err := ctx.Err(); err != nil {
		return CommitResult{Err: err}
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
		return CommitResult{Err: fmt.Errorf("target: open FileRenameInfoEx source: %w", err)}
	}

	closeBeforeCommit := func(commitErr error) CommitResult {
		if closeErr := operations.closeHandle(handle); closeErr != nil {
			commitErr = errors.Join(commitErr, fmt.Errorf("target: close FileRenameInfoEx source before commit: %w", closeErr))
		}
		return CommitResult{Err: commitErr}
	}

	var sourceInfo windows.ByHandleFileInformation
	if err := operations.getFileInformation(handle, &sourceInfo); err != nil {
		return closeBeforeCommit(fmt.Errorf("target: inspect FileRenameInfoEx source: %w", err))
	}
	if sourceInfo.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return closeBeforeCommit(errors.New("target: FileRenameInfoEx source must be a regular non-reparse file"))
	}

	newPath16, err := windows.UTF16FromString(newPath)
	if err != nil {
		return closeBeforeCommit(fmt.Errorf("target: FileRenameInfoEx target path: %w", err))
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
		return closeBeforeCommit(fmt.Errorf("target: FileRenameInfoEx commit: %w", err))
	}

	// Successful SetFileInformationByHandle is the namespace commit point.
	// Caller cancellation and close anomalies cannot erase that effect truth.
	if err := operations.flushFileBuffers(handle); err != nil {
		_ = operations.closeHandle(handle)
		return CommitResult{
			Committed: true,
			Err:       fmt.Errorf("target: FileRenameInfoEx committed but FlushFileBuffers failed: %w", err),
		}
	}
	if err := operations.closeHandle(handle); err != nil {
		return CommitResult{
			Committed: true,
			Err:       fmt.Errorf("target: FileRenameInfoEx committed but source handle close failed: %w", err),
		}
	}
	return CommitResult{Committed: true}
}
