package statedir

import (
	"fmt"
	"os"
	"runtime"
)

// SyncDir flushes directory metadata changes to durable storage.
// On POSIX and WSL systems, it opens the directory and calls Sync() on the directory file descriptor.
// On Windows, directory handle flush via FlushFileBuffers is not supported by Win32 (returning
// ERROR_INVALID_HANDLE); this platform limitation is explicitly documented and handled, while
// unexpected errors (such as a non-existent directory) are returned.
func SyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("failed to open directory for sync %q: %w", dir, err)
	}

	if runtime.GOOS == "windows" {
		// Win32 FlushFileBuffers does not support directory handles.
		// Verify that the handle was opened successfully and close it.
		return f.Close()
	}

	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return fmt.Errorf("failed to sync directory %q: %w", dir, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close directory after sync %q: %w", dir, closeErr)
	}
	return nil
}
