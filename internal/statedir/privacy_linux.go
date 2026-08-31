//go:build linux

package statedir

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/wslruntime"
)

const (
	stateDirWindowsFullControl = uint32(0x001f01ff)
	maxStateDirGuardOutput     = 32 * 1024
)

var (
	windowsHostPathDetector   = wslruntime.IsWindowsHostPath
	windowsStateDirGuard      = runWindowsHostStateDirGuard
	windowsStateDirBatchGuard = runWindowsHostStateDirGuardBatch
)

func createPlatformPrivateDirectory(path string, allowTargetInheritance bool) error {
	hostBacked, err := isWindowsHostBackedStatePath(path)
	if err != nil {
		return fmt.Errorf("%w: determine state-directory filesystem: %v", ErrInsecurePermissions, err)
	}
	if !hostBacked {
		if err := os.Mkdir(path, DirPerm); err != nil {
			return err
		}
		return os.Chmod(path, DirPerm)
	}

	created, err := windowsStateDirGuard(context.Background(), path, allowTargetInheritance, "create")
	if err != nil {
		return fmt.Errorf("%w: create Windows-host-backed state directory: %v", ErrInsecurePermissions, err)
	}
	if !created {
		return os.ErrExist
	}
	return nil
}

func validatePlatformPrivateDirectory(path string, allowTargetInheritance bool) error {
	hostBacked, err := isWindowsHostBackedStatePath(path)
	if err != nil {
		return fmt.Errorf("%w: determine state-directory filesystem: %v", ErrInsecurePermissions, err)
	}
	if !hostBacked {
		return nil
	}
	_, err = windowsStateDirGuard(context.Background(), path, allowTargetInheritance, "validate")
	if err != nil {
		return fmt.Errorf("%w: validate Windows-host-backed state directory: %v", ErrInsecurePermissions, err)
	}
	return nil
}

func isWindowsHostBackedStatePath(path string) (bool, error) {
	return windowsHostPathDetector(path)
}

type windowsHostStateDirRequest struct {
	Path                   string `json:"path"`
	Action                 string `json:"action"`
	AllowTargetInheritance bool   `json:"allow_target_inheritance"`
}

type windowsHostStateDirBatchRequest struct {
	Requests []windowsHostStateDirRequest `json:"requests"`
}

type windowsHostStateDirBatchResult struct {
	Results []windowsHostStateDirResult `json:"results"`
}

type windowsHostStateDirResult struct {
	Path        string                   `json:"path"`
	Owner       string                   `json:"owner"`
	CurrentUser string                   `json:"current_user"`
	Protected   bool                     `json:"protected"`
	Created     bool                     `json:"created"`
	Entries     []windowsHostStateDirACE `json:"entries"`
}

type windowsHostStateDirACE struct {
	Type  uint8  `json:"type"`
	Flags uint8  `json:"flags"`
	Mask  uint32 `json:"mask"`
	SID   string `json:"sid"`
}

func runWindowsHostStateDirGuard(ctx context.Context, path string, allowTargetInheritance bool, action string) (bool, error) {
	if action != "create" && action != "validate" {
		return false, errors.New("unsupported Windows state-directory guard action")
	}
	results, err := runWindowsHostStateDirGuardBatch(ctx, []windowsHostStateDirRequest{{
		Path:                   path,
		Action:                 action,
		AllowTargetInheritance: allowTargetInheritance,
	}})
	if err != nil {
		return false, err
	}
	return results[0].Created, nil
}

