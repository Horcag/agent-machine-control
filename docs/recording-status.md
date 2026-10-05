# Recording lifecycle readback

`console_record` remains synchronous and accepts an optional `recording_id`: a fresh
32-character lowercase hexadecimal ID supplied before the request. Its GIF response is
unchanged. Requests without an ID retain the legacy behavior and create no recording status.
An existing ID is refused before capture; IDs do not authorize replay or background jobs.

Read metadata independently with `console_record_status` using `target` and `recording_id`,
POST `/v1/console/record/status`, or `amc console record-status default --recording-id ID --json`.
The producer CLI accepts `--recording-id ID`. Both direct and daemon paths share the application
service. Status requires current machine read and sensitive evidence capture scopes, the same
authenticated caller and effective actor, and the exact currently enrolled VM. Metadata reads
validate protected local enrollment and enabled host configuration without observing the VM.
Each actual capture still performs fresh enrolled-target and sensitive-authority admission.

The status object has these fields:

| Field | Meaning |
| --- | --- |
| `schema_version` | Public metadata schema version `"1"` |
| `recording_id` | Caller-supplied ID; not a bearer credential |
| `vm_id` | Canonical enrolled locator, including `local:` |
| `requested_frames` | Requested bounded frame count |
| `attempted_captures` | Committed capture attempts, including pre-dispatch intent |
| `completed_captures` | Provider calls returning a validated framebuffer |
| `capture_in_flight` | Conservative dispatch-intent or provider-in-flight flag |
| `terminal` | The synchronous producer has finalized |
| `terminal_reason` | Empty while running; `completed`, `canceled`, `deadline_exceeded`, or `failed` |
| `started_at`, `updated_at`, `expires_at` | UTC lifecycle timestamps |

A true in-flight flag alone does not establish native provider entry. A false flag alone does not
establish producer cessation: admission, frame processing, or GIF finalization may still be active.
Only terminal status with a false flag establishes that the synchronous AMC producer has no
outstanding capture.

Counts describe committed attempts and validated captures, not saved GIF frames or artifact delivery.
Cancellation can occur after
capture returns and before frame metadata or the GIF is saved. The status contains no images,
guest text, provider errors, actor identities, or artifact bytes. A terminal status is published
only after outstanding capture returns. Polling does not cancel or restart a producer.

Cancel the original RPC, then query status independently. A canceled or lost RPC reply, transport
timeout, missing, expired, corrupt, or nonterminal status is inconclusive about producer termination.
For cancellation acceptance require `terminal: true`, `terminal_reason: "canceled"`,
`capture_in_flight: false`, and stable capture counts across subsequent reads. `completed` describes
producer completion; it does not establish that a client received or saved the GIF.

Private status retains at most 64 recording reservations with at most 62 immutable snapshots of
4 KiB each per recording. Snapshots are protected, synchronized, and atomically published to fresh
names. Retention is 15 minutes from admission. Admission cleans only expired terminal reservations
owned by the same authenticated caller, effective actor, and canonical VM. Active, foreign, corrupt,
and unknown entries remain untouched and consume capacity. Exhaustion refuses new recording
admission. No timer, recorder daemon, replay fallback, or general background job system is added.
Metadata readback uses HTTP 409 category `recording_status_inconclusive` and fixed MCP text
`recording_status_inconclusive: recording status is inconclusive` for missing, corrupt, expired,
foreign-owned, or uncertain metadata. Retry only metadata reads within a finite deadline.
Never reuse an ID after expiry or restart capture to resolve an inconclusive status.

During publication or after a publication failure, the retained publication marker makes reads
inconclusive. A directory synchronization failure after snapshot rename still prevents conclusive
readback; producer cessation and durable artifact delivery are separate facts.
Persistence failures return redacted errors and suppress the final GIF response; stale nonterminal
snapshots must never be interpreted as evidence that a capture stopped.
