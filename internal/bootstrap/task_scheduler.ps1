$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Read-EncodedJson([string] $Name) {
    $encoded = [Environment]::GetEnvironmentVariable($Name)
    if ([string]::IsNullOrWhiteSpace($encoded)) {
        throw "Missing encoded bootstrap input"
    }
    $json = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($encoded))
    return ($json | ConvertFrom-Json)
}

function Read-EncodedBytes([string] $Name) {
    $encoded = [Environment]::GetEnvironmentVariable($Name)
    if ([string]::IsNullOrWhiteSpace($encoded)) {
        throw "Missing encoded bootstrap bytes"
    }
    return [Convert]::FromBase64String($encoded)
}

function Test-PrivateAcl([string] $LiteralPath, [string] $OwnerSid, [bool] $Directory) {
    $item = Get-Item -LiteralPath $LiteralPath -Force
    $acl = Get-Acl -LiteralPath $LiteralPath
    $kind = if ($item -is [IO.DirectoryInfo]) { 'directory' } elseif ($item -is [IO.FileInfo]) { 'file' } else { 'other' }
    return Test-PrivateAclFingerprint $kind $item.Attributes $acl $OwnerSid $Directory
}

function Set-PrivateDirectoryAcl([string] $LiteralPath, [string] $OwnerSid) {
    $sid = [Security.Principal.SecurityIdentifier]::new($OwnerSid)
    $system = [Security.Principal.SecurityIdentifier]::new('S-1-5-18')
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetOwner($sid)
    $acl.SetAccessRuleProtection($true, $false)
    $inherit = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    $propagation = [Security.AccessControl.PropagationFlags]::None
    $type = [Security.AccessControl.AccessControlType]::Allow
    $rights = [Security.AccessControl.FileSystemRights]::FullControl
    $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($sid, $rights, $inherit, $propagation, $type))
    $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($system, $rights, $inherit, $propagation, $type))
    Set-Acl -LiteralPath $LiteralPath -AclObject $acl
}

function Set-PrivateFileAcl([string] $LiteralPath, [string] $OwnerSid) {
    $sid = [Security.Principal.SecurityIdentifier]::new($OwnerSid)
    $system = [Security.Principal.SecurityIdentifier]::new('S-1-5-18')
    $acl = [Security.AccessControl.FileSecurity]::new()
    $acl.SetOwner($sid)
    $acl.SetAccessRuleProtection($true, $false)
    $type = [Security.AccessControl.AccessControlType]::Allow
    $rights = [Security.AccessControl.FileSystemRights]::FullControl
    $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($sid, $rights, $type))
    $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($system, $rights, $type))
    Set-Acl -LiteralPath $LiteralPath -AclObject $acl
}

function Test-Hash([string] $LiteralPath, [string] $Expected) {
    if (-not (Test-Path -LiteralPath $LiteralPath -PathType Leaf)) {
        return $false
    }
    $stream = $null
    $sha256 = $null
    try {
        $sha256 = [Security.Cryptography.SHA256]::Create()
        $stream = [IO.File]::OpenRead($LiteralPath)
        $digest = $sha256.ComputeHash($stream)
        $actual = 'sha256:' + [BitConverter]::ToString($digest).Replace('-', '').ToLowerInvariant()
    }
    finally {
        if ($null -ne $sha256) { $sha256.Dispose() }
        if ($null -ne $stream) { $stream.Dispose() }
    }
    return $actual -ceq $Expected
}

function Test-HasLauncher($Spec) {
    return (Test-HasProperty $Spec 'launcher_path') -and -not [string]::IsNullOrEmpty([string] $Spec.launcher_path)
}

function Test-OwnedLauncher($Spec) {
    if (-not (Test-HasLauncher $Spec)) { return $true }
    return (Test-PrivateAcl $Spec.launcher_path $Spec.user_sid $false) -and (Test-Hash $Spec.launcher_path $Spec.launcher_sha256)
}

