# Threat Model and Security Contract

## Overview and Purpose

Agent Machine Control (`amc`) is a local-first control plane for virtual machines that exposes
consistent, policy-enforced operations to human operators, CLI automation, and Model Context
Protocol (MCP) agents. Because machine-control software interacts directly with hypervisors, host
operating system resources, and guest environments, it operates across critical security
boundaries.

This document defines the falsifiable threat model and security contract for Agent Machine
Control. It specifies protected asset objectives, trust boundaries, actor identity models, operation
classes, approval mechanics, secret handling, audit requirements, and concrete failure modes. Every
backend, transport adapter, and client interface must conform to the security invariants documented
here.

---

## Protected Assets and Explicit Non-Assets

### Protected Assets (Objectives and Enforced Invariants)

Within the control-plane boundary, the system enforces strict confidentiality, integrity, and
safety controls to achieve the following security objectives:

1. **Host OS Integrity and Isolation**: The host operating system, host filesystem, host process
   space, hypervisor management interfaces (such as Hyper-V WMI/CIM, libvirt sockets), and host
   kernel security boundaries must not be manipulated or compromised by guest workloads,
   untrusted agent requests, or unauthenticated transport clients.
2. **Guest Isolation Boundary**: Virtual machines must remain isolated from the host and from other
   virtual machines according to hypervisor security boundaries. The control plane enforces that
   cross-guest operations and lateral interactions across isolated virtual switches are strictly
   contained.
3. **Secret Material**: Host-side credentials, guest administrator passwords, Windows Credential
   Manager/DPAPI records, private SSH keys, TLS certificates, mutual authentication tokens, and
   session tokens must never be exposed across trust boundaries, logged in plaintext, or included in
   unredacted receipts or transcripts.
4. **Audit and Receipt Integrity**: The append-only audit log and structured execution receipts
   provide tamper-evident integrity tracking through canonical parameter hashing, restricted local
   storage permissions, and optional future digital signatures. (Canonical hashes verify parameter
   integrity and single-use binding within the control plane, but do not by themselves constitute
   cryptographic proof of author identity or non-repudiation without an asymmetric key infrastructure.)
5. **Operator Control and Takeover Authority**: The human operator is a privileged principal with the
   authority to inspect, intervene, override, or terminate running sessions and guest workloads,
   even during active agent execution or daemon failure.
6. **VM State Consistency and Durability**: Virtual machine disks, memory snapshots, and
   checkpoints must be protected against silent corruption, unintended deletion, and uncoordinated
   concurrent mutations.

### Explicit Non-Assets and Threat Scope Limits

The following items are outside the system's control-plane protection boundary:

1. **Compromised Host Kernel or Hypervisor**: If the underlying host operating system kernel,
   privileged host administrator account, or hypervisor virtualization layer is compromised, the
   control plane cannot guarantee confidentiality or integrity against that host-level adversary.
2. **Ephemeral Guest State in Disposable Environments**: Ephemeral files, scratch memory, and
   transient processes inside explicitly designated disposable test VMs are designed to be mutated
   and destroyed during testing.
3. **Client-Side Prompt Text and Agent Assertions**: Free-form text submitted by LLM agents,
   untrusted client labels, and unverified client-side claims are treated as untrusted data rather
   than authoritative policy or security evidence.
4. **Unmanaged External Network Workloads**: Traffic routed across external physical networks or
   unmanaged virtual switches attached to guests is outside host control-plane boundaries once it
   exits the local management switch.
5. **Internal State of Third-Party Sidecars**: The internal process state, third-party memory, and
   unmanaged features of optional guest helpers (such as Windows-MCP or external automation scripts)
   are not protected assets; the control plane treats sidecars as untrusted external components.
6. **Local Uncommitted Developer Artifacts**: Ignored local workspaces (`kanban/`, `.agent-work/`,
   `.codex/`, `.code-review-graph/`) are local developer aids and carry no system-level durability
   or replication guarantees.

---

## Trust Boundaries and System Architecture

The following diagram illustrates the trust boundaries separating the system components:

