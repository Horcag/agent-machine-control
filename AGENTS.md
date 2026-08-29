# Agent guidance

This repository is an open-source machine-control tool. Treat every backend and transport as a
privileged boundary.

## Architecture

- Keep domain operations in `internal/app` and `internal/domain`.
- Keep CLI and MCP entrypoints thin; they must call the same application service.
- Preserve `amc --direct` as an independent recovery path. It must not require `amcd`, MCP,
  the web UI, SSH, or a guest sidecar.
- Backends advertise capabilities. Do not add hypervisor-specific conditions to shared domain
  packages.
- Third-party MCP and computer-use projects are optional sidecars or references, never the
  source of policy, identity, approval, or audit truth.

## Security and privacy

- Never commit credentials, tokens, private keys, real VM inventories, private IP topology,
  guest data, screenshots, dumps, or unredacted transcripts.
- Use generated fixtures and synthetic machine names in tests and documentation.
- Classify operations as observe, reversible mutation, destructive/privileged, or forbidden.
- Every mutation needs an idempotency key, actor, reason, deadline, and redacted receipt.
- Destructive tests require a disposable target and a verified rollback point.

## Engineering

- Go is the primary language. Use the standard library before adding dependencies.
- Public code and documentation are written in English.
- Add tests with behavior changes and run `make check`; use `make test-race` for concurrent code.
- Keep changes focused. Do not add speculative interfaces or cross-backend abstractions before
  a second concrete backend needs them.
- Use Conventional Commit subjects. Do not add AI attribution or generated-by trailers.
- Local `kanban/`, `.codex/`, `state/`, and evidence directories are intentionally ignored.
