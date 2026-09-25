# Prompt evidence and presentation architecture

Prompt, Delivery, Run, Receipt and Segment have distinct lifetimes. There is one
transactional input claim path and one native execution owner; presentation
never grants execution authority.

| Resource | Owns |
| --- | --- |
| Prompt | Immutable input, enqueue sequence, queue policy, disposition and revision |
| Delivery | Token identity, accepted/queued/consumed/handled/removed/rejected/unknown evidence |
| Run | Exact command, incarnation/owner fence, execution status and reconciliation |
| Receipt | Immutable admission result and request digest |
| Segment | Input boundary, work ownership, final answer and native timing |

Consumed and handled inputs permanently claim the prompt. Handled means that
Session processing completed without entering the model input path; it creates
no model transcript anchor. Only proven removed or rejected input returns to
held. Unknown remains exclusive until original evidence resolves it.

Native settlement closes admission, joins preflights and clears executable
queues before notifying observers. Observer errors are diagnostic. A core
cleanup failure fences the runtime and makes the runner terminate and recover
the original process; it emits no false successful reconciliation.

Display materializes correlated consumption, then attaches subsequent output to
the current Segment. Existing item ownership is resolved before work-side
effects. Consecutive inputs without intervening work coalesce. A continued
Segment remains completed even if a later Segment's Run becomes unknown.
Unknown execution stops live clocks without inventing an end timestamp.

The product supports only its current schema. There is no historical
disposition, import cursor, unmatched-message fallback, format converter,
session-wide queue mutation adapter or persisted prompt execution state.
Prompt/Run-derived statuses at API read boundaries do not acquire write authority.

Queue actions use prompt-local receipts, exact Run fences and per-row in-flight
state. Accepted delivery errors remain visible in the queue. Urgent Stop and
ordinary submission remain independent and preserve drafts. Browser frontiers
advance only after actual page/delta application.

See [the lifecycle inventory](queued-prompts-lifecycle-inventory.md),
[verification](verification.md) and [release procedure](queued-prompts-release.md).
