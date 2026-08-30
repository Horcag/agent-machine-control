//go:build linux

package target

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const windowsGuardScript = `$ErrorActionPreference = 'Stop'
$path = $env:AMC_TARGET_GUARD_PATH
$kind = $env:AMC_TARGET_GUARD_KIND
$action = $env:AMC_TARGET_GUARD_ACTION
$item = Get-Item -LiteralPath $path -Force
$cursor = $item
while ($null -ne $cursor) {
  if (($cursor.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'reparse path denied' }
  $cursor = $cursor.Parent
}
if ($kind -eq 'directory' -and -not $item.PSIsContainer) { throw 'directory required' }
if (($kind -eq 'file' -or $kind -eq 'inherited_file') -and $item.PSIsContainer) { throw 'regular file required' }
if ($kind -ne 'directory' -and $kind -ne 'file' -and $kind -ne 'inherited_file') { throw 'unsupported path kind' }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$allowed = @($identity.User.Value, 'S-1-5-18', 'S-1-5-32-544')
$initialAcl = Get-Acl -LiteralPath $path
$initialOwner = (New-Object Security.Principal.NTAccount($initialAcl.Owner)).Translate([Security.Principal.SecurityIdentifier]).Value
if ($action -eq 'protect') {
  if ($initialOwner -ne $identity.User.Value) { throw 'refusing to protect foreign owner' }
  if ($kind -eq 'inherited_file') { throw 'cannot protect inherited-file proof' }
  if ($kind -eq 'directory') {
    $acl = New-Object Security.AccessControl.DirectorySecurity
    $inheritance = [Security.AccessControl.InheritanceFlags]([int][Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [int][Security.AccessControl.InheritanceFlags]::ObjectInherit)
  } else {
    $acl = New-Object Security.AccessControl.FileSecurity
    $inheritance = [Security.AccessControl.InheritanceFlags]::None
  }
  $acl.SetOwner($identity.User)
  $acl.SetAccessRuleProtection($true, $false)
  foreach ($sidText in $allowed) {
    $sid = New-Object Security.Principal.SecurityIdentifier($sidText)
    $rule = New-Object Security.AccessControl.FileSystemAccessRule($sid, [Security.AccessControl.FileSystemRights]::FullControl, $inheritance, [Security.AccessControl.PropagationFlags]::None, [Security.AccessControl.AccessControlType]::Allow)
    [void]$acl.AddAccessRule($rule)
  }
  Set-Acl -LiteralPath $path -AclObject $acl
} elseif ($action -ne 'validate') {
  throw 'unsupported guard action'
}
$acl = Get-Acl -LiteralPath $path
$owner = (New-Object Security.Principal.NTAccount($acl.Owner)).Translate([Security.Principal.SecurityIdentifier]).Value
$entries = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]) | ForEach-Object {
  $flags = [int]$_.InheritanceFlags -bor [int]$_.PropagationFlags
  if ($_.IsInherited) { $flags = $flags -bor 0x10 }
  @{
    type = [byte][int]$_.AccessControlType
    flags = [byte]$flags
    mask = [uint32]([int64]$_.FileSystemRights -band 0xffffffffL)
    sid = $_.IdentityReference.Value
  }
})
@{
  owner = $owner
  current_user = $identity.User.Value
  protected = [bool]$acl.AreAccessRulesProtected
  kind = $kind
  entries = $entries
} | ConvertTo-Json -Compress -Depth 4
`

const maxGuardOutputBytes = 4096

type powerShellWindowsGuard struct{}

func newPowerShellWindowsGuard() WindowsPathGuard { return powerShellWindowsGuard{} }

func (powerShellWindowsGuard) Validate(ctx context.Context, path string, kind PathKind) error {
	return runWindowsGuard(ctx, path, kind, "validate")
}

func (powerShellWindowsGuard) Protect(ctx context.Context, path string, kind PathKind) error {
	return runWindowsGuard(ctx, path, kind, "protect")
}

func runWindowsGuard(ctx context.Context, path string, kind PathKind, action string) error {
	bounded, cancel := boundedGuardContext(ctx)
	defer cancel()
	windowsPath, err := convertWindowsPath(bounded, path)
	if err != nil {
		return err
	}
	command := exec.CommandContext(bounded, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", windowsGuardScript)
	command.Env = append(os.Environ(),
		"AMC_TARGET_GUARD_PATH="+windowsPath,
		"AMC_TARGET_GUARD_KIND="+string(kind),
		"AMC_TARGET_GUARD_ACTION="+action,
	)
	output, err := boundedCommandOutput(command)
	if err != nil {
		return fmt.Errorf("PowerShell security guard failed: %w", err)
	}
	var proof windowsACLProof
	if err := json.Unmarshal(output, &proof); err != nil {
		return errors.New("PowerShell security guard returned invalid proof")
	}
	if proof.Kind != kind {
		return fmt.Errorf("PowerShell security guard proved %q, want %q", proof.Kind, kind)
	}
	if err := validateWindowsACLProof(proof); err != nil {
		return fmt.Errorf("PowerShell security guard rejected ACL: %w", err)
	}
	return nil
}

func boundedGuardContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= 5*time.Second {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 5*time.Second)
}

func convertWindowsPath(ctx context.Context, path string) (string, error) {
	output, err := boundedCommandOutput(exec.CommandContext(ctx, "wslpath", "-w", "--", path))
	if err != nil {
		return "", fmt.Errorf("convert host path: %w", err)
	}
	converted := strings.TrimSpace(string(output))
	if converted == "" {
		return "", errors.New("convert host path: empty result")
	}
	return converted, nil
}

type boundedOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (o *boundedOutput) Write(data []byte) (int, error) {
	remaining := maxGuardOutputBytes - o.buffer.Len()
	if remaining > 0 {
		_, _ = o.buffer.Write(data[:min(len(data), remaining)])
	}
	if len(data) > remaining {
		o.overflow = true
	}
	return len(data), nil
}

func boundedCommandOutput(command *exec.Cmd) ([]byte, error) {
	var stdout, stderr boundedOutput
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, err
	}
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("security guard output exceeded limit")
	}
	return stdout.buffer.Bytes(), nil
}

func detectWindowsHostPath(path string) (bool, error) {
	cleaned := filepath.Clean(path)
	parts := strings.FieldsFunc(filepath.ToSlash(cleaned), func(r rune) bool { return r == '/' })
	if len(parts) >= 2 && parts[0] == "mnt" && len(parts[1]) == 1 && isASCIILetter(parts[1][0]) {
		return true, nil
	}
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, nil
	}
	defer file.Close()
	bestMount := ""
	bestHostBacked := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := indexOf(fields, "-")
		if separator < 0 || separator+2 >= len(fields) || len(fields) < 5 {
			continue
		}
		mountPoint := strings.ReplaceAll(fields[4], `\040`, " ")
		if !pathWithin(cleaned, mountPoint) || len(mountPoint) <= len(bestMount) {
			continue
		}
		fsType := fields[separator+1]
		sourceAndOptions := strings.Join(fields[separator+2:], " ")
		bestMount = mountPoint
		bestHostBacked = fsType == "drvfs" || fsType == "9p" && strings.Contains(sourceAndOptions, "drvfs")
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

func isASCIILetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}
