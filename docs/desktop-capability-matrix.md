# Desktop control capability and acceptance matrix

This document defines the bounded Windows VM desktop target and the evidence needed to call it ready. Upstream comparison was retrieved on 2026-10-02. Capability descriptions are design targets, not installed-host acceptance. An API success response alone does not prove the intended application changed.

## Execution approaches

The model is a planner: it interprets images, UI elements, and terminal output and chooses operations. The executor captures state and applies validated operations. Reusing an executor does not reproduce proprietary model training. AMC retains machine identity, authorization, actor, reason, deadline, idempotency, and redacted receipts; optional tools cannot become that authority.

| Approach | Useful scope | Integration and license | Material limits |
| --- | --- | --- | --- |
| Native Hyper-V WMI | VM framebuffer capture and VM-addressed input; recovery independent of guest SSH/UI automation | Windows platform API; implement behind AMC backend capabilities, with PNG encoding in AMC | Thumbnail capture is image observation, not a UI tree or desktop video stream. Provider return values need decoding and effect verification. See [thumbnail API][thumbnail]. |
| Guest Win32/UIA helper | Window identity/geometry, guest cursor, input, accessibility properties and supported patterns | AMC-owned narrow helper in the interactive guest session, reached over authenticated SSH; no added automation dependency required | UIA depends on application providers. Custom/legacy controls can lack useful patterns. Input has session, foreground, and integrity constraints. See [UIA][uia] and [SendInput][sendinput]. |
| Cua Driver | Cross-platform native automation reference or optional guest executor; MCP/CLI and native C ABI | Default Driver is MIT; Rust platform code, Python/TypeScript bindings. Preserve notices if copying. Current source metadata declares 0.32.0, not proof of a released installation | Windows requires an interactive graphical session; background action support varies by toolkit. Optional perception includes AGPL code. See [Driver][driver], [support ledger][driver-support], and [licenses][cua-licenses]. |
| Windows-MCP | Guest screenshot, UI tree, mouse/keyboard, windows, clipboard and process tooling | MIT Python sidecar; MCP transport. Copy selected ideas/code only after per-file/dependency license review | Broad tool access must be narrowed by AMC policy. Screenshot scaling requires coordinate conversion. Guest installation/session is required. See [tools][windows-mcp] and [capture implementation][windows-capture]. |
| pywinauto | Windows Win32/UIA automation reference | BSD-3-Clause Python library; optional reference rather than mandatory runtime | Locked/disconnected RDP desktops restrict physical input; some direct control methods behave differently. See [remote execution][pywinauto-remote] and [license][pywinauto-license]. |
| Playwright | Browser DOM operations, browser screenshots and page video | Apache-2.0 browser automation; optional browser-specific executor | Browser coverage does not prove desktop, logon, UAC, or native application coverage. Video is finalized when the browser context closes. See [video][playwright-video] and [license][playwright-license]. |
| WinAppDriver | Selenium-style UWP/WinForms/WPF/Win32 testing reference | Repository source is MIT; server installer/runtime remains a separate distribution to assess | Upstream README specifies Windows 10, Developer Mode and a running server; this is not evidence of all newer guest versions. UI Recorder generates UI test selectors/code, not desktop video. See [README][winappdriver] and [license][winappdriver-license]. |

Recommendation: keep the native VM console path independent, add semantic guest Win32/UIA operations for a verified interactive session, and retain terminal execution for tasks with a reliable command interface. Cua Driver and Windows-MCP are useful references or optional adapters; neither is a reason to replace AMC policy or mandate a cloud VM service. Current Cua sandbox screen/mouse interfaces need reachable `cua-spacesd`, which is FSL-1.1-MIT rather than the default MIT license; see [sandbox README][sandbox] and [license map][cua-licenses].

## Capability and acceptance matrix

N = native VM console fallback; G = interactive guest helper; T = authenticated guest terminal. “Pending” is an evidence placeholder, not a failure or a claim of missing implementation. Record exact backend/build, synthetic VM identity, session/integrity state, request receipt, expected result, observed result, and private artifact digest for each acceptance run.

