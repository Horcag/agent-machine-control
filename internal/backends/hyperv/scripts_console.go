package hyperv

// Console scripts accept data only through a base64 JSON environment value.
// WMI associations start from the exact guest GUID; no host UI APIs are used.
const scriptConsolePrelude = `
$ErrorActionPreference = 'Stop'
$r = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($env:AMC_CONSOLE_REQUEST)) | ConvertFrom-Json
$id = ([guid]::Parse($r.vm_id)).ToString()
$ns = 'root\virtualization\v2'
$vm = @(Get-WmiObject -Namespace $ns -Class Msvm_ComputerSystem -Filter "Name='$id'")
if ($vm.Count -ne 1 -or $vm[0].Name -ne $id) { throw 'Guest unavailable' }
$vm = $vm[0]
function GuestDevice($class) {
    $devices = @($vm.GetRelated($class, 'Msvm_SystemDevice', $null, $null, $null, $null, $false, $null))
    if ($devices.Count -ne 1 -or $devices[0].SystemName -ne $id) { throw 'Guest device unavailable' }
    return $devices[0]
}
function RequireSuccess($result) {
    if ([uint32]$result.ReturnValue -ne 0) { throw 'Guest method failed' }
}
`

const ScriptConsoleCapture = scriptConsolePrelude + `
try {
    $w = [int]$r.width; $h = [int]$r.height
    if ($w -lt 1 -or $h -lt 1 -or $w -gt 65535 -or $h -gt 65535 -or [long]$w*$h -gt 1048576) { throw 'Invalid dimensions' }
    $head = GuestDevice 'Msvm_VideoHead'
    $settings = @($vm.GetRelated('Msvm_VirtualSystemSettingData', 'Msvm_SettingsDefineState', $null, $null, $null, $null, $false, $null))
    $service = @(Get-WmiObject -Namespace $ns -Class Msvm_VirtualSystemManagementService)
    if ($settings.Count -ne 1 -or $service.Count -ne 1) { throw 'Guest settings unavailable' }
    $result = $service[0].GetVirtualSystemThumbnailImage($settings[0].__PATH, [uint16]$w, [uint16]$h)
    RequireSuccess $result
    if ($result.ImageData.Length -ne ($w*$h*2) -and $result.ImageData.Length -ne ($w*$h*2+4)) { throw 'Invalid image' }
    @{ success=$true; vm_id=$id; width=$w; height=$h; native_width=[int]$head.CurrentHorizontalResolution; native_height=[int]$head.CurrentVerticalResolution; rgb565=[Convert]::ToBase64String([byte[]]$result.ImageData) } | ConvertTo-Json -Compress
} catch {
    @{ success=$false; vm_id=$id } | ConvertTo-Json -Compress
}
`

const scriptConsoleRelease = `
function ReleaseGuestInput {
    $failed = $false
    if ($null -ne $keyboard) {
        for ($index=$held.Count-1; $index -ge 0; $index--) {
            try { RequireSuccess ($keyboard.ReleaseKey([uint32]$held[$index])) } catch { $failed=$true }
        }
    }
    if ($null -ne $mouse -and $buttonHeld) {
        try { RequireSuccess ($mouse.SetButtonState([uint32]$button, $false)) } catch { $failed=$true }
    }
    if ($failed) { throw 'Guest release failed' }
}
`

const ScriptConsoleInput = scriptConsolePrelude + scriptConsoleRelease + `
$keyboard=$null; $mouse=$null; $held=@(); $buttonHeld=$false; $button=0; $ok=$false
try {
    $input = $r.input
    if ($input.kind -ne 'key' -and @($r.keys).Count -gt 0) {
        $keyboard = GuestDevice 'Msvm_Keyboard'
        foreach ($key in $r.keys) { $held += [uint32]$key; RequireSuccess ($keyboard.PressKey([uint32]$key)) }
    }
    switch ($input.kind) {
        'type' {
            $keyboard = GuestDevice 'Msvm_Keyboard'
            RequireSuccess ($keyboard.TypeText([string]$input.text))
        }
        'key' {
            $keyboard = GuestDevice 'Msvm_Keyboard'
            foreach ($key in $r.keys) {
                $held += [uint32]$key
                RequireSuccess ($keyboard.PressKey([uint32]$key))
            }
        }
        { $_ -in @('move','click','drag') } {
            $mouse = GuestDevice 'Msvm_SyntheticMouse'
            $head = GuestDevice 'Msvm_VideoHead'
            $x=[int]$input.x; $y=[int]$input.y
            if ($x -lt 0 -or $y -lt 0 -or $x -ge $head.CurrentHorizontalResolution -or $y -ge $head.CurrentVerticalResolution) { throw 'Invalid position' }
            if ($input.kind -eq 'drag' -and ([int]$input.to_x -lt 0 -or [int]$input.to_y -lt 0 -or [int]$input.to_x -ge $head.CurrentHorizontalResolution -or [int]$input.to_y -ge $head.CurrentVerticalResolution)) { throw 'Invalid position' }
            RequireSuccess ($mouse.SetAbsolutePosition($x,$y))
            if ($input.kind -ne 'move') {
                $buttons=@{left=1;right=2;middle=3}
                if (-not $buttons.ContainsKey([string]$input.button)) { throw 'Invalid button' }
                $button=$buttons[[string]$input.button]
                $buttonHeld=$true
                RequireSuccess ($mouse.SetButtonState([uint32]$button,$true))
                if ($input.kind -eq 'drag') {
                    for ($step=1; $step -le 20; $step++) {
                        RequireSuccess ($mouse.SetAbsolutePosition([int]($x+([int]$input.to_x-$x)*$step/20),[int]($y+([int]$input.to_y-$y)*$step/20)))
                        Start-Sleep -Milliseconds 20
                    }
                }
                if ($input.kind -eq 'click' -and [int]$input.count -eq 2) {
                    RequireSuccess ($mouse.SetButtonState([uint32]$button,$false)); $buttonHeld=$false
                    Start-Sleep -Milliseconds 100
                    $buttonHeld=$true; RequireSuccess ($mouse.SetButtonState([uint32]$button,$true))
                }
            }
        }
        default { throw 'Unsupported guest action' }
    }
    $ok=$true
} catch { $ok=$false } finally {
    try { ReleaseGuestInput } catch { $ok=$false }
}
@{success=$ok;vm_id=$id} | ConvertTo-Json -Compress
`

// A separate bounded release attempt covers process termination before finally.
const ScriptConsoleCleanup = scriptConsolePrelude + scriptConsoleRelease + `
$keyboard=$null; $mouse=$null; $held=@(); $buttonHeld=$false; $button=0
if (@($r.keys).Count -gt 0) { $keyboard=GuestDevice 'Msvm_Keyboard'; $held=@($r.keys) }
if ($r.input.kind -in @('click','drag')) {
    $mouse=GuestDevice 'Msvm_SyntheticMouse'
    $buttons=@{left=1;right=2;middle=3}; $button=$buttons[[string]$r.input.button]; $buttonHeld=$true
}
ReleaseGuestInput
@{success=$true;vm_id=$id} | ConvertTo-Json -Compress
`
