$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$sid = $identity.User.Value
$root = Join-Path $env:LOCALAPPDATA 'AMC-Desktop-v1'
$taskName = 'AMC-Desktop-v1-' + $sid
$powerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$taskArguments = '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -STA -WindowStyle Hidden -File "' + (Join-Path $root 'server.ps1') + '"'

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
    $sddl = (Get-Acl -LiteralPath $path -Audit).Sddl
    if ($sddl -notmatch '\(ML;[^;]*;NW;;;HI\)') { throw 'unsafe_integrity' }
}

function New-PrivateDirectory([string]$path) {
    if (Test-Path -LiteralPath $path) { Assert-PrivatePath $path $true; return }
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetSecurityDescriptorSddlForm('O:' + $sid + 'G:' + $sid + 'D:P(A;OICI;FA;;;' + $sid + ')(A;OICI;FA;;;SY)S:(ML;OICI;NW;;;HI)')
    [IO.Directory]::CreateDirectory($path, $acl) | Out-Null
    Assert-PrivatePath $path $true
}

function Write-PrivateFile([string]$path, [byte[]]$data) {
    $acl = [Security.AccessControl.FileSecurity]::new()
    $acl.SetSecurityDescriptorSddlForm('O:' + $sid + 'G:' + $sid + 'D:P(A;;FA;;;' + $sid + ')(A;;FA;;;SY)S:(ML;;NW;;;HI)')
    $file = [IO.FileStream]::new($path, [IO.FileMode]::CreateNew, [Security.AccessControl.FileSystemRights]::FullControl, [IO.FileShare]::None, 4096, [IO.FileOptions]::None, $acl)
    try { $file.Write($data, 0, $data.Length) } finally { $file.Dispose() }
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
    if ($task.Principal.UserId -ne $sid -or $task.Principal.LogonType -ne 'Interactive' -or $task.Principal.RunLevel -ne 'Highest' -or @($task.Actions).Count -ne 1 -or $task.Actions[0].Execute -ne $powerShell -or $task.Actions[0].Arguments -ne $taskArguments) { throw 'foreign_task' }
}

function Assert-Request($request) {
    if ($request.request_id -cnotmatch '^[0-9a-f]{32}$') { throw 'invalid_request' }
    $deadline = [DateTimeOffset]::Parse($request.deadline)
    $now = [DateTimeOffset]::UtcNow
    if ($deadline -le $now -or $deadline -gt $now.AddSeconds(35)) { throw 'expired_request' }
    return $deadline
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
