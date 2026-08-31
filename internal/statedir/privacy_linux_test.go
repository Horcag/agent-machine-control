//go:build linux

package statedir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxPrivateDirectoryRoutesWindowsHostPathsThroughAtomicGuard(t *testing.T) {
	restoreWindowsHostStateDirHooks(t)
	windowsHostPathDetector = func(string) (bool, error) { return true, nil }

	var calls []struct {
		path             string
		allowInheritance bool
		action           string
	}
	windowsStateDirGuard = func(_ context.Context, path string, allowTargetInheritance bool, action string) (bool, error) {
		calls = append(calls, struct {
			path             string
			allowInheritance bool
			action           string
		}{path, allowTargetInheritance, action})
		return true, nil
	}

	if err := createPlatformPrivateDirectory("/mnt/c/synthetic/state", false); err != nil {
		t.Fatalf("create Windows-host-backed directory: %v", err)
	}
	if err := validatePlatformPrivateDirectory("/mnt/c/synthetic/targets", true); err != nil {
		t.Fatalf("validate Windows-host-backed directory: %v", err)
	}
	if got, want := len(calls), 2; got != want {
		t.Fatalf("guard calls = %d, want %d", got, want)
	}
	if calls[0].action != "create" || calls[0].allowInheritance {
		t.Fatalf("ordinary state create = %+v, want atomic non-inheritable create", calls[0])
	}
	if calls[1].action != "validate" || !calls[1].allowInheritance {
		t.Fatalf("targets validation = %+v, want validation-only inheritable proof", calls[1])
	}
}

func TestEnsureDirsBatchesWindowsHostStateTree(t *testing.T) {
	restoreWindowsHostStateDirHooks(t)
	windowsHostPathDetector = func(string) (bool, error) { return true, nil }

	batchCalls := 0
	var captured []windowsHostStateDirRequest
	windowsStateDirBatchGuard = func(_ context.Context, requests []windowsHostStateDirRequest) ([]windowsHostStateDirResult, error) {
		batchCalls++
		captured = append(captured, requests...)
		return make([]windowsHostStateDirResult, len(requests)), nil
	}

	state, err := Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatalf("ensure batched Windows-host-backed state tree: %v", err)
	}
	if batchCalls != 1 {
		t.Fatalf("batch guard calls = %d, want 1", batchCalls)
	}
	if len(captured) != 12 {
		t.Fatalf("batched state-directory requests = %d, want 12", len(captured))
	}
	for _, request := range captured {
		if request.Action != "create" {
			t.Fatalf("batch action = %q, want create-or-validate", request.Action)
		}
		if got, want := request.AllowTargetInheritance, request.Path == state.TargetsDir(); got != want {
			t.Fatalf("inheritance for %q = %t, want %t", request.Path, got, want)
		}
	}
}

func TestEnsurePlatformStateDirectoriesFailsClosedBeforeBatch(t *testing.T) {
	restoreWindowsHostStateDirHooks(t)
	if handled, err := ensurePlatformStateDirectories(nil, ""); handled || err != nil {
		t.Fatalf("empty state-directory set = %t, %v; want unhandled", handled, err)
	}

	windowsHostPathDetector = func(string) (bool, error) { return false, errors.New("synthetic mount inspection failure") }
	handled, err := ensurePlatformStateDirectories([]string{"/synthetic/state"}, "/synthetic/state/targets")
	if !handled || !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("mount inspection failure = %t, %v; want handled insecure-permissions error", handled, err)
	}
}

func TestLinuxPrivateDirectoryTreatsExistingWindowsHostPathAsConcurrent(t *testing.T) {
	restoreWindowsHostStateDirHooks(t)
	windowsHostPathDetector = func(string) (bool, error) { return true, nil }
	windowsStateDirGuard = func(_ context.Context, _ string, _ bool, action string) (bool, error) {
		if action != "create" {
			t.Fatalf("guard action = %q, want create", action)
		}
		return false, nil
	}

	if err := createPlatformPrivateDirectory("/mnt/c/synthetic/state", false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("create existing Windows-host-backed directory = %v, want os.ErrExist", err)
	}
}

