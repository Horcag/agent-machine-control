# Persistent SSH Terminal Sessions Architecture & Operations Guide

## Overview

Agent Machine Control (AMC) provides persistent, durable guest SSH pseudo-terminal (PTY) sessions for Windows guest automation. Sessions operate exclusively via the background daemon (`amcd`), while direct disaster recovery operations (`amc --direct`) remain fully independent.

Terminal sessions allow autonomous agents and operators to open interactive shells, stream buffered output, send keystrokes and control sequences (such as `Ctrl+C`), and wait for regex patterns or quiet settle times.

---

## 1. Machine SSH Configuration & Key Provisioning

Session connections require local machine configuration files stored inside the daemon state directory under `machines/<machine-guid>/config.json`.

```text
<statedir>/
  machines/
    <machine-guid>/
      config.json
  keys/
    default.key          # POSIX hermetic/development path only, mode 0600
    default.dpapi        # Windows production path, user-scoped DPAPI blob
```

### Configuration Structure (`config.json`)

```json
{
  "endpoint": "192.0.2.20:22",
  "user": "automation_admin",
  "default_key_alias": "default",
  "pinned_host_key_sha256": "dGVzdA==",
  "external_effects_contained": true,
  "rollback_checkpoint_id": "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
  "require_production_checkpoint": true
}
```

This flat object is the only accepted schema. Unknown fields, nested `ssh` objects, trailing JSON,
and the obsolete `host_key_pin_sha256` spelling are rejected. The machine GUID is bound by the
parent directory name and is not repeated inside the file. The pin value is the base64-encoded
SHA-256 digest of the SSH public-key bytes, without an `SHA256:` prefix.

### Strict File and ACL Boundaries

1. **Private Key Storage**: POSIX hermetic tests and development may use
   `<statedir>/keys/<alias>.key` with mode `0600`. Windows production accepts only
   `<statedir>/keys/<alias>.dpapi`; plaintext key files are never a fallback.
2. **Symlink Rejection**: Any symlinks detected in machine configs or SSH private keys are strictly rejected with fatal errors to prevent path traversal attacks.
3. **Host Key Pinning**: Connections fail closed if the target guest host key SHA-256 does not
   strictly match `pinned_host_key_sha256`. Pinned keys are checked during the authenticated SSH
   handshake before a session is admitted.
4. **Credential Isolation**: Remote endpoints, host key pins, guest usernames, and private keys are resolved exclusively from server-owned local configuration. Agents invoking MCP tools or CLI users cannot supply raw SSH endpoints, host keys, private key bytes, or user identity overrides.

### Windows service identity and DPAPI provisioning

Windows uses user-scoped DPAPI (`CryptUnprotectData` without `CRYPTPROTECT_LOCAL_MACHINE`). The
blob must be created by deployment automation running as the same dedicated, low-privilege service
identity that runs `amcd`; that identity's user profile and DPAPI master-key directory must be
loaded for noninteractive startup. Provisioning must call `CryptProtectData` with
`CRYPTPROTECT_UI_FORBIDDEN`, write only the resulting blob as `<alias>.dpapi`, and apply an explicit
DACL whose owner and only read-capable allow ACE are the service identity. The daemon verifies that
owner/DACL shape before decryption. A blob protected by an administrator, interactive operator,
LocalSystem, another service identity, or machine-wide DPAPI is rejected under the intended
dedicated-service deployment. Plaintext source key material must stay outside AMC state, command
arguments, environment variables, logs, and temporary files; deployment tooling is responsible for
zeroing its in-memory buffer after protection.

---

## 2. Windows OpenSSH and ConPTY Behavior

- **Native OpenSSH Server**: AMC interacts with the native Windows OpenSSH Server (`sshd`) running inside Hyper-V virtual machines over SSH subsystem channels.
- **ConPTY Emulation**: Windows OpenSSH allocates pseudo-terminals via the Windows ConPTY API. Output streams may contain ANSI escape sequences and UTF-8 multi-byte characters.
- **Linux Verification Boundary**: Note that Windows OpenSSH and ConPTY behaviors remain unverified on Linux hosts; on Linux, tests and verification utilize synthetic SSH server harnesses.
- **Sanitization & Stream Cleansing**:
  - Raw SSH output chunks are sanitized before buffering. Malicious or dangerous escape sequences (such as OSC 52 clipboard hijacking) are stripped.
  - Multi-byte UTF-8 sequences split across network packet boundaries are held in a pending buffer and reassembled before emission to prevent corrupted unicode runes.
  - Redaction state spans arbitrary transport chunks for OSC/CSI sequences, private-key blocks,
    bearer/password/token forms, configured bounded regexes, and server-owned exact active-secret
    values. Exact secret bytes exist only in process memory and are never serialized into machine
    configuration, receipts, audit, journals, errors, or fixtures.

