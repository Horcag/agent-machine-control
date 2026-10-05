# Desktop control capability and acceptance matrix

This document defines the bounded Windows VM desktop target and the evidence needed to call it ready. Upstream comparison was retrieved on 2026-10-02. Capability descriptions are design targets, not installed-host acceptance. An API success response alone does not prove the intended application changed.

## Execution approaches

The model is a planner: it interprets images, UI elements, and terminal output and chooses operations. The executor captures state and applies validated operations. Reusing an executor does not reproduce proprietary model training. AMC retains machine identity, authorization, actor, reason, deadline, idempotency, and redacted receipts; optional tools cannot become that authority.

### Public model protocols and reusable code

OpenAI's [Computer Use API][openai-cu] asks the application to execute structured computer actions or model-generated interface code, then return a screenshot observation. The structured path pairs `computer_call_output.call_id` with the issued call and carries `output.type = computer_screenshot`; its documented PNG transport is `image_url = data:image/png;base64,...`. Generating a call is distinct from delivering its effect. These public API contracts do not establish how proprietary Codex desktop executors are implemented internally.

Anthropic's [computer-use protocol][anthropic-cu] likewise leaves execution in the caller's environment. The application returns one `tool_result` per `tool_use`, with screenshot content represented by an `image` block whose `source` has `type = base64`, `media_type = image/png`, and encoded `data`. Ordered batches stop after a failure and return errors for skipped actions. This comparison describes public execution protocols.

PNG bytes become an image observation when the client supplies the appropriate image block; base64 is a transport encoding, not OCR or a UI tree. Accessibility structure is a separate observation from UIA or another provider. AMC can reuse the capture, validate, execute, observe-again pattern without adding either model SDK. The [OpenAI sample executor][openai-sample] and [Anthropic reference demo][anthropic-sample] are MIT-licensed examples; preserve their notices and check copied dependencies. Their availability does not grant source access to proprietary model weights or Codex executor internals.

### Executor comparison

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

## Agent operating loop

1. Use `desktop_observe` for a fresh VM image and guest metadata. Inspect `windows` and `uia.tree` through `desktop_act` when a control needs a window or element identity. A PNG and a UIA tree are separate observations.
2. Prefer a supported UIA pattern for a known control, authenticated terminal commands for reliable shell work, and native VM pixels/input for custom controls. Bind semantic requests to the observed `window_id`, `window_identity` and `element_id`; bind pointer input to the returned `frame_id` and its coordinate space.
3. Send one `desktop_act` mutation with the normal grant, reason, fresh deadline and idempotency key. Check the actual application state as well as the receipt. An API success response does not prove the intended application changed.
4. On a lost, failed or uncertain result, inspect state and the redacted receipt before deciding what to do next. `receipt_show` retrieves a returned receipt ID. If the ID is missing, use `receipt_list` with the original idempotency key, verify actor, target and operation kind, and read each candidate with `receipt_show`. Results cover bounded recent history; missing or ambiguous matches remain inconclusive. Never blindly replay a mutation. An exact cached retry is separate from a new attempt.
5. Close only owned windows, processes and terminal sessions. Keep `amc --direct` available for independent native console recovery.

AMC's native framebuffer and synthetic input address the VM directly. Guest UIA and clipboard actions run in the enrolled guest session; this workflow does not require controlling the operator's host desktop. `clipboard.snapshot` reads inventory and sequence only; `clipboard.get` explicitly reads text. Neither route establishes the behavior of unrelated clipboard synchronization software.

Native drag accepts an optional `duration_ms` from 20 to 5000 for controlled slower gestures; omission or zero retains the 400-ms movement schedule. CLI uses `amc console drag --duration-ms 5000`. Duration changes movement pacing, not the operation deadline, and is invalid for other input kinds. Provider execution time can exceed the requested movement schedule. Cancellation and failure retain the independent bounded input-release attempt; actual interrupted-path and release evidence are still required for installed cancellation acceptance.

## Capability and acceptance matrix

N = native VM console fallback; G = interactive guest helper; T = authenticated guest terminal. “Pending” is an evidence placeholder, not a failure or a claim of missing implementation. Source tests and review establish implementation behavior only; installed-host evidence establishes only the effects explicitly read back for that build and fixture. Record exact backend/build, synthetic VM identity, session/integrity state, request receipt, expected result, observed result, and private artifact digest for each acceptance run.

