//go:build linux

package lease

import (
	"fmt"
	"os"
	"strings"
)

func platformProcessAlive(pid int, recordedStartTime string) (bool, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if recordedStartTime == "" {
		return true, nil
	}
	current := parseLinuxStartTime(string(data))
	if current == "" {
		return false, fmt.Errorf("lease: cannot determine start identity for pid %d", pid)
	}
	return current == recordedStartTime, nil
}

func platformProcessStartTime(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	return parseLinuxStartTime(string(data))
}

func platformRuntimeIdentity() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func parseLinuxStartTime(statContent string) string {
	lastParen := strings.LastIndex(statContent, ")")
	if lastParen == -1 || lastParen+2 >= len(statContent) {
		return ""
	}
	fields := strings.Fields(statContent[lastParen+2:])
	if len(fields) >= 20 {
		return fields[19]
	}
	return ""
}
