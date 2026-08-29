# Public and local data boundary

This repository is designed to be public. Assume every tracked file, commit, pull request,
workflow log, release artifact, and issue attachment is visible forever.

## Track publicly

- Source code, tests, generated synthetic fixtures, and reusable scripts.
- Architecture decisions, protocol contracts, threat models, and public roadmap outcomes.
- CI, release, lint, dependency, and security-scanning configuration.
- Reproducible local-tool configuration such as pinned `.mcp.json` entries, provided it contains
  no machine paths, credentials, or private endpoints.
- Sanitized examples using synthetic machine names, private documentation ranges such as
  `192.0.2.0/24`, and fake credentials that cannot authenticate anywhere.
- Reproducible benchmark methodology and aggregate results that reveal no private host details.

## Keep local and ignored

- `kanban/`: maintainer assignments, local sequencing, and in-progress notes.
- `.codex/` and `.agent-work/`: agent handoffs, task state, and local orchestration data.
- `.code-review-graph/`: generated graph database, exports, absolute paths, and derived metadata.
- `local/` and `state/`: machine registry, cached capabilities, sockets, receipts, and daemon data.
- `artifacts/`, `recordings/`, `screenshots/`, and `transcripts/`: runtime evidence pending explicit
  sanitization.
- `.env*`, credential exports, developer overrides, IDE state, coverage, and build output.

## Never publish

- Passwords, tokens, private keys, recovery codes, cookies, or credential-store exports.
- Real guest or production data, VM disks/exports, memory dumps, clipboard contents, or case data.
- Private hostnames, usernames, MAC addresses, internal topology, reachable management addresses,
  or firewall rules tied to a real environment.
- Unredacted terminal transcripts, screenshots, crash archives, support bundles, or MCP payloads.
- Proprietary code or license-incompatible third-party source copied for convenience.

## Sanitization rule

Moving a file out of an ignored directory does not make it safe. Before publishing evidence,
create a new sanitized artifact, inspect it manually, and verify that it contains only synthetic
or explicitly approved information. Never edit the original evidence in place and assume the
result is safe.

## Third-party material

Prefer dependencies over copied source. When adaptation requires code reuse, record the origin,
revision, license, modifications, and required notices before adding it. Keep `NOTICE` and
third-party license inventories current in release archives.
