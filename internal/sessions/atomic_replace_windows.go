//go:build windows

package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInformation struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

const fileRenameInfoExFlags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
const moveFileExFlags = windows.MOVEFILE_REPLACE_EXISTING | windows.MOVEFILE_WRITE_THROUGH

type windowsReplaceOperations struct {
	targetExists    func(string) (bool, error)
	moveAbsent      func(context.Context, string, string) error
	replaceExisting func(context.Context, string, string) error
}

func prepareAtomicReplace(oldPath, newPath string) (atomicReplacement, error) {
	return prepareAtomicReplaceWith(oldPath, newPath, windowsReplaceOperations{
		targetExists:    validatedCanonicalTargetExists,
		moveAbsent:      moveFileExReplace,
		replaceExisting: fileRenameInfoExReplace,
	})
}

func syncSessionDirectory(string) error {
	// Windows cannot flush directory handles. Each selected replacement primitive
	// owns its durability contract, so no fallible work follows a successful commit.
	return nil
}

func prepareAtomicReplaceWith(oldPath, newPath string, operations windowsReplaceOperations) (atomicReplacement, error) {
	exists, err := operations.targetExists(newPath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return func(ctx context.Context) error {
			return operations.moveAbsent(ctx, oldPath, newPath)
		}, nil
	}
	return func(ctx context.Context) error {
		return operations.replaceExisting(ctx, oldPath, newPath)
	}, nil
}

func validatedCanonicalTargetExists(path string) (bool, error) {
	file, err := openSessionStateFileOnce(filepath.Dir(path), filepath.Base(path))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("sessions: validate existing canonical target: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("sessions: close validated canonical target: %w", err)
	}
	return true, nil
}

func moveFileExReplace(ctx context.Context, oldPath, newPath string) error {
	oldPath16, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return fmt.Errorf("sessions: MoveFileEx source path: %w", err)
	}
	newPath16, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return fmt.Errorf("sessions: MoveFileEx target path: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windows.MoveFileEx(oldPath16, newPath16, moveFileExFlags); err != nil {
		return fmt.Errorf("sessions: MoveFileEx commit: %w", err)
	}
	return nil
}

func fileRenameInfoExReplace(ctx context.Context, oldPath, newPath string) error {
	oldPath16, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return fmt.Errorf("sessions: FileRenameInfoEx source path: %w", err)
	}
	handle, err := windows.CreateFile(
		oldPath16,
		windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return fmt.Errorf("sessions: open FileRenameInfoEx source: %w", err)
	}

	newPath16, err := windows.UTF16FromString(newPath)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return fmt.Errorf("sessions: FileRenameInfoEx target path: %w", err)
	}
	fileNameLength := len(newPath16)*2 - 2
	var layout fileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.FileName)) + fileNameLength
	buffer := make([]byte, bufferSize)
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.Flags = fileRenameInfoExFlags
	info.FileNameLength = uint32(fileNameLength)
	copy(unsafe.Slice(&info.FileName[0], len(newPath16)-1), newPath16[:len(newPath16)-1])

	if err := ctx.Err(); err != nil {
		_ = windows.CloseHandle(handle)
		return err
	}
	renameErr := windows.SetFileInformationByHandle(handle, windows.FileRenameInfoEx, &buffer[0], uint32(bufferSize))
	closeErr := windows.CloseHandle(handle)
	if renameErr == nil {
		// The replacement is committed once SetFileInformationByHandle succeeds.
		// A later source-handle close anomaly cannot safely reclassify that effect.
		_ = closeErr
		return nil
	}
	if closeErr != nil {
		return errors.Join(
			fmt.Errorf("sessions: FileRenameInfoEx commit: %w", renameErr),
			fmt.Errorf("sessions: close FileRenameInfoEx source after failed commit: %w", closeErr),
		)
	}
	return fmt.Errorf("sessions: FileRenameInfoEx commit: %w", renameErr)
}