| Operation | Primary route and fallback | Contract and session limits | Concrete acceptance check | Evidence |
| --- | --- | --- | --- | --- |
| Screenshot / PNG | N; G adds window/UIA metadata | Report capture time, dimensions, source, coordinate space and scaling; no fake UI tree from pixels | Decode PNG, verify expected dimensions and fresh synthetic marker; test resolution change, stopped VM, and malformed provider output | **Partial.** Installed native CLI/MCP PNG observations decoded successfully for the synthetic fixture. Installed revision `329944db0f90` returned a VM-bound PNG aged 0.082 seconds at receipt, with independently decoded dimensions and pixel hash. Current revision `58f3d013cac9` additionally supplied direct and native Codex MCP PNG observations; the direct PNG decoded at 640 × 480. Resolution-change and stopped-target cases remain pending. |
| Cursor observation and movement | G observes guest cursor; N/G moves it | Distinguish guest cursor position from cursor rendered into image; never substitute host cursor | Move to two bounded guest points, read back guest position; verify host cursor and foreground window unchanged | **Partial. Verified bounded evidence:** a native guest cursor move and paired host observations showed unchanged host cursor coordinates and foreground window. Full movement/session matrix remains pending. |
| Click / right / double click | N pixel fallback; G semantic action when supported | Validate coordinates/buttons/count; use current observation and target identity | Synthetic app increments distinct counters for each gesture; reject out-of-bounds/stale targets without effects | **Partial. Verified bounded evidence:** installed native MCP left, right and double clicks changed distinct synthetic fixture counters. Installed revision `329944db0f90` also applied an omitted left button and changed the click counter once. On the installed `329944db0f90` build, an expired frame and out-of-bounds coordinate were refused, with owned fixture effects unchanged. |
| Drag and button release | N/G | Bounded path/duration; release held buttons on completion, timeout and cancellation | Move synthetic item start-to-end; cancel mid-drag, then verify button released and next click works | **Partial. Verified bounded evidence:** the installed `329944db0f90` build applied an omitted left button to a native drag, produced exact requested native start/end/up coordinates and movement events, and supplied valid interactive telemetry showing all buttons and modifiers released. A subsequent native Shift-drag produced the exact endpoints, Shift modifier events and one mouse-up, with valid telemetry showing all inputs released. Current revision `58f3d013cac9` includes the reviewed optional 20–5000-ms duration contract. One newer D2 run stopped at the caller's fifteen-second frame-age guard before drag dispatch. A following run ended with an uncertain MCP response timeout; it recorded no held-button motion and sent no cancellation notification. Its successful setup click took 8.65 seconds through the full route. No mutation was replayed. Owned process, files, root and terminal cleanup were verified. The thirty-second interop case observed left-button/Shift motion and transmitted cancellation for the exact pending request, then failed its strict output-loss guard. Separate reconciliation read the matching durable `aborted / caller_canceled` receipt, verified all inputs released before the endpoint, and verified a new recovery click. Owned GUI, files, root and terminal were removed. This does not pass the original continuous-telemetry case or its sub-100-ms/five-second timing criteria; the bounded-output rerun removed telemetry loss and verified release, but was constrained because the canceled MCP response lacked its receipt ID. The new receipt-discovery case passed on installed revision `e8ffc0e10da6`: exact-request cancellation, durable canceled receipt, released partial path and successful recovery click were verified with zero telemetry loss. Original sub-100-ms and five-second timing criteria remain constrained. |
| Vertical / horizontal wheel | G; N only if advertised | Declare supported axes and units; unsupported native wheel returns a capability refusal | Scroll fixture with measured before/after offset in each axis; reject unsupported axis without silent substitution | **Partial.** Each axis produced a wheel delta of 120 and corresponding fixture message counts. These counters do not prove a content offset changed. On the installed `329944db0f90` build, vertical input changed the owned content top index from zero to three, and horizontal input changed its scroll position from zero to eighteen. Complete unsupported-axis acceptance remains pending. |
| Text / Unicode / key chords | N/G; T for terminal text | Separate text from key presses; release modifiers; disclose layout/IME limitations | Round-trip Latin, Cyrillic, combining text and supplementary character; prove Ctrl+A and Ctrl+S effects, then no held modifier | **Partial. Verified bounded evidence:** Ctrl+S changed the synthetic save counter and released modifiers on installed revisions `9a96d8dfd1e8` and `329944db0f90`. Installed revision `329944db0f90` inserted the exact Latin, Cyrillic, CJK, combining and supplementary sample into the owned nonpassword field after a verified focus snapshot; text and released inputs were read back. Native key/text input has no atomic window or focus binding; supported UIA value patterns are the targeted alternative. Installed revision `e63ef7419f22` additionally passed native K1 append to a nonempty prefix and K2 Ctrl+A replacement plus Ctrl+S save, with exact Latin/Cyrillic/CJK/combining/supplementary text, unchanged password and released input read back. The observed legacy edit exposed no Value pattern, so setup used its fresh scoped geometry and a native click. Guest-local producer age and controller monotonic delivery bounds were recorded separately. Layout/IME variants and remaining chord cases are pending. |
| Window inventory and lifecycle | G; N visual fallback | HWND bound to process ID/start time; this tuple cannot distinguish same-process handle reuse. Fresh observation limits age but does not prove a new window instance. Enumerate, activate, move, resize, minimize, restore, close; close can discard user work | Two synthetic windows with duplicate titles; operate on observed identity, read geometry/state; refuse vanished handles and changed process lifetimes | **Partial.** An owned synthetic fixture and duplicate-title window were observed with process-lifetime identities. On the installed `329944db0f90` build, duplicate-only move, resize, minimize, maximize and restore effects passed, with exact original geometry restored and the primary window preserved. A changed process-birth identity was refused without geometry or input effects. Revision `39abcb197ac6` freshly verified duplicate-only move, resize, minimize, maximize and restore with exact geometry restoration in a WPF fixture. Native close, vanished handles and same-process recycled handles remain pending. |
| UI tree and element operations | G UIA; N screenshot/input fallback | Bounded tree and supported patterns; no assumption every legacy/custom control exposes UIA | Fixture exposes button/edit/selection/scroll patterns; invoke and read state; custom canvas returns explicit absence with screenshot fallback | **Partial. Installed:** UIA returned the bounded thirteen-element tree without the synthetic password canary; legacy panes had no useful patterns. The installed `329944db0f90` MTA helper returned the same tree. A later installed `cf47796831c6` record covers fifteen distinct WPF semantic, refusal and canvas-fallback cases: thirteen historical cases plus two fresh canvas cases. This historical aggregate is not a single fresh run on the current build. Revision `39abcb197ac6` freshly verified WPF toggle, selection, expansion and scroll effects; these four cases do not reaccept the full historical aggregate. Unsupported patterns and other applications remain constrained. |
| Clipboard read/write | G; T only in verified interactive session | Guest/session scope, bounded payload and redaction; no AMC host clipboard relay; declare independent console clipboard integration | Synthetic Unicode value round-trip; verify host clipboard unchanged without logging its contents; restore fixture clipboard | **Partial. Installed observation:** the current Codex MCP connection returned metadata-only `clipboard.snapshot` results on installed revision `58f3d013cac9` in elevated guest session 1. Sequence 5 and inventory were unchanged around a failed request with expected sequence 4; its durable failed receipt was retrieved by the same actor, but the specific guest error was not verified. An exact-source isolated probe passed ASCII, Unicode and empty cases plus a fourth guarded empty confirmation, with stable tokens and complete cleanup. That probe reopened the private clipboard for metadata instrumentation. A subsequent uninstrumented probe used the full unchanged production source, checked metadata only after each method returned, and passed the same three cases plus a fourth guarded empty confirmation. Both probes recorded zero interactive clipboard calls. Neither establishes normal positive clipboard publication. A normal live Unicode write/read round-trip, host isolation and full-format restoration remain pending. Preserve user clipboard state. |
| GUI terminals and command execution | G for visible terminal; T for deterministic command path; N visual recovery | Return exit status/stdout/stderr and deadline; opening a GUI terminal is distinct from executing a shell command | Launch fixture terminal, run bounded command, verify output/exit; timeout exact owned child process and retain receipt | **Partial.** An authenticated owned terminal executed bounded guest commands and was reopened after graceful closure. Revision `de1f43607a33` separately verified an owned elevated graphical PowerShell workflow: native typing/submission, visible matching nonce and `EXIT_7`, returned prompt, actual child exit 7, Administrator membership, High integrity and session 1. Both exact process lifetimes, owned files/root and terminal session were cleaned. Forced transport-drop recovery, complete deadline cleanup, atomic focus binding, shared-console and secure-desktop coverage remain pending; see the revision-specific graphical-terminal record below. |
| Process and application launch | G/T; N Start-menu fallback | Implemented executable/argv separation; cwd and returned process birth are not exposed. Probe the actual process lifetime before owned cleanup | Launch synthetic app with spaced/non-ASCII argv; verify PID/path; reject unrelated PID; clean only owned process | **Partial.** The owned graphical fixture's actual process lifetime was verified. Complete spaced/Unicode argv effects, refusal of unrelated processes and final cleanup acceptance remain pending. |
| Elevated launch / admin app | G/T with separately established authority; N console recovery | Ordinary SendInput cannot inject into higher-integrity apps; SSH identity alone does not prove GUI session/integrity | Disposable elevated fixture with rollback: establish actual token/integrity, verify permitted action or explicit refusal; no credential recording | **Partial. Verified bounded evidence:** the synthetic fixture ran in the interactive guest session with a verified High integrity token. This does not establish arbitrary admin-app control, elevation authority or secure-desktop support. |
| UAC secure desktop / logon / lock | N observation/input only when empirically supported; G must advertise actual desktop scope | Normal guest desktop helper is not universal secure-desktop authority; no bypass of UAC or login policy | Disposable guest: inspect locked/logon/UAC states, prove exact supported operations or explicit refusal; preserve host state and redact all secrets | **Pending / constrained.** Ordinary guest helper scope does not grant UAC, logon or locked-desktop authority. Empirical native support/refusal checks require a separate authorized disposable-target test. |
| Recording | N bounded screenshot sequence; G optional capture; Playwright only for browser video | Declare frame sequence versus encoded video, interval, dimensions, duration/size cap, cursor mode, dropped frames and timestamps | Record fixture animation, decode every frame or video, verify ordering/timing and finalization; stop on deadline/cancel and remove owned temporary frames | **Partial. Verified bounded evidence:** on installed revision `e7c7fa6dc7a0`, R1 returned six decoded GIF frames at 640 × 480 with ordered capture timestamps, per-frame pixel hashes and frame delays. Static frames were allowed; no animated-marker claim is made. R2 sent an exact-request cancellation and received `operation_canceled` with no GIF in the response. That historical run's captured-frame count is unknown, and it did not prove capture cessation. Revision `de1f43607a33` separately returned a decoded two-frame 320 × 240 GIF with ordered timestamps; its 4.079-second capture spacing did not match the requested one-second interval. A correlated cancellation case reached terminal `canceled` with no capture in flight and stable attempt/completion counts over three later reads, followed by a fresh PNG. This proves bounded producer cessation after synchronous capture returned, not instant interruption inside Hyper-V. Requested interval fidelity, dropped-frame accounting and broader recording timeout behavior remain constrained; see the revision-specific recording record below. |
| Reconnect / unattended operation | N independent recovery; G/T reconnect with fresh session proof | Unattended means no host interaction within declared guest state; it does not imply locked/logon/UAC support. Never replay uncertain mutation automatically | Drop transport after dispatch; inspect state before retry; reject stale session/window/capture identities; resume bounded observation after reconnect | **Partial.** Graceful owned-terminal closure and reopening were observed. Normal core and five-helper installation for revision `58f3d013cac9` verified the healthy running daemon and all three command links. Current native Codex MCP readbacks supplied a PNG and elevated interactive metadata, including a metadata-only clipboard observation. A fresh full desktop action matrix on that connection remains pending. Forced drop after dispatch, uncertain-mutation reconciliation, stale-session recovery and unattended state coverage remain pending. Never replay an uncertain mutation automatically. |
| No host impact / concurrency | All routes | Exact VM GUID/session ownership; per-target mutation serialization; bounded lab authority; no host GUI input | Capture host foreground/cursor before/after, run two synthetic targets and prove isolation; cancellation releases guest inputs and leaves unrelated writers untouched | **Partial. Verified bounded evidence:** paired observations around the bounded native pointer case showed no host cursor/foreground change. Two-target isolation, full cancellation/concurrency behavior and every route's installed-host acceptance remain pending. |

