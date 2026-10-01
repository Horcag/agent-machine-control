# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html) once public releases
begin.

## [Unreleased]

### Fixed

- Resolve and observe the enrolled target with one fresh exact provider query, without
  depending on unrelated machines or repeating fleet discovery.
- Preserve explicit HTTP operation deadlines and typed failure categories across transports.
- Bound inherited subprocess output pipes after cancellation and retain executor timeout causes.
- Probe machine and network-adapter queries in Hyper-V readiness checks.
- Return sanitized MCP failure categories and validated operation/receipt references for recovery.

### Added

- Initial open-source project structure.
- Shared-core architecture for CLI, daemon, MCP, and operator UI surfaces.
- Go build, test, lint, security scanning, and draft release foundations.
- Local-only kanban workflow and explicit public/private data boundary.
- Shared executable bootstrap with contract tests.
- Objective duplication, complexity, maintainability, vulnerability, secret, workflow, and
  documentation gates.
- Pinned local code-review graph MCP and autonomous parallel-agent workflow.
- File-size, cognitive-complexity, and behavior-core coverage ratchets with an explicit anti-
  coverage-theater testing strategy.

[Unreleased]: https://github.com/Horcag/agent-machine-control/commits/main
