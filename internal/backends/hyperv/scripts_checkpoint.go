package hyperv

const (
	// SnapshotNameEnvVar passes the checkpoint name to PowerShell scripts.
	SnapshotNameEnvVar = "AMC_SNAPSHOT_NAME"

	// SnapshotIDEnvVar passes the checkpoint GUID to PowerShell scripts.
	SnapshotIDEnvVar = "AMC_SNAPSHOT_ID"

	// ScriptCheckpointList lists all checkpoints for a virtual machine by GUID.
	ScriptCheckpointList = `$ErrorActionPreference = 'Stop'
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
    $snaps = @(Get-VMSnapshot -VM $vm -ErrorAction Stop)
    $results = @()
    foreach ($s in $snaps) {
        $parentId = ""
        if ($s.ParentSnapshotId) {
            $parentId = [string]$s.ParentSnapshotId.Guid
        }
        $createdIso = ""
        if ($s.CreationTime) {
            $createdIso = $s.CreationTime.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
        }
        $results += @{
            id = [string]$s.Id.Guid
            name = [string]$s.Name
            vm_id = [string]$vmGuid
            parent_id = $parentId
            checkpoint_type = [string]$s.SnapshotType
            creation_time = $createdIso
        }
    }
    @{
        schema_version = "1"
        checkpoints = $results
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

	// ScriptCheckpointCreate creates a new checkpoint for a virtual machine.
	ScriptCheckpointCreate = `$ErrorActionPreference = 'Stop'
$targetId = $env:AMC_TARGET_VM_ID
$snapName = $env:AMC_SNAPSHOT_NAME
if ([string]::IsNullOrWhiteSpace($targetId)) {
    @{
        schema_version = "1"
        error_category = "machine_not_found"
    } | ConvertTo-Json -Compress
    exit 0
}
if ([string]::IsNullOrWhiteSpace($snapName)) {
    @{
        schema_version = "1"
        error_category = "invalid_state"
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
    $created = Checkpoint-VM -VM $vm -SnapshotName $snapName -Passthru -ErrorAction Stop

    $parentId = ""
    if ($created.ParentSnapshotId) {
        $parentId = [string]$created.ParentSnapshotId.Guid
    }
    $createdIso = ""
    if ($created.CreationTime) {
        $createdIso = $created.CreationTime.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
    }

    @{
        schema_version = "1"
        success = $true
        checkpoint = @{
            id = [string]$created.Id.Guid
            name = [string]$created.Name
            vm_id = [string]$vmGuid
            parent_id = $parentId
            checkpoint_type = [string]$created.SnapshotType
            creation_time = $createdIso
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

	// ScriptCheckpointRestore restores a virtual machine to an exact checkpoint GUID.
	ScriptCheckpointRestore = `$ErrorActionPreference = 'Stop'
$targetId = $env:AMC_TARGET_VM_ID
$snapId = $env:AMC_SNAPSHOT_ID
if ([string]::IsNullOrWhiteSpace($targetId)) {
    @{
        schema_version = "1"
        error_category = "machine_not_found"
    } | ConvertTo-Json -Compress
    exit 0
}
if ([string]::IsNullOrWhiteSpace($snapId)) {
    @{
        schema_version = "1"
        error_category = "checkpoint_not_found"
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
    $snapGuid = [guid]::Parse($snapId)
    $snaps = @(Get-VMSnapshot -VM $vm -ErrorAction Stop)
    $matches = @($snaps | Where-Object { $_.Id.Guid -eq $snapGuid })
    if ($matches.Count -ne 1) {
        @{
            schema_version = "1"
            error_category = "checkpoint_not_found"
        } | ConvertTo-Json -Compress
        exit 0
    }
    $snap = $matches[0]

    Restore-VMSnapshot -VMSnapshot $snap -Confirm:$false -ErrorAction Stop

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
        $cat = "checkpoint_not_found"
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
