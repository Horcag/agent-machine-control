//go:build linux

package wslruntime

import (
	"strings"
	"testing"
)

func TestWindowsHostPathRequiresMountEvidence(t *testing.T) {
	for name, test := range map[string]struct {
		path      string
		mountInfo string
		want      bool
	}{
		"conventional native mount": {
			path:      "/mnt/c/ProgramData/amc",
			mountInfo: "31 20 0:27 / /mnt/c rw - ext4 /dev/sda rw\n",
		},
		"conventional WSL drive": {
			path:      "/mnt/c/ProgramData/amc",
			mountInfo: "31 20 0:27 / /mnt/c rw - 9p C:\\134 rw,aname=drvfs;path=C:\\134\n",
			want:      true,
		},
		"custom drvfs mount": {
			path:      "/windows data/amc",
			mountInfo: "31 20 0:27 / /windows\\040data rw - drvfs D:\\134 rw\n",
			want:      true,
		},
		"deeper native mount wins": {
			path: "/mnt/c/native/amc",
			mountInfo: "31 20 0:27 / /mnt/c rw - 9p C:\\134 rw,aname=drvfs\n" +
				"32 31 8:1 / /mnt/c/native rw - ext4 /dev/sda rw\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := isWindowsHostPathFromMountInfo(test.path, strings.NewReader(test.mountInfo))
			if err != nil {
				t.Fatalf("detect host path: %v", err)
			}
			if got != test.want {
				t.Fatalf("host-backed = %t, want %t", got, test.want)
			}
		})
	}
}

func TestWindowsHostPathMountInspectionFailsClosed(t *testing.T) {
	_, err := isWindowsHostPathFromMountInfo("/mnt/c/ProgramData/amc", failingMountInfoReader{})
	if err == nil {
		t.Fatal("mount inspection failure unexpectedly accepted")
	}
}

type failingMountInfoReader struct{}

func (failingMountInfoReader) Read([]byte) (int, error) {
	return 0, errSyntheticMountInfo
}

type syntheticMountInfoError struct{}

func (syntheticMountInfoError) Error() string { return "synthetic mountinfo failure" }

var errSyntheticMountInfo syntheticMountInfoError
