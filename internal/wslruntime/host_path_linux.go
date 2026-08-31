//go:build linux

package wslruntime

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// IsWindowsHostPath reports whether path is backed by a Windows filesystem.
// It uses mount metadata rather than trusting a conventional path prefix.
// Callers that require Windows security rules must treat an inspection failure
// as an error rather than applying POSIX policy.
func IsWindowsHostPath(path string) (bool, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, fmt.Errorf("inspect mount table: %w", err)
	}
	defer file.Close()
	return isWindowsHostPathFromMountInfo(path, file)
}

func isWindowsHostPathFromMountInfo(path string, mountInfo io.Reader) (bool, error) {
	cleaned := filepath.Clean(path)
	bestMount := ""
	bestHostBacked := false
	scanner := bufio.NewScanner(mountInfo)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := indexOf(fields, "-")
		if separator < 0 || separator+2 >= len(fields) || len(fields) < 5 {
			continue
		}
		mountPoint := unescapeMountInfoPath(fields[4])
		if !pathWithin(cleaned, mountPoint) || len(mountPoint) <= len(bestMount) {
			continue
		}
		fsType := fields[separator+1]
		superOptions := strings.Join(fields[separator+3:], " ")
		bestMount = mountPoint
		bestHostBacked = fsType == "drvfs" || fsType == "9p" && strings.Contains(superOptions, "aname=drvfs")
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("inspect mount table: %w", err)
	}
	return bestHostBacked, nil
}

func indexOf(values []string, needle string) int {
	for index, value := range values {
		if value == needle {
			return index
		}
	}
	return -1
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func unescapeMountInfoPath(path string) string {
	return strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	).Replace(path)
}
