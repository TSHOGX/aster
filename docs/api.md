# Browser API

The Go edge routes `/api/` to the authenticated application. Cookies identify the
actor; body fields never supply an owner. State-changing requests and WebSocket
handshakes check Origin. Unknown request fields and trailing JSON are rejected.
Resource paths below use stored Project and child identities.

Identity responses contain `id`, canonical `username`, `display_name`, `status`,
`role`, `revision`, `session_revision` and `must_change_password`; never hashes or
session tokens. Temporary login permits only session inspection, password setup
and logout. Setup submits `new_password`. Self-change submits `current_password`
and `new_password`; both password endpoints replace the cookie only after the
transaction commits. Other devices must sign in again after password changes.

Preferences contain only `revision`, `theme_mode` (`system`, `light`, `dark`) and
`accent` (`neutral`, `blue`, `green`, `violet`). Before first initialization, GET
returns revision 0 and system/neutral. PATCH requires `expected_revision` and at
least one changed appearance field. A simultaneous initializer or stale edit
returns 409. Profile PATCH requires `expected_revision` and `display_name`
(1–128 Unicode characters); its read projection contains only ID, username,
display name and revision. Unknown privilege/resource fields are rejected.
Restricted password-setup sessions cannot call either settings API. Invalid
settings and an incorrect current password return 422; an expired login returns
401. A profile update preserves the current session.

| Scope | Operations |
| --- | --- |
| `POST /api/auth/login`, `POST /api/auth/logout`, `GET /api/auth/session` | Sign in, sign out and inspect current database identity |
| `POST /api/auth/password/setup` | Exchange a restricted temporary-login session for a permanent password and normal session |
| `POST /api/me/password`, `DELETE /api/me/sessions` | Change the current password or revoke all account sessions |
| `GET/PATCH /api/me/preferences` | Revisioned theme mode and accent for the signed-in account |
| `GET/PATCH /api/me/profile` | Immutable ID/username and revisioned display name |
| `GET /api/users` | Authenticated sharing directory containing immutable IDs and display labels |
| `GET/POST /api/admin/users`, `GET/PATCH /api/admin/users/{id}` | Administrator account list/detail/create and display-name/role edits |
| `POST /api/admin/users/{id}/password-reset` | Issue a one-time temporary credential and revoke that account's sessions |
| `GET /api/admin/groups` | Bounded group choices for account creation |
| `GET /api/admin/audit-events` | Bounded actor/target/action/time-filtered administrative/security audit |
| `POST /api/admin/users/{id}/suspend`, `/enable`; `GET /api/admin/operations/{id}` | Durable account-state intent and observed operation state |
| `/api/projects` | List and create Projects |
| `/api/schedules`, `/api/schedules/events` | Owner-scoped schedule overview and metadata invalidation SSE across Projects |
| `/api/projects/{project}` | Inspect, rename and request deletion |
| `…/resume`, `…/pause`, `…/restart`, `…/blockers` | Project lifecycle and admission state |
| `…/sessions` and `…/sessions/{session}` | List/create/inspect/rename/retire Agent Sessions |
| `…/sessions/{session}/turns` | Ordered turns and prompt admission |
| `…/sessions/{session}/control` and `/controls/{request}` | Admit exact-run Steer/Stop and read durable receipts/current evidence |
| `…/sessions/{session}/turns/{turn}/control` | Hold, release or cancel an unclaimed prompt |
| `…/sessions/{session}/queue` | Bounded FIFO prompt page and client projection handoff |
| `…/sessions/{session}/view/*` | Bounded durable conversation views, changes, details and control head |
| `…/terminals` and `…/terminals/{terminal}` | Retained PTY metadata and retirement |
| `…/terminals/{terminal}/start`, `/stop`, `/attach` | Start, stop and WebSocket observation |
| `…/files`, `/files/content`, `/files/editor-content`, `/files/download`, `/files/operations/{operation}` | Directory pages, mutations, preview bytes, explicit export and outcome inspection |
| `…/files/snapshots`, `/files/snapshots/{snapshot}/content`, `/files/snapshots/{snapshot}` | Acquire an immutable source lease, read/range its bytes, and release it |
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
operations require the relevant current revision. Hold/Release/Cancel use
`expected_prompt_revision` and `request_id`; Steer/Stop use an exact
`target_run_id`, `target_incarnation_id`, `target_owner` and `request_id`.
Steer also supplies `source` and `expected_prompt_revision`. The Session
`control_revision` is a refresh marker, not a queue mutation guard. Deletion and Project
lifecycle actions read `revision` from the query. Forced restart explicitly adds
`force=1`.

