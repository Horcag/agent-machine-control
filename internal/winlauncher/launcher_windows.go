//go:build windows

package winlauncher

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

func launch(args []string) (int, error) {
	powershell, err := systemPowerShell()
	if err != nil {
		return 1, err
	}
	cmd := exec.Command(powershell, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW prevents console creation at process startup.
		HideWindow:    true,
	}
	return commandResult(cmd.Run())
}

func systemPowerShell() (string, error) {
	var directory [syscall.MAX_PATH]uint16
	getSystemDirectory := syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW")
	n, _, err := getSystemDirectory.Call(uintptr(unsafe.Pointer(&directory[0])), uintptr(len(directory)))
	if n == 0 {
		return "", fmt.Errorf("resolve Windows system directory: %w", err)
	}
	if n >= uintptr(len(directory)) {
		return "", fmt.Errorf("Windows system directory exceeds %d characters", len(directory)-1)
	}
	return filepath.Join(syscall.UTF16ToString(directory[:n]), "WindowsPowerShell", "v1.0", "powershell.exe"), nil
}
