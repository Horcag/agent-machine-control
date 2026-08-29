# ADR-0002: Go for the control plane, CLI, and MCP adapter

**Status:** Accepted

**Date:** 2026-08-29

**Decision owner:** Project maintainer

## Context

The project needs low-latency local operations, persistent concurrent sessions, Windows host
integration, cross-platform CLI distribution, and MCP transports. It should remain approachable
for contributors and must provide a recovery CLI that does not depend on a language runtime being
installed on the target machine.

## Decision

Use Go 1.25 as the minimum version for the control plane, CLI, daemon, MCP adapter, and backend
interfaces. Track the current supported Go release in CI as an additional test target.

Use the official `modelcontextprotocol/go-sdk` when MCP implementation begins. Start on its latest
stable release, not a pre-release. Use the standard library for CLI parsing and configuration
until the real command tree demonstrates a need for a dependency; Cobra is the preferred first
CLI dependency if nested command ergonomics justify it.

The optional operator web UI may use TypeScript because it has different delivery and interaction
constraints. TypeScript must not own machine-control policy or backend behavior.

## Options considered

### Go

- Produces self-contained Windows, Linux, and macOS binaries.
- Strong concurrency and cancellation primitives fit sessions and event streams.
- Fast builds and a smaller maintenance burden than Rust for this team.
- Official MCP SDK and strong production references, including GitHub MCP Server.
- Native Windows APIs remain reachable through `x/sys/windows` while PowerShell/WMI can bootstrap
  the first Hyper-V backend.

### Rust

Rust offers excellent native performance, memory safety, and Windows bindings. It was rejected as
the primary language because compile times, ownership/lifetime complexity, and the smaller pool of
contributors would slow early product iteration without a demonstrated performance need. Rust
remains appropriate for a future isolated helper if profiling proves a Go boundary inadequate.

### TypeScript

TypeScript has the largest MCP ecosystem and the fastest path to an MCP-only prototype. It was
rejected for the core because Node distribution, runtime/process management, Windows native API
friction, and recovery installation are weaker than a single Go binary. It remains appropriate
for the optional web UI.

### Python

Python is attractive for Windows UI Automation and prototyping. It was rejected for the core due
to interpreter packaging, startup overhead, concurrency ergonomics, and dependency distribution.
Python sidecars such as Windows-MCP may be supported behind an owned adapter.

## Consequences

- Public releases can ship three small executables in one platform archive.
- CI must test Windows, Linux, and macOS and run the race detector on Linux.
- Windows-only behavior still requires real Windows acceptance; cross-compilation is not proof.
- Dependencies require explicit maintenance, licensing, supply-chain, and binary-size rationale.