A Steer/Stop `202` contains a durable receipt, not proof of native consumption
or run completion. `GET …/controls/{request}` returns that immutable receipt
with current command, delivery and run evidence. A not-admitted control has an
HTTP error and `state: not_admitted`, never a successful pending result. Queue
reads accept optional `projection_frontier`, the client's last applied
conversation source frontier; consumed rows remain as synchronization rows
until that client's projection catches up. Terminal start can initially return `202`;
observe its state before attaching. A disconnected response to a mutation is an
unknown outcome and must be retried only with the original request identity.

Immutable preview sources use `GET …/files/snapshots?path=RELATIVE_PATH&max_bytes=N`
with a default 20 MiB source limit and a maximum 100 MiB. The response contains
`id`, SHA-256 `digest`, `size`, and `expires`. Read with
`GET …/files/snapshots/{id}/content` and optional single `Range`; release with
`DELETE …/files/snapshots/{id}`. A snapshot never changes versions. Every access
checks current Project authority; the lease is not a bearer credential. Expired
handles return `410 snapshot_expired` and oversized sources return
`413 source_too_large`. See [preview contract](file-preview/README.md) for quotas
and lifecycle behavior.

Office visual previews use the existing authorized content endpoint. No Office
job, window or copy API is required. See the [preview contract](file-preview/README.md).

File upload uses `PUT …/files/content?path=RELATIVE_PATH` with `files.upload`;
editor saves use `PUT …/files/editor-content?path=RELATIVE_PATH` with `files.edit`.
Both require a bounded Content-Length, `Idempotency-Key` and `X-Content-SHA256` containing **64 lowercase
hexadecimal characters**. Directory mkdir/move/remove requires `files.manage`.
The server selects the action from the route; headers cannot override it. Durable
mutation IDs bind both payload and action. File-operation responses include `id`, `action`, `state`,
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

`GET /api/schedules?project_id=OPTIONAL&q=TITLE&status=active|paused|exhausted|canceled|deleted&after=CURSOR&limit=50`
returns `{schedules: [...], next?: CURSOR, previous?: CURSOR}` with at most 50 metadata rows.
`before=CURSOR` reads the preceding batch; `anchor=TASK_ID` relocates the window
at that task in the current authorized/filter-matching catalog. Selectors are
mutually exclusive. An absent or filtered anchor restarts at the first batch.
Omit status for active and paused tasks. Unicode lowercase title matching, Project filtering,
and owner authorization apply before pagination. Automatic tasks sort by next
occurrence and ID, followed by active manual-only tasks and paused tasks.
Opaque continuation cursors bind the owner and filters to a catalog revision.
Schedule edits/ticks and relevant Project changes invalidate continuation with
`409 resync_required`; reconcile the current window with an anchor read or restart
at the first page. The page and revision come from
one database read snapshot. This replaces ID-only overview pagination.

`GET /api/schedules/events?cursor=CURSOR` uses the same Resource SSE envelope,
`resource` event, opaque incarnation/sequence cursor, `ready`, `resync`, rotation,
heartbeat, reauthorization, bounded replay ring and subscriber queues as Project
resource streams. Its scope is the authenticated owner; it creates no Project
connections or file watchers. Hints use `schedule.created|updated|deleted` and
`run.created|updated|deleted`, with `resource_id` identifying the task and payload
containing only `project_id`, `schedule_id` and optional `run_id`. Revision strings
are outbox positions, not task configuration revisions. Hints never contain
prompts, run contents or inferred outcomes. Clients must read the authoritative
endpoints; initial/resync events reconcile their visible scopes. The durable
owner outbox retains at most 2,048 hints, independently of Project deletion;
expired/restarted streams explicitly resync rather than silently losing updates.

