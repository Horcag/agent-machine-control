. (Join-Path $PSScriptRoot 'queue.ps1')
Assert-Installed
if ([Diagnostics.Process]::GetCurrentProcess().SessionId -eq 0) { exit 1 }
$expiry = [DateTimeOffset]::UtcNow.AddMinutes(20)
$child = $null
try {
    while ([DateTimeOffset]::UtcNow -lt $expiry) {
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
                $arguments = '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -STA -WindowStyle Hidden -File "' + (Join-Path $root 'worker.ps1') + '" -RequestID ' + $entry.BaseName
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
        # Responses are short-lived private evidence; no persistent guest transcripts.
        foreach ($entry in @(Get-ChildItem -LiteralPath (Join-Path $root 'results') -Filter '*.json')) {
            if ($entry.BaseName -cmatch '^[0-9a-f]{32}$' -and $entry.LastWriteTimeUtc -lt [DateTime]::UtcNow.AddMinutes(-2)) {
                Remove-Item -LiteralPath $entry.FullName -Force
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
