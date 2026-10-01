# Runtime reliability and recovery

AMC distinguishes daemon connectivity, backend observation, mutation completion, and guest
transport availability. A successful check at one boundary does not establish the others.

## Enrolled-target observation

CLI, daemon, and MCP resolve the enrolled target through the same application service. The service
loads the protected enrollment, rejects references outside its aliases and canonical identity,
checks that the local host is enabled, and performs a fresh inspection of that exact provider GUID.
The response must validate and match the enrolled host, locator, and normalized GUID.

Machine list and inspect reuse this observation instead of inspecting again. They do not enumerate
unrelated machines. Candidate discovery and enrollment/clear planning retain the full inventory
refresh because they establish or change authority. An observation is scoped to one request;
subsequent operations inspect again. No cached success authorizes a mutation.

## Deadlines

Exact target observation uses the configured host query timeout, bounded by caller cancellation.
The local default is 60 seconds. The PowerShell executor also supplies a 60-second fallback when
the caller has no deadline. It bounds inherited output-pipe draining after process cancellation;
it cannot guarantee rollback of a command already accepted by Hyper-V.

The daemon HTTP client uses an explicit caller deadline end to end. Requests without one receive
a 90-second default. A deliberately supplied HTTP client can impose a shorter timeout. HTTP 504,
context cancellation, and backend executor timeouts retain their typed causes and categories.

## Readiness and failure recovery

`amcd bootstrap status` verifies managed daemon ownership and connectivity. `amc doctor` checks
the PowerShell executable, Hyper-V module/access, host query, machine enumeration, and network
adapter queries. Readiness is a point-in-time observation; it does not prove that starting a VM,
allocating its memory, or connecting to a guest will succeed.

MCP returns fixed public categories for known failures, without forwarding provider output or
private paths. Failed or interrupted mutation calls retain validated operation and receipt IDs
when available. Unknown categories remain generic. Inspect the admitted operation and receipt
before deciding whether another action is necessary:

```sh
amc operation show <operation-id> --json
amc audit show <receipt-id> --json
```

A wait timeout is not proof that a mutation failed or had no effect. Reuse the same idempotency
key for an exact retry and inspect its existing outcome. Do not create a new key merely to retry
an uncertain mutation. Approval expiry, target mismatch, host-key mismatch, and missing rollback
remain authority boundaries and require their existing recovery procedures.

If the Hyper-V provider fails, inspect Windows Hyper-V management logs locally. AMC retains the
direct CLI as an independent recovery path, but both paths depend on the same hypervisor. Fixing
transport errors cannot repair a failing Windows management service. Do not restart shared host
services or change VM configuration automatically to conceal that failure. Keep host evidence,
inventories, guest data, and transcripts out of public issues and pull requests.

## Verification

Regression tests cover single-query reuse, isolation from fleet failure, malformed or mismatched
identities, disabled hosts, late cancellation, typed timeout propagation, inherited pipes, safe
error redaction, and mutation recovery references. PowerShell script fixtures exercise readiness
query failures when native PowerShell is available. Live acceptance should exercise repeated CLI,
direct CLI, and MCP observations as well as the intended mutation and guest workflow on an
authorized disposable target. Record unverified boundaries explicitly.
