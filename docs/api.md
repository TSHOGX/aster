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
| `…/sessions/{session}/view/*` | Bounded durable conversation views, changes, details and control head |
| `…/terminals` and `…/terminals/{terminal}` | Retained PTY metadata and retirement |
| `…/terminals/{terminal}/start`, `/stop`, `/attach` | Start, stop and WebSocket observation |
| `…/files`, `/files/content`, `/files/operations/{operation}` | Directory pages, mutations, streaming content and outcome inspection |
| `…/schedules`, `/schedules/{schedule}`, `/run`, `/runs` | Project schedules and their run ledger |
| `…/sessions/{session}/readers` | Grant/revoke conversation readers |
| `…/sessions/{session}/shares`, `…/shares/{share}`, `…/shares/{share}/rotate` | Freeze, revoke and rotate public shares |
| `/api/shared/{token}`, `/messages`, `/files` | Frozen public projection selected by bearer token |
| `/api/conversations`, `/api/conversations/{session}/messages`, `/head`, `/events` | Conversations shared with the current actor |
| `/api/search`, `/api/notifications`, `/api/notifications/{notification}/read` | Scoped search and inbox |
| `…/resources/events` | Shared resource SSE with independent replay cursor |

For exact methods and payloads, see [routes.go](../orchestrator/internal/producthttp/routes.go),
[controls.go](../orchestrator/internal/producthttp/controls.go),
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
`settled` and any error. Directory queries return frozen, bounded metadata pages. Their opaque `next`
cursor binds Project, path and placement; expired/evicted cursors return 409
`resync_required`. The browser rebuilds the loaded window on that response. Paths never select private
runtime state. Read failures after headers truncate the response instead of
pretending a shorter successful download.

Resource SSE uses opaque Project/incarnation/sequence cursors. Normal rotations
replay without a content invalidation; an explicit resync revalidates visible
resources. Product revisions, file positions and transport cursors are distinct. Terminal sockets carry binary input
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

Conversation items are materialized by a bounded background projector. Native
text/thinking blocks retain stable identities; tool calls appear while arguments
are being prepared and keep their positions through execution updates. Complete
oversized content remains available through versioned detail chunks. Projection
lag never changes command outcomes or journal ACK authority.

`GET …/sessions/{session}/turn-index?before=SEQUENCE&limit=100` supplies a bounded
owner-only navigation index. `turns` are chronological within each page, initially
the newest 100. `next_before` and `next_after` are the real page edges; pass the
corresponding `before` or `after` sequence to page in either direction. Nonzero
selectors are mutually exclusive. The maximum `limit` is 300, allowing refresh
of a complete retained navigation window. Each entry includes turn
ID/sequence/state/time, up to 600 prompt characters, and up to 1200 characters of
the last assistant message only after successful turn completion. Shared reader
grants do not authorize this endpoint. View pages accept `around_turn`,
`around_message` or `around_item` to seek indexed display windows. Only one selector
is allowed. Page and change cursors bind the Session and projection generation.

## Conversation display views

Under `/api/projects/{project}/sessions/{session}/view`, all reads enforce live
Project/session authority before accessing derived content:

| Method/path | Result |
| --- | --- |
| `GET /head` | Control summary, independent display/index revisions, projection generation/state and source frontiers |
| `GET /pages` | Latest bounded page; one of `before`, `after`, `around_turn`, `around_message` or `around_item` selects another window |
| `GET /turns?ids=ID,...` | At most fifty scoped turn summaries with their independent index revision |
| `GET /messages/{message}/content` | Immutable final message text in bounded chunks for complete copy |
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
The former snapshot, raw-record, separate Project-event and file-watch HTTP
routes have been removed. There is no request-time raw-history fallback.


Shared-reader events track finalized-message sequence, not native token traffic.
Unchanged streams send heartbeats; reconnect metadata checks use `/head` and
reauthorize without reloading message bodies. Revocation closes the stream and
clears the shared view. Shared-reader authority remains separate from Project
resource events, live files and terminals.


File GETs capture and hash current bytes before returning a strong SHA-256 ETag.
`If-None-Match` returns 304 only after reauthorization and byte validation. A single
Range uses that immutable capture; matching `If-Range` returns 206 and mismatching
versions return full 200 content. Invalid byte ranges return 416; multipart ranges
fall back to a full representation. Responses use authenticated private/no-cache
semantics, and each stalled body write has a thirty-second deadline. Reads create
no file-operation ID. A detectable mutation during capture returns 409 with
`code: changed_during_read`; capture capacity is a read-only 503, never an unknown
mutation outcome. Writes retain their existing idempotency/unknown-outcome contract.


The owner-scoped `GET /api/schedules` overview returns schedule identity, Project
identity/title, selected Session ID, title, timezone, enabled state, next run time
and revision. It omits full prompt bodies and cron/edit fields. Read the existing
Project-scoped schedule detail route before editing; mutations still validate the
complete schedule and revision. Overview pagination continues after the returned schedule ID over
owned, non-deleted Projects and does not grant shared readers schedule authority.
