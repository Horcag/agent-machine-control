//go:build windows

package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createPlatformMutationJournalDirectory attaches the final Windows owner and protected DACL to
// a new mutation directory atomically. Existing paths are left untouched for validation only.
func createPlatformMutationJournalDirectory(ctx context.Context, path string, _ Security) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	current, err := targetCurrentWindowsSID()
	if err != nil {
		return false, err
	}
	descriptor, err := targetWindowsPrivateDirectoryDescriptor(current)
	if err != nil {
		return false, err
	}
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, fmt.Errorf("target: encode mutation journal path %q: %w", path, err)
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	if err := windows.CreateDirectory(path16, &attributes); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func targetWindowsPrivateDirectoryDescriptor(owner *windows.SID) (*windows.SECURITY_DESCRIPTOR, error) {
	if owner == nil {
		return nil, errors.New("target: current Windows SID is unavailable")
	}
	var sddl strings.Builder
	sddl.WriteString("O:")
	sddl.WriteString(owner.String())
	sddl.WriteString("D:P")
	for _, trustee := range windowsAllowedTrusteeSIDs(owner.String()) {
		sddl.WriteString("(A;OICI;FA;;;")
		sddl.WriteString(trustee)
		sddl.WriteByte(')')
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl.String())
	if err != nil {
		return nil, fmt.Errorf("target: build private mutation directory DACL: %w", err)
	}
	return descriptor, nil
}
