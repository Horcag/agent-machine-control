//go:build windows

package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The private staging directory's validated ACL is inherited. DELETE access
// permits held-handle rename; withholding write/delete sharing excludes other
// writers and staging-name replacement while verification and publication run.
func createGuestStagingFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE, windows.FILE_SHARE_READ, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

// FILE_RENAME_INFORMATION uses the zero union value for ReplaceIfExists=FALSE.
// RootDirectory and the leaf name ensure lookup stays within the held parent.
// The NT rename class supports a relative leaf under the held RootDirectory.
// https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information
// Successful rename removes the original staging entry. The caller retains
// ownership of the open file; cleanup must tolerate that entry being absent.
type guestRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func publishGuestFileHandle(ctx context.Context, file *os.File, destination string) error {
	if err := guestPublicationContextErr(ctx); err != nil {
		return err
	}
	parent, leaf, err := openGuestPublicationParent(destination)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	name, err := windows.UTF16FromString(leaf)
	if err != nil {
		return err
	}
	var layout guestRenameInformation
	size := int(unsafe.Sizeof(layout)) + len(name)*2
	buffer := make([]byte, size)
	rename := (*guestRenameInformation)(unsafe.Pointer(&buffer[0]))
	rename.RootDirectory = parent
	rename.FileNameLength = uint32((len(name) - 1) * 2)
	copy(unsafe.Slice(&rename.FileName[0], len(name)), name)
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var publicationErr error
	err = raw.Control(func(fd uintptr) {
		source := windows.Handle(fd)
		var before, after windows.ByHandleFileInformation
		if publicationErr = windows.GetFileInformationByHandle(source, &before); publicationErr != nil {
			return
		}
		if before.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
			publicationErr = errors.New("guest staging must be a regular non-reparse file")
			return
		}
		if publicationErr = guestPublicationContextErr(ctx); publicationErr != nil {
			return
		}
		var status windows.IO_STATUS_BLOCK
		publicationErr = windows.NtSetInformationFile(source, &status, &buffer[0], uint32(size), windows.FileRenameInformation)
		if publicationErr != nil {
			if nativeStatus, ok := publicationErr.(windows.NTStatus); ok {
				publicationErr = nativeStatus.Errno()
			}
			publicationErr = fmt.Errorf("guest held-handle rename failed: %w", publicationErr)
			return
		}
		installed, inspectErr := openGuestPublicationChild(parent, leaf, false)
		if inspectErr != nil {
			publicationErr = fmt.Errorf("guest file published but destination inspection failed: %w", inspectErr)
			return
		}
		defer windows.CloseHandle(installed)
		var target windows.ByHandleFileInformation
		if publicationErr = windows.GetFileInformationByHandle(installed, &target); publicationErr == nil {
			publicationErr = windows.GetFileInformationByHandle(source, &after)
		}
		if publicationErr != nil {
			publicationErr = fmt.Errorf("guest file published but identity inspection failed: %w", publicationErr)
			return
		}
		if !sameGuestWindowsFile(before, target) || !sameGuestWindowsFile(before, after) || target.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
			publicationErr = errors.New("guest file published but source or destination identity changed")
		}
	})
	if err != nil {
		return err
	}
	return publicationErr
}

func sameGuestWindowsFile(a, b windows.ByHandleFileInformation) bool {
	return a.VolumeSerialNumber == b.VolumeSerialNumber && a.FileIndexHigh == b.FileIndexHigh && a.FileIndexLow == b.FileIndexLow && a.FileSizeHigh == b.FileSizeHigh && a.FileSizeLow == b.FileSizeLow && a.LastWriteTime == b.LastWriteTime
}

func openGuestPublicationParent(destination string) (windows.Handle, string, error) {
	absolute, err := guestPublicationDestination(destination)
	if err != nil {
		return 0, "", err
	}
	volume := filepath.VolumeName(absolute)
	parts := strings.Split(absolute[len(volume)+1:], `\`)
	rootName, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return 0, "", err
	}
	parent, err := windows.CreateFile(rootName, windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := openGuestPublicationChild(parent, part, true)
		windows.CloseHandle(parent)
		if openErr != nil {
			return 0, "", fmt.Errorf("guest held destination directory open failed: %w", openErr)
		}
		parent = next
	}
	return parent, parts[len(parts)-1], nil
}

// Each NT lookup is relative to a held directory handle. OPEN_REPARSE_POINT
// prevents traversal, and attribute inspection rejects even in-tree junctions.
func openGuestPublicationChild(parent windows.Handle, name string, directory bool) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: objectName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	access := uint32(windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
		access |= windows.FILE_TRAVERSE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access, &attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, options, 0, 0)
	if err != nil {
		return 0, err
	}
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err == nil && info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		err = errors.New("guest destination contains a reparse point")
	}
	if err != nil {
		windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
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

// Validate raw components before Abs can erase traversal or normalize aliases.
func guestPublicationDestination(destination string) (string, error) {
	if destination == "" || !utf8.ValidString(destination) || strings.ContainsAny(destination, "\x00\"<>|?*") {
		return "", errors.New("invalid guest destination")
	}
	destination = strings.ReplaceAll(destination, "/", `\`)
	volume := filepath.VolumeName(destination)
	if volume != "" && (len(volume) != 2 || volume[1] != ':' || !((volume[0] >= 'A' && volume[0] <= 'Z') || (volume[0] >= 'a' && volume[0] <= 'z')) || !filepath.IsAbs(destination)) {
		return "", errors.New("guest destination requires a local drive path")
	}
	if volume == "" && strings.HasPrefix(destination, `\`) {
		return "", errors.New("root-relative guest destination is unsupported")
	}
	raw := destination
	if volume != "" {
		raw = destination[len(volume)+1:]
	}
	for _, part := range strings.Split(raw, `\`) {
		if part == "" || part == "." || part == ".." || strings.Contains(part, ":") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return "", errors.New("invalid guest destination segment")
		}
		for _, character := range part {
			if character < 32 {
				return "", errors.New("invalid guest destination control character")
			}
		}
		base := strings.TrimRight(strings.ToUpper(strings.SplitN(part, ".", 2)[0]), " ")
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" || ((strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && utf8.RuneCountInString(base[3:]) == 1 && strings.ContainsAny(base[3:], "123456789¹²³")) {
			return "", errors.New("reserved guest destination segment")
		}
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	// A relative path on a network current directory must not select UNC access.
	if len(filepath.VolumeName(absolute)) != 2 {
		return "", errors.New("guest destination requires a local drive path")
	}
	return absolute, nil
}
