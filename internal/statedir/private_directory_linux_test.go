//go:build linux

package statedir

import (
	"context"
	"errors"
	"testing"
)

func TestPrivateEvidenceDirectoryRequiresAtomicInheritableWindowsGuard(t *testing.T) {
	restoreWindowsHostStateDirHooks(t)
	windowsHostPathDetector = func(string) (bool, error) { return true, nil }
	const path = "/mnt/c/synthetic/private-evidence"
	guardErr := errors.New("synthetic ACL proof rejected")
	for _, failure := range []bool{true, false} {
		calls := 0
		windowsStateDirBatchGuard = func(_ context.Context, requests []windowsHostStateDirRequest) ([]windowsHostStateDirResult, error) {
			calls++
			assertPrivateEvidenceRequests(t, requests, path)
			if failure {
				return nil, guardErr
			}
			return make([]windowsHostStateDirResult, len(requests)), nil
		}
		err := EnsurePrivateDirectory(path)
		if calls != 1 || (failure && !errors.Is(err, ErrInsecurePermissions)) || (!failure && err != nil) {
			t.Fatalf("calls=%d failure=%t error=%v", calls, failure, err)
		}
	}
}

func assertPrivateEvidenceRequests(t *testing.T, requests []windowsHostStateDirRequest, path string) {
	t.Helper()
	if len(requests) == 0 {
		t.Fatal("missing protected directory request")
	}
	for _, request := range requests {
		if request.Action != "create" || request.AllowTargetInheritance != (request.Path == path) {
			t.Fatalf("unsafe evidence directory request: %+v", requests)
		}
	}
	if requests[len(requests)-1].Path != path {
		t.Fatalf("missing evidence directory: %+v", requests)
	}
}
