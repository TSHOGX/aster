# Frontend Workbench

> Next.js single-page workbench in `frontend/`. All browser traffic stays
> same-origin; server-side route handlers proxy to the orchestrator. The API it
> consumes is [api.md](./api.md).

## Stack

- Next.js 16 (App Router), React 19, TypeScript (strict).
- Tailwind CSS v4 (CSS-first `@theme` in `app/globals.css`, OKLCH tokens, light theme).
- `react-markdown` + `remark-gfm` + `rehype-highlight` for content; `highlight.js`
  for code; `lucide-react` icons; `sonner` toasts.
- Vitest for `lib/` unit tests (the pure reducers, trace/message-grouping,
  upload-entries, mime/format helpers).

Scripts: `dev` (`next dev`, binds `0.0.0.0`), `build`, `start`, `lint`, `typecheck`,
`test`. Local dev: `PORT=8000 npm run dev`.

## Same-origin proxy (`lib/orchestrator-proxy.ts`, `app/api/[...path]/route.ts`)

The browser only ever calls the frontend origin. Two Node route handlers proxy to
the orchestrator:

- `app/api/[...path]/route.ts` — forwards `/api/*` to orchestrator `/api/*` (all
  methods), special-casing `/api/healthz` → orchestrator `/healthz`.
- `app/artifacts/[sessionId]/[...path]/route.ts` — streams artifact downloads.

The proxy drops hop-by-hop headers plus `origin`/`referer` (decoupling CORS) but
forwards cookies, injects `x-forwarded-*`, streams the upstream body unbuffered (so
SSE passes through), and forces `cache-control: no-store`. On an upstream failure it
returns `503`, which the UI renders as "backend unreachable".

Proxy target: `HARNESS_API_BASE_URL` → `ORCHESTRATOR_URL` → default
`http://127.0.0.1:8090`. `PORT` sets the frontend port.

## Data layer (`lib/`)

- `api.ts` — typed client for every orchestrator call: auth (`fetchMe`, `login`,
  `logout`), sessions (`fetchSessions`, `createSession`, `destroySession`,
  `renameSession`, `interruptSession`), messages (`fetchMessages`, `postMessage`,
  `postMessageFeedback`), artifacts (`fetchArtifacts`, `uploadArtifacts`,
  `writeArtifact`, `fetchArtifactText`/`Blob`, `buildArtifactHref`), plus
  `searchSessions`, `fetchNotifications`/`markNotificationRead`, and
  `fetchDeploymentCapabilities`/`fetchHealth`. All use `cache: "no-store"` and a
  discriminated `RequestResult<T>`.
- `types.ts` — domain model: `SessionStatus`, `SessionMode`, `ApiSession`,
  `DeploymentCapabilities`, `ApiArtifact`, `ApiMessage` (incl. `stop_reason`, `trace`,
  `feedback`), `ApiNotification`, `HarnessEvent`, `ConnectionStatus`. (Deliberately no
  `agent_home_path`/`restore_id` — a static check asserts these stay hidden.)
- `session-events.ts` — a **pure reducer** `reduceSessionEvent` that folds SSE events
  into session/conversation slices. Framework-agnostic and unit-tested, so live SSE
  and replay reduce identically. It handles status events, terminal-vs-non-terminal
  errors, checkpoint/restore retirement, and turn outcomes.
- `harness-events.ts` — event-type constants and typing for the SSE stream.
- `trace.ts` — parses persisted execution-trace JSON into thinking/tool nodes for the
  trace UI. `message-grouping.ts`, `message-jump.ts` — conversation layout + scroll
  targeting (used by search-result jumps).
- `upload-entries.ts` — normalizes drag-drop / folder `File` entries into relative
  upload paths. `use-composer-state.ts`, `use-event-stream.ts`, `use-popover.ts` —
  React hooks for the composer, SSE lifecycle, and popovers.
- `ws.ts` — despite the name, builds the **SSE** URL (`/api/events/stream` with
  `session_id` / `last_event_id`); WebSocket names are aliases.
- `agents.ts`, `artifact-tree.ts`, `mime.ts`, `languages.ts`, `format.ts`, `guards.ts`,
  `cn.ts` — presentation helpers (mode icons, flat-paths→tree, artifact
  classification, syntax language mapping, formatting, type guards).

## Components (`components/`)

