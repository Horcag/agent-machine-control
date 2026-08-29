# Quality system

The quality system favors objective, reproducible signals over subjective "AI code" detectors.
Automation should catch defects and architectural drift without rewarding ceremonial abstractions
or forcing contributors to satisfy arbitrary style metrics.

## Local gates

| Command | Purpose |
| --- | --- |
| `make quick` | Formatting, module integrity, vet, unit tests, and builds. Fast edit loop. |
| `make check` | `make quick` plus the pinned golangci-lint policy. Commit-ready gate. |
| `make quality` | Check, race detector, Actions lint, vulnerability scan, and secret scan. Pre-push/review gate. |
| `make graph-review` | Local structural blast-radius report for the current diff. Advisory, not authoritative. |

`make hooks` installs the pinned Lefthook configuration. Hooks improve feedback time, but CI
remains authoritative because hooks can be absent or bypassed.

CLI tool versions live in `quality/tool-versions.env`; `make quick` verifies that workflow inputs
and repository MCP configuration still match that source of truth.

## Code signals

The Go policy combines compiler errors, `gofmt`, `go vet`, `staticcheck`, `gosec`, and selected
golangci-lint analyzers. It also reports:

- duplicated blocks (`dupl`);
- excessive control-flow complexity (`cyclop`);
- low maintainability (`maintidx`);
- unused parameters and stale suppressions (`unparam`, `nolintlint`);
- common performance and correctness traps (`gocritic`, `bodyclose`, `makezero`).

Thresholds are early-warning boundaries, not design goals. Do not split cohesive code into noisy
wrappers merely to reduce a score. Prefer deletion and reuse when the duplicated concept is stable;
keep two similar implementations separate when their domain rules genuinely differ.

## Architecture and reuse

- CLI and MCP adapters call the same application service.
- Domain packages do not import transports or hypervisor backends.
- Backends implement capability contracts; shared code does not switch on product or VM names.
- Sidecars remain behind owned interfaces and cannot define authorization or audit truth.
- New dependencies require a concrete need, license review, maintenance evidence, and an exit path.

The architecture is currently protected by review and contract tests. Add an automated import-
boundary gate once the real `internal/app`, `internal/domain`, `internal/policy`, and backend
packages exist; an empty architecture framework would be noise today.

## Review separation

Authors do not self-approve. A merge-ready change requires:

1. objective local and CI evidence;
2. an independent code/security review;
3. an independent architecture/trade-off review for boundary changes;
4. live platform evidence for Windows, Hyper-V, PTY, UI, installer, or secure-desktop claims.

Graph risk scores and green CI narrow review scope; neither proves correctness.

## Future gates

Add fuzz targets with parsers, protocol envelopes, policy inputs, and untrusted paths. Add a
coverage ratchet after domain behavior exists, using the current accepted baseline rather than an
arbitrary initial percentage. Add mutation testing only if it finds real test-quality gaps without
making the ordinary loop too slow.
