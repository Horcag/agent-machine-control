//go:build windows

package ssh

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestStrictFileWalkPathsPreservesWindowsVolumeRoots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "drive",
			path: `C:\synthetic\keys\id.key`,
			want: []string{`C:\`, `C:\synthetic`, `C:\synthetic\keys`, `C:\synthetic\keys\id.key`},
		},
		{
			name: "UNC share",
			path: `\\synthetic-server\synthetic-share\keys\id.key`,
			want: []string{
				`\\synthetic-server\synthetic-share\`,
				`\\synthetic-server\synthetic-share\keys`,
				`\\synthetic-server\synthetic-share\keys\id.key`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cleaned := filepath.Clean(tt.path)
			got, err := strictFileWalkPaths(
				cleaned,
				filepath.VolumeName(cleaned),
				string(filepath.Separator),
				filepath.IsAbs(cleaned),
			)
			if err != nil {
				t.Fatalf("strictFileWalkPaths() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("strictFileWalkPaths() = %#v, want %#v", got, tt.want)
			}
			if got[0] == string(filepath.Separator) {
				t.Fatalf("walker started from bare separator %q instead of volume root", got[0])
			}
		})
	}
}
