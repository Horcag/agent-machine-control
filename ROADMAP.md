# Roadmap

The roadmap describes public product outcomes. Maintainer-specific planning, assignments, and
private environment details remain in the ignored local `kanban/` directory.

## 0.1 — Read-only foundation

- Threat model and operation policy.
- `amc doctor`, machine discovery, inspection, and capability reporting.
- Hyper-V read-only backend with Windows, Linux, and macOS build coverage.
- Stable human output plus machine-readable JSON.

## 0.2 — Recovery and sessions

- Direct CLI recovery backend.
- `amcd` local transport, operation lifecycle, cancellation, and redacted receipts.
- Persistent SSH/PTY sessions with bounded output and human attach/detach.

## 0.3 — MCP and console control

- Official MCP Go SDK integration with stdio and Streamable HTTP.
- MCP conformance and cross-client acceptance.
- Hyper-V framebuffer, keyboard, synthetic mouse, and secure-desktop fallback.

## 0.4 — Desktop and operator experience

- Optional Windows UI Automation sidecar.
- Standalone operator UI and MCP App with approvals and human takeover.
- Signed archives, checksums, SBOMs, and provenance attestations.

## 1.0 — Stable local control plane

- Documented compatibility and deprecation policy.
- At least one additional hypervisor backend proving the capability model.
- Upgrade, rollback, recovery, and security hardening guides.
