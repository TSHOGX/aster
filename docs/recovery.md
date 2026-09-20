# Execution recovery

Recovery preserves the original operation, generation, process handle, epoch and
network allocation. The daemon enumerates durable work with keyset pagination;
each workload class has its own bounded sequential loop. A failed old item does
not hide newer work. Remote calls run outside product database transactions.

## Durable workflows

| Obligation | Discovery and settlement |
|---|---|
| Volume creation | Retry the original engine key; a committed result with a missing storage binding is still pending product work |
| Environment creation/configuration/resume | Replay the recorded request under its reserved fence, then continue the planned generation |
| Workspace preparation | Finish incomplete pre-execution storage and leave it paused until execution is requested |
| Starting execution | Transfer unaccepted startup to the current owner, replay the original process request and accept only after bridge readiness |
| Confirmed startup exit | Record terminal stream evidence, revoke routes, pause, replay the retained journal and retire the failed generation |
| Idle pause | Drain routes, verify writer termination and finish retirement even if the remote pause reply or local completion was lost |
| Active/idle adoption | Verify store, sandbox, epoch, process and journal identity before accepting the retained process |
| Interrupted execution | Pause, resume maintenance, replay committed records, project native/conversation state, restore native state and pause again |
| Workspace mutation | Resume its original writer/maintenance obligation with confined paths and immutable content identity |
| Capture | Continue its recorded maintenance activation and immutable archive/receipt obligation |
| Turn controls | Retry original command identities; guest application outcomes remain distinct from transport receipts |
| Session retirement | Block admission, drain authority, settle writer/history obligations and verify final pause while retaining storage |

Generation configuration records its generation ID. Configuration-operation identity
hashes the entire declaration, so unchanged authored content with new reserved
network destinations cannot reuse an earlier configuration operation. Once applied,
a pending launch uses that recorded declaration even if deployment content changes.
The next generation can select new revisions while paused.

Startup is bounded by the admission/worker action context. A successfully accepted
connection belongs to the controller lifetime and survives completion of its HTTP
request. Canceling a stalled start leaves its intent uncertain and discoverable.

## Evidence and unresolved outcomes

A transport failure is not process termination. Only a terminal engine process
frame establishes startup exit; the diagnostic retains at most 4 KiB of guest
stderr. A confirmed failed startup cannot authorize a turn. Its separate durable
failure record lets normal retained recovery settle it without inventing success
or discarding journal state.

Conditional pause/resume retries can settle lost successful replies by inspecting
the original transition's exact postcondition. Store identity changes and
incompatible epochs remain fenced. Keep the original engine store and retained
volumes available; do not edit product bindings to point at an unrelated store.
Errors name the operation or generation that requires investigation. Temporary
outages retry; contradictory identity or missing retained data requires operator
resolution with verified original state.

Focused fault regressions live in `internal/execution/pending_operations_test.go`,
`environment_service_test.go`, `interrupted_recovery_test.go` and the corresponding
store tests. Real integration exercises the composed HTTP/worker path, retained
native history, repeated managed generations and daemon restart. See
[verification](verification.md) for commands and explicit engine/template inputs.


Capture reads the same pinned engine connection and mTLS files as the daemon via
`native-capture --deployment PATH`. It does not require authored configuration trees
or optional gateways to be available. A rotating session cursor, bounded per-item
attempts and durable retry backoff prevent failed sources from starving other work.
Each full immutable snapshot has an independent receipt; a failed older snapshot
does not prevent archiving a later version. Receipts still match exact original
session/version/digest/epoch evidence.

Projection maintains an indexed pending-source frontier and backs off failures per
stream. Other streams remain eligible, while checkpoint compare-and-swap prevents
skipping a failed record within its stream. Their durable records retain the source frontier, timestamps and last failures
for diagnosis. Archive files and newly created directories
are private to the producer, and new parent directory entries are synced before
receipts commit.

Stored timestamps use fixed-width UTC nanoseconds so SQL ordering agrees with time
ordering, including leases that expire on an exact second.
