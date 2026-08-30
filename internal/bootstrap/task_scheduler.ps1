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

function Resolve-Sid([string] $Identity) {
    if ($Identity -match '^S-1-') {
        return ([Security.Principal.SecurityIdentifier]::new($Identity)).Value
    }
    $account = [Security.Principal.NTAccount]::new($Identity)
    return $account.Translate([Security.Principal.SecurityIdentifier]).Value
}

function Resolve-SidSafe([string] $Identity) {
    try {
        return Resolve-Sid $Identity
    }
    catch {
        return ''
    }
}

function Test-PrivateAcl([string] $LiteralPath, [string] $OwnerSid) {
    $item = Get-Item -LiteralPath $LiteralPath -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        return $false
    }
    $acl = Get-Acl -LiteralPath $LiteralPath
    if ((Resolve-SidSafe ([string] $acl.Owner)) -ne $OwnerSid) {
        return $false
    }
    if (-not $acl.AreAccessRulesProtected) {
        return $false
    }
    $allowed = @($OwnerSid, 'S-1-5-18')
    foreach ($rule in $acl.Access) {
        if ($rule.IsInherited) {
            return $false
        }
        if ($rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) {
            continue
        }
        $ruleSid = Resolve-SidSafe ([string] $rule.IdentityReference)
        if ($ruleSid -notin $allowed) {
            return $false
        }
    }
    return $true
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
    $actual = 'sha256:' + (Get-FileHash -LiteralPath $LiteralPath -Algorithm SHA256).Hash.ToLowerInvariant()
    return $actual -ceq $Expected
}

function Test-SameText([string] $Left, [string] $Right) {
    return [string]::Equals($Left, $Right, [StringComparison]::Ordinal)
}

function Test-SamePath([string] $Left, [string] $Right) {
    return [string]::Equals($Left, $Right, [StringComparison]::OrdinalIgnoreCase)
}