```text
+-----------------------------------------------------------------------------+
|                                HUMAN OPERATOR                               |
|                  (Interactive Terminal / Authenticated UI)                  |
+-----------------------------------------------------------------------------+
         |                                                       |
 [Direct Invocation]                                    [Approvals & Inspect]
         |                                                       |
         v                                                       v
+-----------------------+      HTTP/1.1 Loopback        +---------------------+
|      CLI (`amc`)      | <---------------------------> |   Local UI Bundle   |
|   (Local user context)|                               |      (`amc-ui`)     |
+-----------------------+                               +---------------------+
         |                                                       ^
         | [Local Recovery Path]                                 | [HTTP/WS API]
         | (Shared Host Lock)                                    v
         |        +-----------------------------------------------------------+
         |        |                       DAEMON (`amcd`)                     |
         |        |   - Policy Engine & Classification                        |
         |        |   - Approval Verification & Durable Consumption           |
         |        |   - Secret Redaction Pipeline (Text & Binary Isolation)   |
         |        |   - Session Manager (ConPTY / SSH / Framebuffer)          |
         |        |   - Append-Only Audit Store & Shared Host Lock            |
         |        +-----------------------------------------------------------+
         |                                 ^                      ^
         |                                 |                      |
         |                   [Internal Core Service API]   [Internal Core API]
         |                                 |                      |
         |                      +--------------------+            |
         |                      |     `amc-mcp`      |            |
         |                      | (MCP Stdio/HTTP)   |            |
         |                      +--------------------+            |
         |                                 ^                      |
         |                                 | [MCP JSON-RPC]       |
         |                                 v                      |
         |                      +--------------------+            |
         |                      |  AGENT / MCP HOST  |            |
         |                      | (Untrusted Client) |            |
         |                      +--------------------+            |
         v                                                        v
+-----------------------------------------------------------------------------+
|                          HOST OPERATING SYSTEM / HYPERVISOR                 |
|   - Hyper-V WMI / CIM Handles & PowerShell Direct                           |
|   - Windows Credential Manager / DPAPI                                      |
|   - Host Filesystem, Shared Lock / Lease, Isolated Management Virtual Switch|
+-----------------------------------------------------------------------------+
         |                                                       |
  [VM Bus / Hypervisor]                                   [Isolated VSwitch]
         |                                                       |
         v                                                       v
+-----------------------------------------------------------------------------+
|                              GUEST VIRTUAL MACHINE                          |
|   - Guest OS Kernel & Filesystem                                            |
|   - Guest SSH Server (Key Authenticated)                                    |
|   - Optional Guest Sidecar (`Windows-MCP` / UIA, Untrusted)                 |
+-----------------------------------------------------------------------------+
```

### Component Trust Levels and Responsibilities

1. **Human Operator**: A privileged principal who can authorize destructive operations, execute
   direct recovery operations, and trigger manual takeover. The operator is nonetheless constrained
   by explicit policy invariants and forbidden operations (e.g., cannot bypass audit logging or
   request uncontained host command execution).
2. **CLI (`amc`)**: Runs in the local user/operator's OS security context. In normal mode, it
   forwards commands to `amcd`. In `--direct` mode, it operates independently against local
   hypervisor APIs using shared host-visible locks, without depending on the daemon process or network
   listeners.
3. **Daemon (`amcd`)**: Authoritative host control plane. Owns backend lifecycle, cached CIM
   handles, session management, policy enforcement, durable approval receipt verification, secret
   redaction, and append-only audit persistence.
4. **MCP Adapter (`amc-mcp`)**: A thin transport adapter translating MCP JSON-RPC protocol messages
   into shared domain use-case invocations. It contains no independent policy or business logic and
   grants no authorization. Even when launched via stdio under the operator's host OS account, it
   defaults to a distinct least-privilege agent caller identity.
5. **Agent / MCP Client**: Untrusted external entity. All prompts, parameters, tool calls, and
   asserted intents originating from an agent are treated as untrusted input.
6. **Hypervisor Backend (Hyper-V / libvirt / VMware)**: Executes low-level machine lifecycle,
   checkpointing, and frame capture. Backends explicitly advertise capability sets; the domain core
   never assumes universal backend feature parity.
7. **Host OS**: Trusted foundation hosting the daemon, hypervisor services, and secure credential
   stores.
8. **Guest OS**: Untrusted target environment. Code running inside the guest must not be permitted
   to access the host control plane or cross the guest-to-host boundary.
9. **Optional Sidecars (e.g., Windows-MCP)**: Guest-resident or containerized automation aids.
   Sidecars are treated as untrusted; they are restricted to least privilege, cannot bypass host-side
   policy, and cannot cross the Windows secure-desktop / UAC boundary.
10. **Local Operator UI (`amc-ui`)**: Web bundle served locally by `amcd` or presented as an MCP
    App. All operator actions initiated via the UI are authenticated and verified server-side.
11. **Remote Transport**: Privileged transports bind to an authenticated IP-literal loopback HTTP/1.1
    listener by default. Windows named pipes, remote TLS, and mutual per-client authentication remain
    future transport work and are not implemented yet.
