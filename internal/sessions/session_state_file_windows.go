//go:build windows

package sessions

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func openSessionStateFile(sessionsDir, filename string) (*os.File, error) {
	path, err := windows.UTF16PtrFromString(filepath.Join(sessionsDir, filename))
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		path,
		windows.GENERIC_READ,
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
		return nil, fmt.Errorf("sessions: canonical session is not a regular non-reparse file")
	}
	file := os.NewFile(uintptr(handle), filename)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("sessions: failed to adopt canonical session handle")
	}
	return file, nil
}
