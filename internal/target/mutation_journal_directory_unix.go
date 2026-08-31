//go:build unix

package target

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// createPlatformMutationJournalDirectory creates Windows-hosted WSL directories with their final
// owner and DACL at creation time. Plain POSIX directories retain the restrictive Unix path.
func createPlatformMutationJournalDirectory(ctx context.Context, path string, security Security) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	creator, hostBacked, err := hostBackedMutationJournalDirectoryCreator(path, security)
	if err != nil {
		return false, err
	}
	if hostBacked {
		return creator.CreatePrivateDirectory(ctx, path)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	if err := security.ProtectNewDir(ctx, path); err != nil {
		return true, fmt.Errorf("%w: private mutation directory: %v", ErrInsecureState, err)
	}
	return true, nil
}

func hostBackedMutationJournalDirectoryCreator(path string, security Security) (WindowsPrivateDirectoryCreator, bool, error) {
	platform, ok := security.(*platformSecurity)
	if !ok {
		return nil, false, nil
	}
	hostBacked, err := platform.isHostBacked(path)
	if err != nil || !hostBacked {
		return nil, hostBacked, err
	}
	if platform.windowsGuard == nil {
		return nil, true, ErrHostSecurityUnproven
	}
	creator, ok := platform.windowsGuard.(WindowsPrivateDirectoryCreator)
	if !ok {
		return nil, true, ErrHostSecurityUnproven
	}
	return creator, true, nil
}