12. **Evidence and Audit Storage**: Host-managed, append-only records containing sanitized execution
    traces, approval bindings, and integrity-hashed receipts stored with restricted filesystem
    permissions.

---

## Actor Identity and Delegation

### Stable Actor Identity

Every operation submitted to the system carries an immutable, verifiable caller identity:

- For local CLI invocations: The authenticated host operating system user identity (e.g., Windows
  Security Identifier / SID or POSIX UID).
- For MCP sessions: The authenticated client connection identifier established at transport
  handshake (e.g., client certificate identity or verified transport token).
- For UI sessions: The authenticated local operator session token.

### Adapter Identity Isolation

Thin adapters must not conflate the operating system account under which their process runs with the
logical principal identity of the caller:

- When `amc-mcp` runs as a local stdio subprocess under the operator's OS user account, it defaults
  to a distinct, least-privilege agent identity (e.g., `agent:stdio-local`) rather than inheriting
  the operator's administrative authority.
- The control plane evaluates policy against the caller's explicit agent identity, preventing an
  untrusted agent from implicitly inheriting the operator's OS-level rights simply by communicating
  through a local adapter.

### Authenticated Caller versus Effective Actor

- **Authenticated Caller**: The direct transport entity communicating with `amcd` (e.g., the
  `amc-mcp` adapter process or an active CLI session).
- **Effective Actor**: The principal on whose behalf the operation is executed.

If delegation is configured, the delegation relationship must be explicitly validated by the daemon
against server-side policy. The effective actor's permissions can never exceed the authenticated
caller's permissions.

### Approval Inputs versus Server-Issued Authority

User confirmation flows (such as MCP Elicitation or UI confirmation dialogs) are strictly
**approval inputs**. They convey the human operator's consent for a specific requested action:

- The server-side policy engine receives and validates the approval input against the exact
  parameters of the requested operation.
- Upon successful validation, the server issues and durably records the internal approval record.
- Prompt text, UI display strings, and client-supplied labels or assertion tags carry **zero authority**
  and are never treated as approval receipts or evidence of authorization.

---

## Operation Classification

Operation classification is dynamic, capability-sensitive, and context-sensitive. An operation is
not classified solely by its name or verb; the policy engine evaluates backend capabilities, target
machine configuration, and whether a verified rollback point exists.

Every domain operation is categorized into one of four mutually exclusive classes:

| Class | Definition | Approval Requirement | Examples and Context Conditions |
| --- | --- | --- | --- |
| **Observe** | Read-only operations with no mutating side effects on host or guest state. | Permitted automatically for authenticated callers with read scope; console framebuffer capture is an observe operation permitted only for callers holding explicit sensitive-evidence capture permission (destructive approval is not required). Unauthorized callers receive no capture or metadata. | `machine inspect`, `machine list`, `checkpoint list`, `console screenshot` (requires explicit sensitive-evidence capture permission), `audit tail`, `session list`. |
| **Reversible Mutation** | State changes that alter VM state where a verified, healthy rollback checkpoint exists AND the operation produces no uncontained external side effects. | Permitted automatically only if policy allows AND backend capability permits AND a verified rollback checkpoint exists; otherwise escalates to destructive approval. | `machine start` / `stop` (when backed by verified snapshot), `guest exec` (inside sandbox with verified rollback point and no external side effects), synthetic input injection (when rollback checkpoint exists). |
| **Destructive / Privileged** | State changes that are irreversible, modify host resources, alter hardware configuration, delete data, have external network side effects, or affect uncheckpointed/non-rollbackable VMs. | Requires explicit out-of-band operator approval (via MCP Elicitation or UI confirmation validated and issued by the server). | `machine delete`, `checkpoint restore`, `checkpoint delete`, `guest put` (overwriting uncheckpointed files), host network reconfiguration, guest commands with external side effects, lifecycle actions on VMs without snapshot capabilities. |
| **Forbidden** | Operations that violate fundamental security invariants or safety boundaries. | Denied unconditionally; cannot be approved or overridden by any actor (including the operator). | Executing arbitrary host shell commands from guest/MCP context, bypassing audit logging, disabling secret redaction, exposing plaintext credentials. |

### Context- and Capability-Sensitive Classification Rules

1. **Console Input and Guest Commands**: Keystrokes, mouse clicks, and guest commands cannot be
   assumed universally reversible. If a command can trigger external network calls, mutate external
   databases, or interact with physical hardware devices, or if the target VM lacks an active
   rollback point, the operation is classified as `destructive/privileged`.
