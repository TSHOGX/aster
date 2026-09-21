# Browser API

The Go edge routes `/api/` to the authenticated application. Cookies identify the
actor; body fields never supply an owner. State-changing requests and WebSocket
handshakes check Origin. Unknown request fields and trailing JSON are rejected.
Resource paths below use stored Project and child identities.

| Scope | Operations |
| --- | --- |
| `/api/auth/login`, `/logout`, `/me` | Sign in, sign out and inspect current identity |
| `/api/projects` | List and create Projects |
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