function Write-OwnedLauncher($Spec, [ref] $Created) {
    if (-not (Test-HasLauncher $Spec)) { return }
    if (-not (Test-Hash $Spec.launcher_source $Spec.launcher_sha256)) {
        throw 'Bootstrap launcher source hash does not match'
    }
    $bytes = [IO.File]::ReadAllBytes($Spec.launcher_source)
    $stream = [IO.File]::Open($Spec.launcher_path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    $Created.Value = $true
    try { $stream.Write($bytes, 0, $bytes.Length) } finally { $stream.Dispose() }
    Set-PrivateFileAcl $Spec.launcher_path $Spec.user_sid
    if (-not (Test-OwnedLauncher $Spec)) { throw 'Bootstrap launcher verification failed' }
}

function Get-OwnedObservation($Spec) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    if ($identity.User.Value -ne $Spec.user_sid -or $identity.Name -ne $Spec.account) {
        return [pscustomobject]@{ state = 'drift'; reason = 'current Windows identity does not match metadata'; exact = $false; task_running = $false }
    }

    $task = Get-ScheduledTask -TaskPath $Spec.task_path -TaskName $Spec.task_name -ErrorAction SilentlyContinue
    $wrapperExists = Test-Path -LiteralPath $Spec.wrapper_path -PathType Leaf
    $metadataExists = Test-Path -LiteralPath $Spec.metadata_path -PathType Leaf
    $launcherExists = (Test-HasLauncher $Spec) -and (Test-Path -LiteralPath $Spec.launcher_path)
    $directory = Split-Path -Parent $Spec.wrapper_path
    $directoryExists = Test-Path -LiteralPath $directory -PathType Container
    if ($null -eq $task -and -not $wrapperExists -and -not $metadataExists -and -not $launcherExists) {
        if ($directoryExists -and ((-not (Test-PrivateAcl $directory $Spec.user_sid $true)) -or @((Get-ChildItem -LiteralPath $directory -Force)).Count -ne 0)) {
            return [pscustomobject]@{ state = 'drift'; reason = 'bootstrap directory is not empty private owned state'; exact = $false; task_running = $false }
        }
        return [pscustomobject]@{ state = 'absent'; reason = 'owned task and artifacts are absent'; exact = $false; task_running = $false }
    }
    if ($null -eq $task -or -not $wrapperExists -or -not $metadataExists -or ((Test-HasLauncher $Spec) -and -not $launcherExists)) {
        return [pscustomobject]@{ state = 'drift'; reason = 'owned task artifacts are incomplete'; exact = $false; task_running = $false }
    }
    if (-not (Test-PrivateAcl $Spec.wrapper_path $Spec.user_sid $false) -or -not (Test-PrivateAcl $Spec.metadata_path $Spec.user_sid $false)) {
        return [pscustomobject]@{ state = 'drift'; reason = 'owned artifact ACL or file type does not match'; exact = $false; task_running = $false }
    }
    if (-not (Test-PrivateAcl $directory $Spec.user_sid $true)) {
        return [pscustomobject]@{ state = 'drift'; reason = 'owned artifact directory ACL does not match'; exact = $false; task_running = $false }
    }
    if (-not (Test-Hash $Spec.wrapper_path $Spec.wrapper_sha256) -or -not (Test-Hash $Spec.metadata_path $Spec.metadata_sha256)) {
        return [pscustomobject]@{ state = 'drift'; reason = 'owned artifact hash does not match'; exact = $false; task_running = $false }
    }

    if (-not (Test-OwnedLauncher $Spec)) {
        return [pscustomobject]@{ state = 'drift'; reason = 'owned launcher identity does not match'; exact = $false; task_running = $false }
    }

    $persistedTaskXml = Export-ScheduledTask -TaskPath $Spec.task_path -TaskName $Spec.task_name -ErrorAction Stop
    if (-not (Test-OwnedTaskFingerprint $task $Spec $persistedTaskXml)) {
        return [pscustomobject]@{ state = 'drift'; reason = 'owned task fingerprint does not match'; exact = $false; task_running = $false }
    }

    $running = [string] $task.State -eq 'Running'
    $state = if ($running) { 'healthy' } else { 'stopped' }
    $reason = if ($running) { 'owned task is running' } else { 'owned task is stopped' }
    return [pscustomobject]@{ state = $state; reason = $reason; exact = $true; task_running = $running }
}

function Assert-ExactOwned($Spec) {
    $observation = Get-OwnedObservation $Spec
    if ($observation.state -eq 'absent') {
        throw 'Owned task is absent'
    }
    if (-not $observation.exact) {
        throw 'Owned task fingerprint does not match'
    }
    return $observation
}