Source revision `329944db0f90d9d12bb84ff0636ae3434d8c6e41` passed all sixteen CI checks and the root quality gate with 85.8% coverage. Independent ACP verification executed 62 named MCP contract cases in an isolated native Windows process with synthetic HTTP and in-memory transport fixtures, without failure or skips. Independent code/security and architecture reviews approved the changes. These checks establish source behavior rather than every live operation above.

The immutable package was installed through normal helper and daemon lifecycle operations. Authenticated health, executable hashes, process birth, all three command links, a fresh catalog of 25 MCP tools, a new agent-owned terminal and all five guest-helper file hashes were verified. On that build, MCP returned a final VM-bound PNG after metadata observation, with matching decoded dimensions and pixel hash. Omitted-button click and drag each changed the owned fixture once; exact native endpoints and valid interactive telemetry confirmed input release. Duplicate-only move, resize, minimize, maximize and restore passed, including exact geometry restoration while preserving the primary window. Vertical and horizontal scroll changed actual content offsets; Shift-drag produced exact endpoints and released inputs. Ctrl+S incremented the save counter once and supplied matching modifier down/up telemetry with valid release. Three invalid requests (expired frame, out-of-bounds coordinate and changed process birth) were refused with the tracked fixture effects unchanged.

Installed revision `da69ec8135625c2fc6f10c9b3553ec9bcf4c9c01` includes the tested empty/null clipboard normalization. Its normal daemon replacement verified authenticated health, executable hashes, process birth and all three command links. Fresh guest validation enforced the five expected helper assets, private paths and the exact Interactive/Highest task identity; queue and action hashes were explicitly read back. A new owned terminal executed a bounded PowerShell command as administrator in session zero, while helper status and window observations separately confirmed the elevated interactive session. A fresh 25-tool MCP catalog advertised both left-button defaults. MCP and independent `--direct` captures each returned a decoded 640 × 480 PNG bound to the canonical VM identity, with native geometry 1024 × 768.

