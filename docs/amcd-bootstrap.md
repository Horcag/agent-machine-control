# Automatic local amcd bootstrap

`amcd bootstrap` manages one persistent Windows Scheduled Task for the current Windows user. The
task launches the local WSL `amcd` process automatically without storing a password or bearer
token. It is host-control infrastructure; it does not select, enroll, start, stop, or otherwise
mutate a virtual machine.

## Commands

Run these commands from WSL with the same `amcd` binary and state directory that the scheduled
daemon must use:

```sh
amcd bootstrap ensure \
  --state-dir /mnt/c/ProgramData/amc \
  --reason "install the local control daemon" \
  --idempotency-key "bootstrap-install-2026-08-30" \
  --timeout 45s \
  --json

amcd bootstrap status --state-dir /mnt/c/ProgramData/amc --json

amcd bootstrap start \
  --state-dir /mnt/c/ProgramData/amc \
  --reason "recover the local control daemon" \
  --idempotency-key "bootstrap-recover-2026-08-30" \
  --timeout 45s \
  --json

amcd bootstrap stop \
  --state-dir /mnt/c/ProgramData/amc \
  --reason "perform host maintenance" \
  --idempotency-key "bootstrap-stop-2026-08-30" \
  --timeout 45s \
  --json

amcd bootstrap remove \
  --state-dir /mnt/c/ProgramData/amc \
  --reason "remove the owned daemon task" \
  --idempotency-key "bootstrap-remove-2026-08-30" \
  --timeout 45s \
  --json
```

Use a new idempotency key for a new operator intent. Repeating the exact command with the same key
returns the durable receipt and the original action's terminal semantic without replaying an
already completed effect or consulting current task/daemon health. A prior failed attempt remains
a `failed` historical replay result with the original receipt; use a new key only for a new
operator intent. Reusing a key with a
different action, reason, user, binary fingerprint, state directory, or task fingerprint is a
conflict. Use `bootstrap status` separately when current-state or drift evidence is required.

## Status and ownership

`status` is read-only and reports one of these states:

- `absent`: the exact task, wrapper, and metadata are absent;
- `stopped`: the exact owned task exists but the authenticated daemon is not healthy; the JSON
  `task_running` field distinguishes a released task from a failed or still-running task process;
- `healthy`: the exact task is running and endpoint, PID, process-start, runtime, and authenticated
  health observations agree;
- `drift`: task, principal, trigger, settings, wrapper, metadata, ACL, hash, endpoint, or process
  identity does not match the expected fingerprint.

Drift is fail-closed. `ensure`, `start`, `stop`, and `remove` do not replace, stop, unregister, or
delete ambiguous state. There is intentionally no automatic repair command.

The task identity is fixed at `\AgentMachineControl\amcd-current-user`. Its principal is derived
from `WindowsIdentity.GetCurrent()` and is pinned to current-user `S4U` with `Limited` run level.
It has a current-user logon trigger, `StartWhenAvailable`, `IgnoreNew` instance behavior, bounded
restart-on-failure, battery-independent startup, and no execution time limit for the long-running
daemon.

Read-back compares one canonical action, principal, and logon trigger. It rejects action working
directory or ID drift; principal defaults, extra privileges, disabled triggers, delay, boundary,
repetition, or execution-limit drift; disabled or hidden tasks; and altered demand-start, restart,
execution, battery, idle, network, wake, priority, compatibility, deletion, remote-session,
unified-scheduling, maintenance, or volatile settings. Version-specific settings are compared when
the installed ScheduledTasks provider exposes them.

The Windows-local PowerShell file launcher and metadata are stored below the current user's Local
AppData. The Scheduled Task invokes canonical Windows PowerShell with fixed non-interactive `-File`
arguments. The launcher uses `Start-Process` with the exact Windows `wsl.exe` path, validated WSL
distribution and Linux user, exact Linux `amcd` path, state directory, loopback listen address, and
fixed daemon flags; it waits and returns the child exit code. The files
and their directory have a canonical protected DACL with exactly two explicit FullControl allow
ACEs: the current SID and LocalSystem. File ACEs have no inheritance or propagation; directory ACEs
have exactly container and object inheritance with no propagation. Deny, inherited, extra, missing,
weak, non-canonical, owner-drifted, reparse, and non-regular objects are rejected. ACLs and SHA-256
hashes are re-verified before mutations. Metadata includes the exact binary hash and complete task
fingerprint. Neither artifact contains bearer tokens, passwords, VM identity, inventory, guest data,
or transcripts.

## Stop and removal behavior

`stop` first attempts the authenticated daemon stop endpoint and gives an acknowledged shutdown a
bounded grace interval to drain. The Scheduled Task stop operation is a fallback only when the
endpoint is unavailable or the grace interval expires, and only after the complete owned
fingerprint is read back again. Success requires endpoint, singleton, and process ownership to
disappear and the exact task to no longer be running.

`remove` performs the same stop protocol, unregisters only the exact owned task, re-verifies both
artifact ACLs and hashes, and removes only the exact wrapper and metadata. It removes the dedicated
artifact directory only when empty.

Each mutation is admitted with the explicit current Windows SID as actor, operator reason, absolute
deadline, and idempotency key. Terminal receipts classify this local host-control boundary as
destructive because it has no verified checkpoint. They intentionally omit `rollback_ref` instead
of fabricating rollback evidence and carry the observed post-effect bootstrap state. A failed exact
retry returns the durable failed receipt and does not blindly repeat an install, start, stop, or
remove effect.

## Boundaries

The bootstrap requires WSL interop, Windows PowerShell, and the ScheduledTasks module. Unsupported
or non-WSL hosts fail before creating bootstrap state. A real S4U task canary remains an explicit
operator acceptance step on a disposable, rollback-protected Windows host.

Bootstrap mutation is an explicit local `amcd` operator CLI boundary. It is not exposed through MCP,
agent tokens, or the remote daemon API and does not create a second remote policy engine.

`amc --direct` does not depend on this lifecycle, `amcd`, Task Scheduler, MCP, SSH, or a guest
sidecar. It remains the independent recovery route.