2. **Lifecycle and Snapshot Operations**: Machine start, stop, pause, and reset are classified as
   reversible only when the backend supports live snapshotting and a verified pre-mutation checkpoint
   is active. On backends or disk configurations lacking snapshot support (e.g., physical
   pass-through disks), lifecycle operations are classified as `destructive/privileged`.
3. **Pre-Mutation Checkpoint Creation**: Requesting an automatic pre-mutation checkpoint is itself a
   state-mutating action on host storage. If checkpoint creation cannot be authorized, executed, and
   verified, the parent operation is never silently executed as reversible; it must either fail closed
   or require explicit destructive approval.

---

## Authorization and Approval Lifecycle

### Scope Restrictions

Approvals are strictly scoped and non-transferable. An approval record must explicitly enumerate:

1. Target machine identifier (`MachineRef`).
2. Exact operation type and canonical parameter hash (`SHA-256` of normalized parameters).
3. Authorized effective actor.
4. Hard execution deadline and validity window.

An approval granted for machine `vm-alpha` confers no authority over machine `vm-beta`.

### Expiry, Absolute Deadlines, and Monotonic Clocks

The system distinguishes between admission deadline validation and in-flight elapsed-time
enforcement:

- **Absolute Wall-Clock Deadlines**: Every request and approval record contains an absolute UTC
  timestamp deadline (`deadline`). At admission, the daemon rejects any request received after its
  wall-clock deadline has passed, or whose approval validity window has expired.
- **Host Monotonic Elapsed-Time Enforcement**: Once an operation is admitted, execution duration limits
  and timeouts are tracked and enforced using the host operating system's monotonic clock
  (`CLOCK_MONOTONIC`), ensuring execution timing is immune to host wall-clock adjustments, NTP steps,
  or leap seconds.
- **Fail-Closed Clock Skew Handling**: If host clock integrity is uncertain, or wall-clock drift between
  caller/approval timestamps and server time exceeds configured skew tolerance, admission fails closed
  immediately. The system does not guess or apply arbitrary fallback time windows.

### Single-Operation Binding and Durable Consumption

Approvals are bound one-to-one to a single operation execution:

1. **Parameter Hashing**: The approval record contains a SHA-256 hash of canonicalized operation
   parameters (target, command, arguments, environment, idempotency key). Any parameter modification
   invalidates the hash match and causes rejection.
2. **Durable Atomic Consumption**: An approval is marked as consumed durably and atomically alongside
   operation admission and audit logging *before* backend hypervisor execution begins.
3. **Daemon Crash and Restart Protection**: Because consumed approval state is committed to durable
   storage, a daemon crash or restart cannot reset consumed state or reopen a replay window for
   previously executed approvals.

### Fail-Closed Invariants

If any ambiguity, network partition, storage failure, or integrity check error occurs:

- If audit storage is unwritable or unavailable, mutating operations abort immediately.
- If pre-mutation checkpoint creation or verification fails, the operation must not proceed under
  assumed reversible status.
- If caller identity or transport integrity cannot be verified, the request is denied.

---

## Idempotency, Retries, and Concurrency

### Retry Precedence and Deduplication

Every mutating request includes a client-generated `IdempotencyKey`. When `amcd` receives a request
with a previously recorded key, it evaluates execution and retry precedence in the following strict
order:

1. **Exact Retry Match**: If the incoming request matches the exact tuple `(Actor, Target MachineRef,
   Canonical Operation Hash, IdempotencyKey)`:
   - **In-Flight Operation**: If the prior operation is currently executing, the caller attaches to
     the active execution stream (or receives an in-progress status) without initiating a second
     execution.
   - **Completed Operation**: If the prior operation has completed, `amcd` returns the cached,
     redacted `Receipt` from the prior execution.
   - **Precedence Rule**: This retry match is evaluated **before** checking consumed-approval
     rejection. A valid idempotent retry of an approved operation returns the prior result rather
     than failing with a consumed-approval replay error.
2. **Cross-Actor Collision Prevention**: If the incoming request carries an `IdempotencyKey` that
   matches an existing record but originates from a **different actor**, the request is rejected
   immediately as an unauthorized collision/replay attempt. The daemon **must not** disclose the
   cached receipt, output, or metadata of the existing operation across actor boundaries.
3. **Parameter Collision Rejection**: If the incoming request has the same actor and key but differs in
   target machine, operation type, or canonical parameter hash, the request is rejected immediately as
   an idempotency parameter collision.

### Bounded Idempotency Retention

Idempotency cache records are not retained indefinitely, nor are they governed by an arbitrary fixed
constant. Retention is governed by explicit, configurable server policy tied directly to the audit log
and receipt retention lifecycle. Idempotency retention must never be shorter than the maximum
operation, approval, or receipt replay horizon.

