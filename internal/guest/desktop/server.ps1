. (Join-Path $PSScriptRoot 'queue.ps1')
Assert-Installed
Assert-ConsoleSession
$expiry = [DateTimeOffset]::UtcNow.AddMinutes(20)
$child = $null
$nextPrune = [DateTimeOffset]::MinValue
try {
    while ([DateTimeOffset]::UtcNow -lt $expiry) {
        Assert-ConsoleSession
        if ([DateTimeOffset]::UtcNow -ge $nextPrune) {
            Remove-ExpiredQueueFiles
            $nextPrune = [DateTimeOffset]::UtcNow.AddSeconds(5)
        }
        foreach ($entry in @(Get-ChildItem -LiteralPath (Join-Path $root 'requests') -Filter '*.json' | Select-Object -First 64)) {
            if ($entry.BaseName -cnotmatch '^[0-9a-f]{32}$') { continue }
            $work = $entry.FullName + '.work'
            $result = Join-Path (Join-Path $root 'results') $entry.Name
            try {
                Assert-PrivatePath $entry.FullName $false
                if ($entry.Length -gt 65536) { throw 'oversized_request' }
                [IO.File]::Move($entry.FullName, $work)
                $request = [IO.File]::ReadAllText($work) | ConvertFrom-Json
                $deadline = Assert-Request $request
                if ($request.request_id -ne $entry.BaseName) { throw 'invalid_request' }
                $arguments = Get-WorkerArguments $entry.BaseName
                $child = Start-Process -FilePath $powerShell -ArgumentList $arguments -WindowStyle Hidden -PassThru
                $milliseconds = [Math]::Min(30000, [Math]::Max(1, ($deadline - [DateTimeOffset]::UtcNow).TotalMilliseconds))
                if (-not $child.WaitForExit([int]$milliseconds)) {
                    $child.Kill()
                    $child.WaitForExit()
                    if (-not (Test-Path -LiteralPath $result)) {
                        Write-PrivateFile $result ([Text.Encoding]::UTF8.GetBytes((@{request_id = $entry.BaseName; success = $false; error = 'desktop_timeout'} | ConvertTo-Json -Compress)))
                    }
                }
            } catch {
                if (-not (Test-Path -LiteralPath $result)) {
                    Write-PrivateFile $result ([Text.Encoding]::UTF8.GetBytes((@{request_id = $entry.BaseName; success = $false; error = 'desktop_failed'} | ConvertTo-Json -Compress)))
                }
            } finally {
                if ($child) {
                    if (-not $child.HasExited) { $child.Kill(); $child.WaitForExit() }
                    $child.Dispose()
                    $child = $null
                }
                if (Test-Path -LiteralPath $work) { Remove-Item -LiteralPath $work -Force }
            }
        }
        Start-Sleep -Milliseconds 50
    }
} finally {
    if ($child) {
        if (-not $child.HasExited) { $child.Kill(); $child.WaitForExit() }
        $child.Dispose()
    }
}