func TestLinuxPrivateDirectoryKeepsNativePOSIXBehavior(t *testing.T) {
	restoreWindowsHostStateDirHooks(t)
	windowsHostPathDetector = func(string) (bool, error) { return false, nil }
	windowsStateDirGuard = func(context.Context, string, bool, string) (bool, error) {
		t.Fatal("native POSIX path invoked Windows guard")
		return false, nil
	}

	path := filepath.Join(t.TempDir(), "state")
	if err := createPlatformPrivateDirectory(path, false); err != nil {
		t.Fatalf("create native POSIX directory: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != DirPerm {
		t.Fatalf("native POSIX mode = %04o, want %04o", info.Mode().Perm(), DirPerm)
	}
}

func TestExistingWindowsHostStateDirectorySkipsPOSIXChmod(t *testing.T) {
	restoreWindowsHostStateDirHooks(t)
	windowsHostPathDetector = func(string) (bool, error) { return true, nil }
	windowsStateDirGuard = func(_ context.Context, _ string, _ bool, action string) (bool, error) {
		if action != "validate" {
			t.Fatalf("guard action = %q, want validation", action)
		}
		return false, nil
	}

	path := t.TempDir()
	if err := os.Chmod(path, 0777); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateExistingDir(path, info, false); err != nil {
		t.Fatalf("validate existing Windows-host-backed directory: %v", err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0777 {
		t.Fatalf("host-backed directory mode = %04o, want no POSIX chmod", info.Mode().Perm())
	}
}

func TestWindowsHostStateDirProofRejectsUnexpectedShapes(t *testing.T) {
	valid := windowsHostStateDirResult{
		Owner:       "S-1-5-21-1000",
		CurrentUser: "S-1-5-21-1000",
		Protected:   true,
		Entries: []windowsHostStateDirACE{
			{Mask: stateDirWindowsFullControl, SID: "S-1-5-21-1000"},
			{Mask: stateDirWindowsFullControl, SID: "S-1-5-18"},
			{Mask: stateDirWindowsFullControl, SID: "S-1-5-32-544"},
		},
	}
	for name, proof := range map[string]windowsHostStateDirResult{
		"foreign owner": func() windowsHostStateDirResult { result := valid; result.Owner = "S-1-5-21-2000"; return result }(),
		"unprotected":   func() windowsHostStateDirResult { result := valid; result.Protected = false; return result }(),
		"duplicate SID": func() windowsHostStateDirResult {
			result := valid
			result.Entries = append([]windowsHostStateDirACE(nil), valid.Entries...)
			result.Entries[2].SID = result.Entries[1].SID
			return result
		}(),
		"foreign SID": func() windowsHostStateDirResult {
			result := valid
			result.Entries = append([]windowsHostStateDirACE(nil), valid.Entries...)
			result.Entries[2].SID = "S-1-1-0"
			return result
		}(),
		"deny ACE": func() windowsHostStateDirResult {
			result := valid
			result.Entries = append([]windowsHostStateDirACE(nil), valid.Entries...)
			result.Entries[0].Type = 1
			return result
		}(),
		"inherited ACE": func() windowsHostStateDirResult {
			result := valid
			result.Entries = append([]windowsHostStateDirACE(nil), valid.Entries...)
			result.Entries[0].Flags = 0x10
			return result
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateWindowsHostStateDirResult(proof, false); err == nil {
				t.Fatal("invalid proof unexpectedly accepted")
			}
		})
	}

	inheritable := valid
	inheritable.Entries = append([]windowsHostStateDirACE(nil), valid.Entries...)
	for index := range inheritable.Entries {
		inheritable.Entries[index].Flags = 0x03
	}
	if err := validateWindowsHostStateDirResult(inheritable, true); err != nil {
		t.Fatalf("exact inheritable proof rejected: %v", err)
	}
	if err := validateWindowsHostStateDirResult(inheritable, false); err == nil {
		t.Fatal("ordinary state directory accepted inheritable ACEs")
	}
}

func TestWindowsHostStateDirProofDecodeFailsClosed(t *testing.T) {
	for _, output := range [][]byte{
		nil,
		[]byte(`{"unknown":true}`),
		[]byte(`{}`),
		[]byte(`{"results":[]}`),
		[]byte(`{"results":[{}]}`),
		[]byte(`{} {}`),
	} {
		if _, err := decodeWindowsHostStateDirResults(output); err == nil {
			t.Fatalf("decodeWindowsHostStateDirResults(%q) unexpectedly succeeded", output)
		}
	}
}

func TestWindowsHostStateDirGuardUsesBoundedJSONTransport(t *testing.T) {
	commandDir := t.TempDir()
	writeLinuxGuardExecutable(t, commandDir, "wslpath", "#!/bin/sh\nprintf 'C:\\\\fake\\\\state\\n'\n")
	writeLinuxGuardExecutable(t, commandDir, "powershell.exe", `#!/bin/sh
request=$(cat)
expected='{"requests":[{"path":"C:\\fake\\state","action":"validate","allow_target_inheritance":false}]}'
[ "$request" = "$expected" ] || exit 1
printf '%s' '{"results":[{"path":"C:\\fake\\state","owner":"S-1-5-21-1000","current_user":"S-1-5-21-1000","protected":true,"created":false,"entries":[{"type":0,"flags":0,"mask":2032127,"sid":"S-1-5-21-1000"},{"type":0,"flags":0,"mask":2032127,"sid":"S-1-5-18"},{"type":0,"flags":0,"mask":2032127,"sid":"S-1-5-32-544"}]}]}'
`)
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	created, err := runWindowsHostStateDirGuard(context.Background(), "/mnt/c/synthetic/state", false, "validate")
	if err != nil || created {
		t.Fatalf("runWindowsHostStateDirGuard = %t, %v; want valid existing proof", created, err)
	}

	writeLinuxGuardExecutable(t, commandDir, "powershell.exe", "#!/bin/sh\nprintf '{}\\n'\n")
	if _, err := runWindowsHostStateDirGuard(context.Background(), "/mnt/c/synthetic/state", false, "validate"); err == nil {
		t.Fatal("Windows state-directory guard accepted malformed proof")
	}

	writeLinuxGuardExecutable(t, commandDir, "wslpath", "#!/bin/sh\nexit 1\n")
	if _, err := runWindowsHostStateDirGuard(context.Background(), "/mnt/c/synthetic/state", false, "validate"); err == nil {
		t.Fatal("Windows state-directory guard accepted failed path conversion")
	}
}

func TestWindowsHostStateDirGuardScriptUsesStrictAtomicContract(t *testing.T) {
	for _, want := range []string{
		"[Console]::In.ReadToEnd()",
		"CreateDirectoryW",
		"$aceFlags = if ($allowTargetInheritance) { 'OICI' } else { '' }",
		"$_.InheritanceFlags -band 1) -ne 0) { $flags = $flags -bor 0x02",
		"$_.InheritanceFlags -band 2) -ne 0) { $flags = $flags -bor 0x01",
		"$_.PropagationFlags -band 1) -ne 0) { $flags = $flags -bor 0x04",
		"$_.PropagationFlags -band 2) -ne 0) { $flags = $flags -bor 0x08",
	} {
		if !strings.Contains(windowsHostStateDirGuardScript, want) {
			t.Fatalf("guard script does not contain %q", want)
		}
	}
	if strings.Contains(windowsHostStateDirGuardScript, "Set-Acl") {
		t.Fatal("guard script must not repair existing state by pathname")
	}
}

func restoreWindowsHostStateDirHooks(t *testing.T) {
	t.Helper()
	detector := windowsHostPathDetector
	guard := windowsStateDirGuard
	batchGuard := windowsStateDirBatchGuard
	t.Cleanup(func() {
		windowsHostPathDetector = detector
		windowsStateDirGuard = guard
		windowsStateDirBatchGuard = batchGuard
	})
}

func writeLinuxGuardExecutable(t *testing.T, directory, name, body string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
}
