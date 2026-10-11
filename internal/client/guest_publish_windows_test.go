//go:build windows

package client

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/Horcag/agent-machine-control/internal/target"

	"golang.org/x/sys/windows"
)

// This tests the ABI contract independently of a filesystem or live machine.
func TestGuestRenameInformationLayout(t *testing.T) {
	var layout guestRenameInformation
	if layout.ReplaceIfExists != 0 || windows.FileRenameInformation != 10 {
		t.Fatal("rename must use documented create-new class and replacement disabled")
	}
	wantRoot, wantName := uintptr(4), uintptr(12)
	if unsafe.Sizeof(windows.Handle(0)) == 8 {
		wantRoot, wantName = 8, 20
	}
	if unsafe.Offsetof(layout.RootDirectory) != wantRoot || unsafe.Offsetof(layout.FileName) != wantName {
		t.Fatalf("incorrect FILE_RENAME_INFORMATION ABI: root=%d name=%d", unsafe.Offsetof(layout.RootDirectory), unsafe.Offsetof(layout.FileName))
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

// Exercise the native ABI and filesystem operation, rather than merely checking
// layout constants. This test must execute on the configured Windows CI runner.
func TestGuestPublicationWindowsNativeHeldRename(t *testing.T) {
	for _, mode := range []string{"success", "existing-destination", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			parent := t.TempDir()
			stage, err := newGuestStaging(ctx, parent)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := stage.cleanup(); err != nil {
					t.Error(err)
				}
			})
			if _, err := stage.file.WriteString("verified original"); err != nil {
				t.Fatal(err)
			}
			if err := stage.file.Sync(); err != nil {
				t.Fatal(err)
			}
			original, err := stage.file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(parent, "published")
			if mode == "existing-destination" {
				if err := os.WriteFile(destination, []byte("existing protected bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			err = publishGuestFileHandle(ctx, stage.file, destination)
			if mode != "success" {
				if err == nil {
					t.Fatal("publication unexpectedly succeeded")
				}
				if mode == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation: %v", err)
				}
				if mode == "existing-destination" && !errors.Is(err, os.ErrExist) {
					t.Fatalf("create-new rename returned an unrelated failure: %v", err)
				}
				pending, statErr := os.Stat(stage.file.Name())
				if statErr != nil || !os.SameFile(original, pending) {
					t.Fatalf("failure changed staging identity: %v", statErr)
				}
				if mode == "existing-destination" {
					data, readErr := os.ReadFile(destination)
					if readErr != nil || string(data) != "existing protected bytes" {
						t.Fatalf("existing file overwritten: %q %v", data, readErr)
					}
				} else if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
					t.Fatalf("canceled publication created a file: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(stage.file.Name()); !os.IsNotExist(err) {
				t.Fatalf("rename retained staging entry: %v", err)
			}
			installed, err := os.Stat(destination)
			if err != nil || !os.SameFile(original, installed) {
				t.Fatalf("rename published a different object: %v", err)
			}
			held, err := stage.file.Stat()
			if err != nil || !os.SameFile(original, held) {
				t.Fatalf("publication closed or changed held source: %v", err)
			}
			// The original handle retains WRITE and DELETE access. This reader
			// must share both while independently checking the installed object.
			name, err := windows.UTF16PtrFromString(destination)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
			if err != nil {
				t.Fatal(err)
			}
			reader := os.NewFile(uintptr(handle), destination)
			t.Cleanup(func() { _ = reader.Close() })
			reopened, err := reader.Stat()
			if err != nil || !os.SameFile(original, reopened) {
				t.Fatalf("reader opened a different object: %v", err)
			}
			data, err := io.ReadAll(io.LimitReader(reader, int64(len("verified original")+1)))
			if err != nil || string(data) != "verified original" {
				t.Fatalf("published bytes: %q %v", data, err)
			}
			if err := target.NewPrivatePathSecurity().ValidateFile(ctx, destination); err != nil {
				t.Fatalf("rename changed private ACL: %v", err)
			}
		})
	}
}

func TestGuestPublicationWindowsNativeStagingExcludesOtherWriters(t *testing.T) {
	stage, err := newGuestStaging(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stage.cleanup(); err != nil {
			t.Error(err)
		}
	})
	other, err := os.OpenFile(stage.file.Name(), os.O_WRONLY, 0)
	if err == nil {
		other.Close()
		t.Fatal("held staging allowed a concurrent writer")
	}
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("writer exclusion failed for another reason: %v", err)
	}
}
