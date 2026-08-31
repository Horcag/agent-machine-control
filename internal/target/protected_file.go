package target

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// protectInheritedFileForWrite takes ownership of the initial handle. It proves the
// safe inherited ACL before closing the handle, applies the final protected ACL,
// then reopens the same regular non-reparse file for payload publication.
func protectInheritedFileForWrite(ctx context.Context, file *os.File, path string, security Security) (_ *os.File, err error) {
	if file == nil || security == nil {
		return nil, errors.New("target: protected file requires a handle and security provider")
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if err := security.ValidateInheritedFile(ctx, path); err != nil {
		return nil, fmt.Errorf("validate inherited file: %w", err)
	}
	if err := file.Chmod(0600); err != nil {
		return nil, fmt.Errorf("protect inherited file mode: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close inherited file: %w", err)
	}
	closed = true
	if err := security.ProtectNewFile(ctx, path); err != nil {
		return nil, fmt.Errorf("protect closed file: %w", err)
	}
	protected, err := openNoFollowWrite(path)
	if err != nil {
		return nil, fmt.Errorf("reopen protected file: %w", err)
	}
	return protected, nil
}
