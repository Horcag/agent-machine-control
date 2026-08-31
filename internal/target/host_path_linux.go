//go:build linux

package target

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/wslruntime"
)

const windowsGuardScript = `$ErrorActionPreference = 'Stop'
$requestJSON = [Console]::In.ReadToEnd()
if ([string]::IsNullOrWhiteSpace($requestJSON)) { throw 'guard request is required' }
try {
  $request = $requestJSON | ConvertFrom-Json -ErrorAction Stop
} catch {
  throw 'invalid guard request'
}
if ($null -eq $request -or $request -is [Array] -or @($request.PSObject.Properties).Count -ne 3) {
  throw 'invalid guard request'
}
$path = [string]$request.path
$kind = [string]$request.kind
$action = [string]$request.action
if ([string]::IsNullOrWhiteSpace($path) -or [string]::IsNullOrWhiteSpace($kind) -or [string]::IsNullOrWhiteSpace($action)) {
  throw 'invalid guard request'
}
$created = $false
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$allowed = @($identity.User.Value, 'S-1-5-18', 'S-1-5-32-544') | Select-Object -Unique
if ($action -eq 'create-private-directory') {
  if ($kind -ne 'directory') { throw 'private directory creation requires directory kind' }
  $parentPath = [IO.Path]::GetDirectoryName($path)
  if ([string]::IsNullOrWhiteSpace($parentPath)) { throw 'directory parent is unavailable' }
  $parent = Get-Item -LiteralPath $parentPath -Force
  $cursor = $parent
  while ($null -ne $cursor) {
    if (($cursor.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'reparse path denied' }
    $cursor = $cursor.Parent
  }
  Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class AmcNativeDirectory {
  [StructLayout(LayoutKind.Sequential)]
  public struct SECURITY_ATTRIBUTES {
    public uint nLength;
    public IntPtr lpSecurityDescriptor;
    [MarshalAs(UnmanagedType.Bool)] public bool bInheritHandle;
  }
  public static uint SecurityAttributesSize() {
    return (uint)Marshal.SizeOf(typeof(SECURITY_ATTRIBUTES));
  }
  [DllImport("advapi32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
  [return: MarshalAs(UnmanagedType.Bool)]
  public static extern bool ConvertStringSecurityDescriptorToSecurityDescriptor(
    string sddl, uint revision, out IntPtr descriptor, out uint size);
  [DllImport("kernel32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
  [return: MarshalAs(UnmanagedType.Bool)]
  public static extern bool CreateDirectoryW(string path, ref SECURITY_ATTRIBUTES attributes);
  [DllImport("kernel32.dll", SetLastError = true)]
  public static extern IntPtr LocalFree(IntPtr memory);
}
'@
  $sddl = 'O:' + $identity.User.Value + 'D:P' + (($allowed | ForEach-Object { '(A;OICI;FA;;;' + $_ + ')' }) -join '')
  [IntPtr]$descriptor = [IntPtr]::Zero
  [uint32]$descriptorSize = 0
  if (-not [AmcNativeDirectory]::ConvertStringSecurityDescriptorToSecurityDescriptor($sddl, 1, [ref]$descriptor, [ref]$descriptorSize)) {
    throw ('security descriptor conversion failed: ' + [Runtime.InteropServices.Marshal]::GetLastWin32Error())
  }
  try {
    $attributes = New-Object AmcNativeDirectory+SECURITY_ATTRIBUTES
    $attributes.nLength = [AmcNativeDirectory]::SecurityAttributesSize()
    $attributes.lpSecurityDescriptor = $descriptor
    if ([AmcNativeDirectory]::CreateDirectoryW($path, [ref]$attributes)) {
      $created = $true
    } elseif ([Runtime.InteropServices.Marshal]::GetLastWin32Error() -ne 183) {
      throw ('CreateDirectoryW failed: ' + [Runtime.InteropServices.Marshal]::GetLastWin32Error())
    }
  } finally {
    if ($descriptor -ne [IntPtr]::Zero) { [void][AmcNativeDirectory]::LocalFree($descriptor) }
  }
} elseif ($action -ne 'validate' -and $action -ne 'protect' -and $action -ne 'protect-new') {
  throw 'unsupported guard action'
}
$item = Get-Item -LiteralPath $path -Force
$cursor = $item
while ($null -ne $cursor) {
  if (($cursor.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'reparse path denied' }
  $cursor = $cursor.Parent
}
if ($kind -eq 'directory' -and -not $item.PSIsContainer) { throw 'directory required' }
if (($kind -eq 'file' -or $kind -eq 'inherited_file') -and $item.PSIsContainer) { throw 'regular file required' }
if ($kind -ne 'directory' -and $kind -ne 'file' -and $kind -ne 'inherited_file') { throw 'unsupported path kind' }
$initialAcl = Get-Acl -LiteralPath $path
$initialOwner = (New-Object Security.Principal.NTAccount($initialAcl.Owner)).Translate([Security.Principal.SecurityIdentifier]).Value
if ($action -eq 'protect' -or $action -eq 'protect-new') {
  if ($action -eq 'protect' -and $initialOwner -ne $identity.User.Value) { throw 'refusing to protect foreign owner' }
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
} elseif ($action -ne 'validate' -and $action -ne 'create-private-directory') {
	throw 'unsupported guard action'
}
$acl = Get-Acl -LiteralPath $path
$owner = (New-Object Security.Principal.NTAccount($acl.Owner)).Translate([Security.Principal.SecurityIdentifier]).Value
$entries = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]) | ForEach-Object {
  $flags = 0
  if (([int]$_.InheritanceFlags -band 1) -ne 0) { $flags = $flags -bor 0x02 }
  if (([int]$_.InheritanceFlags -band 2) -ne 0) { $flags = $flags -bor 0x01 }
  if (([int]$_.PropagationFlags -band 1) -ne 0) { $flags = $flags -bor 0x04 }
  if (([int]$_.PropagationFlags -band 2) -ne 0) { $flags = $flags -bor 0x08 }
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
  created = $created
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

func (powerShellWindowsGuard) ProtectNew(ctx context.Context, path string, kind PathKind) error {
	return runWindowsGuard(ctx, path, kind, "protect-new")
}

func (powerShellWindowsGuard) CreatePrivateDirectory(ctx context.Context, path string) (bool, error) {
	return runWindowsGuardCreatePrivateDirectory(ctx, path)
}

func runWindowsGuard(ctx context.Context, path string, kind PathKind, action string) error {
	_, err := runWindowsGuardResult(ctx, path, kind, action)
	return err
}

func runWindowsGuardCreatePrivateDirectory(ctx context.Context, path string) (bool, error) {
	result, err := runWindowsGuardResult(ctx, path, PathDirectory, "create-private-directory")
	if err != nil {
		return false, err
	}
	return result.Created, nil
}

type windowsGuardResult struct {
	windowsACLProof
	Created bool `json:"created"`
}

type windowsGuardRequest struct {
	Path   string   `json:"path"`
	Kind   PathKind `json:"kind"`
	Action string   `json:"action"`
}

func runWindowsGuardResult(ctx context.Context, path string, kind PathKind, action string) (windowsGuardResult, error) {
	bounded, cancel := boundedGuardContext(ctx)
	defer cancel()
	windowsPath, err := convertWindowsPath(bounded, path)
	if err != nil {
		return windowsGuardResult{}, err
	}
	request, err := json.Marshal(windowsGuardRequest{Path: windowsPath, Kind: kind, Action: action})
	if err != nil {
		return windowsGuardResult{}, fmt.Errorf("encode PowerShell security guard request: %w", err)
	}
	return runWindowsGuardRequest(bounded, request, kind)
}

func runWindowsGuardRequest(ctx context.Context, request []byte, kind PathKind) (windowsGuardResult, error) {
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", windowsGuardScript)
	command.Stdin = bytes.NewReader(request)
	output, err := boundedCommandOutput(command)
	if err != nil {
		return windowsGuardResult{}, fmt.Errorf("PowerShell security guard failed: %w", err)
	}
	var result windowsGuardResult
	if err := json.Unmarshal(output, &result); err != nil {
		return windowsGuardResult{}, errors.New("PowerShell security guard returned invalid proof")
	}
	if result.Kind != kind {
		return windowsGuardResult{}, fmt.Errorf("PowerShell security guard proved %q, want %q", result.Kind, kind)
	}
	if err := validateWindowsACLProof(result.windowsACLProof); err != nil {
		return windowsGuardResult{}, fmt.Errorf("PowerShell security guard rejected ACL: %w", err)
	}
	return result, nil
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
	return wslruntime.IsWindowsHostPath(path)
}
