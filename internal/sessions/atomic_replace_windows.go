//go:build windows

package sessions

import (
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

func atomicReplace(oldPath, newPath string) (atomicReplaceMethod, error) {
	oldPath16, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(
		oldPath16,
		windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return "", err
	}

	newPath16, err := windows.UTF16FromString(newPath)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return "", err
	}
	fileNameLength := len(newPath16)*2 - 2
	var layout fileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.FileName)) + fileNameLength
	buffer := make([]byte, bufferSize)
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.Flags = fileRenameInfoExFlags
	info.FileNameLength = uint32(fileNameLength)
	copy(unsafe.Slice(&info.FileName[0], len(newPath16)-1), newPath16[:len(newPath16)-1])

	renameErr := windows.SetFileInformationByHandle(handle, windows.FileRenameInfoEx, &buffer[0], uint32(bufferSize))
	closeErr := windows.CloseHandle(handle)
	if renameErr == nil && closeErr == nil {
		return atomicReplaceMethodFileRenameInfoEx, nil
	}
	if renameErr == nil {
		return "", fmt.Errorf("sessions: close FileRenameInfoEx source handle: %w", closeErr)
	}
	fallbackErr := windows.Rename(oldPath, newPath)
	if fallbackErr == nil {
		return atomicReplaceMethodMoveFileEx, nil
	}
	return "", errors.Join(
		fmt.Errorf("sessions: FileRenameInfoEx: %w", renameErr),
		fmt.Errorf("sessions: close FileRenameInfoEx source handle: %w", closeErr),
		fmt.Errorf("sessions: MoveFileEx fallback: %w", fallbackErr),
	)
}