| Operation | Primary route and fallback | Contract and session limits | Concrete acceptance check | Evidence |
| --- | --- | --- | --- | --- |
| Screenshot / PNG | N; G for guest desktop/window capture | Report capture time, dimensions, source, coordinate space and scaling; no fake UI tree from pixels | Decode PNG, verify expected dimensions and fresh synthetic marker; test resolution change, stopped VM, and malformed provider output | Pending |
| Cursor observation and movement | G observes guest cursor; N/G moves it | Distinguish guest cursor position from cursor rendered into image; never substitute host cursor | Move to two bounded guest points, read back guest position; verify host cursor and foreground window unchanged | Pending |
| Click / right / double click | N pixel fallback; G semantic action when supported | Validate coordinates/buttons/count; use current observation and target identity | Synthetic app increments distinct counters for each gesture; reject out-of-bounds/stale targets without effects | Pending |
| Drag and button release | N/G | Bounded path/duration; release held buttons on completion, timeout and cancellation | Move synthetic item start-to-end; cancel mid-drag, then verify button released and next click works | Pending |
| Vertical / horizontal wheel | G; N only if advertised | Declare supported axes and units; unsupported native wheel returns a capability refusal | Scroll fixture with measured before/after offset in each axis; reject unsupported axis without silent substitution | Pending |
| Text / Unicode / key chords | N/G; T for terminal text | Separate text from key presses; release modifiers; disclose layout/IME limitations | Round-trip Latin, Cyrillic, combining text and supplementary character; prove Ctrl+A and Ctrl+S effects, then no held modifier | Pending |
| Window inventory and lifecycle | G; N visual fallback | Stable window identity plus process identity; enumerate, activate, move, resize, minimize, restore, close; close can discard user work | Two synthetic windows with duplicate titles; operate on exact identity, read geometry/state; refuse vanished/reused handle | Pending |
| UI tree and element operations | G UIA; N screenshot/input fallback | Bounded tree and supported patterns; no assumption every legacy/custom control exposes UIA | Fixture exposes button/edit/selection/scroll patterns; invoke and read state; custom canvas returns explicit absence with screenshot fallback | Pending |
| Clipboard read/write | G; T only in verified interactive session | Guest/session scope, bounded payload and redaction; no host clipboard synchronization requirement | Synthetic Unicode value round-trip; verify host clipboard unchanged without logging its contents; restore fixture clipboard | Pending |
| GUI terminals and command execution | G for visible terminal; T for deterministic command path; N visual recovery | Return exit status/stdout/stderr and deadline; opening a GUI terminal is distinct from executing a shell command | Launch fixture terminal, run bounded command, verify output/exit; timeout exact owned child process and retain receipt | Pending |
| Process and application launch | G/T; N Start-menu fallback | Separated executable/argv/cwd; exact PID identity; termination requires task ownership | Launch synthetic app with spaced/non-ASCII argv; verify PID/path; reject unrelated PID; clean only owned process | Pending |
| Elevated launch / admin app | G/T with separately established authority; N console recovery | Ordinary SendInput cannot inject into higher-integrity apps; SSH identity alone does not prove GUI session/integrity | Disposable elevated fixture with rollback: establish actual token/integrity, verify permitted action or explicit refusal; no credential recording | Pending |
| UAC secure desktop / logon / lock | N observation/input only when empirically supported; G must advertise actual desktop scope | Normal guest desktop helper is not universal secure-desktop authority; no bypass of UAC or login policy | Disposable guest: inspect locked/logon/UAC states, prove exact supported operations or explicit refusal; preserve host state and redact all secrets | Pending |
| Recording | N bounded screenshot sequence; G optional capture; Playwright only for browser video | Declare frame sequence versus encoded video, interval, dimensions, duration/size cap, cursor mode, dropped frames and timestamps | Record fixture animation, decode every frame or video, verify ordering/timing and finalization; stop on deadline/cancel and remove owned temporary frames | Pending |
| Reconnect / unattended operation | N independent recovery; G/T reconnect with fresh session proof | Unattended means no host interaction within declared guest state; it does not imply locked/logon/UAC support. Never replay uncertain mutation automatically | Drop transport after dispatch; inspect state before retry; reject stale session/window/capture identities; resume bounded observation after reconnect | Pending |
| No host impact / concurrency | All routes | Exact VM GUID/session ownership; per-target mutation serialization; bounded lab authority; no host GUI input | Capture host foreground/cursor before/after, run two synthetic targets and prove isolation; cancellation releases guest inputs and leaves unrelated writers untouched | Pending |

The project status supplied for this planning slice is native capture plus five input primitives implemented. That statement needs linked tests and installed-host results before any corresponding row becomes accepted. Guest UIA, recordings, ergonomic workflows, and lab authority are separate integration lanes. Fill evidence from the integrated build; do not infer acceptance from this document or an upstream project test.

## Validation gates and stop condition

1. Unit and contract tests prove parameter validation, capability refusal, provider decoding, idempotency/deadlines and cleanup. They do not prove guest effects.
2. A disposable graphical fixture proves state changes for every supported row, including negative/session cases. Record exact rollback and task ownership before destructive or privileged tests.
3. Independent security/architecture review checks the privileged transport, interactive-session identity, command injection resistance, bounded artifacts and recovery independence.
4. Installed-host acceptance supplies compact receipts and private artifact hashes. No guest images, private inventory, keys or unredacted transcripts enter the repository.
5. Complete cleanup on success/failure: release keys/buttons, finalize recording, stop only verified owned writers, remove owned scripts/temporary artifacts, preserve required rollback and report actual remaining disk usage.

Ready means each required row is accepted on the declared Windows/session matrix or explicitly constrained with a verified refusal and documented recovery path. “Human-like full control” without a session/application boundary is not an acceptance criterion. Stop after this finite matrix passes; optional toolkit adapters require a concrete missing capability rather than speculative dependency expansion.

## Primary sources

[thumbnail]: https://learn.microsoft.com/en-us/windows/win32/hyperv_v2/getvirtualsystemthumbnailimage-msvm-virtualsystemmanagementservice
[uia]: https://learn.microsoft.com/en-us/windows/win32/winauto/uiauto-uiautomationoverview
[sendinput]: https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput
[driver]: https://github.com/trycua/cua/blob/main/libs/cua-driver/README.md
[driver-support]: https://github.com/trycua/cua/blob/main/libs/cua-driver/docs/action-support.md
[cua-licenses]: https://github.com/trycua/cua/blob/main/LICENSING.md
[sandbox]: https://github.com/trycua/cua/blob/main/libs/python/cua-sandbox/README.md
[windows-mcp]: https://github.com/CursorTouch/Windows-MCP/blob/main/README.md
[windows-capture]: https://github.com/CursorTouch/Windows-MCP/blob/main/src/windows_mcp/desktop/screenshot.py
[pywinauto-remote]: https://github.com/pywinauto/pywinauto/blob/master/docs/remote_execution.md
[pywinauto-license]: https://github.com/pywinauto/pywinauto/blob/master/LICENSE
[playwright-video]: https://playwright.dev/docs/videos
[playwright-license]: https://github.com/microsoft/playwright/blob/main/LICENSE
[winappdriver]: https://github.com/microsoft/WinAppDriver/blob/master/README.md
[winappdriver-license]: https://github.com/microsoft/WinAppDriver/blob/master/LICENSE
