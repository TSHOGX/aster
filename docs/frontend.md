# Frontend Workbench

> Next.js single-page workbench in `frontend/`. All browser traffic stays
> same-origin; server-side route handlers proxy to the orchestrator. The API it
> consumes is [api.md](./api.md).

## Stack

- Next.js 16 (App Router), React 19, TypeScript (strict).
- Tailwind CSS v4 (CSS-first `@theme` in `app/globals.css`, OKLCH tokens, light theme).
- `react-markdown` + `remark-gfm` + `rehype-highlight` for content; `highlight.js`
  for code; `lucide-react` icons; `sonner` toasts.
- Vitest for the reducer unit tests.

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

- `api.ts` — typed client for every orchestrator call (`fetchMe`, `login`, `logout`,
  `fetchSessions`, `fetchSession`, `createSession`, `destroySession`,
  `renameSession`, `interruptSession`, `fetchMessages`, `postMessage`,
  `fetchArtifacts`, `fetchDeploymentCapabilities`, `fetchHealth`, plus artifact
  helpers). All use `cache: "no-store"` and a discriminated `RequestResult<T>`.
- `types.ts` — domain model: `SessionStatus`, `SessionMode`, `ApiSession`,
  `DeploymentCapabilities`, `ApiArtifact`, `ApiMessage`, `HarnessEvent`,
  `ConnectionStatus`. (Deliberately no `agent_home_path`/`restore_id` — a release
  static check asserts these stay hidden.)
- `session-events.ts` — a **pure reducer** `reduceSessionEvent` that folds SSE
  events into session/conversation slices. Framework-agnostic and unit-tested
  (`session-events.test.ts`), so live SSE and replay reduce identically. It handles
  status events, terminal-vs-non-terminal errors, checkpoint/restore retirement, and
  turn outcomes.
- `ws.ts` — despite the name, builds the **SSE** URL (`/api/events/stream` with
  `session_id` / `last_event_id`); WebSocket names are legacy aliases.
- `agents.ts`, `artifact-tree.ts`, `mime.ts`, `format.ts`, `cn.ts` — presentation
  helpers (mode icons, flat-paths→tree, artifact classification, formatting).

## Components (`components/`)

- `harness-provider.tsx` — the central React context store. Holds `ready`,
  `bootError`, `connection`, `capabilities`, `sessions`, `selectedId`, per-session
  `conversations` (messages + streaming + runtime lines), per-session `artifacts`,
  and `auth`. Exposes `selectSession`, `createSession`, `destroySession`,
  `renameSession` (optimistic, rolls back on failure), `interruptSession`,
  `sendMessage`, `refresh`, `onLogin`, `logout`.
- `workbench/workbench.tsx` — the shell: a blank hold until auth is checked, then
  `LoginScreen`, backend-unreachable card, or the 3-column grid
  `Sidebar | Conversation | ArtifactPane`.
- `workbench/sidebar.tsx` — session list, "New session" (creates an `agent`
  session), active/ended split, connection status. Each row shows the session
  `title` (falling back to the `id`) with a status dot; a per-row `...` menu offers
  Rename and Delete, and Rename (or double-click) switches the title to an inline
  editor (Enter saves via `renameSession`, Esc cancels).
- `workbench/conversation.tsx` — routes to `ChatConversation` (agent) or
  `ShellConversation` (shell), renders committed messages + streaming bubbles + a
  runtime footer.
- `workbench/shell-conversation.tsx` — terminal-style view; Enter runs, Shift+Enter
  newline, Stop interrupts while running.
- `workbench/composer.tsx`, `message-bubble.tsx`, `markdown-view.tsx`,
  `viewers/code-view.tsx` — input and rendering primitives.
- `workbench/artifact-pane.tsx` + `artifact-viewer.tsx` — live metadata-backed file
  tree with search, tabs (max 6), download links, and previews for markdown, code,
  text, image, JSON, CSV/TSV, and PDF.
- `workbench/login-screen.tsx` — username/password form; maps 429 to "too many
  attempts".

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
   (e.g. a generated or renamed `title`). A `replay_gap` forces a full `refresh()`.
4. As a safety net, after a message post the provider **polls** session/messages/
   artifacts for up to 120s, reconciling streamed text against committed messages so
   a missed SSE frame never permanently desyncs the UI.

## Auth flow

`LoginScreen` posts to `/api/login`; on success `onLogin(userId)` resets the SSE
cursor and reconnects. `logout()` posts `/api/logout` then wipes local state back to
the login screen. A `401` from any authed call routes through the same
unauthorized handler. Auth is entirely cookie/session-based and enforced by the
orchestrator; the browser never talks to it directly.
