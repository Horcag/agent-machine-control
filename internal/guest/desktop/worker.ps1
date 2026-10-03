param([Parameter(Mandatory=$true)][ValidatePattern('^[0-9a-f]{32}$')][string]$RequestID)
. (Join-Path $PSScriptRoot 'queue.ps1')
$response = @{request_id = $RequestID; success = $false; error = 'desktop_failed'}
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
    $response = Invoke-DesktopAction $request
    $response.request_id = $RequestID
    $response.success = $true
} catch {
    # Preserve only this fixed redacted partial-write category, never exception text.
    if ($_.Exception.ToString().Contains('clipboard_possibly_cleared')) { $response.error = 'clipboard_possibly_cleared' }
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
