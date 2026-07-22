# HTTP & Event API Reference

> Orchestrator surface in `orchestrator/internal/server/`. All browser traffic
> reaches these routes same-origin through the Next.js proxy (see
> [frontend.md](./frontend.md)). Default bind `:8090` (publish deployment: `:9090`).

## Route trees

The orchestrator serves three trees:

1. **Public HTTP** (`Server.Routes`) — the browser-facing API, on `cfg.Addr`.
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
| GET | `/api/sessions/{id}/artifacts` | required | list workspace artifacts |
| POST | `/api/sessions/{id}/interrupt` | required | interrupt a running shell turn |
| GET | `/api/quota` | required | session & `/30` pool ceilings |
| GET | `/api/deployment-capabilities` | required | creatable product modes |
| GET | `/api/events` | required | WebSocket event stream (compatibility) |
| GET | `/api/events/stream` | required | SSE event stream (used by the workbench) |
| GET | `/artifacts/{sessionID}/{path...}` | required | stream a workspace file |

Ownership is enforced everywhere: a missing session **or** an owner mismatch returns
`404`, so sessions cannot be probed across users.

## Selected request/response shapes

**`POST /api/login`** — `{"username","password"}` (open mode ignores the body).
→ `200 {"user_id":"..."}`, sets the auth cookie. `401` on bad credentials, `429` when
the `username|IP` key is rate-limited (5 failures / 15 min).

**`GET /api/me`** → `{"auth_required":bool, "user_id":string}`. In open mode
`{auth_required:false, user_id:"lab"}`; with auth enabled but no valid cookie,
`user_id:""`. It never returns 401 — the client uses it to decide whether to show
the login screen.

**`POST /api/sessions`** — `{"mode":"agent"|"shell"}` (default `agent`; raw `agent`
field rejected with `400`). → `200` an `apiSession`. `503 {"error_class":"pool_exhausted"}`
when the live sandbox ceiling is reached.

**`PATCH /api/sessions/{id}`** — `{"title":"..."}`. Renames the session and marks
the title user-authored (so automatic naming never overwrites it). Title is
trimmed, must be non-empty, and is capped at 200 runes (`400` otherwise). →
`200` the updated `apiSession`; broadcasts a `session.updated` event.

**`POST /api/sessions/{id}/messages`** — `{"content":"..."}`.
→ `200 {"status": <session status>, "session_id", "message": <apiMessage>}`. On the
first message of an untitled session the server sets a placeholder title (the
truncated first message) and, when the title LLM is configured
([configuration.md](./configuration.md)), asynchronously replaces it with a
generated title — each emits a `session.updated` event.

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

## Session DTO (`session_dto.go`)

`apiSession` fields: `id`, `user_id`, `title`, `status`, `mode`, `mode_label`
("Agent" / "Shell"), `restore_ms`, `created_at`, `updated_at`, `expires_at`,
`ended_at`, `last_activity_at`, `auto_checkpoint_enabled`, `failure_reason`,
`error_class`. `title` is the session's display name (empty until named); the
frontend falls back to the `id` when it is empty. The `title_custom` flag
(whether the user renamed it) is internal and not exposed.

The internal `driver_id`/`agent` are **deliberately not exposed** — the browser only
sees the product mode. Legacy `workspace`, `agent_home_path`, `restore_id` are
internal-only and omitted from the DTO.

## Events

Transport is SSE at `GET /api/events/stream` (the WebSocket `GET /api/events` is a
compatibility path). Query params: `session_id` (optional filter), `last_event_id`
(or the `Last-Event-ID` header) for replay from the durable log. A `replay_gap`
event is emitted if the requested cursor is older than the retention window; a 20s
heartbeat keeps the connection open; every event is ownership-filtered.

Event envelope: `event_id`, `type`, `session_id`, `turn_id`, `output_sequence`,
`proxy_request_id`, `stream`, `severity`, `time`, `payload`.

Event types:

| Type | Meaning |
|---|---|
| `session.created` | new session |
| `session.running_active` / `.running_idle` / `.checkpointing` / `.checkpointed` / `.destroyed` | status transitions |
| `session.updated` | session metadata changed (currently `title`); carries the full `apiSession` |
| `session.error` | terminal only when `payload.terminal === true` |
| `generation.error`, `session.checkpoint_retired`, `session.restore_fallback_retired` | non-terminal runtime events |
| `ack_turn_completed` | turn outcome (may carry a committed idle transition) |
| `message.created`, `agent.message` | committed assistant/user messages |
| `agent.delta` | streamed token text (`delta_type: text_delta`) |
| `agent.output` | raw runtime stdout/stderr line |
| `system.status` | informational status (toast) |
| `artifact.updated` / `artifact.deleted` | workspace file metadata changes |
| `proxy.request.started` / `.completed` / `.failed` | model-proxy correlation |

Payloads are scrubbed of internal identifiers and host paths (`event_dto.go`):
`generation_id`, `driver_id`, `driver_state`, `data_volumes`, `netns_path`, any
`*_path`/`host_path`, `workspace`, etc. are stripped before reaching the browser.

## Artifact download

`GET /artifacts/{sessionID}/{path...}` streams a single regular file under the
verified session workspace. Ownership is re-checked before any file metadata is
touched. The server rejects path traversal, symlink components, symlink escape,
directories, and non-regular files.

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
