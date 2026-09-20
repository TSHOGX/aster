# HTTP & Event API Reference

> Orchestrator surface in `orchestrator/internal/server/`. All browser traffic
> reaches these routes same-origin through the Next.js proxy (see
> [frontend.md](./frontend.md)). Default development bind `:8090`; deployed listeners are explicitly configured.

## Route trees

The orchestrator serves three trees:

1. **Public HTTP** (`Server.Routes`) — the browser-facing API, on `cfg.Addr`. It
   contains one **unauthenticated** subtree, `/api/shared/`, registered outside
   `requireAuth`; Go's ServeMux prefers the more specific pattern, so it wins over
   `/api/`. That precedence is load-bearing and pinned by a test in both
   directions.
2. **Operator** (`Server.OperatorRoutes`) — `GET /api/agents`; defined but not wired
   into `main.go` today.
3. **Proxy correlation** (`Server.ProxyCorrelationRoutes`) — served over a Unix
   socket, authenticated by SO_PEERCRED; not reachable over TCP.

## Public HTTP routes

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/healthz` | none | `{"status":"ok"}` |
| POST | `/api/login` | none | log in; issues the session cookie |
| GET | `/api/me` | none | `{auth_required, user_id}` (never 401s) |
| POST | `/api/logout` | required | revoke the session, expire the cookie |
| GET | `/api/sessions` | required | list the caller's sessions |
| POST | `/api/sessions` | required | create a session (`{"mode":"agent"\|"shell"}`) |
| GET | `/api/sessions/{id}` | required | session detail |
| PATCH | `/api/sessions/{id}` | required | rename session (`{"title":"..."}`) |
| DELETE | `/api/sessions/{id}` | required | close session, reclaim runtime resources |
| GET | `/api/sessions/{id}/messages` | required | message history |
| POST | `/api/sessions/{id}/messages` | required | enqueue a turn (start/wake generation) |
| GET | `/api/sessions/{id}/runtime` | required | runtime projection: active turn, queued turns, revision |
| DELETE | `/api/sessions/{id}/turns/{turnID}` | required | withdraw a queued turn (`409` once claimed) |
| POST | `/api/sessions/{id}/turns/{turnID}/send` | required | send one queued turn now (steers into the running turn, or releases a parked one) |
| POST | `/api/sessions/{id}/messages/{mid}/feedback` | required | set/clear the caller's 👍/👎 on a message |
| GET | `/api/sessions/{id}/artifacts` | required | list workspace artifacts |
| POST | `/api/sessions/{id}/artifacts` | required | multipart upload into the workspace |
| POST | `/api/sessions/{id}/interrupt` | required | stop the running turn (shell signal; agent abort delivered into the live sandbox) |
| POST | `/api/sessions/{id}/shares` | required (owner) | share the session read-only with a user |
| GET | `/api/sessions/{id}/shares` | required (owner) | list live grants on the session |
| DELETE | `/api/sessions/{id}/shares/{shareID}` | required (owner) | revoke one grant |
| GET | `/api/users` | required | usernames for the share picker |
| POST | `/api/sessions/{id}/share-links` | required (owner) | mint a public read link |
| GET | `/api/sessions/{id}/share-links` | required (owner) | list a session's public links |
| POST | `/api/sessions/{id}/share-links/{linkID}/rotate` | required (owner) | reissue the token, invalidating the old URL |
| DELETE | `/api/sessions/{id}/share-links/{linkID}` | required (owner) | revoke a public link |
| GET | `/api/shared/{token}` | **none** | trimmed session behind a public link |
| GET | `/api/shared/{token}/messages` | **none** | the link's frozen conversation |
| GET | `/api/shared/{token}/artifacts` | **none** | the link's file whitelist |
| GET | `/shared/{token}/files/{path...}` | **none** | frozen bytes of one whitelisted file |
| GET | `/api/quota` | required | session & `/30` pool ceilings |
| GET | `/api/deployment-capabilities` | required | creatable product modes |
| GET | `/api/feature-flags` | required | runtime rollout flags (all disabled by default in R0) |
| GET | `/api/admin/users` | admin | configured user ids and roles (no password material) |
| GET | `/api/admin/feature-flags` | admin | complete flag records |
| PATCH | `/api/admin/feature-flags/{key}` | admin | update a flag and append an audit record |
| GET | `/api/admin/audit` | admin | newest-first bounded audit history |
| GET | `/api/search` | required | full-text search over the caller's sessions & messages |
| GET | `/api/notifications` | required | list the caller's notifications (broadcasts + directed) with read state |
| GET | `/api/schedules` | required | list the caller's Cron Agent schedules |
| POST | `/api/schedules` | required | create a schedule (empty `cron_expr` is manual-only; `result_mode` routes runs to a new or existing session) |
| GET/PATCH/DELETE | `/api/schedules/{id}` | required (owner) | inspect, update, or delete a schedule |
| POST | `/api/schedules/{id}/run` | required (owner) | queue one manual execution and retain its session for history |
| GET | `/api/schedules/{id}/runs` | required (owner) | list durable execution history |
| POST | `/api/notifications/{id}/read` | required | mark one notification read (idempotent) |
| GET | `/api/events` | required | WebSocket event stream (compatibility) |
| GET | `/api/events/stream` | required | SSE event stream (used by the workbench) |
| GET | `/artifacts/{sessionID}/{path...}` | required | stream a workspace file |
| PUT | `/artifacts/{sessionID}/{path...}` | required | online-edit a workspace text file |

For Khora sessions, `DELETE /api/sessions/{id}` retires the product session after
journal settlement and verified pause, retaining its sandbox and stored data.
Success is `200` with `{"status":"destroyed"}`; unresolved retirement returns
`503` with `error_class: session_retirement_pending`. A committed request continues
in the background, and repeated DELETE is idempotent. See
[session retirement](session-lifecycle.md#retained-session-retirement).

### Owner gate vs. read gate

Every `/api/sessions/{id}/*` route is owner-gated except four, which run a
**read gate** instead: `GET /api/sessions/{id}`, `GET .../messages`,
`GET .../runtime`, `GET .../artifacts`, and `GET /artifacts/{sessionID}/{path...}`.
The read gate admits the owner **or** a user holding a live read grant
(`session_shares`).

Everything else — including `PATCH`/`DELETE` on the session, `POST .../messages`,
uploads, `PUT /artifacts/...`, and the share-management routes themselves — stays
owner-only. The split is deliberately in this direction: a route nobody converted
is still owner-only, so a missed route fails closed rather than becoming an
unauthorized write.

In all cases a missing session, an owner mismatch with no grant, and a revoked
grant return the same `404`, so nothing about another user's sessions is probeable.

## Selected request/response shapes

**`POST /api/login`** — `{"username","password"}` (open mode ignores the body).
→ `200 {"user_id":"..."}`, sets the auth cookie. `401` on bad credentials, `429` when
the `username|IP` key is rate-limited (5 failures / 15 min).

**`GET /api/me`** → `{"auth_required":bool, "user_id":string, "role":"user"|"admin"}`.
In open mode `{auth_required:false, user_id:"lab", role:"user"}`; with auth enabled
but no valid cookie, `user_id:""`. It never returns 401 — the client uses it to
decide whether to show the login screen.

**Administrator APIs (R0).** Credentials without a `role` field are regular
users. Only a hot-reloaded credential with `role: admin` can enter
`/api/admin/*`; open/lab mode is deliberately denied. `PATCH` accepts
`{"enabled":bool,"description":"...","reason":"..."}`. Audit pagination is
bounded to 200 rows (`limit` and `offset` query parameters).

**`POST /api/sessions`** — `{"mode":"agent"|"shell"}` (default `agent`; raw `agent`
field rejected with `400`). → `200` an `apiSession`. `503 {"error_class":"pool_exhausted"}`
when the live sandbox ceiling is reached.

**`PATCH /api/sessions/{id}`** — `{"title":"..."}`. Renames the session and marks
the title user-authored (so automatic naming never overwrites it). Title is
trimmed, must be non-empty, and is capped at 200 runes (`400` otherwise). →
`200` the updated `apiSession`; broadcasts a `session.updated` event.

**`POST /api/sessions/{id}/messages`** — `{"content":"..."}`.
→ `202 {"status": <session status>, "session_id", "turn_id", "message":
<apiMessage>, "runtime": <runtime projection>}`. Accepted while the session is
already running: the turn is appended to the queue and the runtime drains it after
the current one. The response pairs the stored message with the post-send
projection so a client applies both in one update instead of following up with a
`GET .../runtime` that could land after the worker claimed the turn.

On the first message of an untitled session the server sets a placeholder title
(the truncated first message) and, when the title LLM is configured
([configuration.md](./configuration.md)), asynchronously replaces it with a
generated title — each emits a `session.updated` event.

### The session runtime projection

`GET /api/sessions/{id}/runtime` and the `session.runtime` event carry the
**identical** payload, so a client has one shape to apply however it arrives:

```
{"session_id", "session_status", "revision",
 "active_turn": <turn|null>, "queued_turns": [<turn>...]}
```

A turn is `{id, sequence, content, status, phase?, phase_at?, tool_name?,
tool_detail?, started_at?, created_at}`. `phase` is one of `thinking`, `tool`,
`streaming`, `compacting`, `retrying`, recorded on the turn row as it happens and
present on the active turn only.

Two properties clients depend on:

- **`active_turn` — not `session_status` — defines "the agent is busy."** A
  session stays `running_active` while a queued turn waits for a generation, and
  reports `running_active` briefly before any turn is claimed. Deriving activity
  from the status is what made an idle session display "Thinking".
- **`revision` is monotonic per session.** Apply a snapshot only when its revision
  exceeds the one already held. An in-flight `GET` can otherwise land after a
  newer push and resurrect a queue entry that was already drained, and the stale
  row then never clears.

**`DELETE /api/sessions/{id}/turns/{turnID}`** withdraws a turn that is still
queued → `200` the resulting projection. Once the worker claims it the turn is
running and this returns `409 turn_not_cancelable`; ending it is then
`POST .../interrupt`. The withdrawn turn's message row is deleted with it: the
turn was never asked, so leaving the text in the transcript would read as an
unanswered prompt.

**`POST /api/sessions/{id}/interrupt`** → `202 {"status", "pending_queued",
"runtime"}`. It stops the running turn and nothing else.

For an agent session the abort travels into the live sandbox as a turn-control
command, so the sandbox, the workspace and the agent process all survive and the
next prompt costs no cold start. The turn ends `canceled` / `user_interrupted`,
and whatever already streamed stays in the transcript — it is real work.

Turns queued behind it survive too, but **parked**: `pending_queued` counts them.
A parked turn is skipped by `ClaimNextTurn`, so nothing dispatches on its own
behind a Stop — a prompt written for the turn that was just stopped is usually
stale, and running it a moment later is what Stop was pressed to avoid. Each one
waits for the user, who sends it with the endpoint below or withdraws it with
`DELETE`.

**`POST /api/sessions/{id}/turns/{turnID}/send`** sends one queued turn now →
`202 {"status", "message"?, "runtime"?, "command_id"?}`. The server picks the mechanism from the
projection, because the client cannot read it reliably at click time:

- a turn is executing → **steer**: the text is injected into that turn, so the
  work already done survives. Legacy delivery returns `status: "steered"`. The prompt becomes a
  transcript message immediately — the agent has been told, so the user has to
  see what was said — and the queue row retires, carrying
  `steered_into_turn_id`.

  Khora delivery returns `status: "steering"` and a stable `command_id` after
  the guest acknowledges receipt. The source row stays parked with
  `steering_pending: true` until its journaled driver-delivery result arrives.
  That result atomically publishes the transcript message and runtime update.
  Pending rows cannot be canceled or dispatched independently. A rejected result
  leaves the prompt parked for explicit release; interrupted recovery with no
  delivery result marks it failed with `steering_outcome_unknown`.
- nothing is executing → **release**: the parked turn is handed to the worker to
  run as a turn of its own (`status: "released"`). A parked queue does not hold a
  sandbox open — that is the point of excluding it from the pending-turn
  predicate — so the session may have been checkpointed or idle-released
  underneath those rows. Release therefore wakes it exactly as a new prompt
  would, cold start and all, rather than answering "session is not running" and
  leaving the row unsendable.

`409 turn_not_sendable` means the row moved on between render and click — claimed,
cancelled, or already sent. Re-read the projection rather than retrying. A steer
whose target turn ended mid-delivery answers `409 turn_not_steerable` and leaves
the row queued, so it can still be sent as a turn of its own. `409
turn_not_releasable` is the release-side counterpart: the row is queued but not
parked, so it is already on its way to the worker and "send it" would be a no-op
the user could not tell from success.

Legacy shell sessions answer `409` outright: their input goes straight to a pty, so there
is no queue to park, release, or steer into.

**Cron Agent (R2/R4).** `POST /api/schedules` accepts `name`, `prompt`, optional
`cron_expr` (five-field standard cron), `timezone` (IANA name), `enabled`,
`timeout_seconds`, `max_retries`, `retry_backoff_seconds`, `result_mode`
(`new_session` or `existing_session`), and `target_session_id` when routing to an
existing session. An empty `cron_expr` creates a manual-only schedule. Schedules
also expose `archived`; archiving is a soft state that disables dispatch while
retaining configuration and run history. `POST .../{id}/run` returns `202` with a
queued run. New-session runs create a durable session titled after the schedule;
existing-session runs enqueue into the configured owner session, preserving its
conversation context and workspace files. `GET .../{id}/runs` exposes durable
status (`queued`, `running`, `succeeded`, `failed`, `timed_out`), attempt,
timestamps, associated `session_id`, error class/message, and a bounded output
summary. The background worker is independent of maintenance and is enabled only
when both `harness.scheduler.enabled` and the `cron_agent` runtime feature flag
are true; manual run rows remain durable while the worker is disabled. Schedule
updates use an optimistic `version` field and due claims are atomic, so restarts
cannot double-dispatch a cron slot.

**`GET /api/quota`** →
`{"soft_session_ceiling", "active_sessions", "live_pool_ceiling",
"allocated_pool_slots", "remaining_pool_slots"}`. The session ceiling
(`max_sessions`) and the `/30` pool ceiling are reported independently — one is not
clamped to the other.

**`GET /api/deployment-capabilities`** →
```json
{"schema_version":1,"default_mode":"agent",
 "session_modes":[
   {"mode":"agent","label":"Agent","visible":true,"create_enabled":true,"disabled_reason":null},
   {"mode":"shell","label":"Shell","visible":true,"create_enabled":true,"disabled_reason":null}]}
```
`disabled_reason` carries the capability error code when a mode is not creatable.

**`GET /api/search`** — `?q=<text>&limit=<1..50>` (default 50). Full-text search
scoped by the store to the caller's own sessions. →
`{"results":[apiSearchHit...], "truncated":bool}`. `truncated` is true when the
result set was capped at `limit`. Matching uses a trigram FTS5 index over message
content, so substring matches work in any script (Chinese included); a match token
must be ≥3 characters. `apiSearchHit`: `kind` (`session`|`message`), `session_id`,
`title`, `status`, `mode`, `message_id`/`role`/`snippet` (message hits), `created_at`.

**`POST /api/sessions/{id}/artifacts`** — `multipart/form-data`; each part's field
name is the relative destination path (folder uploads preserve structure). Rejected
while a turn is in progress (`409 turn_in_progress`). Capped by `MaxUploadBytes` /
`MaxUploadFiles` (`413 too_many_files` / `edit_too_large`). →
`200 {"uploaded":["path", ...]}`; a synchronous workspace scan runs so the artifact
list and SSE are current on return.

**`PUT /artifacts/{sessionID}/{path...}`** — raw text body (`text/plain`), the online
editor. Rejected while a turn is in progress (`409 turn_in_progress`). Optional
`If-Unmodified-Since` (echo the download's `Last-Modified`) guards against clobbering
a newer agent write → `409 stale_edit` if stale. Capped by `MaxEditBytes`
(`413 edit_too_large`). → `200 {"path":"<clean path>"}`.

**`POST /api/sessions/{id}/messages/{mid}/feedback`** — `{"rating":"up"|"down"|"none"|""}`
(empty/`none` clears). → `200 {"feedback":"<effective rating>"}`. `404` if the message
is not in the session.

**`POST /api/sessions/{id}/shares`** — `{"grantee":"<username>","visibility":"summary"|"full"}`
(`visibility` defaults to `full` for identity shares). Owner-only. `400` for a
missing/unknown grantee, sharing with yourself, or an unrecognized visibility;
`409 {"error_class":"share_exists"}` when a live grant already exists for that
user. → `201` an `apiSessionShare`: `id`, `session_id`, `grantee`, `role`
(`reader`), `visibility`, `created_by`, `created_at`.

**`GET /api/sessions/{id}/shares`** → `{"shares":[apiSessionShare...]}` — live
grants only; revoked rows are omitted. **`DELETE .../shares/{shareID}`** →
`200 {"status":"revoked"}`, `404` when the id is unknown *or* belongs to another
session (the store scopes the revoke by `(session_id, id)`, not id alone).

**`GET /api/users`** → `{"users":[{"user_id":"..."}]}` — the configured usernames,
sorted, with the caller removed. Read from the live credentials registry, not the
boot-time config snapshot, so a user added by `orchestrator user add` appears
without a restart. Empty in open mode. Note this exposes the full user list to any
logged-in caller; acceptable for an internal tool, and you cannot share with
someone whose name you cannot type.

**`GET /api/sessions`** → `{"sessions":[...]}` where each entry is an `apiSession`
plus `access` (`owner`|`reader`) and, on reader rows, `shared_by` and `share_id`.
The caller's own sessions come first, then sessions shared with them.

## Public links (`/api/shared/*`)

**`POST /api/sessions/{id}/share-links`** — `{"visibility":"summary"|"full",
"ttl_seconds":<int>}`. Owner-only. Defaults: `summary`, and `ttl_seconds` of 0 or
omitted means the configured cap (`HARNESS_MAX_SHARE_LINK_TTL`, default 30 days).
A TTL above the cap is `400`; **a link can never be permanent**. A shell session
is forced to `full` and an explicit `summary` is rejected with `400` — in shell
mode the command output *is* the content, so a summary would be an empty page. →
`201 {"link": apiShareLink, "token": "<raw>", "path": "/s/<raw>"}`.

The raw token is returned **only here** (and from rotate). Only its SHA-256 is
stored, so no endpoint can echo it later; a lost link is replaced by rotating.

The link freezes `max_message_id` at creation, so turns added afterwards never
appear in a URL that has already been handed out.

**`POST .../share-links/{linkID}/rotate`** → `200 {"token","path"}` — issues a new
token and invalidates the previous URL, keeping the link's window, visibility, and
expiry. **`DELETE .../share-links/{linkID}`** → `200 {"status":"revoked"}`. Both
`404` when the id belongs to another session.

**`GET /api/shared/{token}`** (no auth) → a deliberately minimal DTO: `id`,
`title`, `mode`, `mode_label`, `status`, `created_at`, `updated_at`, `visibility`,
`link_expires_at`, `shared_at`, `frozen`, `frozen_at`. `status` is coarsened to
`active`|`idle`|`ended` rather than exposing the internal state machine. Built
field by field, so a field added to `apiSession` is not published here by accident.

**`GET /api/shared/{token}/messages`** → `{"messages":[{id, role, content,
created_at, stop_reason?, trace?}]}` — windowed by `max_message_id` and trimmed by
the link's visibility. No `session_id`, no `feedback`.

**`GET /api/shared/{token}/artifacts`** → `{"artifacts":[{path,size}]}` — the
link's **whitelist**, never the live workspace listing. The blob digest is an
internal storage key and is not published.

**`GET /shared/{token}/files/{path...}`** (no auth) → the file's bytes. Note this
is a **top-level** path, not under `/api/`, so an HTML/PDF preview iframe has a
URL that is obviously not an API call and the frontend proxy for it is its own
route handler.

The path is resolved against `share_link_files` — an exact whitelist lookup, not a
filesystem join — so a path that was not shared simply does not exist, and there is
no traversal surface. Bytes come from the content-addressed snapshot store, so an
agent's later write cannot change what a published URL returns and the link keeps
working after the session is destroyed. HTML and PDF carry a framable-same-origin
CSP (`connect-src 'none'`, `form-action 'none'`) so the shared page can preview
them; everything else is served inert. A whitelist row whose bytes are missing is
a `404`, not a `500`.

Include files at creation with `"files":["report.html", ...]` (workspace-relative
paths). They are copied into the snapshot store during the request, so both a file
count cap (`HARNESS_MAX_SHARE_LINK_FILES`, default 25) and a total byte cap
(`HARNESS_MAX_SHARE_LINK_BYTES`, default 8 MiB) apply — `400` for too many files,
`413` for too many bytes, `404` for a path that is not in the workspace. A rejected
create leaves no link row. Omit `files` for a conversation-only link.

Unknown, expired, revoked, and rotated-away tokens are all `404`. The subtree is
GET-only (`405` otherwise) and sends `Referrer-Policy: no-referrer`, because the
token is in the URL path. Access counters are buffered in memory and flushed by
the maintenance loop, so anonymous reads never contend for the writer connection;
a restart loses at most one window of counts.

Anonymous requests are **not** admitted to the SSE/WebSocket streams, so a shared
page is a snapshot with an explicit refresh rather than a live view.

## Session DTO (`session_dto.go`)

`apiSession` fields: `id`, `user_id`, `title`, `status`, `mode`, `mode_label`
("Agent" / "Shell"), `restore_ms`, `created_at`, `updated_at`, `expires_at`,
`ended_at`, `last_activity_at`, `auto_checkpoint_enabled`, `failure_reason`,
`error_class`. `title` is the session's display name (empty until named); the
frontend falls back to the `id` when it is empty. The `title_custom` flag
(whether the user renamed it) is internal and not exposed.

The internal `driver_id`/`agent` are **deliberately not exposed** — the browser only
sees the product mode. Internal `workspace`, `agent_home_path`, `restore_id` are
omitted from the DTO.

**Non-owner readers get a trimmed DTO** (`sharedSession()` in `share_dto.go`):
`user_id`, `failure_reason`, and `error_class` are dropped. A failure reason can
carry host paths and internal detail, and the owner's username is not the reader's
business — being inside the same organization is not a reason to relax either.

Message trimming follows the grant's `visibility`: `full` returns the messages the
owner sees minus `feedback` (per-viewer state a reader cannot set); `summary` also
drops `trace` and the intermediate `stop_reason: "toolUse"` steps. `summary` is
defined by *excluding* `toolUse` rather than requiring `stop_reason == "stop"`,
because shell output and pre-trace history carry an empty `stop_reason` and would
otherwise vanish from a shared shell session.

## Message DTO

`GET /api/sessions/{id}/messages` → `{"messages":[Message...]}` (`store.Message`):

- `id`, `session_id`, `role`, `content`, `created_at` — the chat log.
- `turn_id` — the durable turn a user message belongs to, when it has one (absent
  for assistant/system messages and pre-migration history). Lets a client correlate
  a transcript entry with turn state by id rather than by matching content. A turn
  that is still `queued` has no message listed here at all — it lives in the runtime
  projection's `queued_turns` until the worker claims it, which is what makes it
  impossible for one prompt to appear in both places.
- `stop_reason` — pi's terminator for an assistant message: `stop` (final reply),
  `toolUse` (intermediate tool step), or empty (user/shell/pre-migration). Lets the
  client distinguish a completed answer from an interstitial.
- `trace` — execution-trace nodes (thinking phases + tool calls) that produced the
  message; opaque JSON passed through verbatim, absent when there is no stored trace.
- `feedback` — the requesting user's own `up`/`down` rating, empty when unset.

## Events

Transport is SSE at `GET /api/events/stream` (the WebSocket `GET /api/events` is a
compatibility path). Query params: `session_id` (optional filter), `last_event_id`
(or the `Last-Event-ID` header) for replay from the durable log. A `replay_gap`
event is emitted if the requested cursor is older than the retention window; a 20s
heartbeat keeps the connection open; every event is ownership-filtered.

Event envelope: `event_id`, `type`, `session_id`, `turn_id`, `output_sequence`,
`proxy_request_id`, `stream`, `severity`, `time`, `payload`.

**Every frame is named** (`event: <type>`), so a browser client MUST register each
type it consumes via `addEventListener` — `onmessage` receives only unnamed
frames and will never see these. A type missing from the client's registration
list is not degraded, it is dropped entirely and in silence, with a handler that
still looks correct. That failure shipped once (`session.runtime`, see
[runtime synchronization](./runtime-state-synchronization.md)),
so the list in `frontend/lib/use-event-stream.ts` is covered by a test asserting
it contains every type the provider handles. Add new types there and to the
table below together.

Event types:

| Type | Meaning |
|---|---|
| `session.created` | new session |
| `session.running_active` / `.running_idle` / `.checkpointing` / `.checkpointed` / `.destroyed` | status transitions |
| `session.updated` | session metadata changed (currently `title`); carries the full `apiSession` |
| `session.runtime` | the runtime projection changed (turn queued/claimed/ended, or a phase transition); payload is identical to `GET .../runtime`. Live-only — `turns` is the replay source, so a reconnecting client re-reads the endpoint |
| `session.error` | terminal only when `payload.terminal === true` |
| `generation.error`, `session.checkpoint_retired`, `session.restore_fallback_retired` | non-terminal runtime events |
| `ack_turn_completed` | turn outcome (may carry a committed idle transition) |
| `message.created`, `agent.message` | committed assistant/user messages |
| `agent.delta` | streamed token text (`delta_type: text_delta`) |
| `agent.output` | raw runtime stdout/stderr line |
| `system.status` | informational status (toast) |
| `artifact.updated` / `artifact.deleted` | workspace file metadata changes |
| `notification.created` | new broadcast notification; payload is `{notification_id}` only, a "go refetch" signal (not durable, not replayed) |
| `proxy.request.started` / `.completed` / `.failed` | model-proxy correlation |

Payloads are scrubbed of internal identifiers and host paths (`event_dto.go`):
`generation_id`, `driver_id`, `driver_state`, `data_volumes`, `netns_path`, any
`*_path`/`host_path`, `workspace`, etc. are stripped before reaching the browser.

## Artifact download & edit

`GET /artifacts/{sessionID}/{path...}` streams a single regular file under the
verified session workspace; `PUT` on the same path is the online editor (see the
shape above). Ownership is re-checked before any file metadata is touched, and both
methods reject path traversal, symlink components, symlink escape, directories, and
non-regular files. HTML/PDF downloads carry a preview-friendly same-origin CSP; all
other types are served inert. See [security.md](./security.md).

## Errors

- Typed runtime/control-plane failures: `{"error_class":"...","error":"..."}`
  (e.g. `pool_exhausted` → 503, plus the runtime failure classes in
  [sandbox-runtime.md](./sandbox-runtime.md)).
- Generic validation/not-found: `{"error":"..."}`.

## Proxy-correlation routes (Unix socket only)

| Method | Path | Purpose |
|---|---|---|
| POST | `/internal/proxy/requests/start` | model proxy reports a request started → returns session/turn/generation/sequence if authorized |
| POST | `/internal/proxy/requests/finish` | reports completion (latencies, http_status, error_class) |

Authenticated by SO_PEERCRED against `harness.proxy_service_identity`. This is how
the sandbox gets model access only during its own active turn — see
[security.md](./security.md).

`start` returns **503 `active_context_unavailable`** when the turn's proxy context
cannot be resolved. 503 rather than a 4xx is load-bearing: this is a transient
internal bookkeeping condition, and the proxy maps a 4xx to a hard error that pi
treats as unretryable, which ends the whole turn. It previously surfaced to users
as "the model service is unavailable" when the provider was healthy.

A running turn's lease may be honored for `harness.bridge.proxy_lease_grace` past
its expiry (default 60s). Turn leases are renewed only by the bridge heartbeat,
which competes for the single writer connection, so a heartbeat delayed by write
pressure would otherwise reject every model call the turn makes. Identity is
unaffected — the generation must still be live and the lease owner must match —
and the grace must stay below `sandbox_heartbeat_stall_grace` (enforced at config
validation) so a sandbox already judged stalled cannot keep model access. Requests
admitted this way are logged as a warning; a nonzero rate means writer contention
is building. See
[runtime synchronization](./runtime-state-synchronization.md).

## Optional knowledge and external evolution

`GET /api/kb-review` and `POST /api/kb-review/decisions` are orchestrator routes for
staged KB review. They use the normal authenticated API boundary and the configured
KB/memory stores. They are distinct from `/api/admin/*`; do not assume an admin
feature flag controls them.

`/api/evolution/*` is a Next.js integration route, not an orchestrator endpoint.
It requires explicit external project/state/database configuration and the local
`EVOLUTION_VIEWERS` allowlist. Absent configuration returns 503; unauthorized
callers are refused. It reads external reports and can start that runner; it does
not fabricate a default report or ship organization-specific SQL. See
[memory boundaries](agents-kb.md) for lifecycle and ownership.

`GET /healthz` reports version/commit and deployment drain/background-hold status.
These identify the running build but do not prove business acceptance of a
particular database, model or external integration.

## Bounded workbench reads

`GET /api/deployment-capabilities` advertises `message_pages` and
`artifact_pages`. Existing unpaged routes remain available. The new read routes
use the same ownership/share gates; none grant write access.

- `GET /api/sessions/{id}/messages/page`: optional mutually exclusive
  `before_id`, `after_id`, or `around_id`; `limit` defaults to 40 and caps at 80.
  Rows are ascending by message ID. `has_older`, `has_newer`, `before_id`,
  `after_id`, `latest_id`, `latest_final_id`, and `interruption` describe the
  window and authoritative tail. Summary visibility and the public link's
  `max_message_id` apply before pagination. Encoded message rows have a 256 KiB
  budget. Bodies over 4,096 Unicode characters become a 2,048-character preview,
  identified by `content_truncated` and `content_length`. `has_trace` and
  `is_final_reply` avoid downloading execution detail or guessing from page edges.
- `GET .../messages/{messageId}/content`: `offset` and `limit` address Unicode
  characters; default/maximum chunk size is 8,192. Returns `content`, `offset`,
  `next_offset`, `total`, and `has_more`. `download=1` streams the body as a text
  attachment. Reading a hidden or out-of-snapshot message returns 404.
- `GET .../messages/{messageId}/trace`: node `offset`, up to 50 nodes and 256 KiB
  per page; returns `trace`, `next_offset`, and `has_more`. Large nodes carry a
  `detail_index`; `GET .../trace-content?node=N&offset=N` reads their raw JSON in
  bounded character chunks. Summary readers cannot retrieve traces.
- `GET .../messages/{messageId}/mentioned-files`: resolves up to 12 referenced
  files against the accessible file set, independently of loaded directory pages.
- `GET .../artifacts/children`: `directory`, `cursor`, and `limit` (200 default,
  500 maximum). Returns direct child entries, directory size/count aggregates,
  `next_cursor`, and `has_more`. `GET .../artifacts/search?q=...` searches the
  full accessible path set. Empty search lists files. Responses are bounded to
  256 KiB of entries; sort/cursor ordering is binary `(kind,path)` ordering.
  Directory pages are live views, not cross-request frozen snapshots.

The equivalent routes under `/api/shared/{token}` resolve the token and its
visibility/frozen-message boundary on every request. File reads use only the
link's frozen whitelist. Public message DTOs exclude session IDs, turn IDs and
feedback. Public session metadata also advertises the page capabilities.

`GET /api/events/stream?view=workbench` projects committed bodies to bounded
previews and excludes eager trace/tool-result payloads. Commit events can carry
`stream_ids` for removing corresponding live previews. The legacy event shape,
durable event records and internal full-transcript consumers are unchanged.
SSE is not a completeness guarantee; clients reconcile against HTTP windows.

Message pages report database and encoding durations through `Server-Timing`.
The new routes require no schema migration or additional index.
