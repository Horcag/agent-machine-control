# Security policy

Agent Machine Control can execute privileged operations on real computers. Treat an exposed
server or compromised agent client as equivalent to interactive machine access.

## Supported versions

No released version is currently supported. Security support begins with the first tagged
pre-release and will be documented here.

## Reporting a vulnerability

Do not open a public issue, discussion, or pull request for a suspected vulnerability. Use the
repository's private GitHub Security Advisory reporting flow. Include the affected revision,
impact, minimal reproduction, and suggested mitigation when available. Do not include real
credentials or private guest data.

## Deployment boundary

- Bind privileged transports to a local named pipe or loopback by default.
- Require authentication, TLS, per-client identity, and firewall allowlisting for remote HTTP.
- Run with the least privilege that supports the selected capability.
- Keep machine credentials in the operating system credential store, not configuration files.
- Treat screenshots, clipboard content, dumps, transcripts, and VM inventories as sensitive.
- Use disposable VMs and verified checkpoints for destructive tests.

Client-side confirmation is not a security boundary. Authorization, approval validation,
idempotency, redaction, and audit enforcement belong in the shared core and daemon.