Both exact owned terminal sessions were then normally closed and confirmed absent from active inventory. All thirteen owned fixture files and their empty directory were removed with retained private receipts; unrelated resources were preserved. Operator and agent grants were observed active within their existing expiry bounds. This establishes the new installation, terminal lifecycle and read-only observation paths. It does not reattribute historical input effects to this revision or establish a live clipboard round-trip. At that stage, the Codex connection retained an older adapter. The later successful metadata-only observation supersedes that connection status; the remaining live-effect constraints still apply.

Earlier installed revision `9a96d8dfd1e8ba5551ae684d317c27939b9463d7` additionally supplied distinct right/double-click effects, Ctrl+S save telemetry, an interactive High-integrity fixture and paired unchanged host cursor/foreground observations around a bounded native pointer move. A six-frame GIF decoded at 640 × 480 and showed animation changes; measured frame delays differed from the requested interval, so this is not cadence acceptance. Vertical and horizontal wheel counters establish message delivery, not content-offset changes. A protected diagnostic verified one exact owned nonpassword Edit focus snapshot on that earlier revision; this does not prove later typing.

The installed helper uses dedicated MTA workers for supported UIA actions and retains STA for clipboard and other actions. A thirteen-element tree omitted the exact synthetic password canary; legacy panes did not advertise the patterns needed for semantic invoke, value, selection, toggle, expand and scroll acceptance. Native input success is determined from its console operation receipt and application effects, rather than the unused guest semantic response fields.

