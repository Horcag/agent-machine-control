//go:build windows

package target

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func openNoFollow(path string) (*os.File, error) {
	return openNoFollowAccess(path, windows.GENERIC_READ)
}

func openNoFollowWrite(path string) (*os.File, error) {
	return openNoFollowAccess(path, windows.GENERIC_WRITE)
}

func openNoFollowAccess(path string, access uint32) (*os.File, error) {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		path16,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("target: canonical state is not a regular non-reparse file")
	}
	file := os.NewFile(uintptr(handle), StateFileName)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("target: failed to adopt canonical state handle")
	}
	return file, nil
}
