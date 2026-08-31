// Package wslruntime identifies WSL from Linux runtime capabilities.
package wslruntime

import (
	"os"
	"runtime"
	"strings"
)

const (
	kernelReleasePath = "/proc/sys/kernel/osrelease"
	kernelVersionPath = "/proc/version"
)

// IsWSL reports whether this Linux process is running under Windows Subsystem
// for Linux. Environment markers take precedence, while kernel procfs evidence
// supports launchers that strip WSL-specific environment variables.
func IsWSL() bool {
	return detect(runtime.GOOS, os.Getenv, os.ReadFile)
}

func detect(goos string, getenv func(string) string, readFile func(string) ([]byte, error)) bool {
	if goos != "linux" {
		return false
	}
	if strings.TrimSpace(getenv("WSL_DISTRO_NAME")) != "" || strings.TrimSpace(getenv("WSL_INTEROP")) != "" {
		return true
	}
	for _, path := range []string{kernelReleasePath, kernelVersionPath} {
		data, err := readFile(path)
		if err != nil {
			continue
		}
		kernel := strings.ToLower(string(data))
		if strings.Contains(kernel, "microsoft") || strings.Contains(kernel, "wsl") {
			return true
		}
	}
	return false
}
