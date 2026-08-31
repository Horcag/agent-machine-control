package ssh

import (
	"reflect"
	"testing"
)

func TestStrictFileWalkPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cleaned   string
		volume    string
		separator string
		absolute  bool
		want      []string
		wantErr   bool
	}{
		{
			name:      "drive root",
			cleaned:   `C:\synthetic\keys\id.key`,
			volume:    "C:",
			separator: `\`,
			absolute:  true,
			want:      []string{`C:\`, `C:\synthetic`, `C:\synthetic\keys`, `C:\synthetic\keys\id.key`},
		},
		{
			name:      "UNC share",
			cleaned:   `\\synthetic-server\synthetic-share\keys\id.key`,
			volume:    `\\synthetic-server\synthetic-share`,
			separator: `\`,
			absolute:  true,
			want: []string{
				`\\synthetic-server\synthetic-share\`,
				`\\synthetic-server\synthetic-share\keys`,
				`\\synthetic-server\synthetic-share\keys\id.key`,
			},
		},
		{
			name:      "POSIX root",
			cleaned:   "/var/lib/amc/id.key",
			separator: "/",
			absolute:  true,
			want:      []string{"/", "/var", "/var/lib", "/var/lib/amc", "/var/lib/amc/id.key"},
		},
		{
			name:      "relative path",
			cleaned:   "keys/id.key",
			separator: "/",
			want:      []string{"keys", "keys/id.key"},
		},
		{
			name:      "repeated separators",
			cleaned:   "/var//lib///id.key",
			separator: "/",
			absolute:  true,
			want:      []string{"/", "/var", "/var/lib", "/var/lib/id.key"},
		},
		{
			name:      "direct child final path",
			cleaned:   `C:\id.key`,
			volume:    "C:",
			separator: `\`,
			absolute:  true,
			want:      []string{`C:\`, `C:\id.key`},
		},
		{
			name:      "volume does not match path",
			cleaned:   `D:\synthetic\id.key`,
			volume:    "C:",
			separator: `\`,
			absolute:  true,
			wantErr:   true,
		},
		{
			name:      "drive relative path",
			cleaned:   `C:synthetic\id.key`,
			volume:    "C:",
			separator: `\`,
			wantErr:   true,
		},
		{
			name:      "absolute path without root",
			cleaned:   "synthetic/id.key",
			separator: "/",
			absolute:  true,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := strictFileWalkPaths(tt.cleaned, tt.volume, tt.separator, tt.absolute)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("strictFileWalkPaths() error = nil, want error; paths = %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("strictFileWalkPaths() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("strictFileWalkPaths() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
