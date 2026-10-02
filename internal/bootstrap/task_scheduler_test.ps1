$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-True([bool] $Value, [string] $Message) {
    if (-not $Value) { throw $Message }
}

$script:realOwnedObservation = ${function:Get-OwnedObservation}
$script:realHash = ${function:Test-Hash}
$script:useRealHash = $false
$script:denyAclPath = ''

# No scheduler objects are created: only an in-memory COM-shaped fixture is used.
$script:task = [pscustomobject]@{ Sddl = ''; Flags = 0; ReadFlags = 0; WeakReadBack = $false; SetFailure = $false }
$script:task | Add-Member ScriptMethod SetSecurityDescriptor {
    param($sddl, $flags)
    if ($this.SetFailure) { throw 'synthetic ACL write failure' }
    $this.Sddl = $sddl
    $this.Flags = $flags
}
$script:task | Add-Member ScriptMethod GetSecurityDescriptor {
    param($flags)
    $this.ReadFlags = $flags
    if ($this.WeakReadBack) { return 'D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;S-1-5-21-1000)' }
    return $this.Sddl
}
$script:folder = [pscustomobject]@{}
$script:folder | Add-Member ScriptMethod GetTask {
    param($name)
    Assert-True ($name -eq 'synthetic-task') 'wrong scheduler task requested'
    if ($script:lookupDenied) { throw [UnauthorizedAccessException]::new('synthetic lookup denied') }
    if (-not $script:registered) {
        throw [Runtime.InteropServices.COMException]::new('synthetic task absent', -2147024894)
    }
    return $script:task
}
$script:service = [pscustomobject]@{}
$script:service | Add-Member ScriptMethod Connect { }
$script:service | Add-Member ScriptMethod GetFolder {
    param($path)
    Assert-True ($path -eq '\Synthetic') 'COM folder path has invalid trailing slash or unexpected identity'
    return $script:folder
}
function New-Object([string] $ComObject) {
    Assert-True ($ComObject -eq 'Schedule.Service') 'unexpected COM object requested'
    return $script:service
}

$script:registered = $false
$script:removed = $false
$script:observations = 0
$script:existing = $false
$script:fingerprintDrift = $false
$script:rollbackDenied = $false
$script:rollbackNoOp = $false
$script:registrationMode = ''
$script:lookupDenied = $false
function Get-OwnedObservation($Spec) {
    $script:observations++
    if ($script:existing) { return [pscustomobject]@{ state = 'stopped'; exact = $true } }
    if ($script:registered) { return [pscustomobject]@{ state = 'stopped'; exact = -not $script:fingerprintDrift } }
    return [pscustomobject]@{ state = 'absent'; exact = $false }
}
function Test-PrivateAcl($LiteralPath) { return $LiteralPath -ne $script:denyAclPath }
function Set-PrivateDirectoryAcl { }
function Set-PrivateFileAcl { }
 $script:sourceHashDenied = $false
function Test-Hash($LiteralPath, $Expected) { if ($script:useRealHash) { return & $script:realHash $LiteralPath $Expected }; return -not ($script:sourceHashDenied -and $LiteralPath -eq $script:source) }
function Read-EncodedBytes { return [byte[]]@(1, 2, 3) }
function New-ScheduledTaskAction { }
function New-ScheduledTaskPrincipal($UserId, $LogonType, $RunLevel) {
    $script:principal = [pscustomobject]@{ UserId = $UserId; LogonType = $LogonType; RunLevel = $RunLevel }
}
function New-ScheduledTaskTrigger { }
function New-ScheduledTaskSettingsSet { }
function Register-ScheduledTask {
    if ($script:registrationMode -eq 'absent-error') { throw 'synthetic registration failed' }
    $script:registered = $true
    if ($script:registrationMode -ne '') { throw 'synthetic registration failed after persistence' }
}
function Unregister-ScheduledTask {
    if ($script:rollbackDenied) { throw 'synthetic unregister access denied' }
    if ($script:rollbackNoOp) { return }
    $script:removed = $true; $script:registered = $false
}

