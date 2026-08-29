# Testing strategy

Tests exist to protect behavior and risk boundaries, not to maximize a percentage or mirror the
implementation line by line.

## What deserves tests

- Domain decisions, policy classification, authorization, idempotency, and receipt invariants.
- Error handling, cancellation, deadlines, retries, partial failure, and cleanup.
- Parsers and protocol envelopes with malformed, adversarial, and boundary inputs.
- Backend contracts shared by every hypervisor implementation.
- Concurrency, session lifecycle, bounded buffers, and takeover behavior.
- CLI and MCP observable contracts when they can diverge from the shared application service.
- Regression tests for bugs that could recur.

## What usually does not

- Thin `main` packages that only pass arguments into a tested shared entrypoint.
- Literal constants, trivial getters, compiler-enforced type relationships, or generated code.
- Third-party library behavior that the project neither wraps nor changes.
- Private implementation details whose refactoring should not affect users.
- The same assertion repeated at unit, integration, and end-to-end layers without distinct risk.

An uncovered line is a review prompt, not an automatic instruction to add a test. Prefer one
scenario that proves an invariant over several examples that exercise the same happy path.

## Layers

| Layer | Purpose | Default cadence |
| --- | --- | --- |
| Unit | Domain, policy, parsing, state transitions, and deterministic failures. | Edit loop and CI |
| Contract | Every backend and transport obeys shared capability and receipt contracts. | CI |
| Integration | PowerShell, WMI, SSH, loopback HTTP/1.1, filesystem, and MCP transport boundaries. | CI where hermetic; otherwise opt-in |
| Canary | Real Hyper-V, Windows desktop, PTY, installer, recovery, and secure desktop. | Explicit disposable environment |
| Fuzz | Untrusted parsers, path handling, envelopes, and policy input boundaries. | Targeted locally and scheduled CI |

## Coverage policy

Coverage is measured only for `internal/...`, the behavioral core. Thin `cmd/...` wiring and
repository tooling do not dilute the result.

- each covered file: at least 70%;
- each covered package: at least 80%;
- total behavioral core: at least 85%.

Coverage annotations require an explanatory comment and review. Lowering a threshold requires an
ADR that explains the temporary trade-off and a dated recovery task. Raising thresholds is welcome
when meaningful tests naturally increase the baseline; 100% is not a project goal.

Run `make coverage` for the coverage gate and `make quality` before review.

## Test quality review

Reviewers ask:

1. Which user-visible behavior or invariant would fail if this test were removed?
2. Does the test distinguish the intended implementation from a plausible bug?
3. Is it deterministic, isolated, and fast at its chosen layer?
4. Does it avoid real credentials, private data, wall-clock sleeps, and shared mutable state?
5. Would a refactor preserving behavior leave the test useful?

If these questions have no good answer, the test is likely maintenance debt even if it increases
coverage.