`GET /api/projects/{project}/schedules/{schedule}/runs` accepts mutually exclusive
`after=CURSOR`, `before=CURSOR` or `anchor=RUN_ID`, with a maximum `limit=50`, and
returns `{runs: [...], next: CURSOR_OR_EMPTY, previous?: CURSOR}`. New opaque cursors
bind the owner, Project, task and immutable creation-time/ID ordering tuple, so
removing a boundary row does not omit the remaining history. Run-ID continuations are not accepted.
History state uses the same correlated prompt/execution evidence as conversation
controls, including claims before consumption and unknown outcomes.

`POST /api/schedules/preview` accepts `{trigger_kind?, run_at?, cron, timezone, next_at?}` and returns
`{valid, cron, timezone, reference_time, occurrences, errors?}`. Field errors use
`cron`, `interval`, `timezone` or `next_at`. The authenticated, non-mutating endpoint validates with the
execution parser and returns three upcoming UTC instants or none for manual-only.
For `trigger_kind: "at"`, supply a future RFC3339 `run_at` with an explicit
offset and empty `cron`; preview returns that one normalized instant.
It creates no task or run. `cron` accepts five-field Cron, empty for manual-only,
or the canonical interval expression `@every Nm` (integer N from 1 to 10080).
Intervals measure elapsed minutes; the IANA timezone controls their display only.
Optional `next_at` previews an existing interval’s phase. Without it, preview
starts a hypothetical timer at `reference_time`; creation starts its timer when
the server accepts the task. The
pinned parser skips nonexistent spring-forward wall times and can produce both
occurrences of a repeated fall-back wall time; clients display each offset.

An interval first runs one period after creation, enabling, or changing its
interval. Its saved `next_at` preserves the cadence across worker delays and
restarts; missed occurrences coalesce into one run and advance to the first
cadence point after the current time. Name/instruction/target edits, interval
display-zone changes and manual runs do not reset it. Client-supplied operational fields such as `next_at` are rejected on browser
create/update. An unsettled occurrence blocks the next automatic admission for
that schedule, retaining the original due instant with a 30-second recheck.
Different schedules still use ordinary Session queues.

Task mutations and history remain Project-scoped. Run history returns at most 50
rows and an explicit `next` continuation identity only when another row exists.

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
`GET /api/conversations/{session}` returns bounded conversation metadata only
when the actor has an active reader grant; ownership alone is insufficient.
This lookup is independent of list pagination. Generic `/messages`, `/head`, and
`/events` retain their owner-or-reader permission contract.
Unchanged streams send heartbeats; received-view reconnect checks use the
grant-specific detail endpoint without reloading message bodies. Revocation closes the stream and
clears the shared view. Shared-reader authority remains separate from Project
resource events, live files and terminals.


`GET /api/projects/{project}/files` returns bounded sorted directory pages with
`snapshot`, `start`, `total`, and optional `next`/`previous` cursors. Requests use
`path`, `limit`, and `after` for cursor navigation. `seek=around&name=...` obtains
a bounded window containing the requested filename; `seek=after&name=...` starts
after that name. A seek starts a new metadata observation and cannot be combined
with a cursor. Active and paused reads apply the same authority and path checks.
Expired cursors return 409 `resync_required`; clients relocate by name without
scanning all preceding pages. Directory inventories remain bounded to 100,000 names.

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
identity/title, selected Session ID, title, Cron (at most 128 characters), timezone,
enabled state, next run time and revision. It omits full prompt bodies. Read the
Project-scoped detail for the selected task; mutations validate the complete
schedule and revision. Reader grants do not confer schedule authority.

Schedule creation requires `id`, `request_id`, `title`, `prompt`, `trigger_kind`
and `timezone`; `cron` describes cron/interval triggers, while `run_at` describes
an absolute one-shot. Optional authored fields are `session_id` (empty creates a
new Session per occurrence), `state` (`active` or `paused`), `expires_at` and
`notify` (default true). PATCH uses the same typed authored fields, `revision`
and `request_id`; omitted fields are preserved. POST `…/{schedule}/cancel`
requires `revision` and `request_id`. DELETE uses both as query parameters.
Mutations return the immutable schedule snapshot committed with the receipt;
GET reads its current state. Exact retries retain their original outcome even
after edits, cancellation or time passing; changed payloads conflict. New
mutations are rejected during release drain. The Project limit is 100 active or
paused definitions. Cancellation stops future admission, not existing Prompts.