$directory = Join-Path ([IO.Path]::GetTempPath()) ('amc-synthetic-bootstrap-' + [Guid]::NewGuid().ToString('N'))
$script:source = Join-Path ([IO.Path]::GetTempPath()) ('amc-synthetic-launcher-' + [Guid]::NewGuid().ToString('N'))
[IO.File]::WriteAllBytes($source, [byte[]]@(4, 5, 6))
$spec = [pscustomobject]@{
    logon_type = 'Interactive'; user_sid = 'S-1-5-21-1000'; task_path = '\Synthetic\'; task_name = 'synthetic-task'
    wrapper_path = (Join-Path $directory 'wrapper.ps1'); metadata_path = (Join-Path $directory 'metadata.json')
    launcher_path = (Join-Path $directory 'launcher.exe'); launcher_source = $source; launcher_sha256 = 'synthetic'
    wrapper_sha256 = 'synthetic'; metadata_sha256 = 'synthetic'; account = 'synthetic-account'
    action_executable = 'synthetic.exe'; action_arguments = ''; restart_count = 3; restart_interval = 'PT1M'
}
try {
    Install-OwnedTask $spec
    Assert-True ($script:principal.UserId -eq $spec.account -and $script:principal.LogonType -eq 'Interactive' -and $script:principal.RunLevel -eq 'Limited') 'installation changed current-user Interactive Limited principal'
    Assert-True ($script:registered -and -not $script:removed) 'fresh installation did not persist'
    Assert-True (Test-OwnedTaskLifecycleAcl $script:task.Sddl $spec.user_sid) 'fresh installation omitted owner control'
    Assert-True ($script:task.Flags -eq 16 -and $script:task.ReadFlags -eq 4) 'task ACL flags changed'
    Assert-True ($script:observations -eq 2) 'task fingerprint was not rechecked before ACL write'
    Remove-Item -LiteralPath $directory -Recurse -Force

    foreach ($failure in @('write', 'read-back', 'fingerprint')) {
        $script:registered = $false; $script:removed = $false
        $script:task.Sddl = ''
        $script:task.SetFailure = $failure -eq 'write'
        $script:task.WeakReadBack = $failure -eq 'read-back'
        $script:fingerprintDrift = $failure -eq 'fingerprint'
        $failed = $false
        try { Install-OwnedTask $spec } catch { $failed = $true }
        Assert-True $failed "installation accepted $failure failure"
        Assert-True ($script:removed -and -not $script:registered) "task rollback missing after $failure failure"
        Assert-True (-not (Test-Path -LiteralPath $directory)) "artifact rollback missing after $failure failure"
        if ($failure -eq 'fingerprint') {
            Assert-True ($script:task.Sddl -eq '') 'drifted task ACL was rewritten'
        }
    }

    foreach ($rollback in @('write-denied', 'read-back-denied', 'still-present')) {
        $script:registered = $false; $script:removed = $false
        $script:fingerprintDrift = $false
        $script:task.SetFailure = $rollback -eq 'write-denied'
        $script:task.WeakReadBack = $rollback -ne 'write-denied'
        $script:rollbackDenied = $rollback -ne 'still-present'
        $script:rollbackNoOp = $rollback -eq 'still-present'
        $message = ''
        try { Install-OwnedTask $spec } catch { $message = $_.Exception.Message }
        Assert-True ($message -eq 'Scheduled task rollback failed; private bootstrap artifacts retained for recovery') 'unsafe rollback error'
        Assert-True ($script:registered -and -not $script:removed) 'failed rollback unexpectedly removed task'
        Assert-True ((Test-Path -LiteralPath $spec.wrapper_path) -and (Test-Path -LiteralPath $spec.metadata_path) -and (Test-Path -LiteralPath $spec.launcher_path)) 'failed rollback deleted task artifacts'
        Remove-Item -LiteralPath $directory -Recurse -Force
    }
    $script:rollbackDenied = $false
    $script:rollbackNoOp = $false

    foreach ($mode in @('persist-error', 'absent-error', 'unobservable-error')) {
        $script:registrationMode = $mode
        $script:registered = $false; $script:removed = $false
        $script:lookupDenied = $mode -eq 'unobservable-error'
        $message = ''
        try { Install-OwnedTask $spec } catch { $message = $_.Exception.Message }
        Assert-True (-not $script:removed) 'uncertain registration removed a task without proven ownership'
        if ($mode -eq 'absent-error') {
            Assert-True ($message -eq 'synthetic registration failed') 'confirmed absence lost registration error'
            Assert-True (-not (Test-Path -LiteralPath $directory)) 'confirmed absent registration retained artifacts'
        }
        else {
            Assert-True ($message -eq 'Scheduled task registration outcome is uncertain; private bootstrap artifacts retained for recovery') 'uncertain registration error missing'
            Assert-True ($script:registered -and (Test-Path -LiteralPath $spec.wrapper_path) -and (Test-Path -LiteralPath $spec.metadata_path) -and (Test-Path -LiteralPath $spec.launcher_path)) 'uncertain registration deleted persisted task artifacts'
            Remove-Item -LiteralPath $directory -Recurse -Force
        }
    }
    $script:registrationMode = ''
    $script:lookupDenied = $false

    $script:registered = $false; $script:removed = $false
    $script:sourceHashDenied = $true
    $message = ''
    try { Install-OwnedTask $spec } catch { $message = $_.Exception.Message }
    Assert-True ($message -eq 'Bootstrap launcher source hash does not match') 'launcher source drift was accepted'
    Assert-True (-not $script:registered -and -not (Test-Path -LiteralPath $directory)) 'source mismatch created a task or left artifacts'
    $script:sourceHashDenied = $false

    # A matching artifact from a concurrent installer is not this invocation's creation.
    New-Item -ItemType Directory -Path $directory | Out-Null
    [IO.File]::WriteAllBytes($spec.launcher_path, [byte[]]@(4, 5, 6))
    [IO.File]::WriteAllBytes($spec.wrapper_path, [byte[]]@(7, 8))
    [IO.File]::WriteAllBytes($spec.metadata_path, [byte[]]@(9, 10))
    $message = ''
    try { Install-OwnedTask $spec } catch { $message = $_.Exception.Message }
    Assert-True (-not [string]::IsNullOrEmpty($message)) 'launcher CreateNew collision was accepted'
    Assert-True ((Test-Path -LiteralPath $spec.launcher_path) -and -not $script:registered) 'matching concurrent launcher was deleted or registered'
    Assert-True ((Test-Path -LiteralPath $spec.wrapper_path) -and (Test-Path -LiteralPath $spec.metadata_path)) 'concurrent installer artifacts were deleted'
    Remove-Item -LiteralPath $directory -Recurse -Force

    $script:existing = $true
    $script:task.Sddl = ''
    $script:registered = $false; $script:removed = $false
    $failed = $false
    try { Install-OwnedTask $spec } catch { $failed = $true }
    Assert-True $failed 'existing task was adopted'
    Assert-True (-not $script:registered -and -not $script:removed -and $script:task.Sddl -eq '') 'existing task was mutated'
    Assert-True (-not (Test-Path -LiteralPath $directory)) 'existing installation created artifacts'

    # Exercise actual third-artifact observation and removal decisions, with generated files.
    $script:existing = $false; $script:registered = $true
    New-Item -ItemType Directory -Path $directory | Out-Null
    foreach ($path in @($spec.launcher_path, $spec.wrapper_path, $spec.metadata_path)) { [IO.File]::WriteAllBytes($path, [byte[]]@(1, 2, 3)) }
    $owned = $spec.PSObject.Copy()
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $owned.account = $identity.Name; $owned.user_sid = $identity.User.Value
    $owned.wrapper_sha256 = 'sha256:' + (Get-FileHash $owned.wrapper_path).Hash.ToLowerInvariant()
    $owned.metadata_sha256 = 'sha256:' + (Get-FileHash $owned.metadata_path).Hash.ToLowerInvariant()
    $owned.launcher_sha256 = 'sha256:' + (Get-FileHash $owned.launcher_path).Hash.ToLowerInvariant()
    $script:useRealHash = $true
    function Get-ScheduledTask { if ($script:registered) { return [pscustomobject]@{ State = 'Ready' } }; return $null }
    function Export-ScheduledTask { return '<synthetic />' }
    function Test-OwnedTaskFingerprint { return $true }
    function Get-OwnedObservation($Spec) { return & $script:realOwnedObservation $Spec }
    Assert-True ((Get-OwnedObservation $owned).exact) 'exact GUI artifacts were rejected'
    foreach ($drift in @('missing', 'hash', 'acl')) {
        [IO.File]::WriteAllBytes($owned.launcher_path, [byte[]]@(1, 2, 3))
        $script:denyAclPath = ''
        if ($drift -eq 'missing') { Remove-Item -LiteralPath $owned.launcher_path }
        if ($drift -eq 'hash') { [IO.File]::WriteAllBytes($owned.launcher_path, [byte[]]@(4, 5)) }
        if ($drift -eq 'acl') { $script:denyAclPath = $owned.launcher_path }
        Assert-True ((Get-OwnedObservation $owned).state -eq 'drift') "launcher $drift drift was accepted"
        $failed = $false
        try { Remove-OwnedTask $owned } catch { $failed = $true }
        Assert-True ($failed -and $script:registered) "launcher $drift drift allowed task removal"
        Assert-True ((Test-Path $owned.wrapper_path) -and (Test-Path $owned.metadata_path)) "launcher $drift drift deleted other artifacts"
    }
    $script:denyAclPath = ''
    [IO.File]::WriteAllBytes($owned.launcher_path, [byte[]]@(1, 2, 3))
    Remove-OwnedTask $owned
    Assert-True (-not $script:registered -and -not (Test-Path $directory)) 'exact GUI installation was not fully removed'

}
finally {
    Remove-Item -LiteralPath $source -Force
    if (Test-Path -LiteralPath $directory) { Remove-Item -LiteralPath $directory -Recurse -Force }
}
'bootstrap scheduler regressions: passed'
