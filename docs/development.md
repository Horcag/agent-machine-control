# Development guide

## Toolchain

- Go 1.25 or newer.
- `golangci-lint` v2.13.2 for the full lint target. Keep the local and CI versions aligned.
- GoReleaser v2 for packaging checks.

The baseline commands intentionally use ordinary Go tooling so contributors are not required to
install a project-specific task runner.

```sh
make check
make test-race
golangci-lint run
goreleaser release --snapshot --clean
```

On Windows, PowerShell-specific tests must use generated fixtures and disposable VMs. A successful
Linux cross-build does not prove Hyper-V, ConPTY, UI Automation, named-pipe, or secure-desktop
behavior.

## Dependency policy

Use the standard library first. A new dependency must provide a concrete capability, have an
acceptable license, active maintenance, a bounded transitive graph, and a clear removal path.
Pin GitHub Actions to immutable commits and let Dependabot propose reviewed updates.

The planned MCP dependency is the official Go SDK. The planned first optional CLI dependency is
Cobra only if the nested command tree becomes cumbersome with the standard `flag` package.

## Tests

- Unit tests cover domain and policy behavior without a hypervisor.
- Contract tests run every backend against the same capability expectations.
- OS integration tests use generated data and explicit opt-in labels.
- Destructive tests require a disposable target, verified checkpoint, and evidence capture.
- MCP changes require tool-schema snapshots, official conformance checks, and two real clients.

## Commits and releases

Use Conventional Commit subjects. Releases follow SemVer, remain GitHub drafts until reviewed,
and are produced by CI from `v*` tags. Do not publish from a developer workstation.

Release readiness eventually includes tests, lint, race detection, vulnerability scanning,
license inventory, checksums, SBOMs, and provenance attestations. The current bootstrap provides
only the foundations; it is not yet a signed release pipeline.
