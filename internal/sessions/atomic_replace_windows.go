//go:build windows

package sessions

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func atomicReplace(oldPath, newPath string) error {
	oldPath16, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
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
		return err
	}
	defer windows.CloseHandle(handle)

	newPath16, err := windows.UTF16FromString(newPath)
	if err != nil {
		return err
	}
	fileNameLength := len(newPath16)*2 - 2
	var layout fileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.FileName)) + fileNameLength
	buffer := make([]byte, bufferSize)
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.FileNameLength = uint32(fileNameLength)
	copy(unsafe.Slice(&info.FileName[0], len(newPath16)-1), newPath16[:len(newPath16)-1])

	err = windows.SetFileInformationByHandle(handle, windows.FileRenameInfoEx, &buffer[0], uint32(bufferSize))
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_INVALID_FUNCTION) || errors.Is(err, windows.ERROR_INVALID_PARAMETER) ||
		errors.Is(err, windows.ERROR_INVALID_NAME) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) {
		return windows.Rename(oldPath, newPath)
	}
	return err
}
