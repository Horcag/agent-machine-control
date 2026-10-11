# Whole guest operations

Whole guest operations transfer files and run bounded PowerShell scripts through a persistent,
binary SSH worker. CLI and MCP call the same authenticated daemon application service. Interactive
PTY sessions remain available for terminal interaction, and `amc --direct` remains independent of
the daemon and guest SSH.

## Authority and workspace

The daemon resolves the enrolled target and its pinned SSH configuration. A client cannot supply
an SSH endpoint, credentials, or a guest destination path. The operator must provision a private
workspace and record its original volume, 128-bit file ID, creation time, owner and complete
security descriptor digest. Missing provisioning fails before worker launch.

Worker admission is bound to caller, effective actor, enrollment, SSH configuration and workspace
generation. Preparation records the original worker PID, full birth time, image and SID. Changed
configuration, uncertain launch or lost output does not authorize replay or adoption of another
process. A new preparation epoch requires confirmed absence of the original worker.

Each operation carries a reason, idempotency key and exact deadline. On protected targets, the
operator issues a one-use approval for the exact operation and beneficiary. Issuance supports the
current operator or the canonical local MCP actor; MCP cannot issue approvals.

## Operation contracts

| Operation | Result and boundary |
| --- | --- |
| `prepare` | Starts and admits one worker; compilation and runspace creation happen once. |
| `put` | Creates a new file in the held workspace, verifies size and SHA-256, flushes once, then returns an opaque ownership reference. |
| `get` | Reads one original file by ownership reference; verifies identity, security, size and digest before returning bytes. |
| `exec` | Runs a bounded UTF-8 script in the persistent runspace, with bounded output and a final receipt. |
| `remove-owned` | Removes up to 128 referenced original files in one request; reports accepted deletion and observed absence separately. |

Transfers use bounded binary buffers. File content is not encoded into terminal commands, clipboard
messages or control JSON. Script and command output limits are 16 MiB each. Download and command
output are quarantined in private host files and released only after integrity checks and a durable
successful receipt. Concurrent quarantine storage is bounded to 1 GiB.

Ownership references are scoped to actor, target and configuration generation. Deletion checks the
original file and held parent identity, owner, complete security descriptor digest, size and content
digest before each effect. Reparse points, changed files and foreign objects are preserved. A
successful transport write is insufficient evidence of operation completion or disk reclamation.

## Failure and cancellation

An incomplete request, wrong worker identity, deadline expiration, canceled I/O or invalid final
result closes the affected worker and preserves durable uncertainty. Retrying an idempotency key
returns receipt metadata; it does not repeat effects or replay output bytes. Download publication
requires verified bytes and the final result, and never replaces an existing local destination.

The low-level Go `Client.GuestOperation` method writes provisional output to its supplied sink
before validating the final trailer. Callers must keep that sink unpublished and discard its
contents on any error. CLI and MCP use `GuestOperationToFile`, which quarantines output and
publishes a new local destination only after final verification. Local publication supports
Linux and Windows; other host platforms return an explicit unsupported error.

Worker shutdown records retirement before channel closure. Channel closure alone does not prove
native process absence. Unresolved retirement obligations survive daemon restart and block a new
launch in the same authority scope.

## CLI example

Provide a fresh deadline and key for each operation. Protected targets also require the exact
one-use approval returned by `amc guest approve` for the intended beneficiary.

```sh
amc guest put --target lab-vm --source ./payload.bin \
  --reason "Upload benchmark fixture" --idempotency-key benchmark-upload-001 \
  --deadline 2030-01-01T12:01:00Z --approval-id APPROVAL_ID --json
amc guest get --target lab-vm --artifact-ids ARTIFACT_ID \
  --destination ./download.bin --reason "Verify benchmark fixture" \
  --idempotency-key benchmark-download-001 --deadline 2030-01-01T12:02:00Z \
  --approval-id APPROVAL_ID --json
amc guest remove-owned --target lab-vm --artifact-ids ARTIFACT_ID \
  --reason "Remove verified benchmark fixture" --idempotency-key benchmark-delete-001 \
  --deadline 2030-01-01T12:03:00Z --approval-id APPROVAL_ID --json
```

Prepare the worker first. Each upload returns an opaque artifact ID for subsequent download and
deletion. Example timestamps, keys and approval IDs are placeholders.

## Performance acceptance

Source tests establish protocol and policy behavior. Throughput claims require measurements on the
installed revision and intended Windows target. Measure cold preparation separately from warm
commands, upload, download and owned deletion. Include payload size, digest verification, durable
commit, final receipt and cleanup time. Report RPC overhead separately from guest execution where
the evidence permits; retain compact receipts and remove owned fixtures after each run.