## Additional source and installed evidence (2026-10-03)

PR16 source revision `9b1e035` was reviewed and merged as `505f69e`. Windows verification executed 96 required PASS test events across 17 groups, plus 22 named native clipboard and eight text cases. These are source checks, not live guest clipboard acceptance. Source tests also cover semantic and native failure receipts, exact failed retries without redispatch, admission/frame refusals without execution receipts or cache entries, and `receipt_show` retrieval.

PR17 source revision `12a8794` combines clipboard metadata and receipt changes and was merged as `d7f46c0` after all sixteen CI checks passed. Its source verification ran 32 named native cases, including ten inventory cases proving zero payload reads or mutations; separate checks exercised 31 invalid snapshot responses, two valid responses, legacy-helper refusal, STA behavior, sensitive observation authority, absence of mutation receipts and schema compatibility. The final Windows log confirmed the new metadata and helper markers alongside 101 required receipt and snapshot PASS test events. These results do not establish installed effects.

Before the coordinated maintenance window, installed revision `cf47796831c67d1bdbaca5b66d02dbf69752f269` had verified command links, core files, five helper assets, an elevated interactive session, and a read-only native PNG observation. The subsequent installed acceptance record aggregates fifteen distinct WPF cases: thirteen historical cases and two fresh canvas-fallback/constraint cases. It does not establish a single fresh fifteen-case run on installed revision `e63ef7419f22`. A bounded application-level invoke attempt ended with a deadline error; its tracked fixture counter was zero at final cleanup. The timeout cause is unproven, and the final counter does not establish that the call had no effect. Do not blindly retry it. All task-owned fixture/files/root/terminal/host children were removed.

Recorded startup and window-inventory timeouts remain failures alongside subsequent successful readbacks; their causes are not inferred. Graceful daemon replacement and a new owned terminal executed bounded commands, but do not prove recovery from every uncertain mutation. A bounded native Unicode insertion on revision `329944db0f90` populated an initially empty nonpassword field and preserved the password fixture, with exact text and valid released inputs read back after a verified focus snapshot. On that earlier revision, preservation of nonempty prior text and Ctrl+A replacement remained untested; the later E63 K1/K2 evidence is recorded above. Focus changes between observation and input are not atomically guarded; the snapshot age limit applies at the caller decision, not backend dispatch. Layout/IME behavior, secure-desktop states, clipboard restoration, forced transport drops, multi-target isolation and mid-drag cancellation remain separate cases. This record does not establish full desktop readiness.

## Source and installed readback on revision `58f3d013cac9` (2026-10-05)