Pruning an expired idempotency record cannot make a still-valid approval or retry eligible for a
second execution; after retention expiry, stale approval or retry material remains rejected rather
than silently re-executed.

### Concurrent Execution and Shared Host Locking

Operations targeting virtual machines must be serialized to prevent race conditions and disk
corruption:

1. **Shared Host-Visible Lock/Lease**: To coordinate between the daemon (`amcd`) and independent CLI
   invocations (`amc --direct`), locking is enforced via shared host-visible primitives (such as
   per-machine lockfiles/leases on host storage or hypervisor-level reservation locks) rather than an
   in-memory daemon mutex alone. Shared host leases require:
   - **Atomic Acquisition**: Host locks are acquired through atomic creation/reservation primitives.
   - **Owner and Process Identity**: The lease explicitly records owner identity, process PID, and
     invocation context.
   - **Expiry and Heartbeat**: Lease validity is bounded by explicit heartbeat renewal and expiration
     deadlines.
   - **Fencing Generation**: Each lease transition increments a monotonic fencing generation to detect
     and reject stale operations.
2. **Conflict Resolution and Safe Stale-Owner Handling**:
   - When `amcd` is running, it acquires and maintains host-visible leases for active VM operations.
   - Conflicting concurrent requests submitted to `amcd` are serialized or rejected with explicit
     conflict status.
   - When `amc --direct` is invoked, it inspects the shared host lease. If `amcd` holds an active, live
     lease, `--direct` fails closed to prevent uncoordinated concurrent mutation.
   - `amc --direct` may recover a stale lease only after verifying the recorded owner is no longer
     valid; it must never break a lock merely because the daemon is unreachable.

---

## Secret Storage and Redaction Boundaries

### Secret Storage Boundaries

1. **Host-Level Storage on Windows**: Machine credentials, administrator passwords, private SSH keys,
   and sensitive tokens are stored exclusively in the host operating system's secure credential store
   (Windows Credential Manager / DPAPI on Windows).
2. **Capability-Gated Non-Windows Support**: On non-Windows platforms (Linux/macOS), credential
   storage is capability-gated and deferred to platform-specific secure backends when supported; the
   system does not assume or promise unverified Linux Secret Service or macOS Keychain integration.
3. **No Plaintext Persistence**: Plaintext credentials must never be stored in configuration files,
   untrusted process environment variables, repository commits, CLI command arguments, or unencrypted
   temporary disk files.

### Text Redaction Pipeline

All character streams exiting `amcd` across trust boundaries (including CLI stdout/stderr, MCP tool
responses, terminal session streams, and text audit logs) pass through a mandatory server-side text
redaction engine:

```text
[Raw Output Stream] ---> [Server-Side Redaction Engine] ---> [Sanitized Text / Audit]
                                  |
               +------------------+------------------+
               |                                     |
    [Active Credential Matcher]           [Configured Regex & Pattern Filter]
    - Passwords from DPAPI                - Private Key Headers (`BEGIN ... KEY`)
    - Auth Tokens & Session Keys          - Authorization Bearer Headers
    - Known Guest Admin Credentials       - Sensitive Environment Variables
```

1. **Exact Secret Matching**: Any string matching an active secret retrieved from the credential store
   is replaced with `[REDACTED]`.
2. **Pattern Redaction**: Well-known credential structures (PEM private key headers, `Bearer` tokens,
   common API key formats) are masked before transmission.
3. **Pre-Persistence Redaction**: Redaction occurs *before* writing text to audit logs, ensuring on-disk
   records do not store plaintext secrets.

### Binary Evidence and Screenshot Protection

Text redaction filters (exact match and regex) cannot inspect or sanitize raw binary streams, video
feeds, or console framebuffer screenshots:

1. **No Redaction Guarantees for Bitmaps**: The system never claims that text-based filters or regex
   patterns can sanitize graphical framebuffers. Automated image redaction or OCR is recognized as
   imperfect and best-effort, not a guaranteed security boundary.
2. **Access Control and Scope**: Console framebuffer capture and screenshot collection are observe
   operations permitted only for callers holding an explicit sensitive-evidence capture permission;
   capture does not require destructive approval merely because it is sensitive. Unauthorized callers
   receive no capture or metadata.
3. **Bounded Retention and Storage**: Captured frames and binary artifacts are stored in protected local
   directories with restricted permissions and short, configurable retention lifecycles.
4. **Publication Sanitization**: When exporting evidence outside the local security boundary, binary
   assets must be explicitly gated or omitted unless publication sanitization is confirmed.

