//go:build linux

package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
)

type priorRuntimeFileInfo struct {
	mode os.FileMode
	stat any
}

func (f priorRuntimeFileInfo) Name() string       { return "synthetic-record" }
func (f priorRuntimeFileInfo) Size() int64        { return 1 }
func (f priorRuntimeFileInfo) Mode() os.FileMode  { return f.mode }
func (f priorRuntimeFileInfo) ModTime() time.Time { return time.Time{} }
func (f priorRuntimeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f priorRuntimeFileInfo) Sys() any           { return f.stat }

func TestPriorRuntimeFileOwnershipFailsClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mode os.FileMode
		stat any
		want bool
	}{
		{"owned private file", 0600, &syscall.Stat_t{Uid: 1000}, true},
		{"owned private directory", os.ModeDir | 0700, &syscall.Stat_t{Uid: 1000}, true},
		{"foreign file", 0600, &syscall.Stat_t{Uid: 1001}, false},
		{"foreign directory", os.ModeDir | 0700, &syscall.Stat_t{Uid: 1001}, false},
		{"unproven owner", 0600, nil, false},
		{"symlink", os.ModeSymlink | 0600, &syscall.Stat_t{Uid: 1000}, false},
		{"public file", 0644, &syscall.Stat_t{Uid: 1000}, false},
		{"public directory", os.ModeDir | 0755, &syscall.Stat_t{Uid: 1000}, false},
		{"special file", os.ModeNamedPipe | 0600, &syscall.Stat_t{Uid: 1000}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := priorRuntimeFileOwned(priorRuntimeFileInfo{tc.mode, tc.stat}, 1000); got != tc.want {
				t.Fatalf("ownership = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLocalDaemonPriorRuntimeSingletonWithoutEndpoint(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		alive   bool
		public  bool
		wantErr error
	}{
		{name: "dead owned prior singleton"},
		{name: "live prior singleton", alive: true, wantErr: app.ErrBootstrapDrift},
		{name: "public prior singleton", public: true, wantErr: app.ErrBootstrapDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := filepath.Join(t.TempDir(), "state")
			writeSingletonOwner(t, stateDir, priorBootstrapRuntime, 100, "old-start")
			if tc.public {
				if err := os.Chmod(filepath.Join(stateDir, "daemon", "singleton.lock", "owner.json"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			controller := &LocalDaemon{
				identity: bootstrapRuntimeIdentity(currentBootstrapRuntime),
				liveness: bootstrapRuntimeLiveness(func(int, string) (bool, error) { return tc.alive, nil }),
			}
			healthy, err := controller.Healthy(context.Background(), stateDir)
			if healthy || !errors.Is(err, tc.wantErr) {
				t.Fatalf("Healthy() = %v, %v; want unavailable with %v", healthy, err, tc.wantErr)
			}
			observation, err := controller.ObserveRelease(context.Background(), stateDir)
			if err != nil || observation.State != app.BootstrapDaemonReleaseDrift {
				t.Fatalf("ObserveRelease() = %#v, %v; want strict drift", observation, err)
			}
		})
	}
}

func TestPriorRuntimeNativePathDetectionFailsClosed(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "state")
	writeSingletonOwner(t, stateDir, priorBootstrapRuntime, 100, "old-start")
	daemonDir := filepath.Join(stateDir, "daemon")
	endpoint := filepath.Join(daemonDir, "endpoint.json")
	if err := os.WriteFile(endpoint, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	paths := []string{stateDir, daemonDir, filepath.Join(daemonDir, "singleton.lock"), filepath.Join(daemonDir, "singleton.lock", "owner.json"), endpoint}
	t.Run("all native paths inspected", func(t *testing.T) {
		var calls []string
		got := priorRuntimeNativePathsOwned(daemonDir, true, func(path string) (bool, error) {
			calls = append(calls, path)
			return false, nil
		})
		if !got || !slices.Equal(calls, paths) {
			t.Fatalf("native ownership = %v, inspected %v; want all paths %v", got, calls, paths)
		}
	})
	for i, rejected := range paths {
		for _, failure := range []struct {
			name string
			err  error
		}{
			{"Windows backed", nil},
			{"inspection error", errors.New("synthetic nested mount inspection failure")},
		} {
			t.Run(filepath.Base(rejected)+"/"+failure.name, func(t *testing.T) {
				assertPriorRuntimePathRefusal(t, daemonDir, paths, i, failure.err)
			})
		}
	}
}

func assertPriorRuntimePathRefusal(t *testing.T, daemonDir string, paths []string, rejectedIndex int, detectionErr error) {
	t.Helper()
	var calls []string
	got := priorRuntimeNativePathsOwned(daemonDir, true, func(path string) (bool, error) {
		calls = append(calls, path)
		if path == paths[rejectedIndex] {
			return detectionErr == nil, detectionErr
		}
		return false, nil
	})
	if got || !slices.Equal(calls, paths[:rejectedIndex+1]) {
		t.Fatalf("native ownership = %v, inspected %v; want refusal after %v", got, calls, paths[:rejectedIndex+1])
	}
}
