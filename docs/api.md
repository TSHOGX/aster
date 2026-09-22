# Browser API

The Go edge routes `/api/` to the authenticated application. Cookies identify the
actor; body fields never supply an owner. State-changing requests and WebSocket
handshakes check Origin. Unknown request fields and trailing JSON are rejected.
Resource paths below use stored Project and child identities.

| Scope | Operations |
| --- | --- |
| `/api/auth/login`, `/logout`, `/me` | Sign in, sign out and inspect current identity |
| `/api/projects` | List and create Projects |
| `/api/schedules` | Owner-scoped, paginated schedule overview across Projects |
| `/api/projects/{project}` | Inspect, rename and request deletion |
| `…/resume`, `…/pause`, `…/restart`, `…/blockers` | Project lifecycle and admission state |
| `…/sessions` and `…/sessions/{session}` | List/create/inspect/rename/retire Agent Sessions |
| `…/sessions/{session}/turns` | Ordered turns and prompt admission |
| `…/sessions/{session}/control` | Continue, interrupt, steer or retry recovery |
| `…/sessions/{session}/turns/{turn}/control` | Control held/queued work |
| `…/sessions/{session}/snapshot`, `/records`, `/messages` | Bounded conversation/history projection |
| `…/terminals` and `…/terminals/{terminal}` | Retained PTY metadata and retirement |
| `…/terminals/{terminal}/start`, `/stop`, `/attach` | Start, stop and WebSocket observation |
| `…/files`, `/files/content`, `/files/watch`, `/files/operations/{operation}` | Directory pages, mutations, streaming content, watches and outcome inspection |
| `…/schedules`, `/schedules/{schedule}`, `/run`, `/runs` | Project schedules and their run ledger |
| `…/sessions/{session}/readers` | Grant/revoke conversation readers |
| `…/sessions/{session}/shares`, `…/shares/{share}`, `…/shares/{share}/rotate` | Freeze, revoke and rotate public shares |
| `/api/shared/{token}`, `/messages`, `/files` | Frozen public projection selected by bearer token |
| `/api/conversations`, `/api/conversations/{session}/messages`, `/events` | Conversations shared with the current actor |
| `/api/search`, `/api/notifications`, `/api/notifications/{notification}/read` | Scoped search and inbox |
| `…/events` | Project SSE invalidation/projection stream |

For exact methods and payloads, see [routes.go](../orchestrator/internal/producthttp/routes.go),
[projections.go](../orchestrator/internal/producthttp/projections.go),
[adjacent.go](../orchestrator/internal/producthttp/adjacent.go) and
[project-api.ts](../frontend/lib/project-api.ts).

Create Projects/Sessions with an explicit `id` and `title`. Admit a prompt with
`request_id`, `content` and optional `held`. Repeating the same admitted identity
and payload returns its prior intent; a changed payload conflicts. Rename/control
operations require the relevant current revision. Agent streaming controls use
`control_revision`, separate from projection revision. Deletion and Project
lifecycle actions read `revision` from the query. Forced restart explicitly adds
`force=1`.

A `202` means pending/accepted work, not completion. Inspect the resource or its
operation until evidence settles. Terminal start can initially return `202`;
observe its state before attaching. A disconnected response to a mutation is an
unknown outcome and must not be treated as permission to repeat an effect.

File content writes use `PUT …/files/content?path=RELATIVE_PATH`, a bounded
Content-Length, `Idempotency-Key` and `X-Content-SHA256` containing **64 lowercase
hexadecimal characters**. File-operation responses include `id`, `state`,
`settled` and any error. Directory queries are paged. Paths never select private
runtime state. Read failures after headers truncate the response instead of
pretending a shorter successful download.

SSE uses monotonically increasing projection cursors. A reconnect or explicit
gap requires a fresh authorized snapshot. Terminal sockets carry binary input
and JSON controls such as `claim_input`, `resize`, `interrupt`, `eof` and `signal`.
Server frames communicate input ownership, output, gaps and exit. Controls remain
bound to the observer's original incarnation and owner; reauthorization continues
throughout the connection. Slow observers have bounded queues and disconnect
with a gap.

