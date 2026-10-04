param([Parameter(Mandatory=$true)][ValidatePattern('^[0-9a-f]{32}$')][string]$RequestID)
. (Join-Path $PSScriptRoot 'queue.ps1')
$response = @{request_id = $RequestID; success = $false; error = 'desktop_failed'}
$actionStarted = $false
try {
    Assert-Installed
    $path = Join-Path (Join-Path $root 'requests') ($RequestID + '.json.work')
    Assert-PrivatePath $path $false
    if ((Get-Item -LiteralPath $path).Length -gt 65536) { throw 'oversized_request' }
    $request = [IO.File]::ReadAllText($path) | ConvertFrom-Json
    $deadline = Assert-Request $request
    if ($request.request_id -ne $RequestID -or [Diagnostics.Process]::GetCurrentProcess().SessionId -eq 0) { throw 'invalid_session' }
    Assert-ConsoleSession
    Add-Type -Path (Join-Path $root 'native.cs')
    Add-Type -AssemblyName UIAutomationClient
    Add-Type -AssemblyName UIAutomationTypes
    Add-Type -AssemblyName System.Windows.Forms
    . (Join-Path $root 'actions.ps1')
    Assert-Deadline $deadline
    $actionStarted = $true
    $response = Invoke-DesktopAction $request
    $response.request_id = $RequestID
    $response.success = $true
} catch {
    # Only exact fixed native pre-effect failures prove a guarded write was rejected.
    $failure = $_.Exception
    while ($null -ne $failure.InnerException) { $failure = $failure.InnerException }
    $preEffect = @('invalid_clipboard_guard', 'clipboard_requires_sta', 'clipboard_unavailable', 'clipboard_conflict', 'clipboard_enumeration_failed', 'clipboard_inventory_incomplete', 'clipboard_allocation_failed', 'clipboard_encoding_failed', 'clipboard_owner_failed', 'expired_request', 'protected_desktop', 'invalid_console_session')
    if ($null -ne $request -and $request.action -ceq 'clipboard.set.guarded') {
        if (-not $actionStarted -or $preEffect -ccontains $failure.Message) { $response.error = 'clipboard_pre_effect_rejected' }
        else { $response.error = 'clipboard_possibly_cleared' }
    } elseif ($failure.Message -ceq 'clipboard_possibly_cleared') { $response.error = 'clipboard_possibly_cleared' }
    # A generic category is intentional: exceptions frequently contain guest data.
}
$result = Join-Path (Join-Path $root 'results') ($RequestID + '.json')
$temp = $result + '.tmp'
try {
    $data = [Text.Encoding]::UTF8.GetBytes(($response | ConvertTo-Json -Compress -Depth 12))
    if ($data.Length -gt 524288) { throw 'oversized_response' }
    Write-PrivateFile $temp $data
    [IO.File]::Move($temp, $result)
} finally {
    if (Test-Path -LiteralPath $temp) { Remove-Item -LiteralPath $temp -Force }
}
