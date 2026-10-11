//go:build linux

package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func createGuestStagingFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
}

// publishGuestFileHandle never resolves the staging pathname. The caller owns
// the open file and staging cleanup. Errors after Linkat leave the published
// entry intact: removing it by pathname could remove another actor's file.
func publishGuestFileHandle(ctx context.Context, file *os.File, destination string) error {
	if err := guestPublicationContextErr(ctx); err != nil {
		return err
	}
	parent, leaf, err := openGuestPublicationParent(destination)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var publicationErr error
	err = raw.Control(func(fd uintptr) {
		var before unix.Stat_t
		if publicationErr = unix.Fstat(int(fd), &before); publicationErr != nil {
			return
		}
		if before.Mode&unix.S_IFMT != unix.S_IFREG {
			publicationErr = errors.New("guest staging must be a regular file")
			return
		}
		if publicationErr = guestPublicationContextErr(ctx); publicationErr != nil {
			return
		}
		// Control pins the descriptor against concurrent Close and reuse throughout
		// source lookup, publication, and identity validation.
		publicationErr = unix.Linkat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", fd), parent, leaf, unix.AT_SYMLINK_FOLLOW)
		if publicationErr != nil {
			return
		}
		var installed, after unix.Stat_t
		if publicationErr = unix.Fstatat(parent, leaf, &installed, unix.AT_SYMLINK_NOFOLLOW); publicationErr != nil {
			publicationErr = fmt.Errorf("guest file published but destination inspection failed: %w", publicationErr)
			return
		}
		if publicationErr = unix.Fstat(int(fd), &after); publicationErr != nil {
			publicationErr = fmt.Errorf("guest file published but source inspection failed: %w", publicationErr)
			return
		}
		if installed.Mode&unix.S_IFMT != unix.S_IFREG || installed.Dev != before.Dev || installed.Ino != before.Ino || after.Dev != before.Dev || after.Ino != before.Ino || after.Size != before.Size || after.Mtim != before.Mtim || installed.Size != before.Size || installed.Mtim != before.Mtim {
			publicationErr = errors.New("guest file published but source or destination identity changed")
			return
		}
		if publicationErr = unix.Fsync(parent); publicationErr != nil {
			publicationErr = fmt.Errorf("guest file published but directory sync failed: %w", publicationErr)
		}
	})
	if err != nil {
		return err
	}
	return publicationErr
}

// Walk each component relative to its held directory, rejecting symlinks even
// when they point inside the tree. Lexical traversal is rejected before Abs
// can clean it away.
func openGuestPublicationParent(destination string) (int, string, error) {
	absolute, err := guestPublicationDestination(destination)
	if err != nil {
		return -1, "", err
	}
	parts := strings.Split(strings.TrimPrefix(absolute, "/"), "/")
	parent, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(parent, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(parent)
		if openErr != nil {
			return -1, "", openErr
		}
		parent = next
	}
	return parent, parts[len(parts)-1], nil
}

// Check the wall clock as well as Err: deadline timer delivery can lag.
func guestPublicationContextErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func guestPublicationDestination(destination string) (string, error) {
	if destination == "" || strings.ContainsRune(destination, 0) {
		return "", errors.New("invalid guest destination")
	}
	for i, part := range strings.Split(destination, "/") {
		if (part == "" && i != 0) || part == "." || part == ".." {
			return "", errors.New("invalid guest destination segment")
		}
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	return absolute, nil
}
