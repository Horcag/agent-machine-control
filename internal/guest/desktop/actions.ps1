function Get-WindowHandle([string]$id, [string]$expected) {
    if ($id -cnotmatch '^[1-9][0-9]{0,18}$') { throw 'invalid_window' }
    $hwnd = [IntPtr]::new([long]::Parse($id))
    if (-not [AMCDesktop]::IsWindow($hwnd)) { throw 'missing_window' }
    $processID = [uint32]0
    [AMCDesktop]::GetWindowThreadProcessId($hwnd, [ref]$processID) | Out-Null
    $process = [Diagnostics.Process]::GetProcessById($processID)
    try {
        if ($process.SessionId -ne [Diagnostics.Process]::GetCurrentProcess().SessionId) { throw 'foreign_session' }
        $actual = $id + ':' + $processID + ':' + $process.StartTime.ToUniversalTime().Ticks
        if ($expected -and $actual -cne $expected) { throw 'stale_window' }
    } finally { $process.Dispose() }
    return $hwnd
}

function Get-Bounds($rectangle) {
    if ($rectangle.IsEmpty) { return @{left = 0; top = 0; width = 0; height = 0} }
    return @{left = [int]$rectangle.Left; top = [int]$rectangle.Top; width = [int]$rectangle.Width; height = [int]$rectangle.Height}
}

function Get-WindowInfo($hwnd) {
    $rect = [AMCDesktop+Rect]::new()
    if (-not [AMCDesktop]::GetWindowRect($hwnd, [ref]$rect)) { throw 'window_unavailable' }
    $processID = [uint32]0
    [AMCDesktop]::GetWindowThreadProcessId($hwnd, [ref]$processID) | Out-Null
    $state = 'normal'
    if ([AMCDesktop]::IsIconic($hwnd)) { $state = 'minimized' }
    elseif ([AMCDesktop]::IsZoomed($hwnd)) { $state = 'maximized' }
    $process = [Diagnostics.Process]::GetProcessById($processID)
    try { $windowIdentity = $hwnd.ToInt64().ToString() + ':' + $processID + ':' + $process.StartTime.ToUniversalTime().Ticks }
    finally { $process.Dispose() }
    return @{id = $hwnd.ToInt64().ToString(); identity = $windowIdentity; title = [AMCDesktop]::Title($hwnd); class_name = [AMCDesktop]::ClassName($hwnd); process_id = $processID; bounds = @{left = $rect.Left; top = $rect.Top; width = $rect.Right - $rect.Left; height = $rect.Bottom - $rect.Top}; state = $state}
}

function Focus-Window($hwnd) {
    if ([AMCDesktop]::IsIconic($hwnd)) { [AMCDesktop]::ShowWindowAsync($hwnd, 9) | Out-Null }
    [AMCDesktop]::SetForegroundWindow($hwnd) | Out-Null
    Start-Sleep -Milliseconds 50
    if ([AMCDesktop]::GetForegroundWindow() -ne $hwnd) { throw 'foreground_denied' }
}

function Get-Elements($hwnd) {
    $rootElement = [Windows.Automation.AutomationElement]::FromHandle($hwnd)
    $walker = [Windows.Automation.TreeWalker]::ControlViewWalker
    $queue = [Collections.Generic.Queue[object]]::new()
    $queue.Enqueue(@{element = $rootElement; depth = 0})
    $elements = [Collections.Generic.List[object]]::new()
    while ($queue.Count -gt 0 -and $elements.Count -lt 256) {
        if ([DateTimeOffset]::UtcNow -ge $deadline) { throw 'expired_request' }
        $item = $queue.Dequeue()
        $element = $item.element
        $properties = $element.Current
        $name = $properties.Name
        if ($properties.IsPassword) { $name = '' }
        if ($name.Length -gt 256) { $name = $name.Substring(0, 256) }
        $patterns = @($element.GetSupportedPatterns() | ForEach-Object { $_.ProgrammaticName.Replace('PatternIdentifiers.Pattern', '') })
        $elements.Add(@{id = ($element.GetRuntimeId() -join ':'); name = $name; automation_id = $properties.AutomationId.Substring(0, [Math]::Min(256, $properties.AutomationId.Length)); control_type = $properties.ControlType.ProgrammaticName; bounds = (Get-Bounds $properties.BoundingRectangle); enabled = $properties.IsEnabled; offscreen = $properties.IsOffscreen; patterns = $patterns; reference = $element})
        if ($item.depth -ge 8) { continue }
        $child = $walker.GetFirstChild($element)
        while ($child -and $queue.Count + $elements.Count -lt 256) {
            $queue.Enqueue(@{element = $child; depth = $item.depth + 1})
            $child = $walker.GetNextSibling($child)
        }
    }
    return $elements.ToArray()
}