---

## 3. Daemon Mutation Lifecycle, Safety Resolution & Policy Truth

All session mutations (`session.open`, `session.write`, `session.control`, `session.close`) are governed by the core application service (`internal/app.SessionService`):

1. **Dynamic Safety Resolution**:
   - The safety resolver inspects the target machine's server-owned `config.json` (`external_effects_contained`, `rollback_checkpoint_id`) and queries the Hyper-V backend (`ListCheckpoints`).
   - If external effects are contained and the rollback checkpoint exists and is verified in Hyper-V, the operation is classified as `ClassReversibleMutation` with the checkpoint GUID as `rollbackRef`.
   - If external effects are not contained or the rollback checkpoint is missing/invalid, the operation is classified as `ClassDestructivePrivileged`, requiring an explicit, unconsumed operator approval record.
2. **Host Mutation Lease**:
   - Every session mutation acquires a host lease before reading containment configuration or
     querying the configured checkpoint. The same lease remains held through guest effect and
     durable finalization.
   - A checkpoint must be a current observed record for the exact VM, have a non-empty provider
     type other than `Microsoft:Hyper-V:Snapshot:Missing`, and have a complete acyclic parent chain.
     When `require_production_checkpoint` is true, every checkpoint in that chain must be
     `Production`; AMC does not invent or claim a snapshot health field.
3. **Idempotency & Zero-Side-Effect Retry**:
   - Exact retries (matching actor, target, operation parameters, and idempotency key) return durable reconstructed outcomes and receipts without re-executing transport calls or duplicating keystrokes.
   - Retries for `open` reconstruct session observations from disk/receipt; `write` returns the exact byte count written (`len(params.Data)`); `control` returns success; `close` returns the closed observation.
   - Parameter collisions (same idempotency key with differing parameters or caller) fail closed with conflict errors.
   - Before a guest effect, a session-scoped durable reservation binds the safe operation identity.
     Pending or incomplete receipt/audit/journal finalization blocks replay after restart. The
     reservation becomes finalized only after both the terminal receipt and terminal audit are
     durable; its immutable response snapshot is then used for exact retries.
4. **Plaintext Redaction & Receipt Integrity**:
   - Plaintext data written to sessions is never stored in `Operation.Parameters`, audit records, receipts, or error messages.
   - Session write operations store and bind idempotency to `data_sha256` and `data_length`.
   - Session receipts are marked with `RedactionApplied` status.
   - Every admitted mutation terminates in a terminal receipt and audit outcome (success, failed, aborted, or denied).
5. **Sensitive Evidence Isolation**:
   - Reading session output (`ReadSession`, `WaitSession`) requires `session:read` AND a sensitive-evidence scope (`evidence:sensitive`, `evidence:sensitive:capture`, or `policy.DefaultSensitiveEvidenceScope`).
   - Unauthorized access attempts return `domain.ErrSessionAccessDenied` or `domain.ErrSessionNotFound` without leaking session output, exit codes, or observation metadata.

### Deadlines and close semantics

The required mutation timeout is one end-to-end budget beginning at the application service. It
includes flight coordination, lease acquisition, server-owned safety resolution, policy and approval,
durable reservation, TCP dial, SSH handshake, PTY/session/shell requests, writes, controls, and close;
the budget is never reset at the transport boundary. If it expires before a guest effect begins, AMC
records and finalizes an aborted zero-effect result, releases the lease, and exact retry cannot perform
the effect. Transport cancellation uses connection deadlines and never leaves a detached goroutine able
to perform a late guest write.

Normal close reports success only after transport close succeeds. On transport failure, `force=false`
leaves local state `closing` (indeterminate) and records a non-success outcome. `force=true` permits
AMC to finalize local state as terminal `failed`, but it does not turn the transport failure into
success and does not claim confirmed remote termination. Concurrent close calls serialize behind the
same local close lane and do not issue a second transport close.

