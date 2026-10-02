# Native VM console

AMC captures and controls the enrolled Hyper-V guest through its WMI display, keyboard, and
synthetic mouse devices. It does not open VMConnect, move the host pointer, activate host windows,
or require a guest desktop sidecar. CLI, authenticated daemon endpoints, and MCP share the same
application service. `amc --direct` remains available without the daemon or SSH.

## Capture

```sh
amc console screenshot default --output /private/guest.png --json
amc --direct console screenshot default --width 1024 --height 768 \
  --output /private/guest-direct.png --json
```

The output must be a new file in an already-private directory: POSIX mode `0700`, or a protected
Windows directory with private inheritable ACLs. AMC refuses broad parents without changing their
permissions, refuses existing files and symlinks, and applies private file permissions or Windows ACLs.
The maximum image size is 1,048,576 pixels. Metadata includes the
canonical VM identity, native display dimensions, image dimensions, timestamp, digest, and an
opaque frame ID. Screenshot bytes are sensitive and cannot be reliably redacted; capture requires
`machine:read` and `evidence:sensitive:capture` authority.

The `console_screenshot` MCP tool returns PNG image content and structured frame metadata. Image
bytes use the MCP SDK's native image encoding. The model can see the image directly. AMC retains
only private frame metadata for up to two minutes and at most 512 live records. It does not persist
the screenshot. The CLI stores the requested PNG under the caller's ownership.

## Input

`console_input` supports `key`, `type`, `move`, `click`, and `drag`. Keyboard chords include ordinary
keys, modifiers, navigation, function keys, and `ctrl+alt+delete`. Text is bounded to 256 UTF-16 units
per operation. Pointer actions require a fresh frame ID and coordinates in the captured image;
AMC maps them into native guest pixels and refuses changed display dimensions or another target.
Mouse buttons and held keys are released on failure with a bounded independent cleanup attempt.

Console input is privileged: typing or clicking can affect arbitrary guest applications and external
systems. It uses existing exact server-issued approvals, deadlines, idempotency, leases, audit, and
redacted receipts. Typed text is hashed in operation parameters and never persisted in those records.
An agent cannot issue its own operator approval.

Prepare a JSON action file for the operator approval, for example:

```json
{"kind":"key","key":"ctrl+alt+delete"}
```

```sh
amc operation approve console.input default --input-file /private/action.json \
  --reason "Open guest security screen" --idempotency-key console-example-1 \
  --valid-for 1m --for-mcp --json
```

Execute the identical action through `console_input` with the returned approval ID and exact
deadline. For CLI execution omit `--for-mcp` and use:

```sh
amc console key default --key ctrl+alt+delete \
  --reason "Open guest security screen" --idempotency-key console-example-1 \
  --approval-id APPROVAL_ID --deadline EXACT_DEADLINE --json
```

For sensitive text, prefer `amc console type --text-file /private/input.txt` over command-line text.
Use persistent SSH/PTTY sessions for commands, interactive terminals, file operations, and privileged
nonvisual diagnostics; console input complements that existing channel.

## Scope and acceptance

The console capture is the Hyper-V console framebuffer. Enhanced RDP sessions may display a
different Windows desktop. Guest administrator rights, accessibility trees, wheel scrolling, and
application-specific behavior are not implied by a successful screenshot. Wheel actions currently
return unsupported. Locked desktops, UAC, drag timing, Unicode typing, and guest applications require
live acceptance evidence on the intended VM. The implementation never authorizes host desktop input.

Optional guest UIA or computer-use drivers can add semantic element targeting later. They remain
outside AMC's policy, identity, approval, and audit authority.
