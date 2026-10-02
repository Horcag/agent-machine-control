//go:build unix

package target

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func enrollmentFileIdentity(file *os.File) (string, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return "", err
	}
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("unix:%d:%d:%d", stat.Dev, stat.Ino, info.ModTime().UnixNano()), nil
}