func runWindowsHostStateDirGuardBatch(ctx context.Context, requests []windowsHostStateDirRequest) ([]windowsHostStateDirResult, error) {
	if len(requests) == 0 {
		return nil, errors.New("windows state-directory guard requires at least one request")
	}
	bounded, cancel := boundedStateDirGuardContext(ctx)
	defer cancel()

	converted := make([]windowsHostStateDirRequest, len(requests))
	for index, request := range requests {
		if request.Action != "create" && request.Action != "validate" {
			return nil, errors.New("unsupported Windows state-directory guard action")
		}
		windowsPath, err := convertStateDirWindowsPath(bounded, request.Path)
		if err != nil {
			return nil, err
		}
		request.Path = windowsPath
		converted[index] = request
	}
	request, err := json.Marshal(windowsHostStateDirBatchRequest{Requests: converted})
	if err != nil {
		return nil, fmt.Errorf("encode Windows state-directory guard request: %w", err)
	}
	command := exec.CommandContext(bounded, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", windowsHostStateDirGuardScript)
	command.Stdin = bytes.NewReader(request)
	output, err := boundedStateDirGuardOutput(command)
	if err != nil {
		return nil, fmt.Errorf("windows state-directory guard failed: %w", err)
	}
	results, err := decodeWindowsHostStateDirResults(output)
	if err != nil {
		return nil, err
	}
	if len(results) != len(converted) {
		return nil, errors.New("windows state-directory guard returned an unexpected proof count")
	}
	for index, result := range results {
		if result.Path != converted[index].Path {
			return nil, errors.New("windows state-directory guard returned proof for an unexpected path")
		}
		if err := validateWindowsHostStateDirResult(result, converted[index].AllowTargetInheritance); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func boundedStateDirGuardContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= 10*time.Second {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 10*time.Second)
}

func convertStateDirWindowsPath(ctx context.Context, path string) (string, error) {
	output, err := boundedStateDirGuardOutput(exec.CommandContext(ctx, "wslpath", "-w", "--", path))
	if err != nil {
		return "", fmt.Errorf("convert state-directory path: %w", err)
	}
	converted := strings.TrimSpace(string(output))
	if converted == "" || strings.ContainsAny(converted, "\r\n") {
		return "", errors.New("convert state-directory path: invalid result")
	}
	return converted, nil
}

type boundedStateDirOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (o *boundedStateDirOutput) Write(data []byte) (int, error) {
	remaining := maxStateDirGuardOutput - o.buffer.Len()
	if remaining > 0 {
		_, _ = o.buffer.Write(data[:min(len(data), remaining)])
	}
	if len(data) > remaining {
		o.overflow = true
	}
	return len(data), nil
}

func boundedStateDirGuardOutput(command *exec.Cmd) ([]byte, error) {
	var stdout, stderr boundedStateDirOutput
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, err
	}
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("windows state-directory guard output exceeded limit")
	}
	return stdout.buffer.Bytes(), nil
}

func decodeWindowsHostStateDirResults(output []byte) ([]windowsHostStateDirResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil {
		return nil, errors.New("windows state-directory guard returned invalid proof")
	}
	if len(fields) != 1 {
		return nil, errors.New("windows state-directory guard returned unexpected proof fields")
	}
	rawResults, ok := fields["results"]
	if !ok {
		return nil, errors.New("windows state-directory guard returned incomplete proof")
	}
	var resultFields []map[string]json.RawMessage
	if err := json.Unmarshal(rawResults, &resultFields); err != nil || len(resultFields) == 0 {
		return nil, errors.New("windows state-directory guard returned invalid proof results")
	}
	for _, result := range resultFields {
		if len(result) != 6 {
			return nil, errors.New("windows state-directory guard returned unexpected result fields")
		}
		for _, name := range []string{"path", "owner", "current_user", "protected", "created", "entries"} {
			if _, ok := result[name]; !ok {
				return nil, errors.New("windows state-directory guard returned incomplete result")
			}
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var result windowsHostStateDirBatchResult
	if err := decoder.Decode(&result); err != nil {
		return nil, errors.New("windows state-directory guard returned invalid proof")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("windows state-directory guard returned trailing data")
	}
	return result.Results, nil
}

func validateWindowsHostStateDirResult(result windowsHostStateDirResult, allowTargetInheritance bool) error {
	if result.CurrentUser == "" || result.Owner != result.CurrentUser || !result.Protected {
		return errors.New("windows state-directory proof has an unexpected owner or DACL protection")
	}
	expectedFlags := uint8(0)
	if allowTargetInheritance {
		expectedFlags = 0x03
	}
	allowed := distinctWindowsHostStateDirSIDs(result.CurrentUser)
	if len(result.Entries) != len(allowed) {
		return errors.New("windows state-directory proof has an unexpected ACE count")
	}
	want := make(map[string]struct{}, len(allowed))
	for _, sid := range allowed {
		want[sid] = struct{}{}
	}
	for _, entry := range result.Entries {
		if entry.Type != 0 || entry.Flags != expectedFlags || entry.Mask != stateDirWindowsFullControl {
			return errors.New("windows state-directory proof has an unexpected ACE")
		}
		if _, ok := want[entry.SID]; !ok {
			return errors.New("windows state-directory proof has an unexpected or duplicate SID")
		}
		delete(want, entry.SID)
	}
	return nil
}

func distinctWindowsHostStateDirSIDs(currentUser string) []string {
	candidates := [...]string{currentUser, "S-1-5-18", "S-1-5-32-544"}
	allowed := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, sid := range candidates {
		if _, duplicate := seen[sid]; !duplicate {
			seen[sid] = struct{}{}
			allowed = append(allowed, sid)
		}
	}
	return allowed
}