---

## Receipt and Audit Invariants

### Append-Only Audit Trail

`amcd` maintains an append-only audit log recording every received command, approval decision, and
execution outcome. Audit entries are immutable and preserved locally under the protected state
directory with restricted filesystem permissions.

### Mandatory Receipt Schema

Every completed operation produces a structured `Receipt` containing:

- `receipt_id`: Unique UUID.
- `timestamp_started` and `timestamp_completed`: ISO 8601 UTC timestamps.
- `actor`: Authenticated principal identity.
- `machine_ref`: Target virtual machine identifier.
- `operation_class`: `observe`, `reversible_mutation`, or `destructive_privileged`.
- `operation_hash`: SHA-256 hash of canonical operation parameters.
- `idempotency_key`: Client-supplied idempotency identifier.
- `approval_ref`: Reference to the granting approval record (if required).
- `effective_backend`: Backend implementation used (e.g., `hyperv-cim`, `direct-psdirect`).
- `exit_status`: Execution outcome and return code.
- `observation_type`: Explicit tag indicating `observed` versus `inferred` state.
- `evidence_refs`: Array of hashes referencing stored screenshots, output streams, or logs.

### The Observed versus Inferred Distinction

To prevent automated systems and operators from mistaking heuristic guesses for verified facts, all
state assertions in receipts and status reports must be explicitly classified:

- **Observed State**: A fact measured directly from hypervisor APIs or verifiable OS interfaces
  (e.g., Hyper-V reports VM operational status as `Running`, process PID 412 exited with code 0).
