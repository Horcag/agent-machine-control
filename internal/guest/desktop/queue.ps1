$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$sid = $identity.User.Value
$root = Join-Path $env:LOCALAPPDATA 'AMC-Desktop-v1'
$taskName = 'AMC-Desktop-v1-' + $sid
$powerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$taskArguments = '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -STA -WindowStyle Hidden -File "' + (Join-Path $root 'server.ps1') + '"'

# Pass the complete descriptor to Win32 creation: .NET ACL constructors omit labels.
# This helper contains filesystem security APIs only, never host or guest UI APIs.
if (-not ('AMCDesktopSecurity' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
public static class AMCDesktopSecurity {
    [StructLayout(LayoutKind.Sequential)]
    private struct Attributes {
        public int Length;
        public IntPtr Descriptor;
        [MarshalAs(UnmanagedType.Bool)] public bool Inherit;
    }
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    private static extern bool ConvertStringSecurityDescriptorToSecurityDescriptor(string value, uint revision, out IntPtr descriptor, out uint size);
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    private static extern bool ConvertSecurityDescriptorToStringSecurityDescriptor(IntPtr descriptor, uint revision, uint information, out IntPtr text, out uint size);
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode)]
    private static extern uint GetNamedSecurityInfo(string path, uint type, uint information, out IntPtr owner, out IntPtr group, out IntPtr dacl, out IntPtr sacl, out IntPtr descriptor);
    [DllImport("kernel32.dll")] public static extern uint WTSGetActiveConsoleSessionId();
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    private static extern bool CreateDirectory(string path, ref Attributes attributes);
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    private static extern SafeFileHandle CreateFile(string path, uint access, uint share, ref Attributes attributes, uint creation, uint flags, IntPtr template);
    [DllImport("kernel32.dll")] private static extern IntPtr LocalFree(IntPtr value);
    private static Attributes Parse(string sddl) {
        IntPtr descriptor; uint size;
        if (!ConvertStringSecurityDescriptorToSecurityDescriptor(sddl, 1, out descriptor, out size)) throw new Win32Exception(Marshal.GetLastWin32Error());
        return new Attributes {Length=Marshal.SizeOf(typeof(Attributes)), Descriptor=descriptor, Inherit=false};
    }
    public static void NewDirectory(string path, string sddl) {
        Attributes attributes=Parse(sddl);
        try {
            if (!CreateDirectory(path, ref attributes)) throw new Win32Exception(Marshal.GetLastWin32Error());
        } finally { LocalFree(attributes.Descriptor); }
    }
    public static void NewFile(string path, string sddl, byte[] data) {
        Attributes attributes=Parse(sddl);
        try {
            using (SafeFileHandle handle=CreateFile(path, 0x40000000, 0, ref attributes, 1, 0x80, IntPtr.Zero)) {
                if (handle.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error());
                if (!Label(path).Contains(";NW;;;HI)")) throw new UnauthorizedAccessException("unsafe_integrity");
                using (FileStream file=new FileStream(handle, FileAccess.Write)) file.Write(data, 0, data.Length);
            }
        } finally { LocalFree(attributes.Descriptor); }
    }
    public static string Label(string path) {
        IntPtr owner, group, dacl, sacl, descriptor, text=IntPtr.Zero; uint size;
        // LABEL_SECURITY_INFORMATION reads only MIC, without the audit-SACL privilege.
        uint error=GetNamedSecurityInfo(path, 1, 0x10, out owner, out group, out dacl, out sacl, out descriptor);
        if (error!=0) throw new Win32Exception((int)error);
        try {
            if (!ConvertSecurityDescriptorToStringSecurityDescriptor(descriptor, 1, 0x10, out text, out size)) throw new Win32Exception(Marshal.GetLastWin32Error());
            return Marshal.PtrToStringUni(text);
        } finally { if (text!=IntPtr.Zero) LocalFree(text); LocalFree(descriptor); }
    }
}
'@
}

function Assert-PrivatePath([string]$path, [bool]$directory) {
    $item = Get-Item -LiteralPath $path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or $item.PSIsContainer -ne $directory) { throw 'unsafe_path' }
    $acl = Get-Acl -LiteralPath $path
    if (-not $acl.AreAccessRulesProtected) { throw 'unsafe_acl' }
    $owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
    if ($owner -ne $sid) { throw 'foreign_owner' }
    foreach ($rule in $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])) {
        if ($rule.AccessControlType -ne 'Allow' -or $rule.IdentityReference.Value -notin @($sid, 'S-1-5-18') -or $rule.IsInherited) { throw 'unsafe_acl' }
    }
    # High mandatory integrity prevents medium-integrity callers writing a Highest task queue.
    $sddl = [AMCDesktopSecurity]::Label($path)
    if ($sddl -notmatch '\(ML;[^;]*;NW;;;HI\)') { throw 'unsafe_integrity' }
}

function New-PrivateDirectory([string]$path) {
    if (Test-Path -LiteralPath $path) { Assert-PrivatePath $path $true; return }
    $sddl = 'O:' + $sid + 'G:' + $sid + 'D:P(A;OICI;FA;;;' + $sid + ')(A;OICI;FA;;;SY)S:(ML;OICI;NW;;;HI)'
    [AMCDesktopSecurity]::NewDirectory($path, $sddl)
    Assert-PrivatePath $path $true
}