function Set-OwnedTaskLifecycleAcl($Spec) {
    $ownerSid = ([Security.Principal.SecurityIdentifier]::new([string] $Spec.user_sid)).Value
    $sddl = "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;$ownerSid)"
    $service = New-Object -ComObject 'Schedule.Service'
    $service.Connect()
    $folder = $service.GetFolder($Spec.task_path.TrimEnd([char] '\'))
    $task = $folder.GetTask($Spec.task_name)
    # TASK_DONT_ADD_PRINCIPAL_ACE: preserve the explicit private DACL exactly.
    $task.SetSecurityDescriptor($sddl, 16)
    # DACL_SECURITY_INFORMATION: read back only the lifecycle access boundary.
    if (-not (Test-OwnedTaskLifecycleAcl ($task.GetSecurityDescriptor(4)) $ownerSid)) {
        throw 'Scheduled task lifecycle ACL verification failed'
    }
}

function Install-OwnedTask($Spec) {
    $before = Get-OwnedObservation $Spec
    if ($before.state -ne 'absent') {
        throw 'Refusing to replace an existing or partial task installation'
    }
    $directory = Split-Path -Parent $Spec.wrapper_path
    $createdDirectory = -not (Test-Path -LiteralPath $directory)
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    Set-PrivateDirectoryAcl $directory $Spec.user_sid
    if (-not (Test-PrivateAcl $directory $Spec.user_sid $true)) {
        throw 'Bootstrap directory ACL verification failed'
    }

    $launcherCreated = $false
    $registered = $false
    $registrationAttempted = $false
    try {
        if (Test-HasLauncher $Spec) {
            # Exact absent private directory was verified above. CreateNew refuses replacement.
            Write-OwnedLauncher $Spec ([ref] $launcherCreated)
        }
        [IO.File]::WriteAllBytes($Spec.wrapper_path, (Read-EncodedBytes 'AMC_BOOTSTRAP_WRAPPER_B64'))
        [IO.File]::WriteAllBytes($Spec.metadata_path, (Read-EncodedBytes 'AMC_BOOTSTRAP_METADATA_B64'))
        Set-PrivateFileAcl $Spec.wrapper_path $Spec.user_sid
        Set-PrivateFileAcl $Spec.metadata_path $Spec.user_sid
        if (-not (Test-PrivateAcl $Spec.wrapper_path $Spec.user_sid $false) -or -not (Test-PrivateAcl $Spec.metadata_path $Spec.user_sid $false)) {
            throw 'Bootstrap artifact ACL verification failed'
        }
        if (-not (Test-Hash $Spec.wrapper_path $Spec.wrapper_sha256) -or -not (Test-Hash $Spec.metadata_path $Spec.metadata_sha256)) {
            throw 'Bootstrap artifact hash verification failed'
        }

        $action = New-ScheduledTaskAction -Execute $Spec.action_executable -Argument $Spec.action_arguments
        $principal = New-ScheduledTaskPrincipal -UserId $Spec.account -LogonType $Spec.logon_type -RunLevel Limited
        $trigger = New-ScheduledTaskTrigger -AtLogOn -User $Spec.account
        $settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew `
            -RestartCount $Spec.restart_count -RestartInterval ([Xml.XmlConvert]::ToTimeSpan($Spec.restart_interval)) `
            -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
        $registrationAttempted = $true
        Register-ScheduledTask -TaskPath $Spec.task_path -TaskName $Spec.task_name -Action $action `
            -Principal $principal -Trigger $trigger -Settings $settings | Out-Null
        $registered = $true
        $after = Get-OwnedObservation $Spec
        if (-not $after.exact) {
            throw 'Scheduled task read-back verification failed'
        }
        Set-OwnedTaskLifecycleAcl $Spec
    }
    catch {
        if ((Test-HasLauncher $Spec) -and -not $launcherCreated) {
            # Losing CreateNew does not own any artifacts from a competing installer.
            if ($createdDirectory -and @((Get-ChildItem -LiteralPath $directory -Force)).Count -eq 0) {
                Remove-Item -LiteralPath $directory -Force -ErrorAction SilentlyContinue
            }
            throw
        }
        if ($registrationAttempted) {
            try {
                # A failed registration may have persisted, or another creator may
                # have won the task name. Only a successful registration is ours to remove.
                if ($registered) {
                    Unregister-ScheduledTask -TaskPath $Spec.task_path -TaskName $Spec.task_name -Confirm:$false -ErrorAction Stop
                }
                $service = New-Object -ComObject 'Schedule.Service'
                $service.Connect()
                try {
                    $folder = $service.GetFolder($Spec.task_path.TrimEnd([char] '\'))
                    $null = $folder.GetTask($Spec.task_name)
                    throw 'Scheduled task absence could not be verified'
                }
                catch {
                    # HRESULT_FROM_WIN32(ERROR_FILE_NOT_FOUND) proves exact task absence.
                    if ($_.Exception.GetBaseException().HResult -ne -2147024894) { throw }
                }
            }
            catch {
                if ($registered) {
                    throw 'Scheduled task rollback failed; private bootstrap artifacts retained for recovery'
                }
                throw 'Scheduled task registration outcome is uncertain; private bootstrap artifacts retained for recovery'
            }
        }
        if ($launcherCreated -and (Test-Path -LiteralPath $Spec.launcher_path)) {
            if (-not (Test-OwnedLauncher $Spec)) {
                throw 'Bootstrap launcher outcome is uncertain; private artifacts retained for recovery'
            }
        }
        Remove-Item -LiteralPath $Spec.wrapper_path -Force -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath $Spec.metadata_path -Force -ErrorAction SilentlyContinue
        if ($launcherCreated -and (Test-Path -LiteralPath $Spec.launcher_path)) { Remove-Item -LiteralPath $Spec.launcher_path -Force }
        if ($createdDirectory -and @((Get-ChildItem -LiteralPath $directory -Force)).Count -eq 0) {
            Remove-Item -LiteralPath $directory -Force -ErrorAction SilentlyContinue
        }
        throw
    }
}

function Remove-OwnedTask($spec) {
    $owned = Assert-ExactOwned $spec
    if ($owned.task_running) {
        throw 'Owned task must be stopped before removal'
    }
    Unregister-ScheduledTask -TaskPath $spec.task_path -TaskName $spec.task_name -Confirm:$false
    $directory = Split-Path -Parent $spec.wrapper_path
    if (-not (Test-PrivateAcl $directory $spec.user_sid $true) -or -not (Test-PrivateAcl $spec.wrapper_path $spec.user_sid $false) -or -not (Test-PrivateAcl $spec.metadata_path $spec.user_sid $false)) {
        throw 'Owned artifact identity changed before removal'
    }
    if (-not (Test-Hash $spec.wrapper_path $spec.wrapper_sha256) -or -not (Test-Hash $spec.metadata_path $spec.metadata_sha256)) {
        throw 'Owned artifact hash changed before removal'
    }
    if (-not (Test-OwnedLauncher $spec)) { throw 'Owned launcher identity changed before removal' }
    Remove-Item -LiteralPath $spec.wrapper_path -Force
    Remove-Item -LiteralPath $spec.metadata_path -Force
    if (Test-HasLauncher $spec) { Remove-Item -LiteralPath $spec.launcher_path -Force }
    if ((Test-Path -LiteralPath $directory) -and @((Get-ChildItem -LiteralPath $directory -Force)).Count -eq 0) {
        Remove-Item -LiteralPath $directory -Force
    }
}

$spec = Read-EncodedJson 'AMC_BOOTSTRAP_SPEC_B64'
$actionName = [Environment]::GetEnvironmentVariable('AMC_BOOTSTRAP_ACTION')
switch ($actionName) {
    'inspect' { }
    'install' { Install-OwnedTask $spec }
    'start' {
        $owned = Assert-ExactOwned $spec
        if (-not $owned.task_running) {
            Start-ScheduledTask -TaskPath $spec.task_path -TaskName $spec.task_name
        }
    }
    'stop' {
        $owned = Assert-ExactOwned $spec
        if ($owned.task_running) {
            Stop-ScheduledTask -TaskPath $spec.task_path -TaskName $spec.task_name
        }
    }
    'remove' { Remove-OwnedTask $spec }
    default { throw 'Unsupported bootstrap scheduler action' }
}

(Get-OwnedObservation $spec) | ConvertTo-Json -Compress
