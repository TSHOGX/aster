# Scheduling future work

Create future work in conversation when `schedules_*` tools are available, or use
the Scheduled page. Both surfaces manage the same persisted Project tasks. A
saved task survives browser closure, Agent exit and idle compute suspension.

Examples:

- “At 23:00 Asia/Shanghai, check the final score and report a source here.”
- “Every weekday at 09:00, summarize these files in a new conversation.”
- “Starting at 22:00, check every five minutes until the match finishes; stop by
  midnight or after 12 checks, and notify me when confirmed.”

The Agent asks when event identity, timezone or a repeated local time is ambiguous.
The one-shot editor also rejects missing DST times and asks for an offset when a
wall time occurs twice. Relative requests use the server clock. Only a successful
persisted receipt means the task was saved. If a response is lost, inspect or retry
the original request; creating a fresh task can duplicate intent.

Conversational tasks default to the current Session. New-Session tasks receive
self-contained saved instructions and ordinary Project assets, not an implicit
transcript copy. All Sessions still share the Project filesystem and Linux trust
domain. Different tasks can modify the same files.

A one-shot is consumed when its Prompt enters the ordinary execution queue.
“Exhausted” describes its trigger; use run history to distinguish queued, running,
completed, failed and unknown execution. Run now consumes an active one-shot.
It cannot be rearmed after consumption; create a new task for a new intention.
Recurring Run now does not alter cadence. Content edits preserve the next due
instant; trigger/resume changes deliberately recalculate it.

Pause or cancel stops future admissions. Already queued/running work stays under
its conversation controls. Fixed-target deletion cancels future work. Deleting a
source-only conversation clears provenance and private receipt details while an
independently targeted task survives. Deleted definitions remain tombstones until
normal retirement; canceled and exhausted definitions remain inspectable.

Bounded checks are ordinary interval tasks with a deadline and a maximum count
(default 12 in the native tool, hard maximum 100). The Agent checks the external
source and calls `schedules_finish` only when confirmed. Pending checks are quiet
under `on_finish`; failure/unknown and budget exhaustion need attention. The last
admitted check can finish after the count/deadline closes further admission, but
cannot overwrite a newer pause/cancel/edit. A finish request does not claim native
execution has completed. There is no external event subscription or generic
workflow engine.

Automatic recurrence admits at most one unsettled occurrence per task and
coalesces missed times. Unknown execution is not automatically retried. An overdue
one-shot executes once after recovery unless its explicit deadline has passed.
Eligibility is not an exact start-time promise.

Result notices use the existing inbox and link to the execution conversation.
Visible native task cards refresh from authorized metadata and show the latest
occurrence; the native receipt remains the original acknowledgment. Editing a
notification preference changes future occurrences only. No email, push provider,
third-party message or extra summarization model call is involved.

## Enablement and release

Configure `schedule_tools: true` on the Agent's declared gateway audience. The
owner checks platform protocol 1 and the Project's measured current runtime
before launching tools. A zero-gateway Project can still use the Scheduled page.
Existing immutable service bindings are never silently modified.

Current source requires schema 4, manifest 4, native protocol 4 and schedule
protocol 1. Install a measured runtime containing the reviewed explicit extension
and qualify the reserved gateway prefix before enabling it. Gateway replacement
requires full quiescence. Owner-only handoff is insufficient for this schema or
runtime transition. Existing databases are refused without modification; a
fresh installation follows the explicitly scoped reset in [bootstrap](bootstrap.md).
There is one supported runtime and schema, with no migrations or retained-image
catalog. The development installation has been reset and qualified with these
contracts; see [deployment evidence](fresh-deployment.md).

See [API](api.md), [gateway contract](gateway-contract.md),
[runtime](sandbox-runtime.md), [operations](operations.md), and
[implementation acceptance](conversational-scheduling-implementation.md).