Task47 source revision `58f3d013cac9cfd37194d916d2b23a03641046f7` was merged through PR25 as `a63fbb926a18` after all sixteen CI checks passed and independent code/security and architecture review cleared the change. Full quality passed with 86.0% coverage. Windows source verification executed all 51 named mocked native clipboard cases and five real NLS encoding ABI cases without skips. The NLS fixture used trusted synthetic text and called no clipboard or window API. This revision also includes PR24's reviewed optional drag-duration contract.

Normal installation verified revision `58f3d013cac9` and all five guest-helper assets, including the changed native clipboard writer and worker failure classification. Direct and current native Codex MCP observations supplied PNG readback; the direct capture decoded at 640 × 480 and its temporary image was removed. MCP separately reported elevated interactive session 1. These observations do not reattribute historical WPF, K1/K2 or recording effects to this build.

The isolated clipboard probe used the byte-exact production class body with a gated native boundary. Its three payload cases and fourth own-token CAS confirmation returned stable sequence/inventory through close, owner destruction and return, with zero interactive clipboard calls and zero remaining owned windows, buffers, files or root bytes. Extra metadata-only reopen instrumentation changes the intervention, so this is bounded private-namespace evidence. The normal route preserved sequence 5 metadata around a failed expected-sequence-4 request and durable receipt readback; its error category remained unknown. Normal positive clipboard publication remains pending.

A subsequent isolated probe compiled the full unchanged production native source without a shim or method hooks. ASCII and Unicode writes returned sequences 5 and 10 with complete inventories; clearing returned sequence 11. Independent metadata reads occurred only after the public method returned, and each next write accepted the preceding own token. A fourth empty confirmation returned sequence 12. The private namespace started and ended empty, with zero interactive clipboard calls, errors or remaining owned windows. The exact process lifetime exited; all owned files and the root were removed, and the owned terminal was normally closed. This establishes uninstrumented private-namespace behavior, not positive publication through the normal interactive helper. Individual native memory handles were not independently counted.

The current thirty-second drag-cancellation experiment also produced bounded physical evidence: an in-progress left-button/Shift drag, cancellation of the exact pending request, a matching durable canceled receipt, released inputs before the endpoint and a successful fresh recovery click. Its original run failed after the telemetry producer overflowed the terminal ring; the original RPC response was not retained. Read-only gap acknowledgment and exact-key durable reconciliation supplied the later release, recovery and cleanup evidence without replaying the drag. This separate reconciliation does not turn the failed continuous-telemetry run into a pass. The owned graphical process, files and directory were removed and its terminal was closed. The subsequent bounded-output run reported no terminal output loss and verified released inputs, then refused its missing-response-receipt check. Its owned process, files, directory and terminal were cleaned. This preserves the failed criterion and motivates explicit receipt discovery; the later discovery-enabled run passed its separately declared acceptance case, as recorded below.

Sanitized private artifact SHA-256 digests: installation `bcd62b084660886bf76e531fed42e76c4b34000a774872bda4a0538a27b0f8ad`; instrumented isolated readback `6e85ccce6601e6da4d4a6a8ff6385b4fc7dddabda79f7441ece85d6acb7ae83b`; uninstrumented isolated readback `75843289a869250842b7ebbec43cdc15822ac37e894e2a86c7810b428e9e1706`; installed MCP readback `47b76e4744fde356429bf7a4fc85eea35f7a5201210a9d792a376555fb4f298c`; decoded direct observation `2935e9df760704f87234f0219711415d838890d702c0de77b6c0e4d77e9992b1`. The reconciled cancellation receipt readback digest is `1bcdc670f21f90a3a8ef0f2d2ea09b626050163c337212347699a674e0eb15a2`. The artifacts remain private and confer no broader desktop or unattended acceptance.

## Receipt discovery and installation on revision `e8ffc0e10da6`

PR26 source revision `e8ffc0e10da6f0f5e88bf8dcba3523e0e129e90b` adds MCP `receipt_list` over the existing authenticated receipt endpoint. The default limit is 50 and the maximum is 1000; optional exact-key filtering preserves all matches within the recent result window. Existing daemon actor visibility rules remain the authority. Missing results do not prove that an operation had no effect, and a key alone is not globally unique across actors and targets. All 25 prior tool schemas remain unchanged, with one additional tool.

