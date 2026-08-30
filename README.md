# Agent Machine Control

Agent Machine Control is a local-first control plane for virtual machines that gives humans,
CLI automation, and MCP-capable agents the same policy-enforced operations.

> [!WARNING]
> This project is pre-alpha. Discovery, privileged recovery mutations, daemon operations, MCP,
> and persistent SSH/PTTY sessions are implemented, but live Windows acceptance and the remaining
> release backlog are incomplete. Do not use unreleased development builds on production systems.

## Design goals

- One domain core shared by CLI and MCP, so behavior cannot drift between clients.
- A direct CLI recovery path that remains usable when the daemon or MCP adapter is down.
- Fast persistent terminal and desktop sessions without making a third-party sidecar the
  security boundary.
- Capability-based hypervisor backends rather than hard-coded assumptions about one VM.
- Server-side approvals, redacted receipts, least privilege, and explicit rollback boundaries.

Hyper-V is the first backend and Windows 10 LTSC is the first acceptance target. The design
leaves room for libvirt, VMware, and VirtualBox backends without pretending that their
capabilities are identical.

## Available executables

| Binary | Purpose |
| --- | --- |
| `amc` | Human- and agent-friendly CLI, including local `--direct` recovery mode. |
| `amcd` | Authenticated operations, redacted receipts, audit, and persistent sessions. |
| `amc-mcp` | Thin stdio and Streamable HTTP adapter over the shared application core. |

## Implemented commands

`amc` implements discovery and direct recovery commands for local Hyper-V management:

```sh
# Check Hyper-V and host readiness
amc doctor
amc doctor --json

# List discovered virtual machines
amc machine list
amc machine list --json

# Inspect a single virtual machine by GUID
amc machine inspect c4a523d4-6b99-4d62-a5e2-4752c0f20001
amc machine inspect c4a523d4-6b99-4d62-a5e2-4752c0f20001 --json

# Direct recovery mutations (in-process fallback when daemon is down)
amc --direct machine start <guid> --reason "recovering vm" --idempotency-key "k-1"
amc --direct machine stop <guid> --mode shutdown --reason "stopping vm" --idempotency-key "k-2"
amc --direct checkpoint list <guid>
amc --direct checkpoint create <guid> --name "pre-maintenance" --reason "snapshotting" --idempotency-key "k-3"
amc --direct checkpoint restore <guid> <checkpoint-guid> --reason "reverting vm" --idempotency-key "k-4"
```

Daemon-backed CLI commands and the MCP adapter expose the same application service for managed
operations, receipts, audit records, and persistent guest SSH/PTTY sessions. See
[Persistent SSH sessions](docs/ssh-sessions.md) for session setup, security boundaries, and the
`session open`, `read`, `write`, `control`, `wait`, `list`, `show`, `close`, and operator-only
`session approve` commands.

### JSON mode

Every command supports `--json` for machine-readable automation. Output envelopes conform to schema version `1`, emitting sorted arrays for `capabilities`, `machines`, `network_adapters`, and `ip_addresses`.

### VM-GUID inspect requirement

The `amc machine inspect <guid>` command requires a valid 36-character Hyper-V VM GUID (for example `c4a523d4-6b99-4d62-a5e2-4752c0f20001`). Non-GUID inputs or missing arguments are rejected before invoking the provider.

### Hyper-V and PowerShell prerequisites

Observation and recovery operations require `powershell.exe` in `PATH`, the Windows Hyper-V PowerShell module (`Hyper-V`), and a Windows security token with membership in the local `Administrators` (`S-1-5-32-544`) or `Hyper-V Administrators` (`S-1-5-32-578`) group.

- **Windows**: `powershell.exe` is available by default on Windows systems. The current user token must belong to local `Administrators` or `Hyper-V Administrators`.
- **WSL interop**: When running inside WSL, WSL interop discovers `powershell.exe` in the host Windows PATH (for example `/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe`) and queries the host Hyper-V instance. The host Windows security token must belong to local `Administrators` or `Hyper-V Administrators`.

### Exit categories and codes

`amc` uses deterministic process exit codes:

| Code | Name | Description |
| --- | --- | --- |
| `0` | `ExitSuccess` | Normal successful completion. |
| `2` | `ExitUsage` | Incorrect command invocation, missing arguments, or invalid flags. |
| `3` | `ExitNotFound` | The requested virtual machine GUID was not found. |
| `4` | `ExitBackendUnavailable` | PowerShell, the Hyper-V module, or host management is unreachable or denied. |
| `5` | `ExitMalformedProvider` | The provider returned corrupt, invalid, or oversized data. |
| `6` | `ExitTimeout` | The operation exceeded its configured deadline. |
| `7` | `ExitDenied` | Policy evaluation denied the operation, or interactive confirmation was rejected. |
| `8` | `ExitConflict` | Concurrent lease conflict, fencing violation, or idempotency key collision. |

### Privileged-operation boundary

Observation and mutation operations are scope-gated. Mutations carry an actor, reason, deadline,
and idempotency key and pass through server-owned policy, lease, audit, approval, and redacted
receipt handling. `amc --direct` keeps an independent in-process recovery path while using the same
application contracts and shared host coordination. Persistent SSH/PTTY sessions require `amcd`;
they do not make guest sidecars authoritative for policy, identity, approval, or audit truth.

`amcd bootstrap ensure|status|start|stop|remove` manages the single current-user S4U Limited
Scheduled Task that starts local WSL `amcd` automatically. It fingerprints the exact principal,
logon trigger, settings, action, wrapper, metadata, binary, state directory, and process identity;
drift is refused rather than repaired. See [Automatic local amcd bootstrap](docs/amcd-bootstrap.md).

### Current release backlog

Hyper-V console framebuffer capture and synthetic input, the optional Windows UIA sidecar, the
operator web/MCP App UI, signed packaging, and the cross-client Windows canary are not complete.
The repository does not claim live Windows acceptance for these capabilities.

## Build the bootstrap

Go 1.25 or newer is required.

```sh
go test ./...
go build ./cmd/...
go run ./cmd/amc --version
```

The public API and command surface are not stable before `v0.1.0`.

## Documentation

- [Architecture decision](docs/adr/0001-core-cli-mcp-architecture.md)
- [Language and toolchain decision](docs/adr/0002-language-and-toolchain.md)
- [Public versus local data boundary](docs/public-private-boundary.md)
- [Threat model](docs/threat-model.md)
- [Development guide](docs/development.md)
- [Quality system](docs/quality.md)
- [Testing strategy](docs/testing.md)
- [Automatic local amcd bootstrap](docs/amcd-bootstrap.md)
- [Autonomous and parallel agent workflow](docs/agent-workflow.md)
- [Recommended repository settings](docs/repository-settings.md)
- [Open-source baseline research](docs/research/open-source-baseline.md)
- [Roadmap](ROADMAP.md)

## Security and contributions

Machine-control software is privileged by nature. Read [SECURITY.md](SECURITY.md) before
testing and never publish credentials, real guest data, private topology, memory dumps, or
unredacted automation transcripts.

Contributions are welcome under the [Apache License 2.0](LICENSE). Start with
[CONTRIBUTING.md](CONTRIBUTING.md) and the [Code of Conduct](CODE_OF_CONDUCT.md).

The repository includes an optional pinned code-review-graph MCP configuration. Its local graph,
Kanban board, agent handoffs, and runtime evidence are ignored and never published by default.
