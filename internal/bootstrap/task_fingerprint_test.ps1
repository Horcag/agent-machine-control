$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-True([bool] $Value, [string] $Message) {
    if (-not $Value) {
        throw $Message
    }
}

function Assert-False([bool] $Value, [string] $Message) {
    if ($Value) {
        throw $Message
    }
}

function New-SyntheticAcl([bool] $Directory) {
    $inheritance = if ($Directory) {
        [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    }
    else {
        [Security.AccessControl.InheritanceFlags]::None
    }
    $owner = [pscustomobject]@{
        IdentityReference = 'S-1-5-21-1000'
        AccessControlType = [Security.AccessControl.AccessControlType]::Allow
        FileSystemRights = [Security.AccessControl.FileSystemRights]::FullControl
        InheritanceFlags = $inheritance
        PropagationFlags = [Security.AccessControl.PropagationFlags]::None
        IsInherited = $false
    }
    $system = [pscustomobject]@{
        IdentityReference = 'S-1-5-18'
        AccessControlType = [Security.AccessControl.AccessControlType]::Allow
        FileSystemRights = [Security.AccessControl.FileSystemRights]::FullControl
        InheritanceFlags = $inheritance
        PropagationFlags = [Security.AccessControl.PropagationFlags]::None
        IsInherited = $false
    }
    return [pscustomobject]@{
        Owner = 'S-1-5-21-1000'
        AreAccessRulesProtected = $true
        AreAccessRulesCanonical = $true
        Access = @($owner, $system)
    }
}

function Test-AclFingerprints {
    foreach ($directory in @($false, $true)) {
        $kind = if ($directory) { 'directory' } else { 'file' }
        $acl = New-SyntheticAcl $directory
        Assert-True (Test-PrivateAclFingerprint $kind ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $directory) "exact $kind ACL was rejected"

        $acl = New-SyntheticAcl $directory
        $acl.Access[0].AccessControlType = [Security.AccessControl.AccessControlType]::Deny
        Assert-False (Test-PrivateAclFingerprint $kind ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $directory) "deny $kind ACE was accepted"

        $acl = New-SyntheticAcl $directory
        $acl.Access = @($acl.Access[1])
        Assert-False (Test-PrivateAclFingerprint $kind ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $directory) "missing owner $kind ACE was accepted"

        $acl = New-SyntheticAcl $directory
        $acl.Access[0].FileSystemRights = [Security.AccessControl.FileSystemRights]::Read
        Assert-False (Test-PrivateAclFingerprint $kind ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $directory) "weak owner $kind ACE was accepted"

        $acl = New-SyntheticAcl $directory
        $acl.Access = @($acl.Access[0])
        Assert-False (Test-PrivateAclFingerprint $kind ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $directory) "missing System $kind ACE was accepted"

        $acl = New-SyntheticAcl $directory
        $acl.Access += [pscustomobject]@{
            IdentityReference = 'S-1-5-32-544'
            AccessControlType = [Security.AccessControl.AccessControlType]::Allow
            FileSystemRights = [Security.AccessControl.FileSystemRights]::FullControl
            InheritanceFlags = [Security.AccessControl.InheritanceFlags]::None
            PropagationFlags = [Security.AccessControl.PropagationFlags]::None
            IsInherited = $false
        }
        Assert-False (Test-PrivateAclFingerprint $kind ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $directory) "extra $kind principal was accepted"

        $acl = New-SyntheticAcl $directory
        $acl.Access[0].InheritanceFlags = [Security.AccessControl.InheritanceFlags]::None
        if (-not $directory) {
            $acl.Access[0].InheritanceFlags = [Security.AccessControl.InheritanceFlags]::ObjectInherit
        }
        Assert-False (Test-PrivateAclFingerprint $kind ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $directory) "altered $kind inheritance was accepted"

        $acl = New-SyntheticAcl $directory
        $acl.Access[0].PropagationFlags = [Security.AccessControl.PropagationFlags]::InheritOnly
        Assert-False (Test-PrivateAclFingerprint $kind ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $directory) "altered $kind propagation was accepted"
    }

    $acl = New-SyntheticAcl $false
    $acl.Owner = 'S-1-5-21-2000'
    Assert-False (Test-PrivateAclFingerprint 'file' ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $false) 'owner drift was accepted'
    $acl = New-SyntheticAcl $false
    $acl.AreAccessRulesProtected = $false
    Assert-False (Test-PrivateAclFingerprint 'file' ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $false) 'inherited DACL was accepted'
    $acl = New-SyntheticAcl $false
    $acl.AreAccessRulesCanonical = $false
    Assert-False (Test-PrivateAclFingerprint 'file' ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $false) 'non-canonical DACL was accepted'
    $acl = New-SyntheticAcl $false
    Assert-False (Test-PrivateAclFingerprint 'file' ([IO.FileAttributes]::ReparsePoint) $acl 'S-1-5-21-1000' $false) 'reparse point was accepted'
    Assert-False (Test-PrivateAclFingerprint 'other' ([IO.FileAttributes]::Normal) $acl 'S-1-5-21-1000' $false) 'non-regular object was accepted'
}

function New-SyntheticSpec {
    return [pscustomobject]@{
        action_executable = 'C:\Windows\System32\cmd.exe'
        action_arguments = '/d /s /c synthetic'
        user_sid = 'S-1-5-21-1000'
        logon_type = 'S4U'
        run_level = 'Limited'
        start_when_available = $true
        multiple_instances = 'IgnoreNew'
        restart_count = 3
        restart_interval = 'PT1M'
        execution_time_limit = 'PT0S'
        allow_start_on_batteries = $true
        dont_stop_on_batteries = $true
    }
}

function New-SyntheticTask {
    $action = [pscustomobject]@{
        CimClass = [pscustomobject]@{ CimClassName = 'MSFT_TaskExecAction' }
        Execute = 'C:\Windows\System32\cmd.exe'
        Arguments = '/d /s /c synthetic'
        WorkingDirectory = $null
        Id = $null
    }
    $principal = [pscustomobject]@{
        UserId = 'S-1-5-21-1000'
        LogonType = 'S4U'
        RunLevel = 'Limited'
        Id = $null
        DisplayName = $null
        GroupId = $null
        ProcessTokenSidType = 'Default'
        RequiredPrivilege = $null
    }
    $trigger = [pscustomobject]@{
        CimClass = [pscustomobject]@{ CimClassName = 'MSFT_TaskLogonTrigger' }
        Enabled = $true
        UserId = 'S-1-5-21-1000'
        Delay = $null
        StartBoundary = $null
        EndBoundary = $null
        ExecutionTimeLimit = $null
        Id = $null
        Repetition = $null
    }
    $settings = [pscustomobject]@{
        AllowDemandStart = $true
        AllowHardTerminate = $true
        Compatibility = 'Win7'
        DeleteExpiredTaskAfter = $null
        DisallowStartIfOnBatteries = $false
        Enabled = $true
        ExecutionTimeLimit = 'PT0S'
        Hidden = $false
        IdleSettings = [pscustomobject]@{ IdleDuration = 'PT10M'; RestartOnIdle = $false; StopOnIdleEnd = $true; WaitTimeout = 'PT1H' }
        MultipleInstances = 'IgnoreNew'
        NetworkSettings = [pscustomobject]@{ Id = $null; Name = $null }
        Priority = 7
        RestartCount = 3
        RestartInterval = 'PT1M'
        RunOnlyIfIdle = $false
        RunOnlyIfNetworkAvailable = $false
        StartWhenAvailable = $true
        StopIfGoingOnBatteries = $false
        WakeToRun = $false
        DisallowStartOnRemoteAppSession = $false
        UseUnifiedSchedulingEngine = $true
        MaintenanceSettings = $null
        Volatile = $false
    }
    return [pscustomobject]@{
        Actions = @($action)
        Principal = $principal
        Triggers = @($trigger)
        Settings = $settings
        State = 'Ready'
    }
}

function Test-TaskFingerprints {
    $spec = New-SyntheticSpec
    Assert-True (Test-OwnedTaskFingerprint (New-SyntheticTask) $spec) 'exact task fingerprint was rejected'

    $cases = [ordered]@{
        'extra action' = { param($task) $task.Actions += $task.Actions[0] }
        'working directory' = { param($task) $task.Actions[0].WorkingDirectory = 'C:\Temp' }
        'action id' = { param($task) $task.Actions[0].Id = 'changed' }
        'principal' = { param($task) $task.Principal.RunLevel = 'Highest' }
        'principal defaults' = { param($task) $task.Principal.ProcessTokenSidType = 'Unrestricted' }
        'disabled trigger' = { param($task) $task.Triggers[0].Enabled = $false }
        'trigger boundary' = { param($task) $task.Triggers[0].StartBoundary = '2026-08-31T00:00:00' }
        'trigger repetition' = { param($task) $task.Triggers[0].Repetition = [pscustomobject]@{ Interval = 'PT1H' } }
        'disabled task' = { param($task) $task.Settings.Enabled = $false }
        'hidden task' = { param($task) $task.Settings.Hidden = $true }
        'demand start' = { param($task) $task.Settings.AllowDemandStart = $false }
        'restart settings' = { param($task) $task.Settings.RestartInterval = 'PT5M' }
        'battery settings' = { param($task) $task.Settings.DisallowStartIfOnBatteries = $true }
        'idle settings' = { param($task) $task.Settings.IdleSettings.StopOnIdleEnd = $false }
        'network requirement' = { param($task) $task.Settings.RunOnlyIfNetworkAvailable = $true }
        'wake setting' = { param($task) $task.Settings.WakeToRun = $true }
        'priority' = { param($task) $task.Settings.Priority = 4 }
        'compatibility' = { param($task) $task.Settings.Compatibility = 'Win8' }
        'delete expiry' = { param($task) $task.Settings.DeleteExpiredTaskAfter = 'PT1H' }
        'remote session' = { param($task) $task.Settings.DisallowStartOnRemoteAppSession = $true }
        'unified scheduling' = { param($task) $task.Settings.UseUnifiedSchedulingEngine = $false }
        'volatile task' = { param($task) $task.Settings.Volatile = $true }
    }
    foreach ($name in $cases.Keys) {
        $task = New-SyntheticTask
        & $cases[$name] $task
        Assert-False (Test-OwnedTaskFingerprint $task $spec) "$name drift was accepted"
    }
}

function Test-NativeTaskDefinitionFingerprint {
    if ($null -eq (Get-Command New-ScheduledTask -ErrorAction SilentlyContinue)) {
        return
    }
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $spec = New-SyntheticSpec
    $spec.user_sid = $identity.User.Value
    $action = New-ScheduledTaskAction -Execute $spec.action_executable -Argument $spec.action_arguments
    $principal = New-ScheduledTaskPrincipal -UserId $identity.Name -LogonType S4U -RunLevel Limited
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity.Name
    $settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew `
        -RestartCount $spec.restart_count -RestartInterval ([Xml.XmlConvert]::ToTimeSpan($spec.restart_interval)) `
        -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    $task = New-ScheduledTask -Action $action -Principal $principal -Trigger $trigger -Settings $settings
    Assert-True (Test-OwnedTaskFingerprint $task $spec) 'native in-memory task definition fingerprint was rejected'
}

Test-AclFingerprints
Test-TaskFingerprints
Test-NativeTaskDefinitionFingerprint
'bootstrap fingerprint regressions: passed'