---

## 4. Daemon Restart & Crash Reconciliation

When the daemon process starts:

1. `sessions.ReconcileCrashedSessions` scans the `<statedir>/sessions/` directory.
2. Any sessions that were left in non-terminal states (`active`, `opening`) are marked as `crashed` with error message `daemon_crash_recovered` and closed timestamp.
3. Updated session state files are synchronized to disk (`SyncDir`).
4. `Get` and `List` fall back to durable session files on disk when querying sessions across daemon restarts.

---

## 5. CLI Commands Reference

All CLI session commands interact with `amcd` via HTTP/JSON REST API with Bearer token authentication:

```bash
# Open a new persistent session
amc session open <machine-guid> --reason "Deploying build" --cols 120 --rows 40 [--approval-file approval.json]

# Read buffered output
amc session read <session-id> --after-seq 0 --limit 65536

# Write input or shell commands
amc session write <session-id> "powershell.exe -NoProfile\r\n" --reason "Start PowerShell" [--approval-file approval.json]

# Send control keystroke (ctrl-c, ctrl-d, enter, tab, up, down, left, right)
amc session control <session-id> ctrl-c --reason "Interrupt command" [--approval-file approval.json]

# Wait for output settle or regex match
amc session wait <session-id> --settle-ms 500 --timeout 30
amc session wait <session-id> --regex "PS [A-Z]:\\\\> " --timeout 30

# List active and recent sessions
amc session list [--machine <machine-guid>]

# Show session details
amc session show <session-id>

# Close a session
amc session close <session-id> --reason "Automation finished" [--approval-file approval.json]

# Interactive attach (stream output until disconnect or close)
amc session attach <session-id>
```

---

## 6. Complete 20 MCP Tools Reference

AMC exposes exactly 20 Model Context Protocol (MCP) tools for agent integration:

### Machine, Operation & Receipt Management Tools (12)

1. `doctor`: Run preflight health checks and report environment capabilities.
2. `machine_list`: List discovered Hyper-V virtual machines.
3. `machine_inspect`: Inspect detailed state and configuration of a virtual machine.
4. `checkpoint_list`: List checkpoints for a virtual machine.
5. `machine_start`: Start or resume a virtual machine.
6. `machine_stop`: Stop, power off, or save a virtual machine.
7. `checkpoint_create`: Create a VM checkpoint / snapshot.
8. `checkpoint_restore`: Restore a VM to a specified checkpoint.
9. `operation_list`: List operations.
10. `operation_show`: Inspect progress and outcome of an asynchronous operation.
11. `operation_wait`: Wait for operation completion or state transition.
12. `receipt_show`: Fetch cryptographic execution receipt by receipt ID.

### Persistent SSH Terminal Sessions Tools (8)

1. `session_open`: Open a persistent SSH terminal session with explicit reason, idempotency key, and required timeout.
2. `session_read`: Read incremental terminal output chunks with cursor pagination.
3. `session_write`: Send command text or input bytes with a required execution timeout (redacted in audit/receipts).
4. `session_control`: Send supported control keystrokes (`ctrl-c`, `ctrl-d`, `enter`, etc.) with a required execution timeout.
5. `session_wait`: Wait for terminal output to settle or match a regular expression pattern.
6. `session_list`: List active and durable persistent sessions.
7. `session_show`: Show detailed observation metrics and metadata for a specific session.
8. `session_close`: Close an active terminal session with a required execution timeout and truthful force semantics.

---

## 7. Acceptance & Verification Testing

AMC provides comprehensive synthetic and unit testing suites:

- `internal/guest/ssh`: Fake SSH server with PTY emulation, OSC 52 injection, UTF-8 chunk splitting, and host key verification tests.
- `internal/sessions`: Ring buffer bounds, multi-reader isolation, crash reconciliation, and concurrent read/write/close race tests.
- `internal/app`: Dynamic safety classification, SessionService mutation policy enforcement, admission and terminal audit logs, idempotent receipt deduplication, and racing input synchronization tests.
- `internal/daemon`: End-to-end HTTP API integration tests for all session routes with strict Bearer token authentication and error sanitization.
- `internal/mcpadapter`: JSON schema golden snapshot verification ensuring all 20 MCP tools match standard contracts.
