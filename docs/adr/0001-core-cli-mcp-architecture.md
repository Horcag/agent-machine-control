# ADR-0001: Shared machine-control core with CLI, daemon, and MCP adapters

**Status:** Accepted

**Date:** 2026-08-29

**Deciders:** Nikita; implementation agent after a live canary

## Context

The existing Tiger Hyper-V bridge provides useful primitives, but every invocation starts a
new elevated PowerShell path and a one-shot PowerShell Direct call. It has no mouse, PTY,
semantic Windows UI, bidirectional file copy, server-side policy, or common interface for
agents other than the one holding the shell.

The replacement must be useful through both CLI and MCP. MCP failure must not remove the
operator's recovery path. Hyper-V is the first backend, while the domain model must not bake
in Tiger, VM names, WSL paths, Codex, or a particular agent client.

## Decision

Build a standalone Go project named Agent Machine Control with four executable surfaces:

1. `amc`: stable human/agent CLI. It can call `amcd` for fast persistent operations or use a
   local `--direct` backend for recovery when the daemon is unavailable.
2. `amcd`: long-lived host runtime. It owns cached Hyper-V CIM handles, persistent PowerShell
   Direct/SSH/PTY sessions, event-driven waits, policy, receipts, audit, and live state.
3. `amc-mcp`: thin stdio or Streamable HTTP MCP adapter. It translates MCP tools to the same
   application service used by `amc`; it contains no VM business logic.
4. `amc-ui`: one web bundle served by `amcd`, optionally also returned as an MCP App. It shows
   VM state, console frames, terminal output, approvals, receipts, and human takeover.

The source layout should begin as:

```text
cmd/amc/                 CLI entrypoint
cmd/amcd/                long-lived daemon
cmd/amc-mcp/             MCP adapter
internal/app/            use cases and capability negotiation
internal/domain/         machine, session, operation, receipt, approval types
internal/policy/         read/mutate/destructive classification and gates
internal/backends/       backend interfaces and registry
internal/backends/hyperv host lifecycle, checkpoints, console image/input
internal/guest/psdirect/ PowerShell Direct commands and bidirectional files
internal/guest/ssh/      SSH and persistent terminal sessions
internal/desktop/windows optional Windows-MCP/UIA sidecar adapter
internal/transport/      named pipe, loopback HTTP, stdio
internal/audit/          append-only redacted operation records
web/                     shared standalone/MCP App operator UI
```

### Core contract

Backends advertise capabilities instead of forcing a lowest-common-denominator interface:

```go
type Backend interface {
    Capabilities(ctx context.Context, machine MachineRef) (CapabilitySet, error)
    Inspect(ctx context.Context, machine MachineRef) (MachineState, error)
    Execute(ctx context.Context, op Operation) (Receipt, error)
    Watch(ctx context.Context, query WatchQuery) (<-chan Event, error)
}
```

Every mutating call accepts an idempotency key, deadline, actor, reason, and approval receipt.
Every result includes timestamps, effective backend, exit status, evidence references, and
whether the state was observed or inferred.

### CLI contract

The first CLI slice should expose:

```text
amc doctor
amc machine list|inspect|start|stop
amc checkpoint list|create|restore
amc guest exec|shell|put|get
amc console screenshot|key|type|move|click|drag|scroll
amc session list|attach|close
amc operation wait|show|cancel
amc audit tail|show
```

CLI output defaults to concise human text and supports `--json`. Destructive commands require
an exact machine identifier and interactive confirmation unless a valid approval receipt or
explicit non-interactive policy is supplied. `--direct` never silently falls back to a broader
privilege or network path.

### Fast path and recovery path

- Fast path: client -> `amcd` over a Windows named pipe or authenticated loopback/internal
  HTTP -> cached session/backend.
- Recovery path: `amc --direct` -> local Hyper-V WMI/PowerShell Direct. It must not require
  MCP, the web UI, Windows-MCP, SSH, or `amcd`.
- MCP path: agent -> `amc-mcp` -> `amcd`; selected safe read operations may use embedded direct
  mode only when configured explicitly.

### Terminal and desktop

- Use an isolated Hyper-V management switch, not the production LAN.
- Use Windows OpenSSH with key authentication for persistent ConPTY-backed terminal sessions.
- Adapt the proven session ideas from `pty-mcp`: incremental reads, control keys, settle/regex
  waits, bounded buffers, transcripts, redaction, attach/detach, and cleanup.
- Run Windows-MCP as an optional least-privilege guest sidecar for UI Automation and fast
  semantic element actions. Disable telemetry and unnecessary shell/filesystem tools.
- Keep host-side Hyper-V framebuffer plus synthetic keyboard/mouse as the console and secure-
  desktop fallback. Guest UIA cannot cross the Windows lock/UAC secure-desktop boundary.

### Approval and operator UI

Policy is enforced server-side; client confirmation dialogs are supplementary. Classify tools:

- observe: automatic;
- reversible mutation: automatic only when policy allows and a rollback checkpoint exists;
- destructive/privileged: MCP elicitation or UI approval receipt required;
- forbidden: denied regardless of agent request.

Use MCP Elicitation for accept/decline/cancel where supported. Serve the same controls through
the standalone local web UI for clients without MCP Apps. Secrets never pass through elicitation.

### Security

- Store guest credentials in Windows Credential Manager/DPAPI; never in scripts, configs, tool
  arguments, transcripts, or MCP results.
- Bind privileged services to named pipe/loopback by default. Remote HTTP requires TLS,
  authentication, per-client identity, and firewall allowlisting.
- Separate lifecycle, terminal, desktop, and destructive permissions.
- Redact credentials, auth headers, private keys, clipboard contents, and configured patterns.
- Make checkpoints and evidence capture explicit prerequisites for destructive sandbox tests.

## Options considered

### Extend the existing `vmctl` Bash wrapper

Low initial cost, but Bash cannot efficiently own persistent Windows sessions, structured MCP
schemas, capability discovery, streaming frames, policy, and multi-client concurrency. Keep it
only as migration input and later a compatibility shim.

### Adopt `originsec/hyperv-mcp` unchanged

It covers lifecycle, PowerShell Direct, and file transfer, but not framebuffer input, mouse,
semantic GUI, PTY, operator takeover, or CLI recovery. Reuse patterns after source review rather
than making it the core.

### Compose independent MCP servers only

Fast to demo, but safety, identity, receipts, idempotency, naming, and failure behavior would
diverge. External projects should remain optional sidecars or implementation references behind
one domain contract.

## Consequences

- CLI and MCP cannot drift because both call the same application service.
- A dead MCP adapter does not prevent local recovery.
- Fast persistent sessions require `amcd`; recovery remains slower but independent.
- Hyper-V-specific behavior stays isolated, making libvirt/VMware/VirtualBox future backends
  possible without pretending their capabilities are identical.
- The project owns a meaningful security boundary and therefore needs adversarial tests,
  explicit policy defaults, and real-client acceptance rather than configuration-only checks.

## Initial acceptance

The first canary is complete only when a clean Tiger sandbox checkpoint can be inspected through
both CLI and two distinct MCP clients; a persistent TUI can receive arrows and `y/n`; a normal
Windows dialog can be driven semantically; a console screenshot plus synthetic mouse handles the
fallback path; a file round-trips both directions; killing `amc-mcp` still leaves `amc --direct`
usable; and every mutation has a redacted receipt.