A one-shot becomes exhausted when its Prompt is admitted, independently of
execution success. Run-now consumes its original occurrence, and a retry returns
that same run. Expiry stops admission without retracting existing work. An
exhausted one-shot cannot be rearmed: create new intent with a new identity.
Scheduler failures are isolated per task; invalid configurations pause with a
bounded diagnostic, transient admission failures back off (5 seconds, 30 seconds,
then 5 minutes). Unknown execution outcomes are never automatically resubmitted.

`GET …/schedules/{schedule}/metadata` returns a prompt-free definition projection
with source Session, lifecycle, next time and latest occurrence state/Session/Prompt
IDs. It uses the same ownership checks as full definitions, including deleted
resource tombstones. Visible native receipt cards use this endpoint and the
shared owner-scoped invalidation stream. `GET …/schedules` also returns metadata
only, with a maximum 50-row page and `next` ID continuation; full prompts are
available only from individual GET. Both browser and native list operations use
the same product query.

The Scheduled page supports one-shot local date/time entry, explicit repeated-hour
offset selection, result notifications, cancellation and exhausted/canceled/deleted
history filters. Missing DST wall times cannot be saved. Native receipt cards
validate IDs against the current Project, ignore embedded navigation URLs, and
fetch authoritative metadata while visible. Source/result links use normal
resource authorization. Tombstones replace active promises after deletion.

Scheduled result delivery and generic Agent outcome delivery share
`turn/<prompt_id>` notice IDs. The per-occurrence notify snapshot composes with the
global setting, including scheduled inputs consumed as steers. Unknown resolution
updates the same notice and resets unread only for a changed outcome. Scheduled
queued cancellation/handled disposition can notify without an execution Run;
queueing or trigger exhaustion alone never reports success. Inbox navigation uses
the input's materialized message when available and otherwise its Session.

Bounded condition checking uses an interval schedule with `max_occurrences`
(1–100), mandatory `expires_at`, optional future `starts_at`, and
`notification_policy` (`each_run` or `on_finish`). The first explicit start must
precede the deadline; subsequent interval occurrences preserve that phase.
Admission increments the durable count, including manual invocation, and the
last permitted occurrence exhausts future admission. PATCH may clear the bounded
option with `max_occurrences: 0`, empty `starts_at`, and `each_run`; it cannot
silently rearm an exhausted definition. Already admitted Prompts retain their
notification policy snapshot.

`on_finish` suppresses ordinary completed pending-check notices, including when
generic Agent notices are enabled. Failure/unknown remains visible. Count/deadline
exhaustion posts one `schedule/<id>` attention notice once admitted work settles;
a still-live final check can complete without a premature exhaustion notice.
`notify: false` suppresses the task's notices. Finish does not emit success until
native execution settles; failure after finish remains stopped and notifies.

## Administration

Every admin service checks a current normal session and platform-admin role.
Writes recheck that authority inside the same transaction as mutation, audit and
receipt. Origin protection applies to every admin mutation. List endpoints accept
`limit` (1–100, default 50) and opaque `cursor`. User filtering by `search`, `status`
and `role` happens before pagination; audit supports `actor`, `target`, `action`,
`after` and `before` (RFC3339 times). Cursors cannot be reused with other filters.

Create sends `request_id`, `username`, `display_name`, `role` and optional
`group_ids`. Omission selects the standard group; an explicit empty array selects
none. At most 256 distinct current groups may be selected. Edits send
`request_id`, `expected_revision` and the changed `display_name` and/or `role`.
Username is immutable. Reset/suspend/enable send request ID and expected revision.
Role changes revoke affected sessions; the last active admin cannot be removed.

A mutation result contains `user`, `replayed` and, on the first successful create
or reset response only, `temporary_password`. A same-ID/body replay returns its
recorded non-secret result before stale revision checks; another action, target or
body conflicts. Results are snapshots of that mutation; use detail/operation GET
for current state. Requests still require current authority to read their outcome.
An uncertain credential response is resolved by checking the original request,
then explicitly resetting with a new request ID if a new password is needed.

