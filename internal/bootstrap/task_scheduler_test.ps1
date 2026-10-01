$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-True([bool] $Value, [string] $Message) {
    if (-not $Value) { throw $Message }
}

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
function Test-PrivateAcl { return $true }
function Set-PrivateDirectoryAcl { }
function Set-PrivateFileAcl { }
function Test-Hash { return $true }
function Read-EncodedBytes { return [byte[]]@(1, 2, 3) }
function New-ScheduledTaskAction { }
function New-ScheduledTaskPrincipal { }
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
$spec = [pscustomobject]@{
    user_sid = 'S-1-5-21-1000'; task_path = '\Synthetic\'; task_name = 'synthetic-task'
    wrapper_path = (Join-Path $directory 'wrapper.ps1'); metadata_path = (Join-Path $directory 'metadata.json')
    wrapper_sha256 = 'synthetic'; metadata_sha256 = 'synthetic'; account = 'synthetic-account'
    action_executable = 'synthetic.exe'; action_arguments = ''; restart_count = 3; restart_interval = 'PT1M'
}
try {
    Install-OwnedTask $spec
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
        Assert-True ((Test-Path -LiteralPath $spec.wrapper_path) -and (Test-Path -LiteralPath $spec.metadata_path)) 'failed rollback deleted task artifacts'
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
            Assert-True ($script:registered -and (Test-Path -LiteralPath $spec.wrapper_path) -and (Test-Path -LiteralPath $spec.metadata_path)) 'uncertain registration deleted persisted task artifacts'
            Remove-Item -LiteralPath $directory -Recurse -Force
        }
    }
    $script:registrationMode = ''
    $script:lookupDenied = $false

    $script:existing = $true
    $script:task.Sddl = ''
    $script:registered = $false; $script:removed = $false
    $failed = $false
    try { Install-OwnedTask $spec } catch { $failed = $true }
    Assert-True $failed 'existing task was adopted'
    Assert-True (-not $script:registered -and -not $script:removed -and $script:task.Sddl -eq '') 'existing task was mutated'
    Assert-True (-not (Test-Path -LiteralPath $directory)) 'existing installation created artifacts'
}
finally {
    if (Test-Path -LiteralPath $directory) { Remove-Item -LiteralPath $directory -Recurse -Force }
}
'bootstrap scheduler regressions: passed'