- `harness-provider.tsx` — the central React store. Holds `ready`, `bootError`,
  `connection`, `capabilities`, `sessions`, `selectedId`, per-session `conversations`
  (messages + streaming + runtime lines + per-turn activity), per-session `artifacts`,
  `notifications`, and `auth`. Exposes `selectSession`, `createSession`,
  `destroySession`, `renameSession` (optimistic, rolls back on failure),
  `interruptSession`, `sendMessage`, `setMessageFeedback`, `markNotificationRead`,
  `refresh`, `onLogin`, `logout`. State is exposed through selector hooks
  (`useNotifications`, etc.) so components subscribe to slices instead of re-rendering
  on every store change.
- `workbench/workbench.tsx` — the shell: a blank hold until auth is checked, then
  `LoginScreen`, backend-unreachable card, or the 3-column grid
  `Sidebar | Conversation | ArtifactPane`.
- `workbench/sidebar.tsx` — session list: single-line rows (`title` → `id`, status
  dot), collapse toggle with hover flyout, active/ended split, connection status,
  "New session". A per-row `…` menu offers Rename and Delete; Rename (or double-click)
  switches to an inline editor.
- `workbench/conversation.tsx` — routes to the agent chat or `shell-conversation.tsx`;
  renders committed messages + streaming bubbles + a runtime footer, with the
  notification bell in the header.
- `workbench/activity-indicator.tsx` — per-turn status (thinking / tool / streaming /
  compacting / retrying) with request count + elapsed time.
- `workbench/trace-list.tsx` + `trace-node.tsx` — progressive-disclosure execution
  trace (thinking phases + tool calls), collapsed behind a summary for long traces.
- `workbench/message-actions.tsx` — per-message action bar (copy, 👍/👎 feedback via
  `setMessageFeedback`). `message-bubble.tsx`, `markdown-view.tsx`, `inline-diff.tsx`,
  `viewers/` — rendering primitives.
- `workbench/composer.tsx` — floating auto-expanding chat bar.
- `workbench/artifact-pane.tsx` + `artifact-viewer.tsx` — live metadata-backed file
  tree with search and tabs; upload entry (multi-file / folder / drag-drop),
  Artifacts-style preview panel with full-screen/sidebar toggle, and previews for
  Markdown, code, text, image, JSON, CSV/TSV, PDF, HTML, and Office files (with a
  Download fallback). Text files get an inline **edit** mode (`writeArtifact`), gated
  on no turn in progress and guarded against clobbering a newer agent write.
- `search/search-palette.tsx` — ⌘P / Ctrl+P command palette: full-text search across
  the caller's session titles and messages (`searchSessions`), relevance-ranked with
  match highlighting; selecting a hit opens the session and jumps to the message.
- `notifications/notification-bell.tsx` + `notification-detail-modal.tsx` — unread
  badge, dropdown, and detail modal for broadcast notifications.
- `workbench/login-screen.tsx` — username/password form; maps 429 to "too many
  attempts". `theme/`, `ui/` — theme script and shared primitives.

## Boot & event flow

1. On mount the provider calls `GET /api/me` to set `auth`. A blank screen is held
   until `auth.checked` (prevents a login flash).
2. Once auth is ready it opens `EventSource(/api/events/stream)` (with
   `last_event_id` for replay), and on connect/reconnect calls `refresh()`
   (health → sessions → deployment capabilities → seed conversations).
3. Live events dispatch through `handleEvent` / the pure reducer:
   `agent.delta` streams tokens into a streaming bubble; `agent.message` /
   `message.created` commit messages; `agent.output` appends a runtime line;
   `artifact.updated`/`.deleted` update the tree; `system.status` toasts;
   `session.*` updates status; `session.updated` merges changed session metadata
   (e.g. a generated or renamed `title`); `notification.created` refetches the
   notification list (the event carries only an id). A `replay_gap` forces a full
   `refresh()`.
4. As a safety net, after a message post the provider **polls** session/messages/
   artifacts for up to 120s, reconciling streamed text against committed messages so
   a missed SSE frame never permanently desyncs the UI.

## Auth flow

`LoginScreen` posts to `/api/login`; on success `onLogin(userId)` resets the SSE
cursor and reconnects. `logout()` posts `/api/logout` then wipes local state back to
the login screen. A `401` from any authed call routes through the same
unauthorized handler. Auth is entirely cookie/session-based and enforced by the
orchestrator; the browser never talks to it directly.