function Get-OwnedObservation($Spec) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    if ($identity.User.Value -ne $Spec.user_sid -or $identity.Name -ne $Spec.account) {
        return [pscustomobject]@{ state = 'drift'; reason = 'current Windows identity does not match metadata'; exact = $false; task_running = $false }
    }

    $task = Get-ScheduledTask -TaskPath $Spec.task_path -TaskName $Spec.task_name -ErrorAction SilentlyContinue
    $wrapperExists = Test-Path -LiteralPath $Spec.wrapper_path -PathType Leaf
    $metadataExists = Test-Path -LiteralPath $Spec.metadata_path -PathType Leaf
    $directory = Split-Path -Parent $Spec.wrapper_path
    $directoryExists = Test-Path -LiteralPath $directory -PathType Container
    if ($null -eq $task -and -not $wrapperExists -and -not $metadataExists) {
        if ($directoryExists -and ((-not (Test-PrivateAcl $directory $Spec.user_sid)) -or @((Get-ChildItem -LiteralPath $directory -Force)).Count -ne 0)) {
            return [pscustomobject]@{ state = 'drift'; reason = 'bootstrap directory is not empty private owned state'; exact = $false; task_running = $false }
        }
        return [pscustomobject]@{ state = 'absent'; reason = 'owned task and artifacts are absent'; exact = $false; task_running = $false }
    }
    if ($null -eq $task -or -not $wrapperExists -or -not $metadataExists) {
        return [pscustomobject]@{ state = 'drift'; reason = 'owned task artifacts are incomplete'; exact = $false; task_running = $false }
    }
    if (-not (Test-PrivateAcl $Spec.wrapper_path $Spec.user_sid) -or -not (Test-PrivateAcl $Spec.metadata_path $Spec.user_sid)) {
        return [pscustomobject]@{ state = 'drift'; reason = 'owned artifact ACL or file type does not match'; exact = $false; task_running = $false }
    }
    if (-not (Test-PrivateAcl $directory $Spec.user_sid)) {
        return [pscustomobject]@{ state = 'drift'; reason = 'owned artifact directory ACL does not match'; exact = $false; task_running = $false }
    }
    if (-not (Test-Hash $Spec.wrapper_path $Spec.wrapper_sha256) -or -not (Test-Hash $Spec.metadata_path $Spec.metadata_sha256)) {
        return [pscustomobject]@{ state = 'drift'; reason = 'owned artifact hash does not match'; exact = $false; task_running = $false }
    }

    $actions = @($task.Actions)
    $triggers = @($task.Triggers)
    if ($actions.Count -ne 1 -or $triggers.Count -ne 1) {
        return [pscustomobject]@{ state = 'drift'; reason = 'owned task action or trigger count does not match'; exact = $false; task_running = $false }
    }
    $principalSid = Resolve-SidSafe ([string] $task.Principal.UserId)
    $triggerClass = [string] $triggers[0].CimClass.CimClassName
    $triggerSid = Resolve-SidSafe ([string] $triggers[0].UserId)
    $exact = (Test-SamePath ([string] $actions[0].Execute) ([string] $Spec.action_executable)) -and
        (Test-SameText ([string] $actions[0].Arguments) ([string] $Spec.action_arguments)) -and
        ($principalSid -eq $Spec.user_sid) -and
        ([string] $task.Principal.LogonType -eq $Spec.logon_type) -and
        ([string] $task.Principal.RunLevel -eq $Spec.run_level) -and
        ($triggerClass -eq 'MSFT_TaskLogonTrigger') -and
        ($triggerSid -eq $Spec.user_sid) -and
        ([bool] $task.Settings.StartWhenAvailable -eq [bool] $Spec.start_when_available) -and
        ([string] $task.Settings.MultipleInstances -eq $Spec.multiple_instances) -and
        ([int] $task.Settings.RestartCount -eq [int] $Spec.restart_count) -and
        ([string] $task.Settings.RestartInterval -eq $Spec.restart_interval) -and
        ([string] $task.Settings.ExecutionTimeLimit -eq $Spec.execution_time_limit) -and
        (-not [bool] $task.Settings.DisallowStartIfOnBatteries) -and
        (-not [bool] $task.Settings.StopIfGoingOnBatteries)
    if (-not $exact) {
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

function Install-OwnedTask($Spec) {
    $before = Get-OwnedObservation $Spec
    if ($before.state -ne 'absent') {
        throw 'Refusing to replace an existing or partial task installation'
    }
    $directory = Split-Path -Parent $Spec.wrapper_path
    $createdDirectory = -not (Test-Path -LiteralPath $directory)
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    Set-PrivateDirectoryAcl $directory $Spec.user_sid
    if (-not (Test-PrivateAcl $directory $Spec.user_sid)) {
        throw 'Bootstrap directory ACL verification failed'
    }

    $registered = $false
    try {
        [IO.File]::WriteAllBytes($Spec.wrapper_path, (Read-EncodedBytes 'AMC_BOOTSTRAP_WRAPPER_B64'))
        [IO.File]::WriteAllBytes($Spec.metadata_path, (Read-EncodedBytes 'AMC_BOOTSTRAP_METADATA_B64'))
        Set-PrivateFileAcl $Spec.wrapper_path $Spec.user_sid
        Set-PrivateFileAcl $Spec.metadata_path $Spec.user_sid
        if (-not (Test-PrivateAcl $Spec.wrapper_path $Spec.user_sid) -or -not (Test-PrivateAcl $Spec.metadata_path $Spec.user_sid)) {
            throw 'Bootstrap artifact ACL verification failed'
        }
        if (-not (Test-Hash $Spec.wrapper_path $Spec.wrapper_sha256) -or -not (Test-Hash $Spec.metadata_path $Spec.metadata_sha256)) {
            throw 'Bootstrap artifact hash verification failed'
        }

        $action = New-ScheduledTaskAction -Execute $Spec.action_executable -Argument $Spec.action_arguments
        $principal = New-ScheduledTaskPrincipal -UserId $Spec.account -LogonType S4U -RunLevel Limited
        $trigger = New-ScheduledTaskTrigger -AtLogOn -User $Spec.account
        $settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew `
            -RestartCount $Spec.restart_count -RestartInterval ([TimeSpan]::Parse($Spec.restart_interval)) `
            -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
        Register-ScheduledTask -TaskPath $Spec.task_path -TaskName $Spec.task_name -Action $action `
            -Principal $principal -Trigger $trigger -Settings $settings | Out-Null
        $registered = $true
        $after = Get-OwnedObservation $Spec
        if (-not $after.exact) {
            throw 'Scheduled task read-back verification failed'
        }
    }
    catch {
        if ($registered) {
            Unregister-ScheduledTask -TaskPath $Spec.task_path -TaskName $Spec.task_name -Confirm:$false -ErrorAction SilentlyContinue
        }
        Remove-Item -LiteralPath $Spec.wrapper_path -Force -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath $Spec.metadata_path -Force -ErrorAction SilentlyContinue
        if ($createdDirectory) {
            Remove-Item -LiteralPath $directory -Force -ErrorAction SilentlyContinue
        }
        throw
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
    'remove' {
        $owned = Assert-ExactOwned $spec
        if ($owned.task_running) {
            throw 'Owned task must be stopped before removal'
        }
        Unregister-ScheduledTask -TaskPath $spec.task_path -TaskName $spec.task_name -Confirm:$false
        if (-not (Test-PrivateAcl $spec.wrapper_path $spec.user_sid) -or -not (Test-PrivateAcl $spec.metadata_path $spec.user_sid)) {
            throw 'Owned artifact identity changed before removal'
        }
        if (-not (Test-Hash $spec.wrapper_path $spec.wrapper_sha256) -or -not (Test-Hash $spec.metadata_path $spec.metadata_sha256)) {
            throw 'Owned artifact hash changed before removal'
        }
        Remove-Item -LiteralPath $spec.wrapper_path -Force
        Remove-Item -LiteralPath $spec.metadata_path -Force
        $directory = Split-Path -Parent $spec.wrapper_path
        if ((Test-Path -LiteralPath $directory) -and @((Get-ChildItem -LiteralPath $directory -Force)).Count -eq 0) {
            Remove-Item -LiteralPath $directory -Force
        }
    }
    default { throw 'Unsupported bootstrap scheduler action' }
}

(Get-OwnedObservation $spec) | ConvertTo-Json -Compress
