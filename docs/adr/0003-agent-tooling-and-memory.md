# ADR-0003: Local structural graph, objective gates, and reviewable memory

**Status:** Accepted

**Date:** 2026-08-29

**Decision owner:** Project maintainer

## Context

Autonomous and parallel agents need fast repository orientation, impact analysis, coordination,
and review without duplicating source into opaque external memory or loading overlapping MCP tools.
The project must stay usable by clients that support different extension sets.

## Decision

Use pinned `code-review-graph==2.3.8` as the sole repository-level code graph MCP. Store its SQLite
graph under ignored `.code-review-graph/`; keep `.mcp.json` tracked. Expose only graph build and
read-only analysis tools; refactor application, wiki generation, embeddings, and cross-repository
operations remain disabled. Use graph results to narrow inspection, never as authoritative proof.

Use tracked ADRs, contracts, guidance, tests, changelog, and roadmap as public durable memory. Keep
Kanban assignments, handoffs, graph data, and environment evidence local and ignored.

Use Lefthook for fast optional local feedback and CI for authoritative formatting, tests, race
detection, linting, vulnerability scanning, secret scanning, workflow security, dependency review,
CodeQL, documentation, and release checks. Require independent author/reviewer separation.

## Alternatives considered

### Serena plus code-review-graph

Serena offers strong language-server retrieval, refactoring, and memory. It is not enabled now
because its editing, shell, retrieval, and memory tools overlap native agent/IDE capabilities and
would enlarge the privileged tool surface. Revisit if symbol-level retrieval becomes a measured
bottleneck that code-review-graph and native language tooling do not solve.

### External semantic or graph memory service

Rejected by default because it adds synchronization, privacy, availability, and stale-context
failure modes. The reproducible local graph plus versioned repository documents cover the current
need without another service.

### Subjective AI-slop detector

Rejected. Automated gates check duplication, complexity, maintainability, architecture, tests, and
security. Semantic simplicity and unnecessary abstraction remain independent review concerns; a
keyword or style classifier would create false confidence and easy gaming.

### Additional static-analysis MCP servers

Not enabled. Go compiler/vet, golangci-lint, govulncheck, CodeQL, and dependency review already cover
the current source. Revisit an MCP security scanner when a functional MCP server exists and a real
server-specific threat model can be tested.

## Consequences

- Agents get one local structural graph instead of overlapping context tools.
- Public memory is diffable and reviewable; local state cannot silently become project canon.
- Hooks remain fast and optional while CI preserves the full gate.
- New tooling must demonstrate a missing capability, not merely popularity.