function Get-GuestCursor {
    $cursor = [AMCDesktop+CursorInfo]::new()
    $cursor.Size = [Runtime.InteropServices.Marshal]::SizeOf($cursor)
    if (-not [AMCDesktop]::GetCursorInfo([ref]$cursor)) { throw 'cursor_unavailable' }
    return @{x = $cursor.Position.X; y = $cursor.Position.Y; visible = ($cursor.Flags -band 1) -ne 0}
}

function Invoke-DesktopAction($request) {
    Assert-ConsoleSession
    $sessionID = [Diagnostics.Process]::GetCurrentProcess().SessionId
    $elevated = [Security.Principal.WindowsPrincipal]::new($identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    $response = @{session_id = $sessionID; elevated = $elevated}
    if ($request.action -eq 'status') { return $response }
    if (-not [AMCDesktop]::InteractiveDesktop()) { throw 'protected_desktop' }
    if ($request.action -eq 'cursor') {
        $response.cursor = Get-GuestCursor
        return $response
    }
    if ($request.action -eq 'windows') {
        $response.windows = @([AMCDesktop]::Windows() | ForEach-Object { Get-WindowInfo $_ })
        $response.cursor = Get-GuestCursor
        return $response
    }
    if ($request.action -eq 'clipboard.get') {
        $text = [Windows.Forms.Clipboard]::GetText()
        if ($text.Length -gt 4096) { throw 'oversized_clipboard' }
        $response.text = $text
        return $response
    }
    if ($request.action -eq 'clipboard.set') {
        if ($request.text.Length -gt 4096) { throw 'oversized_clipboard' }
        if ($request.text -eq '') { [Windows.Forms.Clipboard]::Clear() }
        else { [Windows.Forms.Clipboard]::SetText([string]$request.text) }
        return $response
    }
    if ($request.action -eq 'launch') {
        if ($request.executable -notmatch '^[A-Za-z]:\\.*\.exe$' -or $request.executable.Length -gt 1024 -or @($request.arguments).Count -gt 16) { throw 'invalid_application' }
        $info = [Diagnostics.ProcessStartInfo]::new()
        $info.FileName = $request.executable
        $info.UseShellExecute = $false
        $info.Arguments = (@($request.arguments | ForEach-Object { [AMCDesktop]::Quote([string]$_) }) -join ' ')
        $process = [Diagnostics.Process]::Start($info)
        try { $response.process_id = $process.Id } finally { $process.Dispose() }
        return $response
    }
    if ($request.action -ne 'uia.tree' -and -not $request.window_identity) { throw 'missing_window_identity' }
    $hwnd = Get-WindowHandle $request.window_id $request.window_identity
    switch ($request.action) {
        'window.focus' { Focus-Window $hwnd }
        'window.move' {
            if ([Math]::Abs([long]$request.x) -gt 32768 -or [Math]::Abs([long]$request.y) -gt 32768) { throw 'invalid_bounds' }
            if (-not [AMCDesktop]::SetWindowPos($hwnd, [IntPtr]::Zero, $request.x, $request.y, 0, 0, 0x0015)) { throw 'window_failed' }
        }
        'window.resize' {
            if ($request.width -lt 1 -or $request.width -gt 16384 -or $request.height -lt 1 -or $request.height -gt 16384) { throw 'invalid_bounds' }
            if (-not [AMCDesktop]::SetWindowPos($hwnd, [IntPtr]::Zero, 0, 0, $request.width, $request.height, 0x0016)) { throw 'window_failed' }
        }
        'window.close' { if (-not [AMCDesktop]::PostMessage($hwnd, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero)) { throw 'window_failed' } }
        'window.minimize' { if (-not [AMCDesktop]::ShowWindowAsync($hwnd, 6)) { throw 'window_failed' } }
        'window.maximize' { if (-not [AMCDesktop]::ShowWindowAsync($hwnd, 3)) { throw 'window_failed' } }
        'window.restore' { if (-not [AMCDesktop]::ShowWindowAsync($hwnd, 9)) { throw 'window_failed' } }
        'scroll' {
            if ($request.delta -eq 0 -or [Math]::Abs([long]$request.delta) -gt 1200) { throw 'invalid_scroll' }
            $window = Get-WindowInfo $hwnd
            $bounds = $window.bounds
            if ($request.x -lt $bounds.left -or $request.y -lt $bounds.top -or $request.x -ge $bounds.left + $bounds.width -or $request.y -ge $bounds.top + $bounds.height) { throw 'invalid_pointer' }
            Focus-Window $hwnd
            if ($request.axis -and $request.axis -notin @('horizontal', 'vertical')) { throw 'invalid_axis' }
            if (-not [AMCDesktop]::SetCursorPos($request.x, $request.y) -or -not [AMCDesktop]::Wheel($request.delta, $request.axis -eq 'horizontal')) { throw 'input_failed' }
        }
        { $_ -in @('uia.tree', 'uia.invoke', 'uia.setvalue', 'uia.select', 'uia.toggle', 'uia.expand', 'uia.collapse', 'uia.scroll') } {
            $elements = @(Get-Elements $hwnd)
            if ($request.action -eq 'uia.tree') {
                foreach ($element in $elements) { $element.Remove('reference') }
                $response.elements = $elements
            } else {
                $matches = @($elements | Where-Object { $_.id -eq $request.element_id })
                if ($matches.Count -ne 1) { throw 'stale_element' }
                $element = $matches[0].reference
                if (-not $element.Current.IsEnabled -or $element.Current.IsPassword) { throw 'element_unavailable' }
                Invoke-ElementPattern $element $request
            }
        }
        default { throw 'unsupported_action' }
    }
    return $response
}

function Invoke-ElementPattern($element, $request) {
    switch ($request.action) {
        'uia.invoke' { $element.GetCurrentPattern([Windows.Automation.InvokePattern]::Pattern).Invoke() }
        'uia.select' { $element.GetCurrentPattern([Windows.Automation.SelectionItemPattern]::Pattern).Select() }
        'uia.toggle' { $element.GetCurrentPattern([Windows.Automation.TogglePattern]::Pattern).Toggle() }
        'uia.expand' { $element.GetCurrentPattern([Windows.Automation.ExpandCollapsePattern]::Pattern).Expand() }
        'uia.collapse' { $element.GetCurrentPattern([Windows.Automation.ExpandCollapsePattern]::Pattern).Collapse() }
        'uia.setvalue' {
            if ($request.text.Length -gt 4096) { throw 'oversized_value' }
            $pattern = $element.GetCurrentPattern([Windows.Automation.ValuePattern]::Pattern)
            if ($pattern.Current.IsReadOnly) { throw 'element_readonly' }
            $pattern.SetValue([string]$request.text)
        }
        'uia.scroll' {
            if ($request.delta -eq 0 -or [Math]::Abs([long]$request.delta) -gt 10 -or ($request.axis -and $request.axis -notin @('horizontal', 'vertical'))) { throw 'invalid_scroll' }
            $pattern = $element.GetCurrentPattern([Windows.Automation.ScrollPattern]::Pattern)
            $amount = [Windows.Automation.ScrollAmount]::SmallIncrement
            if ($request.delta -lt 0) { $amount = [Windows.Automation.ScrollAmount]::SmallDecrement }
            for ($index = 0; $index -lt [Math]::Abs($request.delta); $index++) {
                if ([DateTimeOffset]::UtcNow -ge $deadline) { throw 'expired_request' }
                if ($request.axis -eq 'horizontal') { $pattern.Scroll($amount, [Windows.Automation.ScrollAmount]::NoAmount) }
                else { $pattern.Scroll([Windows.Automation.ScrollAmount]::NoAmount, $amount) }
            }
        }
        default { throw 'unsupported_pattern' }
    }
}
