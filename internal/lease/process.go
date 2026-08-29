package lease

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
)

// DefaultIdentityProvider provides process identity using the host OS runtime.
type DefaultIdentityProvider struct{}

// CurrentIdentity returns the current runtime ID, process PID, and start time.
func (d *DefaultIdentityProvider) CurrentIdentity() (string, int, string) {
	pid := os.Getpid()
	runtimeID := detectRuntimeID()
	startTime := readProcessStartTime(pid)
	return runtimeID, pid, startTime
}

// DefaultLivenessChecker checks process existence in the local OS namespace.
type DefaultLivenessChecker struct{}

// IsAlive checks whether the process with the specified PID is currently running.
func (d *DefaultLivenessChecker) IsAlive(pid int, recordedStartTime string) (bool, error) {
	if pid <= 0 {
		return false, nil
	}

	if runtime.GOOS == "linux" {
		return checkLinuxProcessAlive(pid, recordedStartTime)
	}

	return checkPosixProcessAlive(pid)
}

func checkLinuxProcessAlive(pid int, recordedStartTime string) (bool, error) {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		// Access denied or IO error - fail closed
		return false, err
	}

	if recordedStartTime == "" {
		return true, nil
	}

	currentStartTime := parseLinuxStartTime(string(data))
	if currentStartTime != "" && currentStartTime != recordedStartTime {
		// PID was recycled for a different process! Original process is dead.
		return false, nil
	}
	return true, nil
}

func checkPosixProcessAlive(pid int) (bool, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, nil
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true, nil
	}
	if errorsIsNoSuchProcess(err) {
		return false, nil
	}
	if errorsIsPermission(err) {
		return true, nil
	}
	return false, err
}

func detectRuntimeID() string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}
	// Include boot id or os name
	if runtime.GOOS == "linux" {
		if bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
			return fmt.Sprintf("linux:%s:%s", hostname, strings.TrimSpace(string(bootID)))
		}
	}
	return fmt.Sprintf("%s:%s", runtime.GOOS, hostname)
}

func readProcessStartTime(pid int) string {
	if runtime.GOOS == "linux" {
		statPath := fmt.Sprintf("/proc/%d/stat", pid)
		if data, err := os.ReadFile(statPath); err == nil {
			return parseLinuxStartTime(string(data))
		}
	}
	return ""
}

func parseLinuxStartTime(statContent string) string {
	// /proc/[pid]/stat field 22 (1-indexed) is starttime after the closing paren ')'
	lastParen := strings.LastIndex(statContent, ")")
	if lastParen == -1 || lastParen+2 >= len(statContent) {
		return ""
	}
	fields := strings.Fields(statContent[lastParen+2:])
	if len(fields) >= 20 {
		return fields[19] // field 22 in full stat (20th field after comm)
	}
	return ""
}

func errorsIsNoSuchProcess(err error) bool {
	return strings.Contains(err.Error(), "no such process") || strings.Contains(err.Error(), "process already finished")
}

func errorsIsPermission(err error) bool {
	return strings.Contains(err.Error(), "operation not permitted") || strings.Contains(err.Error(), "permission denied")
}
