# Agent Machine Control

Agent Machine Control is a local-first control plane for virtual machines that gives humans,
CLI automation, and MCP-capable agents the same policy-enforced operations.

> [!WARNING]
> This project is pre-alpha. The current binaries only expose build information and do not
> control machines yet. Do not use unreleased development builds on production systems.

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

## Planned executables

| Binary | Purpose |
| --- | --- |
| `amc` | Human- and agent-friendly CLI, including local `--direct` recovery mode. |
| `amcd` | Long-lived sessions, events, policy, audit, and operator UI. |
| `amc-mcp` | Thin stdio and Streamable HTTP adapter over the shared application core. |

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
