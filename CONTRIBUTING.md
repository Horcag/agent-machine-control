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
- GoReleaser v2 only when testing packaging.

```sh
git clone https://github.com/Horcag/agent-machine-control.git
cd agent-machine-control
make check
make test-race
```

## Pull requests

- Link the issue that established scope.
- Keep each pull request focused and explain security or compatibility consequences.
- Include tests for behavior changes and synthetic evidence for OS-specific behavior.
- Update public documentation and `CHANGELOG.md` when users are affected.
- Do not add dependencies without explaining maintenance, licensing, supply-chain, and binary-
  size costs.
- Use Conventional Commit subjects such as `feat(cli): add machine inspection`.

All pull requests require review. Passing CI is necessary but does not replace review or live
acceptance for Hyper-V, Windows desktop, installer, and secure-desktop claims.
