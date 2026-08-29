# Recommended GitHub repository settings

These settings cannot take effect until the public repository exists. Apply them after the first
push and after every required check has run once so GitHub can select it by name.

## General

- Default branch: `main`.
- Enable issues, discussions, private vulnerability reporting, dependency graph, Dependabot alerts,
  and automatic security updates.
- Allow squash merge with Conventional Commit subjects; disable merge commits and force pushes.
- Automatically delete head branches after merge.
- Enable merge queue when concurrent contribution volume justifies it.

## Main-branch ruleset

- Require a pull request with at least one approval.
- Dismiss stale approvals and require approval of the latest reviewable push.
- Require Code Owner review for owned paths.
- Require conversation resolution and linear history.
- Block branch deletion and force pushes.
- Require the branch to be current, or use merge queue to avoid repeated manual rebases.
- Do not allow routine bypass; reserve an audited break-glass path for maintainers.

Required checks should include the CI matrix, lint, race detector, CodeQL, security workflow,
dependency review on pull requests, documentation checks, and code-review-graph impact analysis.
Do not make scheduled-only Scorecard runs a pull-request requirement.

## Release protection

- Protect `v*` tags from modification or deletion.
- Release only from GitHub Actions; do not publish workstation-built archives.
- Keep releases as drafts until checksums, provenance, license contents, and platform smoke results
  are reviewed.
- Add environments with reviewer approval before introducing package registries or signing secrets.

## Ownership

`.github/CODEOWNERS` provides an initial single-maintainer default. Split ownership by backend,
security policy, protocol, and UI when reliable maintainers exist; avoid fictional teams or owners.