Account suspension commits disabled status, revocation and durable operation/
project targets before returning 202. This development stage records the intent;
resource stop/pause reconciliation is not yet implemented. The operation remains
`accepted`, never falsely `succeeded`. Enable restores identity only and leaves
resource/share/schedule reactivation separate. The admin UI currently exposes
account metadata/security and audit; suspension controls await that reconciler.

## Group and policy management

`GET/POST /api/admin/groups`, `GET/PATCH/DELETE /api/admin/groups/{id}` and
`GET/POST /api/admin/policies`, `GET/PATCH/DELETE /api/admin/policies/{id}` manage
named groups and reusable policies. Mutations carry `request_id`; existing objects
also require `expected_revision`. Group metadata is name/description; policies
add a `statements` map using the closed product action set and allow/deny/unset.
Unknown actions and wildcards are rejected. A bound policy must be detached before
deleting it.

`GET/PATCH /api/admin/groups/{id}/members` lists members or applies bounded
`add_user_ids`/`remove_user_ids` deltas (up to 100 each). Accounts may belong to at
most 256 groups. `PUT .../policies` replaces the complete `policy_ids` list (up to
256); an explicit empty list clears bindings. Omission does not silently clear it.
Membership/policy mutations advance affected authorization revisions and commit
audit plus replay receipt atomically.

`GET /api/me/capabilities` returns the current account status, management role,
authorization revision, effective actions and allow/deny provenance. Administrative
inspection uses `GET /api/admin/users/{id}/effective-access`. These projections
never grant resource ownership; service admission still checks stored scope.

Before deleting a populated group, read `GET .../deletion-preview`. Its bounded
user pages show before/after grants and current numeric entitlements. Delete sends
the preview's `expected_dependencies`; a changed contributing configuration
conflicts. Zero-member groups need only revision validation. Deletion detaches
memberships/bindings without assigning another group or removing admin roles.
The numeric preview currently covers group/platform values; per-user numeric
overrides are integrated with the resource-settings implementation.

Queued prompt responses expose `dispatch_error` when current Agent authority
rejects pending work. `permission_revoked` holds the prompt until an explicit
release under a current `agents.run` grant or cancellation. Control outcome reads
also expose this error, separately from the immutable acceptance receipt. Enqueue,
release and steer require the current grant even on replay; hold, cancel and stop
retain active-owner reduction authority.

Terminal start/attach/input/keyboard-claim/resize require `terminals.use` independently
of `projects.read`. Revocation closes an existing attachment within the five-second
connection target. It leaves the retained PTY alive; explicit owned termination,
delete and exit settlement remain available through their reduction authority.

Workspace listing requires `files.list`; preview/content and cached preview ranges
require `files.read`. `GET …/files/download?path=RELATIVE_PATH` requires both
`files.read` and `files.export`, with the same confinement, captures, byte ranges
and transfer bounds as preview. Preview bytes remain copyable when export is
denied. Frozen public shares retain their separate resource grant. Releasing an
owned preview lease and inspecting a file-operation outcome remain available to
an active owner after functional grants disappear.

Conversation reader grants use immutable account IDs: POST
`…/sessions/{session}/readers` accepts `{ "reader_id": "ACCOUNT_ID" }` and requires
`conversations.share`. GET `…/reader-candidates?q=QUERY&after=ID&limit=50` provides
bounded active-account choices under that same grant. GET `…/readers?after=ID&limit=50`
lists current recipients so the active owner can revoke them even after losing the
grant. Self-grants and nonexistent/disabled recipients are refused. Account names
are display/search labels, never authorization identifiers.

Public snapshot creation and link rotation require `shares.public.create`.
Removing that grant leaves existing frozen links valid; explicit revoke remains
available to the active owner. Disabled owner status denies public content access
immediately, independently of later suspension reconciliation.

Schedule create/edit/resume and their retries require `schedules.manage`; Run now
also requires `agents.run`. A pure `{ "state": "paused" }` patch, cancel and delete
retain active-owner reduction authority. Definition/outcome inspection remains
available for those controls. Automatic dispatch rechecks both grants and pauses
a denied due task with `last_error: "permission_revoked"`; no occurrence is created.
Platform schedule context omits denied create/update operations, and mutations
reauthorize even through an already-open gateway request.