function Write-PrivateFile([string]$path, [byte[]]$data) {
    $sddl = 'O:' + $sid + 'G:' + $sid + 'D:P(A;;FA;;;' + $sid + ')(A;;FA;;;SY)S:(ML;;NW;;;HI)'
    [AMCDesktopSecurity]::NewFile($path, $sddl, $data)
    Assert-PrivatePath $path $false
}

function Assert-Installed {
    Assert-PrivatePath $root $true
    foreach ($dir in @('requests', 'results')) { Assert-PrivatePath (Join-Path $root $dir) $true }
    $manifestPath = Join-Path $root 'manifest.json'
    Assert-PrivatePath $manifestPath $false
    $manifest = [IO.File]::ReadAllText($manifestPath) | ConvertFrom-Json
    if ($manifest.version -ne 1 -or $manifest.sid -ne $sid) { throw 'foreign_installation' }
    foreach ($name in @('queue.ps1', 'server.ps1', 'worker.ps1', 'actions.ps1', 'native.cs')) {
        $path = Join-Path $root $name
        Assert-PrivatePath $path $false
        if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $manifest.hashes.$name) { throw 'changed_installation' }
    }
    $task = Get-ScheduledTask -TaskName $taskName -TaskPath '\' -ErrorAction Stop
    try {
        $taskIdentity = [string]$task.Principal.UserId
        if ($taskIdentity -match '^S-1-') {
            $taskSid = [Security.Principal.SecurityIdentifier]::new($taskIdentity)
        } else {
            $taskSid = [Security.Principal.NTAccount]::new($taskIdentity).Translate([Security.Principal.SecurityIdentifier])
        }
    } catch { throw 'foreign_task' }
    if (-not $taskSid.Equals($identity.User) -or $task.Principal.LogonType -ne 'Interactive' -or $task.Principal.RunLevel -ne 'Highest' -or @($task.Actions).Count -ne 1 -or $task.Actions[0].Execute -ne $powerShell -or $task.Actions[0].Arguments -ne $taskArguments) { throw 'foreign_task' }
}

function Assert-Request($request) {
    if ($request.request_id -cnotmatch '^[0-9a-f]{32}$') { throw 'invalid_request' }
    $deadline = [DateTimeOffset]::Parse($request.deadline)
    $now = [DateTimeOffset]::UtcNow
    if ($deadline -le $now -or $deadline -gt $now.AddSeconds(35)) { throw 'expired_request' }
    return $deadline
}

function Assert-Deadline([DateTimeOffset]$deadline) {
    if ([DateTimeOffset]::UtcNow -ge $deadline) { throw 'expired_request' }
}

function Write-Response($response) {
    [Console]::Out.Write(($response | ConvertTo-Json -Compress -Depth 12))
}

function Get-WorkerArguments([string]$id) {
    return '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -STA -WindowStyle Hidden -File "' + (Join-Path $root 'worker.ps1') + '" -RequestID ' + $id
}

function Get-LiveRequestIDs {
    $live = @{}
    $processes = @(Get-CimInstance -ClassName Win32_Process -Filter "Name='powershell.exe'" -OperationTimeoutSec 2 | Select-Object -First 129)
    if ($processes.Count -gt 128) { throw 'worker_inventory_unproven' }
    foreach ($process in $processes) {
        if ($process.CommandLine -cnotmatch ' ([0-9a-f]{32})$') { continue }
        $id = $Matches[1]
        if (-not $process.CommandLine.EndsWith((Get-WorkerArguments $id), [StringComparison]::Ordinal)) { continue }
        if (-not [string]::Equals($process.ExecutablePath, $powerShell, [StringComparison]::OrdinalIgnoreCase)) { throw 'worker_identity_unproven' }
        $owner = Invoke-CimMethod -InputObject $process -MethodName GetOwnerSid -OperationTimeoutSec 2
        if ($owner.ReturnValue -ne 0 -or $owner.Sid -ne $sid) { throw 'worker_owner_unproven' }
        # Process inspection is observational. Never stop a process discovered by name.
        $live[$id] = $true
    }
    return $live
}

function Remove-ExpiredQueueFiles {
    Assert-PrivatePath $root $true
    $live = Get-LiveRequestIDs
    $cutoff = [DateTime]::UtcNow.AddMinutes(-2)
    foreach ($kind in @('requests', 'results')) {
        $directory = Join-Path $root $kind
        Assert-PrivatePath $directory $true
        foreach ($entry in @(Get-ChildItem -LiteralPath $directory -Force | Select-Object -First 256)) {
            $pattern = '^([0-9a-f]{32})\.json(?:\.tmp|\.work)?$'
            if ($kind -eq 'results') { $pattern = '^([0-9a-f]{32})\.json(?:\.tmp)?$' }
            if ($entry.Name -cnotmatch $pattern) { continue }
            $id = $Matches[1]
            if ($live.ContainsKey($id) -or $entry.LastWriteTimeUtc -ge $cutoff) { continue }
            try { Assert-PrivatePath $entry.FullName $false } catch { continue }
            # Open exclusively before deletion: an in-flight writer keeps its file.
            try {
                $file = [IO.File]::Open($entry.FullName, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::None)
                $file.Dispose()
            } catch { continue }
            try { Remove-Item -LiteralPath $entry.FullName -Force } catch { continue }
        }
    }
}

function Assert-ConsoleSession {
    $session = [Diagnostics.Process]::GetCurrentProcess().SessionId
    $console = [AMCDesktopSecurity]::WTSGetActiveConsoleSessionId()
    if ($session -eq 0 -or $console -eq [uint32]::MaxValue -or $session -ne $console) { throw 'invalid_console_session' }
}