The source passed independent code/security and architecture review, full quality with 86.0% coverage, normal commit and push hooks, eight explicitly discovered targeted test cases and all sixteen CI checks. PR26 merged as `945f36f4f82f`. Normal core activation verified the immutable package and all three command links, plus all five unchanged guest-helper assets before and after the switch. Its owned terminal sessions were closed. A current native Codex observation returned a 640 × 480 VM PNG with native geometry 1024 × 768 and an elevated interactive helper in session 1. Fresh owned preparation verified the 26-tool catalog and optional receipt-query schema before creating the fixture. The declared discovery-enabled cancellation case then passed: held left-button/Shift and incremented motion preceded exact-request cancellation; the receipt-less response was preserved, and `receipt_list` returned one matching caller/target/kind/key record. `receipt_show` independently bound both fingerprints, redaction and the full canceled outcome. The path stopped at native x=180 before endpoint x=312, all inputs were released, and a new recovery click incremented its counter once. Paired telemetry reported zero loss; the measured causal bound was 12.239 seconds, guest-local sample age 0.136 seconds and dispatch frame age 9.44 seconds, within this case's 30/0.5/15-second limits. Exact GUI process exit, zero guest files/root bytes and normal owned terminal closure were verified. Two independent actual-evidence reviews cleared this case. Original shorter timing criteria remain constrained.

An additional recording smoke on this installation requested two 320 × 240 frames with a one-second interval and returned `operation_timeout`, with no GIF in the response. The generic timeout does not identify the failing phase or captured-frame count; it does not establish zero captures or completed producer cessation. Current-build recording performance and cancellation correlation remain constrained.

The revision `e8ffc0e10da6` installed-profile digest is `7d32cd74bf7fafe505d8184688f16ed5511a1bf44d3844bc90da991dfb670d90`. Helper identity/source proof is `af75558fb839bfe667759ee79c252076f853d1a6518d811a8eaffe4acae1eac2`. The new case result digest is `bd24e9e71e08918009dd4baf10ba2f7841028b3bc5c3a7f5ca20877e302fdab3`; durable receipt readback is `a2f020d9c833bec4e67ddffce622a1ce30ebbb27b80d93a9c4e3f24311c27a84`. These bind revision `e8ffc0e10da6` and its separately verified cancellation case without reattributing other historical UI effects to the new build.

## Correlated recording and display readiness on revision `de1f43607a33`

PR28 source revision `de1f43607a332e6874ae34701d33df702bebb0f7` passed all sixteen
exact-head CI checks and was merged as `64620b86413d`. Normal activation verified its
immutable core, command links and five unchanged guest-helper assets. A fresh owned
MCP connection advertises 27 tools. Existing clients can retain an older tool catalog;
a fresh connection is required to discover newly added tools.

Optional caller-generated `recording_id` correlates a recording with actor- and
VM-scoped `console_record_status` metadata. An actual cancellation after the first
completed capture yielded terminal `canceled`, no capture in flight, and unchanged
attempt/completion counts over three subsequent reads. A fresh PNG from the same VM
followed. This establishes bounded AMC producer cessation after the synchronous capture
returned; it does not establish instant termination inside Hyper-V. Missing or
inconclusive status is not permission to replay the recording. The private result
digest is `f810c32077746002c8c7a43bad7091ab7a1b2b5b96994ba26b9c80abe5576881`.

A separate recording on revision `de1f43607a33` returned a GIF independently decoded as two
320 × 240 frames, 18,556 bytes, with ordered timestamps. Actual capture spacing was
4.079 seconds despite a requested one-second interval; the encoded delays were
4,070 and 1,000 milliseconds. Provider execution contributes to cadence. This passes
bounded sequence completion and decoding, without claiming real-time interval fidelity
or an animated marker. Compact decoded evidence digest:
`8d9d1b8c0e788024a79c5ee59a06f5f2c60b7d2bd54573a35d95a89129c03239`.

A structurally valid black PNG coexisted with an active Default console-session helper,
window inventory and guest cursor. One nearby native guest pointer move produced a
visible desktop PNG and verified cursor motion, without host desktop control. A separate
read-only guest display-policy query completed through a newly owned terminal, which
was normally closed. The underlying black-frame cause remains unknown; no guest or host
power policy was changed. The recovery evidence digest is
`cf6cafd77be2012e82c4175d8ba21264df2dea3e30240bc324af9e77d4d6a449`.

These revision `de1f43607a33` cases supplement the rows above. Historical application effects,
protected clipboard state and unverified secure-desktop/layout cases retain their
declared scope; successful recording or readiness recovery does not reattribute them
to this build.

## Elevated graphical terminal on revision `de1f43607a33`

A separate case on revision `de1f43607a33` launched an owned graphical PowerShell window in the
interactive guest session. Its visible window belonged to the independently verified
PowerShell process itself; a console host image name alone is not an ownership proof.
The case bound the window, PID and process creation time to a nonce and trusted
executable hash before focusing or typing.

Native VM keyboard input typed and submitted an owned synthetic command. Independent
before/after PNG inspection showed the invocation, matching output nonce with `EXIT_7`,
and returned prompt. Independent guest files bound the native child to the GUI process
and confirmed exit code 7, Administrator membership, High integrity and session 1.
Both exact process lifetimes were absent afterward; the owned root contained zero
files and bytes, and the owned terminal session closed normally. Independent code/security
and architecture reviews cleared this declared case. The private acceptance digest is
`f5b97c3db4b7b6966422c2d588598fb89c15225bf8161b36014d396784e6f6bd`.