- **Inferred State**: A deduction based on secondary indicators or heuristics (e.g., "guest boot
  complete because port 22 is open", "software installation succeeded because CPU usage dropped").
  Inferred assertions must never be reported as observed facts.

---

## Rollback and Checkpoint Prerequisites

### Pre-Mutation Checkpoint Verification as a Mutation

An operation may be treated as a `reversible_mutation` only if a verified rollback checkpoint exists
prior to execution:

1. **Pre-Execution Check**: `amcd` queries the hypervisor backend to confirm that a valid, healthy
   checkpoint exists for the target VM.
2. **Checkpoint Creation as a Mutation**: Creating an automatic pre-mutation checkpoint is itself a
   storage-mutating operation. It requires backend snapshot capability, sufficient disk quota, and
   policy authorization.
3. **Integrity Verification**: If the pre-mutation checkpoint cannot be created, authorized, or
   verified, the requested operation **must not** proceed as reversible. It must either fail closed
   immediately or be reclassified as `destructive/privileged` requiring explicit operator approval.
4. **Rollback Target Binding**: The receipt records the specific verified checkpoint identifier to
   which the VM can be reverted.

### Cases Where Rollback Is Impossible or Best-Effort

When rollback cannot be guaranteed by hypervisor snapshots, the operation is classified as
**destructive/privileged** and requires explicit operator approval:

1. **Pass-Through and Direct Disks**: Virtual machines utilizing physical pass-through disks or
   direct-attached LUNs that do not support hypervisor snapshotting.
2. **External Network and API Side Effects**: Outbound HTTP requests, external database mutations, or
   remote network calls initiated by guest processes cannot be rolled back by reverting a local VM
   snapshot.
3. **Active Directory and Domain Identity Boundaries**: Reverting a domain-joined guest across a
   Kerberos ticket or machine account password rollover window may break domain trust.
4. **Hardware Passthrough Devices**: Dedicated PCIe passthrough or GPU devices whose internal state
   persists across VM snapshot restoration.

---

## Direct-Mode Invariants (`amc --direct`)

`amc --direct` provides a dedicated recovery path when the daemon (`amcd`), MCP adapter (`amc-mcp`),
or network stack is unavailable:

1. **Zero Daemon Dependency**: `amc --direct` executes purely in-process using direct local hypervisor
   APIs (such as Hyper-V WMI/CIM and PowerShell Direct). It does not require `amcd`, `amc-mcp`, the web
   UI, SSH, or guest sidecars.
2. **Shared Host Locking and Stale Lease Recovery**: `--direct` coordinates with `amcd` via shared
   host-visible leases requiring atomic acquisition, owner/process identity, expiry/heartbeat, and
   fencing generations. If a live `amcd` daemon holds an active lease on a machine, `--direct`
   refuses concurrent mutation to prevent races. `amc --direct` may recover a stale lease only after
   verifying the recorded owner is no longer valid; it must never break a lock merely because the
   daemon is unreachable.
3. **Direct Audit Failure vs Safe Observation**:
   - Direct mode writes execution receipts to local host audit storage without depending on daemon
     services.
   - For mutating operations, if local audit storage is unwritable or fails, `--direct` fails closed
     immediately and aborts the mutation.
   - For read-only observation operations (`observe` class), if local audit writing fails during
     disaster recovery, observation may still proceed when safe so the operator can inspect system
     state.
4. **No Silent Privilege or Transport Broadening**: `--direct` mode runs strictly within the invoking
   user's local operating system privilege boundary. It never silently escalates privileges, opens
   network listeners, or falls back to unencrypted or unauthenticated remote transports.
5. **Local Safety Enforcement**: `--direct` mode independently enforces parameter validation,
   interactive confirmation for destructive commands, and local receipt generation.

---

## Threat Scenarios, Mitigations, and Residual Risks

### Scenario 1: Prompt Injection Leading to Rogue Agent Actions

- **Threat**: An LLM agent consumes untrusted guest content (e.g., an adversarial README or web page
  inside a VM) that instructs the agent to delete the VM, reconfigure host networks, or read host
  files.
- **Mitigation**:
  - Agent requests are treated as untrusted inputs.
  - Server-side policy classifies VM deletion and network reconfiguration as `destructive/privileged`.
  - The control plane refuses execution without an authentic approval record issued via operator
    approval input.
  - Prompt text and client labels carry zero authorization authority.
- **Residual Risk**: Operator approval fatigue leading to the operator confirming an unverified
  destructive request without inspecting the parameters.

### Scenario 2: Guest Sidecar or Integration Compromise

- **Threat**: A malicious process inside the guest compromises an optional sidecar (such as
  `Windows-MCP`) or attempts to exploit hypervisor integration services to escape into the host.
- **Mitigation**:
  - Sidecars run inside the untrusted guest OS boundary and have no host control-plane credentials.
  - Dangerous tools and telemetry capabilities are disabled on sidecars.
  - The host maintains independent out-of-band console capture (framebuffer) and synthetic input
    injection that do not rely on guest agents.
  - Host policy enforcement occurs entirely on the host side of the hypervisor boundary.
- **Residual Risk**: Zero-day vulnerabilities in the underlying host hypervisor virtualization layer
  (e.g., Hyper-V VMBus vulnerabilities).

### Scenario 3: Approval Replay and Parameter Tampering

- **Threat**: An adversary captures a valid approval record from an audit log or network trace and
  replays it to execute an unauthorized operation or modifies the target machine ID.
- **Mitigation**:
  - Approval records are bound to the SHA-256 hash of canonical operation parameters.
  - Single-use consumption tracking commits consumed state durably and atomically before execution,
    persisting across daemon restarts.
  - Short freshness deadlines cause expired approval records to be rejected upon admission.
- **Residual Risk**: Host clock skew or host system time tampering affecting absolute deadline checks
  (mitigated by fail-closed skew detection and host monotonic clock enforcement during execution).

### Scenario 4: Secret Exfiltration via Console or Output Streams

- **Threat**: An agent runs a command that prints administrative credentials or displays sensitive
  keys on the guest desktop, attempting to read them via screenshot or stdout capture.
- **Mitigation**:
  - Multi-layer server-side text redaction filters all terminal buffers, stdout/stderr streams, and
    text receipts against known credentials and pattern databases before output leaves the daemon.
  - Machine credentials are kept in DPAPI/Credential Manager and never supplied as raw CLI flags.
  - Framebuffer captures and screenshots are treated as sensitive binary evidence protected by strict
    authorization, bounded retention, and publication sanitization; the system does not rely on text
    redaction for bitmaps.
- **Residual Risk**: Operator or client exporting unsanitized binary screenshot evidence outside the
  secure enclave.

### Scenario 5: Denial of Service / Daemon Lockup

- **Threat**: High concurrency, memory starvation, or a deadlocked daemon process prevents the
  operator from controlling a runaway virtual machine.
- **Mitigation**:
  - `amc --direct` provides an independent, in-process recovery path using shared host-visible locks,
    bypassing the daemon completely when `amcd` is unresponsive.
  - Hypervisor resource limits prevent single-VM resource starvation from halting host management.
- **Residual Risk**: Host-wide OS kernel panic or host storage exhaustion preventing all local
  process execution.

---

## Testable Security Properties

The following properties are concrete, falsifiable contracts that must be verifiable by automated
unit, integration, contract, or sandbox test suites:

1. **Property 1 (Destructive Gating)**: Submitting any operation classified as
   `destructive/privileged` without an active, server-validated approval record returns an
   authorization rejection and performs zero hypervisor mutations.
2. **Property 2 (Durable Replay Invalidation)**: Presenting an already-consumed approval record returns
   an approval-consumed rejection and executes no mutations. This rejection holds even after an
   intervening daemon restart.
3. **Property 3 (Parameter Tampering Rejection)**: Mutating any field of an operation (such as target
   VM, command line, or environment) after an approval record is issued causes parameter hash
   validation to fail and aborts execution.
4. **Property 4 (Fail-Closed Audit and Direct-Mode Observation)**: If the audit storage backend is
   unwritable, all mutating operations in `amcd` and `amc --direct` abort immediately and leave
   machine state unchanged. Read-only observation in direct mode may proceed if safe.
5. **Property 5 (Text Redaction Guarantee)**: For any text output stream containing an active secret
   value registered in the credential store, the string emitted across the control-plane boundary
   contains `[REDACTED]` in place of the secret bytes.
6. **Property 6 (Binary Screenshot Evidence Protection)**: Console framebuffer capture and screenshot
   requests are observe operations permitted only for callers holding an explicit sensitive-evidence
   capture permission; they do not require destructive approval. Unauthorized callers receive no
   capture or metadata. Captured frames are stored in restricted local directories with bounded
   retention and remain isolated from text redaction filters.
7. **Property 7 (Direct-Mode Independence and Shared Host Locking)**: With the `amcd` daemon stopped,
   `amc --direct` commands successfully inspect machine state and manage VMs. If `amcd` holds an
   active host lock lease, `amc --direct` detects the lock and refuses conflicting mutations. `amc
   --direct` recovers a stale lease only after verifying the recorded owner is no longer valid, never
   breaking a lock merely because the daemon is unreachable.
8. **Property 8 (Prompt Authority Invalidation)**: Supplying prompt text, headers, or metadata
   asserting administrative authority (e.g., `"elevated": true`, `"reason": "authorized by user"`)
   results in identical policy classification and requires identical server-issued approvals as
   requests without such annotations.
9. **Property 9 (Idempotency, Retry Precedence, and Cross-Actor Isolation)**: Submitting an
   identical retry with the same actor, target, canonical parameter hash, and idempotency key returns
   the prior execution result before checking consumed-approval rejection. Submitting an existing
   idempotency key with a different actor results in collision rejection without disclosing cached
   receipts across actor boundaries. Idempotency retention is never shorter than the maximum replay
   horizon; pruning cannot make a still-valid approval or retry eligible for a second execution, and
   after retention expiry, stale approval or retry material remains rejected rather than silently
   re-executed.
10. **Property 10 (Capability-Sensitive Checkpoint Verification)**: Attempting a reversible mutation
    when pre-mutation checkpoint creation cannot be authorized, created, or verified causes the
    operation to fail closed or escalate to destructive approval; it is never executed as a reversible
    mutation.
11. **Property 11 (Adapter Identity Isolation)**: Invocations through `amc-mcp` running under the
    operator's host OS account default to a distinct agent caller identity and do not inherit
    administrative operator authority.

---

## Forbidden Operations and Pre-Alpha Non-Goals

### Explicit Forbidden Operations

The control plane refuses to implement or execute the following operations under all circumstances:

1. **Host Shell Execution via MCP/Guest**: Directly spawning arbitrary, uncontained host operating
   system shell commands through MCP tools or guest channels.
2. **Audit Bypass**: Executing mutating operations with audit logging disabled or silenced.
3. **Unauthenticated Public Transports**: Exposing daemon control interfaces or MCP adapters over
   unauthenticated, unencrypted public network interfaces.
4. **Plaintext Credential Transmission**: Passing plaintext passwords or private keys through MCP
   elicitation dialogs, command-line arguments, or unredacted receipts.
5. **Silent Escalation**: Silently broadening permissions, switching to less secure transports, or
   bypassing approval gates when an operation encounters an error.

### Pre-Alpha Non-Goals

The following features are explicitly out of scope for the pre-alpha release:

1. **Multi-Tenant Cloud Orchestration**: Managing multi-tenant public cloud infrastructure or
   distributed hypervisor clusters across multiple physical data centers.
2. **Autonomous Self-Approvals**: Enabling autonomous agents to generate or self-issue approval
   records for destructive operations without a human in the loop.
3. **Custom Host Kernel Drivers**: Developing custom host kernel-mode drivers or proprietary hypervisor
   extensions.
4. **Legacy Hypervisor Support**: Supporting deprecated virtualization platforms that lack modern
   integration services, PowerShell Direct, or CIM/WMI management interfaces.
