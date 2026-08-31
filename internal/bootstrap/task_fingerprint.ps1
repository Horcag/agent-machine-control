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

function Test-PrivateAclFingerprint(
    [string] $ObjectKind,
    [IO.FileAttributes] $Attributes,
    $Acl,
    [string] $OwnerSid,
    [bool] $Directory
) {
    $expectedKind = if ($Directory) { 'directory' } else { 'file' }
    if ($ObjectKind -ne $expectedKind -or ($Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        return $false
    }
    if ((Resolve-SidSafe ([string] $Acl.Owner)) -ne $OwnerSid) {
        return $false
    }
    if (-not [bool] $Acl.AreAccessRulesProtected -or -not [bool] $Acl.AreAccessRulesCanonical) {
        return $false
    }
    $rules = @($Acl.Access)
    if ($rules.Count -ne 2) {
        return $false
    }
    $expectedInheritance = if ($Directory) {
        [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    }
    else {
        [Security.AccessControl.InheritanceFlags]::None
    }
    $seen = @{}
    foreach ($rule in $rules) {
        $ruleSid = Resolve-SidSafe ([string] $rule.IdentityReference)
        if ($ruleSid -notin @($OwnerSid, 'S-1-5-18') -or $seen.ContainsKey($ruleSid)) {
            return $false
        }
        if ($rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow -or [bool] $rule.IsInherited) {
            return $false
        }
        if ([int] $rule.FileSystemRights -ne [int] [Security.AccessControl.FileSystemRights]::FullControl) {
            return $false
        }
        if ([int] $rule.InheritanceFlags -ne [int] $expectedInheritance) {
            return $false
        }
        if ($rule.PropagationFlags -ne [Security.AccessControl.PropagationFlags]::None) {
            return $false
        }
        $seen[$ruleSid] = $true
    }
    return $seen.ContainsKey($OwnerSid) -and $seen.ContainsKey('S-1-5-18')
}

function Test-HasProperty($Object, [string] $Name) {
    return $null -ne $Object -and $null -ne $Object.PSObject.Properties[$Name]
}

function Convert-NullableText($Value) {
    if ($null -eq $Value) {
        return ''
    }
    return [string] $Value
}

function Convert-ComparablePath($Value) {
    return (Convert-NullableText $Value).ToLowerInvariant()
}

function Get-PersistedPrincipalSid([string] $PersistedTaskXml) {
    try {
        if ([string]::IsNullOrWhiteSpace($PersistedTaskXml)) {
            return ''
        }
        [xml] $taskXml = $PersistedTaskXml
        $userIds = @($taskXml.SelectNodes('/*[local-name()="Task"]/*[local-name()="Principals"]/*[local-name()="Principal"]/*[local-name()="UserId"]'))
        if ($userIds.Count -ne 1) {
            return ''
        }
        return Convert-NullableText $userIds[0].InnerText
    }
    catch {
        return ''
    }
}

function Convert-ComparablePrincipalId($Value) {
    $principalId = Convert-NullableText $Value
    if ($principalId -ceq 'Author') {
        return ''
    }
    return $principalId
}

function Test-EmptyTaskRepetition($Repetition) {
    if ($null -eq $Repetition) {
        return $true
    }
    if (-not (Test-HasProperty $Repetition 'Interval') -or -not (Test-HasProperty $Repetition 'Duration') -or -not (Test-HasProperty $Repetition 'StopAtDurationEnd')) {
        return $false
    }
    return [string]::IsNullOrEmpty((Convert-NullableText $Repetition.Interval)) -and `
        [string]::IsNullOrEmpty((Convert-NullableText $Repetition.Duration)) -and `
        $Repetition.StopAtDurationEnd -eq $false
}

function Get-CimClassName($Object) {
    if ($null -eq $Object -or -not (Test-HasProperty $Object 'CimClass')) {
        return ''
    }
    return Convert-NullableText $Object.CimClass.CimClassName
}

function Get-OptionalValue($Object, [string] $Name) {
    if (-not (Test-HasProperty $Object $Name)) {
        return '<not-exposed>'
    }
    return $Object.$Name
}

function Get-OptionalExpected($Object, [string] $Name, $Expected) {
    if (-not (Test-HasProperty $Object $Name)) {
        return '<not-exposed>'
    }
    return $Expected
}

function Get-OwnedTaskFingerprint($Task, [string] $PersistedTaskXml) {
    $actions = @($Task.Actions)
    $triggers = @($Task.Triggers)
    if ($actions.Count -ne 1 -or $triggers.Count -ne 1) {
        return [ordered]@{ action_count = $actions.Count; trigger_count = $triggers.Count }
    }
    $action = $actions[0]
    $principal = $Task.Principal
    $trigger = $triggers[0]
    $settings = $Task.Settings
    $privileges = @($principal.RequiredPrivilege | Where-Object { $null -ne $_ } | ForEach-Object { [string] $_ } | Sort-Object)
    return [ordered]@{
        action_count = $actions.Count
        action_class = Get-CimClassName $action
        action_executable = Convert-ComparablePath $action.Execute
        action_arguments = Convert-NullableText $action.Arguments
        action_working_directory = Convert-NullableText $action.WorkingDirectory
        action_id = Convert-NullableText $action.Id
        principal_sid = Get-PersistedPrincipalSid $PersistedTaskXml
        principal_logon_type = [string] $principal.LogonType
        principal_run_level = [string] $principal.RunLevel
        principal_id = Convert-ComparablePrincipalId $principal.Id
        principal_display_name = Convert-NullableText $principal.DisplayName
        principal_group_id = Convert-NullableText $principal.GroupId
        principal_process_token_sid_type = Convert-NullableText $principal.ProcessTokenSidType
        principal_required_privileges = $privileges
        trigger_count = $triggers.Count
        trigger_class = Get-CimClassName $trigger
        trigger_enabled = [bool] $trigger.Enabled
        trigger_sid = Resolve-SidSafe ([string] $trigger.UserId)
        trigger_delay = Convert-NullableText $trigger.Delay
        trigger_start_boundary = Convert-NullableText $trigger.StartBoundary
        trigger_end_boundary = Convert-NullableText $trigger.EndBoundary
        trigger_execution_time_limit = Convert-NullableText $trigger.ExecutionTimeLimit
        trigger_id = Convert-NullableText $trigger.Id
        trigger_has_repetition = -not (Test-EmptyTaskRepetition $trigger.Repetition)
        settings_enabled = [bool] $settings.Enabled
        settings_hidden = [bool] $settings.Hidden
        settings_allow_demand_start = [bool] $settings.AllowDemandStart
        settings_allow_hard_terminate = [bool] $settings.AllowHardTerminate
        settings_compatibility = [string] $settings.Compatibility
        settings_delete_expired_after = Convert-NullableText $settings.DeleteExpiredTaskAfter
        settings_start_when_available = [bool] $settings.StartWhenAvailable
        settings_multiple_instances = [string] $settings.MultipleInstances
        settings_restart_count = [int] $settings.RestartCount
        settings_restart_interval = Convert-NullableText $settings.RestartInterval
        settings_execution_time_limit = Convert-NullableText $settings.ExecutionTimeLimit
        settings_disallow_start_on_batteries = [bool] $settings.DisallowStartIfOnBatteries
        settings_stop_on_batteries = [bool] $settings.StopIfGoingOnBatteries
        settings_run_only_if_idle = [bool] $settings.RunOnlyIfIdle
        settings_idle_duration = Convert-NullableText $settings.IdleSettings.IdleDuration
        settings_idle_restart = [bool] $settings.IdleSettings.RestartOnIdle
        settings_idle_stop = [bool] $settings.IdleSettings.StopOnIdleEnd
        settings_idle_wait_timeout = Convert-NullableText $settings.IdleSettings.WaitTimeout
        settings_run_only_if_network = [bool] $settings.RunOnlyIfNetworkAvailable
        settings_network_id = Convert-NullableText $settings.NetworkSettings.Id
        settings_network_name = Convert-NullableText $settings.NetworkSettings.Name
        settings_wake_to_run = [bool] $settings.WakeToRun
        settings_priority = [int] $settings.Priority
        settings_disallow_remote_session = Get-OptionalValue $settings 'DisallowStartOnRemoteAppSession'
        settings_unified_scheduling = Get-OptionalValue $settings 'UseUnifiedSchedulingEngine'
        settings_maintenance_is_null = if (Test-HasProperty $settings 'MaintenanceSettings') { $null -eq $settings.MaintenanceSettings } else { '<not-exposed>' }
        settings_volatile = Get-OptionalValue $settings 'volatile'
    }
}

function Get-ExpectedOwnedTaskFingerprint($Task, $Spec) {
    $settings = $Task.Settings
    return [ordered]@{
        action_count = 1
        action_class = 'MSFT_TaskExecAction'
        action_executable = Convert-ComparablePath $Spec.action_executable
        action_arguments = [string] $Spec.action_arguments
        action_working_directory = ''
        action_id = ''
        principal_sid = [string] $Spec.user_sid
        principal_logon_type = [string] $Spec.logon_type
        principal_run_level = [string] $Spec.run_level
        principal_id = ''
        principal_display_name = ''
        principal_group_id = ''
        principal_process_token_sid_type = 'Default'
        principal_required_privileges = @()
        trigger_count = 1
        trigger_class = 'MSFT_TaskLogonTrigger'
        trigger_enabled = $true
        trigger_sid = [string] $Spec.user_sid
        trigger_delay = ''
        trigger_start_boundary = ''
        trigger_end_boundary = ''
        trigger_execution_time_limit = ''
        trigger_id = ''
        trigger_has_repetition = $false
        settings_enabled = $true
        settings_hidden = $false
        settings_allow_demand_start = $true
        settings_allow_hard_terminate = $true
        settings_compatibility = 'Win7'
        settings_delete_expired_after = ''
        settings_start_when_available = [bool] $Spec.start_when_available
        settings_multiple_instances = [string] $Spec.multiple_instances
        settings_restart_count = [int] $Spec.restart_count
        settings_restart_interval = [string] $Spec.restart_interval
        settings_execution_time_limit = [string] $Spec.execution_time_limit
        settings_disallow_start_on_batteries = -not [bool] $Spec.allow_start_on_batteries
        settings_stop_on_batteries = -not [bool] $Spec.dont_stop_on_batteries
        settings_run_only_if_idle = $false
        settings_idle_duration = 'PT10M'
        settings_idle_restart = $false
        settings_idle_stop = $true
        settings_idle_wait_timeout = 'PT1H'
        settings_run_only_if_network = $false
        settings_network_id = ''
        settings_network_name = ''
        settings_wake_to_run = $false
        settings_priority = 7
        settings_disallow_remote_session = Get-OptionalExpected $settings 'DisallowStartOnRemoteAppSession' $false
        settings_unified_scheduling = Get-OptionalExpected $settings 'UseUnifiedSchedulingEngine' $true
        settings_maintenance_is_null = if (Test-HasProperty $settings 'MaintenanceSettings') { $true } else { '<not-exposed>' }
        settings_volatile = Get-OptionalExpected $settings 'volatile' $false
    }
}

function Test-OwnedTaskFingerprint($Task, $Spec, [string] $PersistedTaskXml) {
    try {
        $actual = (Get-OwnedTaskFingerprint $Task $PersistedTaskXml) | ConvertTo-Json -Compress -Depth 6
        $expected = (Get-ExpectedOwnedTaskFingerprint $Task $Spec) | ConvertTo-Json -Compress -Depth 6
        return [string]::Equals($actual, $expected, [StringComparison]::Ordinal)
    }
    catch {
        return $false
    }
}
