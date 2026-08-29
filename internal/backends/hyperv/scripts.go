package hyperv

const (
	// ScriptDoctor verifies Hyper-V module presence and host query readiness.
	ScriptDoctor = `$ErrorActionPreference = 'Stop'
try {
    Import-Module Hyper-V -ErrorAction Stop
} catch {
    @{
        schema_version = "1"
        ready = $false
        error_category = "module_missing"
    } | ConvertTo-Json -Compress
    exit 0
}
try {
    Get-VMHost -ErrorAction Stop | Out-Null
    @{
        schema_version = "1"
        ready = $true
    } | ConvertTo-Json -Compress
} catch {
    $catInfo = $_.CategoryInfo.Category
    $cat = "host_unavailable"
    if ($_.Exception -is [System.UnauthorizedAccessException] -or $catInfo -eq [System.Management.Automation.ErrorCategory]::PermissionDenied -or $catInfo -eq [System.Management.Automation.ErrorCategory]::SecurityError -or [string]$catInfo -eq "PermissionDenied" -or [string]$catInfo -eq "SecurityError") {
        $cat = "access_denied"
    }
    @{
        schema_version = "1"
        ready = $false
        error_category = $cat
    } | ConvertTo-Json -Compress
}`

	// ScriptList discovers all virtual machines and their network adapters.
	ScriptList = `$ErrorActionPreference = 'Stop'
try {
    Import-Module Hyper-V -ErrorAction Stop
} catch {
    @{
        schema_version = "1"
        error_category = "module_missing"
    } | ConvertTo-Json -Compress
    exit 0
}
try {
    $vms = @(Get-VM -ErrorAction Stop)
    $results = @()
    foreach ($vm in $vms) {
        $adapters = @()
        $netAdapters = @(Get-VMNetworkAdapter -VM $vm -ErrorAction Stop)
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
        if ($vm.Uptime) {
            $uptimeMs = [int64]$vm.Uptime.TotalMilliseconds
            if ($uptimeMs -lt 0) { $uptimeMs = [int64]0 }
        }

        $results += @{
            id = [string]$vm.VMId.Guid
            name = [string]$vm.Name
            state = [string]$vm.State
            status = [string]$vm.Status
            generation = [int]$vm.Generation
            version = [string]$vm.Version
            uptime_ms = $uptimeMs
            cpu_usage = [int]$vm.CPUUsage
            memory_assigned_bytes = [int64]$vm.MemoryAssigned
            network_adapters = $adapters
        }
    }
    @{
        schema_version = "1"
        machines = $results
    } | ConvertTo-Json -Depth 5 -Compress
} catch {
    $catInfo = $_.CategoryInfo.Category
    $cat = "host_unavailable"
    if ($_.Exception -is [System.UnauthorizedAccessException] -or $catInfo -eq [System.Management.Automation.ErrorCategory]::PermissionDenied -or $catInfo -eq [System.Management.Automation.ErrorCategory]::SecurityError -or [string]$catInfo -eq "PermissionDenied" -or [string]$catInfo -eq "SecurityError") {
        $cat = "access_denied"
    }
    @{
        schema_version = "1"
        error_category = $cat
    } | ConvertTo-Json -Compress
}`

	// ScriptInspect inspects a single virtual machine using the target GUID from the environment.
	ScriptInspect = `$ErrorActionPreference = 'Stop'
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
try {
    $vmGuid = [guid]::Parse($targetId)
    $vm = Get-VM -Id $vmGuid -ErrorAction Stop
    $adapters = @()
    $netAdapters = @(Get-VMNetworkAdapter -VM $vm -ErrorAction Stop)
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
    if ($vm.Uptime) {
        $uptimeMs = [int64]$vm.Uptime.TotalMilliseconds
        if ($uptimeMs -lt 0) { $uptimeMs = [int64]0 }
    }

    @{
        schema_version = "1"
        machine = @{
            id = [string]$vm.VMId.Guid
            name = [string]$vm.Name
            state = [string]$vm.State
            status = [string]$vm.Status
            generation = [int]$vm.Generation
            version = [string]$vm.Version
            uptime_ms = $uptimeMs
            cpu_usage = [int]$vm.CPUUsage
            memory_assigned_bytes = [int64]$vm.MemoryAssigned
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
    }
    @{
        schema_version = "1"
        error_category = $cat
    } | ConvertTo-Json -Compress
}`
)
