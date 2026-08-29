# Open-source repository baseline

Research date: 2026-08-29. Counts are snapshots, not permanent claims.

## References reviewed

| Repository | Snapshot | What we adopt |
| --- | ---: | --- |
| [modelcontextprotocol/servers](https://github.com/modelcontextprotocol/servers) | 89k stars | Protocol-first documentation and clear separation of reference implementations. |
| [microsoft/playwright-mcp](https://github.com/microsoft/playwright-mcp) | 36k stars | Issue-first contribution policy, hermetic tests, high dependency bar, explicit statement that MCP is not a security boundary. |
| [github/github-mcp-server](https://github.com/github/github-mcp-server) | 32k stars | Go layout, cross-platform CI, golangci-lint, GoReleaser, draft releases, checksums, provenance, tool documentation, and license checks. |
| [cli/cli](https://github.com/cli/cli) | 46k stars | Mature Go CLI packaging, command ownership, linting, vulnerability checks, and release automation. |
| [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) | 5k stars | Official SDK, protocol compatibility, conformance, race tests, CodeQL, Scorecard, pinned Actions, and multi-version Go testing. |
| [tirth8205/code-review-graph](https://github.com/tirth8205/code-review-graph) | 30k stars | Local-first structural graph, incremental impact analysis, multi-client MCP, and no external code upload. |
| [oraios/serena](https://github.com/oraios/serena) | 28k stars | Symbol-level language-server retrieval and memory model; reviewed but deferred due overlap. |
| [evilmartians/lefthook](https://github.com/evilmartians/lefthook) | 8k stars | Fast cross-platform hooks with parallel pre-push jobs. |
| [gitleaks/gitleaks](https://github.com/gitleaks/gitleaks) | 29k stars | Local and CI Git-history secret scanning. |
| [zizmorcore/zizmor](https://github.com/zizmorcore/zizmor) | 6k stars | GitHub Actions security analysis in addition to syntax validation. |

## Adopted baseline

- Go 1.25 minimum with current Go tested in CI.
- Cross-platform test/build jobs and Linux race tests.
- `gofmt`, `go vet`, golangci-lint v2, and CodeQL.
- Read-only workflow permissions by default; elevated permissions only in release/security jobs.
- Immutable action revisions, Dependabot, OpenSSF Scorecard, and draft GoReleaser releases.
- Apache-2.0 licensing, private vulnerability reporting, issue-first contributions, and an
  explicit public/local data boundary.
- Small focused pull requests, synthetic fixtures, and no acceptance claims from CI alone.
- One local structural code graph, optional hooks, independent review lanes, and worktree isolation
  for parallel writers.

## Deliberately not copied

- Enterprise-specific moderation, GitHub-internal CodeQL packs, OAuth secrets, and organization
  automation from GitHub MCP Server.
- Playwright's CLA bot and monorepo-only contribution flow.
- Large third-party license generation machinery before the project has dependencies.
- Docker publishing, registry publishing, Homebrew, Winget, Scoop, installers, and SBOM tooling
  before a functional pre-release exists.
- Unpinned community Actions or security controls that only look protective but are bypassable.
- Overlapping semantic memory/editing MCPs before a measured retrieval gap exists.
- Subjective AI-slop classifiers; objective duplication, complexity, architecture, test, and
  security evidence is harder to game and easier to review.

## Review trigger

Revisit this baseline before `v0.1.0`, when adding the web UI, and whenever the official MCP SDK
changes its supported protocol or Go-version policy.
