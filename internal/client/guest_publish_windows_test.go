//go:build windows

package client

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This tests the ABI contract independently of a filesystem or live machine.
func TestGuestRenameInformationLayout(t *testing.T) {
	var layout guestRenameInformation
	if layout.ReplaceIfExists != 0 || windows.FileRenameInfo != 3 {
		t.Fatal("rename must use documented create-new class and replacement disabled")
	}
	wantRoot, wantName := uintptr(4), uintptr(12)
	if unsafe.Sizeof(windows.Handle(0)) == 8 {
		wantRoot, wantName = 8, 20
	}
	if unsafe.Offsetof(layout.RootDirectory) != wantRoot || unsafe.Offsetof(layout.FileName) != wantName {
		t.Fatalf("incorrect FILE_RENAME_INFO ABI: root=%d name=%d", unsafe.Offsetof(layout.RootDirectory), unsafe.Offsetof(layout.FileName))
	}
}

func TestGuestPublicationWindowsRejectsAliasesBeforeOpening(t *testing.T) {
	for _, path := range []string{`C:\safe\file:stream`, `C:\safe\..\file`, `C:\safe\CON.txt`, `C:\safe\file.`, `C:\safe\file `, `\\server\share\file`, `\\?\C:\safe\file`, `C:file`, `C:\safe\badname`, `C:\safe\COM¹.txt`, `C:\safe\\file`} {
		if handle, _, err := openGuestPublicationParent(path); err == nil {
			windows.CloseHandle(handle)
			t.Fatalf("unsafe destination accepted: %q", path)
		}
	}
}
