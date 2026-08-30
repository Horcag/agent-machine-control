# Development guide

## Toolchain

- Go 1.25 or newer.
- `golangci-lint` v2.13.2 for the full lint target. Keep the local and CI versions aligned.
- ShellCheck for the small repository shell-tooling surface.
- GoReleaser v2 for packaging checks.

The baseline commands intentionally use ordinary Go tooling so contributors are not required to
install a project-specific task runner.

```sh
make quick
make check
make quality
make hooks
goreleaser release --snapshot --clean
```

`make quick` includes the fast file-size ratchet but excludes coverage, race detection, security
scans, and documentation tooling. Those run in `make quality`, pre-push hooks, and CI so the inner
loop remains responsive.

The pinned analysis commands run through `go run`, `npx`, or `uvx`; the first invocation downloads
tooling and later invocations use local caches. Override a tool command only when reproducing the
same pinned version. CI remains the reference environment.

On Windows, PowerShell-specific tests must use generated fixtures and disposable VMs. A successful
Linux cross-build does not prove Hyper-V, ConPTY, UI Automation, named-pipe, or secure-desktop
behavior.

## Dependency policy

Use the standard library first. A new dependency must provide a concrete capability, have an
acceptable license, active maintenance, a bounded transitive graph, and a clear removal path.
Pin GitHub Actions to immutable commits and let Dependabot propose reviewed updates.

The planned MCP dependency is the official Go SDK. The planned first optional CLI dependency is
Cobra only if the nested command tree becomes cumbersome with the standard `flag` package.

Persistent guest terminals require `golang.org/x/crypto/ssh` as a direct dependency because the Go
standard library does not implement SSH transport, host-key verification, or PTY channel requests.
Windows key protection and ACL validation require the direct, upgraded `golang.org/x/sys/windows`
surface for DPAPI and security-descriptor APIs. These are distinct capability additions; the session
implementation did not merely start using a dependency that was already indirect.

The repo-local development MCP is `code-review-graph==2.3.8`, configured in `.mcp.json`. It is an
analysis aid with an explicit read-oriented tool allowlist and is not linked into product binaries.
Serena and additional code-analysis MCPs are deliberately deferred until a measured gap justifies
overlapping tool and memory surfaces.

## Tests

- Unit tests cover domain and policy behavior without a hypervisor.
- Contract tests run every backend against the same capability expectations.
- OS integration tests use generated data and explicit opt-in labels.
- Destructive tests require a disposable target, verified checkpoint, and evidence capture.
- MCP changes require tool-schema snapshots, official conformance checks, and two real clients.

Coverage applies to `internal/...` only and follows `docs/testing.md`. It is a lower debt boundary,
not a request for tests around thin command wiring or implementation trivia.

## Commits and releases

Use Conventional Commit subjects. Releases follow SemVer, remain GitHub drafts until reviewed,
and are produced by CI from `v*` tags. Do not publish from a developer workstation.

Release readiness eventually includes tests, lint, race detection, vulnerability scanning,
license inventory, checksums, SBOMs, and provenance attestations. The current bootstrap provides
only the foundations; it is not yet a signed release pipeline.