Missing and foreign resources return `404`; absent login returns `401`; Origin
violations return `403`; stale/conflicting intent returns `409`. Capacity and
pending outcomes remain distinct from successful completion. Browser access does
not expose engine handles or operator control routes.

`GET /api/schedules?project_id=OPTIONAL&after=ID&limit=50` returns
`{schedules: [...], next?: ID}`. Entries include existing schedule fields plus
`project_title`. Ownership, project deletion state and schedule deletion are
filtered before stable ID pagination; conversation reader grants do not grant
schedule access. Continue with `next` until absent. Task mutations and run history
use the existing Project-scoped routes.

Owner Session snapshots include `timeline` and `timeline_truncated`. Timeline items carry a stable `id`, native `cursor`, `turn_id`, `kind` and bounded display fields. They are derived from the existing journal inside the authorized snapshot transaction; no database migration or new sharing permission is introduced.

Tool-call content in native assistant messages is projected immediately as a
`tool` row with `state: "pending"`, before execution starts. Execution start,
partial updates, and end events merge into the same turn-scoped call ID, yielding
`running`, `completed`, or `failed`. Rows retain the original call order even
when parallel executions start or finish in a different order. Text-only tool
results are extracted before the display limit is applied, preserving line
breaks in long output; other results retain their bounded JSON representation.

`turn_summaries` in owner snapshots supplies `id`, `state`, `created_at`, optional
`finished_at`, and `final_message_id` only for completed turns. It includes turns
referenced by the visible history page even when they are outside the live
control window. These are projections of existing turns/messages, not new
lifecycle authority or a database migration.

`GET …/sessions/{session}/turn-index?before=SEQUENCE&limit=100` supplies a bounded
owner-only navigation index. `turns` are chronological within each page, initially
the newest 100; `next_before` loads the preceding page. Each entry includes turn
ID/sequence/state/time, up to 600 prompt characters, and up to 1200 characters of
the last assistant message only after successful turn completion. Shared reader
grants do not authorize this endpoint. `snapshot?turn=ID` validates the turn's
session membership and loads surrounding messages anchored at its user message
(including adjacent turns), with the target prompt retained;
`message` targeting takes precedence when both are supplied. Turn summaries also
include `sequence` for stable numbering of off-page navigation targets.

Owner snapshots accept `before` for preceding messages or `after` for following
messages, and return `has_earlier` / `has_later` for the two page boundaries.
The client merges only adjacent pages in its current reading window; turn IDs
identify scroll destinations rather than conversation filters.


## Conversation display views

Under `/api/projects/{project}/sessions/{session}/view`, all reads enforce live
Project/session authority before accessing derived content:

| Method/path | Result |
| --- | --- |
| `GET /head` | Control summary, independent display/index revisions, projection generation/state and source frontiers |
| `GET /pages` | Latest bounded page; one of `before`, `after`, `around_turn` or `around_message` selects another window |
| `GET /changes?after_revision=R` | Immutable changes to a fixed target revision; follow returned `cursor` with the same base |
| `GET /items/{item}/detail?version=V` | A UTF-8 chunk of full item JSON; follow `cursor` until `next` is absent |
| `POST /rebuild` | Invalidate only derived state and enqueue reconstruction; never replay execution |

Page/delta responses are capped at 256 KiB encoded JSON. Pages contain at most 50
items; delta batches contain at most 100 operations. Oversized inline items have
`truncated` and `detail_version`, preserving complete content through the detail
route. Encode item IDs as URL path components. Position/content/index revisions
are decimal strings; cursors bind session, generation and position. Head returns
at most 32 relevant control turns and flags additional turns for the existing
turn-history route.

A building projection returns `202`, never an authoritative empty conversation.
`409` with `code: resync_required` rejects stale generations/expired changes.
Malformed selectors/cursors return `400`; absent or unauthorized resources retain
`404` equivalence. A failed projector leaves control available through head and
reports its display failure separately. Read saturation returns `503`.
Snapshot routes remain during the active browser cutover, with no raw-history
fallback inside the new view endpoints.
