//go:build linux

package statedir

const windowsHostStateDirGuardScript = `$ErrorActionPreference = 'Stop'
$requestJSON = [Console]::In.ReadToEnd()
if ([string]::IsNullOrWhiteSpace($requestJSON)) { throw 'state-directory guard request is required' }
try {
  $request = $requestJSON | ConvertFrom-Json -ErrorAction Stop
} catch {
  throw 'invalid state-directory guard request'
}
$properties = @($request.PSObject.Properties)
if ($null -eq $request -or $request -is [Array] -or $properties.Count -ne 1 -or $properties[0].Name -ne 'requests') { throw 'invalid state-directory guard request' }
$requests = @($request.requests)
if ($requests.Count -eq 0 -or $requests.Count -gt 64) { throw 'invalid state-directory guard request count' }

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$allowed = @($identity.User.Value, 'S-1-5-18', 'S-1-5-32-544') | Select-Object -Unique
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class AmcStateDirectory {
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
  public static extern bool ConvertStringSecurityDescriptorToSecurityDescriptor(string sddl, uint revision, out IntPtr descriptor, out uint size);
  [DllImport("kernel32.dll", SetLastError = true, CharSet = CharSet.Unicode)]
  [return: MarshalAs(UnmanagedType.Bool)]
  public static extern bool CreateDirectoryW(string path, ref SECURITY_ATTRIBUTES attributes);
  [DllImport("kernel32.dll", SetLastError = true)]
  public static extern IntPtr LocalFree(IntPtr memory);
}
'@

$results = @()
foreach ($entry in $requests) {
  $entryProperties = @($entry.PSObject.Properties)
  if ($null -eq $entry -or $entry -is [Array] -or $entryProperties.Count -ne 3) { throw 'invalid state-directory guard entry' }
  if ((@($entryProperties.Name | Sort-Object) -join ',') -ne 'action,allow_target_inheritance,path') { throw 'invalid state-directory guard entry' }
  $path = $entry.path
  $action = $entry.action
  $allowTargetInheritance = $entry.allow_target_inheritance
  if ($path -isnot [string] -or [string]::IsNullOrWhiteSpace($path) -or $action -isnot [string] -or ($action -ne 'create' -and $action -ne 'validate') -or $allowTargetInheritance -isnot [bool]) { throw 'invalid state-directory guard entry' }

  $created = $false
  if ($action -eq 'create') {
    $parentPath = [IO.Path]::GetDirectoryName($path)
    if ([string]::IsNullOrWhiteSpace($parentPath)) { throw 'state-directory parent is unavailable' }
    $parent = Get-Item -LiteralPath $parentPath -Force
    for ($cursor = $parent; $null -ne $cursor; $cursor = $cursor.Parent) {
      if (($cursor.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'reparse path denied' }
    }
    $aceFlags = if ($allowTargetInheritance) { 'OICI' } else { '' }
    $sddl = 'O:' + $identity.User.Value + 'D:P' + (($allowed | ForEach-Object { '(A;' + $aceFlags + ';FA;;;' + $_ + ')' }) -join '')
    [IntPtr]$descriptor = [IntPtr]::Zero
    [uint32]$descriptorSize = 0
    if (-not [AmcStateDirectory]::ConvertStringSecurityDescriptorToSecurityDescriptor($sddl, 1, [ref]$descriptor, [ref]$descriptorSize)) { throw ('security descriptor conversion failed: ' + [Runtime.InteropServices.Marshal]::GetLastWin32Error()) }
    try {
      $attributes = New-Object AmcStateDirectory+SECURITY_ATTRIBUTES
      $attributes.nLength = [AmcStateDirectory]::SecurityAttributesSize()
      $attributes.lpSecurityDescriptor = $descriptor
      if ([AmcStateDirectory]::CreateDirectoryW($path, [ref]$attributes)) {
        $created = $true
      } elseif ([Runtime.InteropServices.Marshal]::GetLastWin32Error() -ne 183) {
        throw ('CreateDirectoryW failed: ' + [Runtime.InteropServices.Marshal]::GetLastWin32Error())
      }
    } finally {
      if ($descriptor -ne [IntPtr]::Zero) { [void][AmcStateDirectory]::LocalFree($descriptor) }
    }
  }

  $item = Get-Item -LiteralPath $path -Force
  for ($cursor = $item; $null -ne $cursor; $cursor = $cursor.Parent) {
    if (($cursor.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'reparse path denied' }
  }
  if (-not $item.PSIsContainer) { throw 'state directory required' }
  $acl = Get-Acl -LiteralPath $path
  $owner = (New-Object Security.Principal.NTAccount($acl.Owner)).Translate([Security.Principal.SecurityIdentifier]).Value
  $entries = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]) | ForEach-Object {
    $flags = 0
    if (([int]$_.InheritanceFlags -band 1) -ne 0) { $flags = $flags -bor 0x02 }
    if (([int]$_.InheritanceFlags -band 2) -ne 0) { $flags = $flags -bor 0x01 }
    if (([int]$_.PropagationFlags -band 1) -ne 0) { $flags = $flags -bor 0x04 }
    if (([int]$_.PropagationFlags -band 2) -ne 0) { $flags = $flags -bor 0x08 }
    if ($_.IsInherited) { $flags = $flags -bor 0x10 }
    @{ type = [byte][int]$_.AccessControlType; flags = [byte]$flags; mask = [uint32]([int64]$_.FileSystemRights -band 0xffffffffL); sid = $_.IdentityReference.Value }
  })
  $results += @{ path = $path; owner = $owner; current_user = $identity.User.Value; protected = [bool]$acl.AreAccessRulesProtected; created = $created; entries = $entries }
}
@{ results = @($results) } | ConvertTo-Json -Compress -Depth 5
`
