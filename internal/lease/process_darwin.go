//go:build darwin

package lease

import (
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/unix"
)

func platformProcessAlive(pid int, recordedStartTime string) (bool, error) {
	identity, err := darwinProcessStartTime(pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return false, nil
		}
		return false, err
	}
	if recordedStartTime == "" {
		return true, nil
	}
	return identity == recordedStartTime, nil
}

func platformProcessStartTime(pid int) string {
	identity, _ := darwinProcessStartTime(pid)
	return identity
}

func darwinProcessStartTime(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.EIO) {
			// Darwin can report EIO for a PID that no longer exists. Confirm
			// absence without weakening fail-closed handling of other errors.
			if killErr := unix.Kill(pid, 0); errors.Is(killErr, unix.ESRCH) {
				return "", unix.ESRCH
			}
		}
		return "", err
	}
	if info == nil || info.Proc.P_pid != int32(pid) {
		return "", unix.ESRCH
	}
	return strconv.FormatInt(info.Proc.P_starttime.Sec, 10) + ":" + strconv.FormatInt(int64(info.Proc.P_starttime.Usec), 10), nil
}

func platformRuntimeIdentity() string {
	return fmt.Sprintf("pid1-start:%s", platformProcessStartTime(1))
}
