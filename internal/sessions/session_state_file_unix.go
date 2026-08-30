//go:build unix

package sessions

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openSessionStateFile(sessionsDir, filename string) (*os.File, error) {
	dir, err := os.Open(sessionsDir)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	fd, err := unix.Openat(int(dir.Fd()), filename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filename)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("sessions: failed to adopt canonical session handle")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = file.Close()
		return nil, fmt.Errorf("sessions: canonical session is not a regular file")
	}
	return file, nil
}
