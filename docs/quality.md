# Quality system

The quality system favors objective, reproducible signals over subjective "AI code" detectors.
Automation should catch defects and architectural drift without rewarding ceremonial abstractions
or forcing contributors to satisfy arbitrary style metrics.

## Local gates

| Command | Purpose |
| --- | --- |
| `make quick` | Formatting, module integrity, vet, unit tests, and builds. Fast edit loop. |
| `make check` | `make quick` plus the pinned golangci-lint policy. Commit-ready gate. |
| `make quality` | Check, race, coverage, shell/Actions lint, vulnerability, secrets, and docs. Pre-push/review gate. |
| `make graph-review` | Local structural blast-radius report for the current diff. Advisory, not authoritative. |

`make hooks` installs the pinned Lefthook configuration. Hooks improve feedback time, but CI
remains authoritative because hooks can be absent or bypassed.

CLI tool versions live in `quality/tool-versions.env`; `make quick` verifies that workflow inputs
and repository MCP configuration still match that source of truth.

Warm-cache feedback budgets are 10 seconds for `make quick`, 30 seconds for `make check`, and two
minutes for `make quality`. If a gate exceeds its budget consistently, profile or move a check to
the next layer instead of teaching contributors to skip it. The initial measured times were 1.62
seconds for quick and 38.32 seconds for quality on the maintainer workstation.

## Code signals

The Go policy combines compiler errors, `gofmt`, `go vet`, `staticcheck`, `gosec`, and selected
golangci-lint analyzers. It also reports:

- duplicated blocks (`dupl`);
- excessive control-flow complexity (`cyclop`);
- low maintainability (`maintidx`);
- unused parameters and stale suppressions (`unparam`, `nolintlint`);
- common performance and correctness traps (`gocritic`, `bodyclose`, `makezero`).
- cognitive and nested-branch complexity (`gocognit`, `nestif`) alongside cyclomatic complexity.

Thresholds are early-warning boundaries, not design goals. Do not split cohesive code into noisy
wrappers merely to reduce a score. Prefer deletion and reuse when the duplicated concept is stable;
keep two similar implementations separate when their domain rules genuinely differ.

## Size and complexity ratchets

Production source files under the owned code roots warn above 300 physical lines and fail above
500. Test files warn above 450 and fail above 700 so cohesive table-driven scenarios have room
without turning into unreviewable fixtures. The policy covers Go plus future shell, PowerShell,
TypeScript, JavaScript, and CSS sources. Temporary exceptions live in
`quality/file-size-exceptions.txt`; stale exceptions fail the gate automatically.

Function gates currently fail at cyclomatic complexity 15, cognitive complexity 20, or nested-if
complexity 5. These are review boundaries, not invitations to split logic into meaningless helpers.
When a cohesive state machine legitimately exceeds a threshold, prefer a narrowly documented lint
exception with rationale over architecture distortion.

Coverage protects only the behavioral `internal/...` core with file/package/total floors of
70/80/85 percent. See `docs/testing.md` for what deserves a test and what should remain untested.

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

Add fuzz targets with parsers, protocol envelopes, policy inputs, and untrusted paths. Add coverage
diff reporting after the public default branch exists and can provide a trustworthy base artifact.
Add mutation testing only if it finds real test-quality gaps without slowing the ordinary loop.
