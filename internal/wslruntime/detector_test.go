package wslruntime

import (
	"errors"
	"testing"
)

func TestDetect(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		goos    string
		env     map[string]string
		proc    map[string]string
		wantWSL bool
	}{
		{
			name: "environment marker", goos: "linux",
			env:     map[string]string{"WSL_DISTRO_NAME": "Ubuntu-24.04"},
			wantWSL: true,
		},
		{
			name: "stripped environment with WSL kernel", goos: "linux",
			proc:    map[string]string{kernelReleasePath: "6.6.87.2-microsoft-standard-WSL2"},
			wantWSL: true,
		},
		{
			name: "ordinary Linux", goos: "linux",
			proc:    map[string]string{kernelReleasePath: "6.8.0-generic", kernelVersionPath: "Linux version 6.8.0-generic"},
			wantWSL: false,
		},
		{
			name: "non Linux", goos: "windows",
			env:     map[string]string{"WSL_INTEROP": "/run/WSL/interop"},
			wantWSL: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(name string) string { return tc.env[name] }
			readFile := func(path string) ([]byte, error) {
				value, ok := tc.proc[path]
				if !ok {
					return nil, errors.New("not found")
				}
				return []byte(value), nil
			}
			if got := detect(tc.goos, getenv, readFile); got != tc.wantWSL {
				t.Fatalf("detect() = %t, want %t", got, tc.wantWSL)
			}
		})
	}
}

func TestProcfsDetectionSupportsWSLEnvironmentForwarding(t *testing.T) {
	t.Parallel()

	isWSL := detect("linux", func(string) string { return "" }, func(path string) ([]byte, error) {
		if path == kernelReleasePath {
			return []byte("6.6.87.2-microsoft-standard-WSL2"), nil
		}
		return nil, errors.New("not available")
	})
	if !isWSL {
		t.Fatal("procfs WSL evidence was not detected after environment markers were stripped")
	}
	got := ForwardNamesViaWSLEnv([]string{"PATH=/usr/bin"}, []string{"AMC_BOOTSTRAP_ACTION"})
	if value := environmentValue(got, "WSLENV"); value != "AMC_BOOTSTRAP_ACTION" {
		t.Fatalf("WSLENV = %q, want bootstrap payload forwarded", value)
	}
}
