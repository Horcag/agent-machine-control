try {
    if ($null -eq $request) { throw 'invalid_request' }
    $deadline = Assert-Request $request
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'elevation_required' }
    if ($request.mode -eq 'provision') {
        if (Test-Path -LiteralPath $root) {
            Assert-Installed
            $manifest = [IO.File]::ReadAllText((Join-Path $root 'manifest.json')) | ConvertFrom-Json
            foreach ($name in @('queue.ps1', 'server.ps1', 'worker.ps1', 'actions.ps1', 'native.cs')) {
                if ($request.hashes.$name -ne $manifest.hashes.$name) { throw 'version_conflict' }
            }
        } else {
            if (Get-ScheduledTask -TaskName $taskName -TaskPath '\' -ErrorAction SilentlyContinue) { throw 'foreign_task' }
            Assert-Deadline $deadline
            New-PrivateDirectory $root
            foreach ($dir in @('requests', 'results')) {
                Assert-Deadline $deadline
                New-PrivateDirectory (Join-Path $root $dir)
            }
            foreach ($name in @('queue.ps1', 'server.ps1', 'worker.ps1', 'actions.ps1', 'native.cs')) {
                $data = [Convert]::FromBase64String($request.files.$name)
                Assert-Deadline $deadline
                Write-PrivateFile (Join-Path $root $name) $data
            }
            $manifest = @{ version = 1; sid = $sid; hashes = $request.hashes }
            Assert-Deadline $deadline
            Write-PrivateFile (Join-Path $root 'manifest.json') ([Text.Encoding]::UTF8.GetBytes(($manifest | ConvertTo-Json -Compress)))
            $action = New-ScheduledTaskAction -Execute $powerShell -Argument $taskArguments
            $principal = New-ScheduledTaskPrincipal -UserId $sid -LogonType Interactive -RunLevel Highest
            $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Minutes 21) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
            $trigger = New-ScheduledTaskTrigger -AtLogOn -User $sid
            Assert-Deadline $deadline
            Register-ScheduledTask -TaskName $taskName -TaskPath '\' -Action $action -Principal $principal -Settings $settings -Trigger $trigger | Out-Null
            Assert-Installed
        }
        $request.action = 'status'
    } elseif ($request.mode -eq 'remove') {
        Assert-Installed
        Assert-Deadline $deadline
        Stop-ScheduledTask -TaskName $taskName -TaskPath '\'
        $until = [DateTimeOffset]::UtcNow.AddSeconds(5)
        while ((Get-ScheduledTask -TaskName $taskName -TaskPath '\').State -eq 'Running') {
            if ([DateTimeOffset]::UtcNow -gt $until) { throw 'cleanup_timeout' }
            Start-Sleep -Milliseconds 50
        }
        Assert-Deadline $deadline
        Unregister-ScheduledTask -TaskName $taskName -TaskPath '\' -Confirm:$false
        # Delete only this validated installation, never an arbitrary caller path.
        Assert-Deadline $deadline
        Remove-Item -LiteralPath $root -Recurse -Force
        Write-Response @{request_id = $request.request_id; success = $true}
        exit 0
    } elseif ($request.mode -ne 'execute') { throw 'invalid_mode' }
    Assert-Installed
    Assert-Deadline $deadline
    Remove-ExpiredQueueFiles
    Assert-Deadline $deadline
    Start-ScheduledTask -TaskName $taskName -TaskPath '\'
    $request.PSObject.Properties.Remove('files')
    $request.PSObject.Properties.Remove('hashes')
    $inputPath = Join-Path (Join-Path $root 'requests') ($request.request_id + '.json')
    $outputPath = Join-Path (Join-Path $root 'results') ($request.request_id + '.json')
    if (@(Get-ChildItem -LiteralPath (Join-Path $root 'requests') -Force | Where-Object { $_.Name -cmatch '^[0-9a-f]{32}\.json(?:\.tmp|\.work)?$' } | Select-Object -First 64).Count -ge 64) { throw 'queue_full' }
    foreach ($path in @($inputPath, ($inputPath + '.work'), $outputPath)) {
        if (Test-Path -LiteralPath $path) { throw 'duplicate_request' }
    }
    $tempPath = $inputPath + '.tmp'
    $ownsTemp = $false
    $ownsInput = $false
    try {
        Assert-Deadline $deadline
        Write-PrivateFile $tempPath ([Text.Encoding]::UTF8.GetBytes(($request | ConvertTo-Json -Compress -Depth 12)))
        $ownsTemp = $true
        Assert-Deadline $deadline
        [IO.File]::Move($tempPath, $inputPath)
        $ownsInput = $true
        $ownsTemp = $false
        while (-not (Test-Path -LiteralPath $outputPath)) {
            if ([DateTimeOffset]::UtcNow -ge $deadline) { throw 'desktop_timeout' }
            Start-Sleep -Milliseconds 50
        }
        Assert-PrivatePath $outputPath $false
        if ((Get-Item -LiteralPath $outputPath).Length -gt 524288) { throw 'oversized_response' }
        [Console]::Out.Write([IO.File]::ReadAllText($outputPath))
    } finally {
        if ($ownsTemp -and (Test-Path -LiteralPath $tempPath)) { Remove-Item -LiteralPath $tempPath -Force }
        # Keep the complete sensitive response private until exact-ID pruning after two minutes.
        # The dispatcher must still see it when the worker exits; readers never remove it.
        if ($ownsInput -and (Test-Path -LiteralPath $inputPath)) { Remove-Item -LiteralPath $inputPath -Force }
    }
} catch {
    # Never expose exceptions containing paths, UIA properties, clipboard or typed input.
    Write-Response @{request_id = $request.request_id; success = $false; error = 'desktop_unavailable'}
}
