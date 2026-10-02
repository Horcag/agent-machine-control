# Guest desktop control

AMC combines a native Hyper-V console with an optional elevated Windows guest driver.
All mouse, keyboard, window and clipboard operations target the enrolled VM. Opening
VMConnect or moving the operator's host cursor is unnecessary.

## Enable a bounded lab session

A configured, verified rollback checkpoint is required. The enrolled SSH account must
be an administrator and have an interactive Windows session under the same account.
The guest driver runs in that interactive session through a highest-privilege scheduled
task. SSH itself can remain in session zero.

The operator enables an agent session once:

```sh
amc desktop enable --for-mcp --valid-for 8h \
  --acknowledge-external-effects \
  --reason 'exercise the enrolled disposable Windows lab' \
  --idempotency-key lab-session-example
```

Omit `--for-mcp` for an operator CLI session. The acknowledgement records that a VM
checkpoint cannot undo network or other external effects; it does not declare the
machine contained. A grant binds the beneficiary, exact enrolled target and enrollment
publication to an expiry. Revocation and an enrollment change fence new desktop actions.
Every action still records a reason, exact retry key, deadline and redacted receipt.
Only the operator can issue or revoke a grant. MCP agents discover their own active grant.

```sh
amc desktop status --lab-grant-id GRANT_ID
amc desktop disable --lab-grant-id GRANT_ID \
  --reason 'finish this lab session' --idempotency-key lab-session-end-example
```

Issuing a grant does not install the guest driver. Provision it using `desktop_act` with
`action: {"action":"provision"}`, a fresh retry key and a deadline within one minute.
A driver update must remove the old verified installation with its matching build before
provisioning the new build. Foreign or modified installations are refused.

## Agent interaction loop

1. Call `desktop_observe`. It returns a PNG content block, frame identity, native and
   image dimensions, capture time, window identities and guest cursor when the helper
   is available. Optional `window_id` and `window_identity` request its UIA tree.
2. Choose a semantic `action` or native console `input` in `desktop_act`, never both.
   Supply a concrete reason, retry key and RFC3339 deadline. `observe_after: true`
   attaches a fresh PNG and observation after success.
3. Verify the intended effect from the new image, UI tree, application state or terminal
   output. A successful input receipt does not prove a button performed its purpose.
4. After a transport failure, observe before deciding to retry. Reuse the exact payload,
   retry key and deadline only for an exact retry. A cached success returns its receipt
   without replaying the operation or retaining private response data.

Semantic actions include:

| Action | Additional fields |
| --- | --- |
| `status`, `cursor`, `windows`, `clipboard.get` | No window required |
| `provision`, `remove` | Guest driver lifecycle; mutation authority required |
| `window.focus`, `window.close`, `window.minimize`, `window.maximize`, `window.restore` | `window_id`, `window_identity` |
| `window.move` | Window identity and native `x`, `y` |
| `window.resize` | Window identity and positive `width`, `height` |
| `uia.tree` | `window_id`; pass `window_identity` to reject a stale window |
| `uia.invoke`, `uia.select`, `uia.toggle`, `uia.expand`, `uia.collapse` | Window identity and observed `element_id` |
| `uia.setvalue` | Window identity, element and `text` |
| `uia.scroll` | Window identity, element, `axis` and signed `delta` in small increments |
| `scroll` | Window identity, native `x`, `y`, axis and signed Windows wheel units |
| `clipboard.set` | Bounded guest `text`; empty text clears the guest clipboard |
| `launch` | Absolute Windows `.exe` path and separate `arguments` array |

Window identities bind HWND to process ID and process start time. They detect a
different process lifetime, but cannot prove that the same process has not recycled
its HWND. Re-observe after a window disappears or an application restarts. UIA trees are bounded to 256 elements and
8 levels. Unsupported patterns, password controls and unavailable desktops are refused.
Use the native PNG/input fallback for custom controls without useful accessibility data.

Native `input` supports `key`, `type`, `move`, `click` and `drag`. Pointer actions require
an observed `frame_id`; coordinates use that PNG's pixels. A click supports `button`
(`left`, `right`, `middle`) and `count: 2` for double click. Pointer modifiers use a
bounded chord such as `ctrl+shift`. A drag uses `x`, `y`, `to_x`, `to_y` and `button`.
Keyboard chords use `key`, for example `ctrl+a`; `type` accepts Unicode text. Buttons and
modifiers are released on completion and bounded cleanup after failure. Native wheel
input is not advertised; use the interactive driver instead.

Semantic coordinates use native guest screen pixels. Do not mix them with scaled PNG
coordinates. Fresh native dimensions are checked before a frame-bound pointer mutation.

## CLI, recording and recovery

```sh
amc desktop observe windows
amc desktop action --request-file action.json --lab-grant-id GRANT_ID \
  --reason 'focus the observed fixture' --idempotency-key focus-example
amc console record --output recording.gif --width 640 --height 480 \
  --frames 8 --interval-ms 250
amc --direct console screenshot --output recovery.png
```

`action.json` contains a strict `DesktopRequest`, including its 32-character lowercase
hex `request_id` and future RFC3339 `deadline`. Unknown fields and extra JSON values are
rejected. Native console and recording also work through `--direct` without the daemon,
SSH or guest helper.

Guest exchanges use the earliest caller deadline or a 25-second transport budget.
The guest's unchanged 35-second admission limit provides nominal ten-second clock
headroom; it does not synchronize clocks. Host and guest UTC must remain reasonably
aligned. Greater or changing skew can still cause refusal or leave guest work valid
after host expiry. The bootstrap validates identity and expiry before loading its
program, and the helper checks expiry before later mutations. These checks prevent
starting the next guarded mutation after expiry; an operation already in progress
may finish. Cancelling SSH does not prove remote exit or roll back an effect already
completed.

`console_record` returns an animated GIF and metadata. Capture is bounded to 2–30 frames,
100–2000 ms intervals, 30 seconds of requested intervals and 307200 pixels per frame.
Frame capture times determine playback delays. This is a screenshot sequence, not a
continuous high-frame-rate video or an audio recording. PNG/GIF CLI files are created
exclusively with private permissions; partial task-owned files are removed after failure.

The existing terminal session tools provide deterministic command execution and streaming
output. `launch` opens a visible guest application or graphical terminal; it does not
pretend that starting a terminal proves a command ran successfully.

The guest helper needs an ordinary unlocked interactive desktop. It is not a universal
UAC, logon or lock-screen injector. Native hypervisor observation/input remains the
independent recovery path; supported protected-desktop effects require live VM evidence.
No login policy or UAC setting is disabled by provisioning.

The guest response queue uses private HIGH-integrity files and publishes complete
responses atomically. Readers retain a completed response until the dispatcher has
observed worker completion. Completed responses and failed staging files become
eligible for protected pruning after two minutes. The running server prunes every
five seconds; later transport calls also prune. This is cleanup eligibility, not a
strict deletion timer when the helper is stopped. Sensitive response data stays out
of receipts and the application retry cache.

For accepted states, limitations and upstream comparisons, see the
[capability matrix](desktop-capability-matrix.md).
