package hyperv

const (
	// StopModeEnvVar passes the stop mode (shutdown, save, turn-off).
	StopModeEnvVar = "AMC_STOP_MODE"

	// ScriptStart starts a virtual machine by GUID and inspects the resulting state.
	ScriptStart = `$ErrorActionPreference = 'Stop'
$targetId = $env:AMC_TARGET_VM_ID
if ([string]::IsNullOrWhiteSpace($targetId)) {
    @{
        schema_version = "1"
        error_category = "machine_not_found"
    } | ConvertTo-Json -Compress
    exit 0
}
try {
    Import-Module Hyper-V -ErrorAction Stop
} catch {
    @{
        schema_version = "1"
        error_category = "module_missing"
    } | ConvertTo-Json -Compress
    exit 0
}
` + scriptAccessPreflightQuery + `
try {
    $vmGuid = [guid]::Parse($targetId)
    $vm = Get-VM -Id $vmGuid -ErrorAction Stop
    Start-VM -VM $vm -ErrorAction Stop

    $vmAfter = Get-VM -Id $vmGuid -ErrorAction Stop
    $adapters = @()
    $netAdapters = @(Get-VMNetworkAdapter -VM $vmAfter -ErrorAction Stop)
    foreach ($na in $netAdapters) {
        $ips = @()
        if ($na.IPAddresses) {
            foreach ($ip in $na.IPAddresses) {
                if ($ip) { $ips += [string]$ip }
            }
        }
        $adapters += @{
            name = [string]$na.Name
            switch_name = [string]$na.SwitchName
            mac_address = [string]$na.MacAddress
            ip_addresses = $ips
            status = [string]$na.Status
        }
    }

    $uptimeMs = [int64]0
    if ($vmAfter.Uptime) {
        $uptimeMs = [int64]$vmAfter.Uptime.TotalMilliseconds
        if ($uptimeMs -lt 0) { $uptimeMs = [int64]0 }
    }

    @{
        schema_version = "1"
        success = $true
        machine = @{
            id = [string]$vmAfter.VMId.Guid
            name = [string]$vmAfter.Name
            state = [string]$vmAfter.State
            status = [string]$vmAfter.Status
            generation = [int]$vmAfter.Generation
            version = [string]$vmAfter.Version
            uptime_ms = $uptimeMs
            cpu_usage = [int]$vmAfter.CPUUsage
            memory_assigned_bytes = [int64]$vmAfter.MemoryAssigned
            network_adapters = $adapters
        }
    } | ConvertTo-Json -Depth 5 -Compress
} catch {
    $catInfo = $_.CategoryInfo.Category
    $cat = "host_unavailable"
    if ($catInfo -eq [System.Management.Automation.ErrorCategory]::ObjectNotFound -or [string]$catInfo -eq "ObjectNotFound" -or $catInfo -eq [System.Management.Automation.ErrorCategory]::InvalidArgument -or [string]$catInfo -eq "InvalidArgument") {
        $cat = "machine_not_found"
    } elseif ($_.Exception -is [System.UnauthorizedAccessException] -or $catInfo -eq [System.Management.Automation.ErrorCategory]::PermissionDenied -or $catInfo -eq [System.Management.Automation.ErrorCategory]::SecurityError -or [string]$catInfo -eq "PermissionDenied" -or [string]$catInfo -eq "SecurityError") {
        $cat = "access_denied"
    } elseif ($catInfo -eq [System.Management.Automation.ErrorCategory]::InvalidOperation -or [string]$catInfo -eq "InvalidOperation") {
        $cat = "invalid_state"
    }
    @{
        schema_version = "1"
        error_category = $cat
    } | ConvertTo-Json -Compress
}`

	// ScriptStop stops a virtual machine by GUID with mode (shutdown, save, turn-off) and inspects resulting state.
	ScriptStop = `$ErrorActionPreference = 'Stop'
$targetId = $env:AMC_TARGET_VM_ID
if ([string]::IsNullOrWhiteSpace($targetId)) {
    @{
        schema_version = "1"
        error_category = "machine_not_found"
    } | ConvertTo-Json -Compress
    exit 0
}
try {
    Import-Module Hyper-V -ErrorAction Stop
} catch {
    @{
        schema_version = "1"
        error_category = "module_missing"
    } | ConvertTo-Json -Compress
    exit 0
}
` + scriptAccessPreflightQuery + `
try {
    $vmGuid = [guid]::Parse($targetId)
    $vm = Get-VM -Id $vmGuid -ErrorAction Stop
    $mode = $env:AMC_STOP_MODE
    if ($mode -eq "save") {
        Save-VM -VM $vm -ErrorAction Stop
    } elseif ($mode -eq "turn-off") {
        Stop-VM -VM $vm -TurnOff -ErrorAction Stop
    } else {
        Stop-VM -VM $vm -ErrorAction Stop
    }

    $vmAfter = Get-VM -Id $vmGuid -ErrorAction Stop
    $adapters = @()
    $netAdapters = @(Get-VMNetworkAdapter -VM $vmAfter -ErrorAction Stop)
    foreach ($na in $netAdapters) {
        $ips = @()
        if ($na.IPAddresses) {
            foreach ($ip in $na.IPAddresses) {
                if ($ip) { $ips += [string]$ip }
            }
        }
        $adapters += @{
            name = [string]$na.Name
            switch_name = [string]$na.SwitchName
            mac_address = [string]$na.MacAddress
            ip_addresses = $ips
            status = [string]$na.Status
        }
    }

    $uptimeMs = [int64]0
    if ($vmAfter.Uptime) {
        $uptimeMs = [int64]$vmAfter.Uptime.TotalMilliseconds
        if ($uptimeMs -lt 0) { $uptimeMs = [int64]0 }
    }

    @{
        schema_version = "1"
        success = $true
        machine = @{
            id = [string]$vmAfter.VMId.Guid
            name = [string]$vmAfter.Name
            state = [string]$vmAfter.State
            status = [string]$vmAfter.Status
            generation = [int]$vmAfter.Generation
            version = [string]$vmAfter.Version
            uptime_ms = $uptimeMs
            cpu_usage = [int]$vmAfter.CPUUsage
            memory_assigned_bytes = [int64]$vmAfter.MemoryAssigned
            network_adapters = $adapters
        }
    } | ConvertTo-Json -Depth 5 -Compress
} catch {
    $catInfo = $_.CategoryInfo.Category
    $cat = "host_unavailable"
    if ($catInfo -eq [System.Management.Automation.ErrorCategory]::ObjectNotFound -or [string]$catInfo -eq "ObjectNotFound" -or $catInfo -eq [System.Management.Automation.ErrorCategory]::InvalidArgument -or [string]$catInfo -eq "InvalidArgument") {
        $cat = "machine_not_found"
    } elseif ($_.Exception -is [System.UnauthorizedAccessException] -or $catInfo -eq [System.Management.Automation.ErrorCategory]::PermissionDenied -or $catInfo -eq [System.Management.Automation.ErrorCategory]::SecurityError -or [string]$catInfo -eq "PermissionDenied" -or [string]$catInfo -eq "SecurityError") {
        $cat = "access_denied"
    } elseif ($catInfo -eq [System.Management.Automation.ErrorCategory]::InvalidOperation -or [string]$catInfo -eq "InvalidOperation") {
        $cat = "invalid_state"
    }
    @{
        schema_version = "1"
        error_category = $cat
    } | ConvertTo-Json -Compress
}`
)
