//go:build windows

package lease

import (
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/windows"
)

func platformProcessAlive(pid int, recordedStartTime string) (bool, error) {
	identity, err := windowsProcessStartTime(pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) && recordedStartTime == "" {
			return true, nil
		}
		return false, err
	}
	if recordedStartTime == "" {
		return true, nil
	}
	return identity == recordedStartTime, nil
}

func platformProcessStartTime(pid int) string {
	identity, _ := windowsProcessStartTime(pid)
	return identity
}

func windowsProcessStartTime(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", err
	}
	return strconv.FormatInt(creation.Nanoseconds(), 10), nil
}

func platformRuntimeIdentity() string {
	return fmt.Sprintf("pid4-start:%s", platformProcessStartTime(4))
}