This establishes one native elevated graphical-terminal workflow without host desktop
control or clipboard use. Foreground observation and later input are separate actions;
the case does not establish an atomic focus lock, shared-terminal control, secure-desktop
access, or general acceptance of every Windows application.

## Current installed core and scoped WPF acceptance (2026-10-05)

PR30 source revision `39abcb197ac69e0b62b6b44e55ec9d1076f63a9e` passed independent
code/security and architecture review, full quality and all sixteen exact-head CI
checks, including the required named Windows cases without skips. It merged as
`b7c1bb780827`. Normal activation verified the immutable core and command links,
with all five guest-helper assets unchanged before and after installation.
A fresh native Codex reconnect returned a 640 × 480 PNG with native framebuffer
geometry 1024 × 768 and an elevated interactive helper in session 1; the observation
route took 43.504 seconds.
A fresh connection advertised 27 tools. These are bounded installed readbacks.

Six fresh WPF cases on this revision verified toggle, selection, expansion and
scroll effects, duplicate-window move/resize/minimize/maximize/restore with exact
geometry restoration, and the fixture's High-integrity token. Independent reviews
accepted these effects together with separately verified cleanup: the exact owned
process and both windows were absent, the guest root was absent with zero remaining
files/bytes, and both owned terminal sessions were closed. The original failed
aggregate remains failed. No key, button or drag requests occurred in these six
cases; physical input release and native window close were not proved.

Desktop lab admission now performs two fresh target queries instead of three.
The final fenced binding and both rollback/safety checks remain intact; exact
cached retries do not redispatch. Two successful same-target PowerShell launch
samples measured intent-to-response file intervals of 23.56 seconds on the prior
core and 14.92 seconds on this core. Their fixture arguments, STA use and host load
differed. Receipt start precedes final fenced validation, and no phase timing or
controlled causal performance gain was established.

A separate current-core transport-loss variant freshly observed left-button/Shift
motion, abruptly terminated only its owned pending MCP child, reconnected as the
same actor, and read the unique redacted `aborted / caller_canceled` receipt without
replay. Valid telemetry showed a released partial path. The subsequent fresh click
had an uncertain response deadline, so the full drop/recovery case remains failed.
A missing matching receipt does not establish zero effect. Owned cleanup recovery
is tracked separately; continuity and original timing criteria remain constrained. Historical recording, terminal and UI effects
retain their original revision scope. Clipboard publication, secure-desktop access
and the other finite constraints above remain unchanged.

Private evidence digests: installed profile
`0c6761a6f3bdceaa98de8d19e79131a7fe93691d11c83378424fa59e462daa08`;
scoped effects with separate cleanup
`b917cdafab103c63b21529e6b2987bf54b35da48a744aee56388e195beca8905`.

## Validation gates and stop condition

1. Unit and contract tests prove parameter validation, capability refusal, provider decoding, idempotency/deadlines and cleanup. They do not prove guest effects.
2. A disposable graphical fixture proves state changes for every supported row, including negative/session cases. Record exact rollback and task ownership before destructive or privileged tests.
3. Independent security/architecture review checks the privileged transport, interactive-session identity, command injection resistance, bounded artifacts and recovery independence.
4. Installed-host acceptance supplies compact receipts and private artifact hashes. No guest images, private inventory, keys or unredacted transcripts enter the repository.
5. Complete cleanup on success/failure: release keys/buttons, finalize recording, stop only verified owned writers, remove owned scripts/temporary artifacts, preserve required rollback and report actual remaining disk usage.

Ready means each required row is accepted on the declared Windows/session matrix or explicitly constrained with a verified refusal and documented recovery path. “Human-like full control” without a session/application boundary is not an acceptance criterion. Stop after this finite matrix passes; optional toolkit adapters require a concrete missing capability rather than speculative dependency expansion.

## Primary sources

Provider protocols: [OpenAI Computer Use][openai-cu] and [Anthropic computer use][anthropic-cu]. Public executors: [OpenAI sample][openai-sample] and [Anthropic reference demo][anthropic-sample]. These complement the platform and executor sources linked in the comparison table.

[openai-cu]: https://developers.openai.com/api/docs/guides/tools-computer-use
[anthropic-cu]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/computer-use-tool
[openai-sample]: https://github.com/openai/openai-cua-sample-app
[anthropic-sample]: https://github.com/anthropics/anthropic-quickstarts/tree/main/computer-use-demo
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
