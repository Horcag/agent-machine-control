# Contributing

Thank you for helping improve Agent Machine Control. Contributions are accepted under the
project's Apache License 2.0.

## Before writing code

For anything beyond a typo or narrowly scoped documentation fix, open an issue first. Describe
the user problem, affected platform or hypervisor, security impact, proposed behavior, and how
the result can be tested without real or sensitive data.

Do not attach private logs, screenshots, dumps, VM exports, credentials, or production topology.
Use synthetic names and redact evidence before posting it.

## Development

Requirements:

- Go 1.25 or newer;
- Git;
- `golangci-lint` v2.13.2 for the full local lint target;
- ShellCheck for repository shell tooling;
- GoReleaser v2 only when testing packaging.

```sh
git clone https://github.com/Horcag/agent-machine-control.git
cd agent-machine-control
make check
make quality
make hooks
```

## Pull requests

- Link the issue that established scope.
- Keep each pull request focused and explain security or compatibility consequences.
- Include tests for behavior changes and synthetic evidence for OS-specific behavior.
- Do not add tests solely to raise coverage. Each test should protect an observable behavior,
  invariant, failure mode, or regression described in `docs/testing.md`.
- Update public documentation and `CHANGELOG.md` when users are affected.
- Do not add dependencies without explaining maintenance, licensing, supply-chain, and binary-
  size costs.
- Do not bypass file-size or complexity gates by scattering cohesive logic into trivial wrappers.
  Temporary exceptions need explicit rationale and a recovery task.
- Use Conventional Commit subjects such as `feat(cli): add machine inspection`.

Use `make quick` for the edit loop, `make check` before committing, and `make quality` before
requesting review. `make hooks` installs the pinned Lefthook configuration; hooks are convenience,
not a replacement for CI.

Parallel contributors use separate Git worktrees and non-overlapping ownership as described in
[`docs/agent-workflow.md`](docs/agent-workflow.md). Authors must not self-approve their changes.

All pull requests require review. Passing CI is necessary but does not replace review or live
acceptance for Hyper-V, Windows desktop, installer, and secure-desktop claims.
