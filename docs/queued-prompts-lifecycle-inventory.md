# Prompt lifecycle authority inventory

The current schema has no persisted `turns.state` column. Each concern has one
authority and one write path:

| Concern | Authority | Contract |
| --- | --- | --- |
| Input intent | `turns` | Content, request identity, enqueue sequence, queue policy, disposition and prompt revision. |
| Native delivery | `input_deliveries` | Exact prompt/command/run/token, acceptance, ACK, consumption, handled, removal, rejection and unknown evidence. |
| Execution | `execution_runs` | One active run per Session, owner/incarnation fence, lifecycle, reconciliation and outcome. |
| Admission idempotency | `control_receipts` | Immutable request digest and accepted/not-admitted result. |
| Conversation display | `conversation_segments` and projection tables | Consumed/handled anchors, run/segment ownership, output work, final answer and native timing. |
| Service attribution | Live run + incarnation + grant | Gateway requests resolve the current Run; no prompt status cache is authoritative. |
| Scheduling | Prompt + correlated Run | Schedule history exposes derived prompt/run status and never writes execution state directly. |

Normal dispatch and Steer call the same transactional delivery claim helper.
Hold/Release/Cancel mutate only pending prompt policy/disposition. Stop and Steer
bind the exact Run, incarnation and owner fence. Recovery, idle retirement,
release readiness, owner handoff and deletion all block on live Runs and
unresolved deliveries.

Conversation projection consumes durable native evidence. ACK never creates a
user message. `input_consumed` creates a model input anchor; `inputs_handled`
closes extension work without creating one; removal/rejection returns a prompt
to held; unknown remains non-dispatchable. Segment item ownership is resolved
before applying work-side effects, so late tool updates cannot alter a newer
segment.

The old schema, converter, session-wide control adapters and compatibility DTOs
are removed. A fresh schema/runtime identity is required after any contract
change. Incompatible old data is handled by the separately authorized
destructive reset, never by application startup.
