# Frontend Workbench

> Next.js single-page workbench in `frontend/`. All browser traffic stays
> same-origin; server-side route handlers proxy to the orchestrator. The API it
> consumes is [api.md](./api.md).

## Stack

- Next.js 16 (App Router), React 19, TypeScript (strict).
- Tailwind CSS v4 (CSS-first `@theme` in `app/globals.css`). One token contract
  drives both themes: light is white panels on a pale blue-grey canvas, dark is a
  neutral deep blue-grey, and both share a single indigo accent as the only
  saturated hue in the chrome. Type (`--text-*`, 12px floor), elevation
  (`--shadow-*`, cool and shallow; border-led in dark mode), reading width
  (`--measure`) and chart series (`--chart-1..8`) are tokens too — components
  reference the variables rather than hardcoding literals.
- `react-markdown` + `remark-gfm` + `rehype-highlight` for content; `highlight.js`
  for code; `lucide-react` icons; `sonner` toasts.
- Vitest for `lib/` unit tests (the pure reducers, trace/message-grouping,
  upload-entries, mime/format helpers).

Scripts: `dev` (`next dev`, binds `0.0.0.0`), `build`, `start`, `lint`, `typecheck`,
`test`. Local dev: `PORT=8000 npm run dev`.

Build-time UI toggles (`lib/ui-config.ts`). Next inlines `NEXT_PUBLIC_*` at build
time, so changing one needs `npm run build` + a frontend restart — there is no
runtime knob:

| Variable | Default | Effect |
| --- | --- | --- |
| `NEXT_PUBLIC_HARNESS_BRAND_NAME` | `BatteryGPT` | Product name in the wordmark, the page title, and the byline on every assistant reply. Never translated — a brand reads the same in both locales, and the byline has to match the wordmark. |
| `NEXT_PUBLIC_HARNESS_UPLOAD_UI` | unset (off) | Shows the workspace upload buttons and drop target. The backend route is unaffected; this only withholds the UI. |

UI language follows the browser: Chinese for any `zh-*` tag, English otherwise.
There is deliberately no in-app switcher — the browser already carries the user's
answer, and a second control that can disagree with it is a bug surface. See
`lib/i18n/` under Data layer.

## Same-origin proxy (`lib/orchestrator-proxy.ts`, `app/api/[...path]/route.ts`)

The browser only ever calls the frontend origin. Two Node route handlers proxy to
the orchestrator:

- `app/api/[...path]/route.ts` — forwards `/api/*` to orchestrator `/api/*` (all
  methods), special-casing `/api/healthz` → orchestrator `/healthz`.
- `app/artifacts/[sessionId]/[...path]/route.ts` — streams artifact downloads.
- `app/shared/[token]/files/[...path]/route.ts` — streams public-link snapshot
  files. A separate handler because the other two cover `/api/*` and `/artifacts/*`
  only; GET/HEAD are the only verbs exported, matching the read-only surface
  upstream. `next.config.mjs` excepts `/shared/` from the app-wide
  `X-Frame-Options: DENY` for the same reason `/artifacts/` is excepted: the
  orchestrator sends `SAMEORIGIN` for HTML/PDF, and two conflicting values make the
  browser block the frame.

The proxy drops hop-by-hop headers plus `origin`/`referer` (decoupling CORS) but
forwards cookies, injects `x-forwarded-*`, streams the upstream body unbuffered (so
SSE passes through), and forces `cache-control: no-store`. On an upstream failure it
returns `503`, which the UI renders as "backend unreachable".

Proxy target: `HARNESS_API_BASE_URL` → `ORCHESTRATOR_URL` → default
`http://127.0.0.1:8090`. `PORT` sets the frontend port.

## Data layer (`lib/`)

- `api.ts` — typed client for every orchestrator call: auth (`fetchMe`, `login`,
  `logout`), sessions (`fetchSessions`, `createSession`, `destroySession`,
  `renameSession`, `interruptSession`), turns (`cancelQueuedTurn`,
  `sendQueuedTurn`), messages (`fetchMessages`, `postMessage`,
  `postMessageFeedback`), artifacts (`fetchArtifacts`, `uploadArtifacts`,
  `writeArtifact`, `fetchArtifactText`/`Blob`, `buildArtifactHref`), plus
  `searchSessions`, `fetchNotifications`/`markNotificationRead`, and
  `fetchDeploymentCapabilities`/`fetchHealth`. All use `cache: "no-store"` and a
  discriminated `RequestResult<T>`.
- `types.ts` — domain model: `SessionStatus`, `SessionMode`, `ApiSession`,
  `DeploymentCapabilities`, `ApiArtifact`, `ApiMessage` (incl. `stop_reason`, `trace`,
  `feedback`), `ApiNotification`, `HarnessEvent`, `ConnectionStatus`. (Deliberately no
  execution internals — a static check asserts these stay hidden.)
- `session-events.ts` — a **pure reducer** `reduceSessionEvent` that folds SSE events
  into session/conversation slices. Framework-agnostic and unit-tested, so live SSE
  and replay reduce identically. It handles status events, terminal-vs-non-terminal
  errors, retained lifecycle transitions, and turn outcomes.
- `harness-events.ts` — event-type constants and typing for the SSE stream.
- `trace.ts` — parses persisted execution-trace JSON into thinking/tool nodes for the
  trace UI. `message-grouping.ts`, `message-jump.ts` — conversation layout + scroll
  targeting (used by search-result jumps).
- `mentioned-files.ts` — which workspace files an assistant reply refers to, for the
  **In this reply** section. The session's real artifact list is a **whitelist**: a
  token counts only when it resolves to a file that exists, so precision comes from
  that intersection rather than from a path-shaped regex (which reads `v1.2`, `3.5%`
  and `e.g.` as filenames). A basename seen on two different paths is marked
  ambiguous and matches nothing — opening the wrong `report.md` is worse than
  offering neither — and a slash-free token must contain a dot, or an artifact named
  `data` would match every prose use of the word. Folds `./`, `~/` and the
  `/workspace/` mount prefix away, strips CJK and markdown punctuation, and returns
  paths in **first-appearance** order: the agent's own narrative order is the order
  the user just read them in. Unit-tested, since every rule here is a false-positive
  class someone would otherwise rediscover in production.
- `use-persisted-flag.ts` — a boolean UI preference in localStorage, same
  external-store shape as `use-pane-width.ts` and for the same reason: it must be
  readable on the client's *first* render, or the section visibly flips itself shut
  one frame in. Only a non-default value is written, so an untouched preference
  leaves no key behind and a later change of default still reaches those users.
- `upload-entries.ts` — normalizes drag-drop / folder `File` entries into relative
  upload paths. `use-composer-state.ts`, `use-event-stream.ts`, `use-popover.ts`,
  `use-media-query.ts` — React hooks for the composer, SSE lifecycle, popovers, and
  the narrow-width layout switch.
- `use-pane-width.ts` — persisted width for a resizable panel (the workspace rail).
  The value lives in a module-level store read through `useSyncExternalStore`, not
  component state, so the client's first render already has the stored width while
  the server renders the responsive default — restoring it in an effect would mean
  a setState-in-effect and a visible width jump on load. The raw preference is
  stored and clamped at *read* time, so a temporarily narrow window never
  overwrites the user's choice.
- `i18n/` — `locale.ts` (browser-language detection: matches the `zh` primary
  subtag, so every Chinese variant resolves, and honours preference *order* so
  `["en","zh"]` stays English), `messages-en.ts` (the source of truth — `MessageKey`
  derives from it), `messages-zh.ts` (typed `Record<MessageKey, string>`, so an
  untranslated key is a build error, not a silent English fallback), and `index.ts`
  (`useT`, `format`, `splitAround`). Translation is client-side, which is only safe
  because the workbench holds a blank screen until `/api/me` resolves — there is no
  translated text in the server HTML to mismatch on hydration.
- `composer-draft.ts` — module-level pub/sub for "put this text in the composer",
  modeled on `message-jump.ts` and used by the suggested-prompt cards. Carries a
  monotonic sequence number so picking the same prompt twice still registers.
- `table-cells.ts` — `isNumericText`, the predicate deciding which markdown table
  cells right-align as figures. Unit-tested, because what counts as numeric ("80%"
  yes, "3 cycles" no) is easy to regress silently.
- `ws.ts` — builds the SSE URL (`/api/events/stream` with `session_id` /
  `last_event_id`).
- `agents.ts`, `artifact-tree.ts`, `mime.ts`, `languages.ts`, `format.ts`, `guards.ts`,
  `cn.ts` — presentation helpers (mode icons, flat-paths→tree, artifact
  classification, syntax language mapping, formatting, type guards).

## Components (`components/`)

- `harness-provider.tsx` — the central React store. Holds `ready`, `bootError`,
  `connection`, `capabilities`, `sessions`, `selectedId`, per-session `conversations`
  (messages + streaming + runtime lines + per-turn activity), per-session `artifacts`,
  `notifications`, and `auth`. Exposes `selectSession`, `createSession`,
  `destroySession`, `renameSession` (optimistic, rolls back on failure),
  `interruptSession`, `cancelQueuedTurn`, `sendQueuedTurn`, `sendMessage`,
  `setMessageFeedback`, `markNotificationRead`,
  `refresh`, `onLogin`, `logout`. `useLastReply(sessionId)` selects just the last
  committed turn-final assistant message (by the same `isFinalReply` rule the action
  bar uses) so the workspace panel can derive its mentioned-files list without
  subscribing to the whole conversation — which would re-render the file tree on
  every streamed token. It deliberately ignores streaming text: extracting paths
  from a half-typed reply would grow a filename character by character, so the
  previous turn's list stays up until the new reply commits. State is exposed
  through selector hooks
  (`useNotifications`, etc.) so components subscribe to slices instead of re-rendering
  on every store change.
- `workbench/workbench.tsx` — the shell: a blank hold until auth is checked, then
  `LoginScreen`, backend-unreachable card, or the 3-column grid
  `Sidebar | Conversation | ArtifactPane` (viewport-relative clamps, not fixed
  widths, and the workspace column is drag-resizable via `workbench/pane-resizer.tsx`
  — a focusable `separator` with arrow-key steps, persisted through
  `lib/use-pane-width.ts`). Below 1024px — where three columns cannot all be usable — the side
  panels become Escape-dismissable overlay drawers behind a compact top bar. The
  open drawer is *derived* from the media query rather than synced in an effect, so
  widening the window cannot leave one floating over a layout that has room for it.
- `workbench/sidebar.tsx` — session list: single-line rows (`title` → `id`, status
  dot), collapse toggle with hover flyout, active/ended split, connection status,
  "New session". A per-row `…` menu offers Rename, Share and Delete; Rename (or
  double-click) switches to an inline editor. Rows with `access: "reader"` (shared
  *with* the caller) show none of those actions — the server 404s every write for a
  reader, so the UI does not offer buttons that can only fail — and are listed under
  a separate **Shared with me** group rather than mixed into History, which would
  make the list look like the user owns work they cannot touch. The same
  `access === "reader"` check disables the composer (chat and shell), the artifact
  upload/drag-drop entry points, and the inline file editor. A reader's conversation
  header gains a **Refresh** button: `eventVisibleTo` filters session events to the
  owner, so a shared view receives nothing live and would otherwise go stale
  silently. That also means the provider must *actually refetch* for a reader:
  `refresh()` and `selectSession()` pass `skipLoaded` only for an **owner**, whose
  updates arrive over SSE anyway. Passing it for a reader made their Refresh button
  a no-op once messages existed, freezing both the conversation and the file list
  at whatever was there on first load — which is what made the mentioned-files
  section look unsupported on a shared session.
- `workbench/share-dialog.tsx` — owner-only sharing: read grants to named users
  (`/api/users` feeds the picker) and public links. A freshly minted or rotated
  token is displayed once, held only in component state, and copied to the
  clipboard on request; it is never refetchable.
- `app/s/[token]/page.tsx` + `components/shared/shared-session-view.tsx` — the
  public read-only page. It mounts **no** `HarnessProvider`: that provider owns
  auth, SSE and the session list, so rendering it would fire an authenticated
  request and bounce an anonymous visitor to the login screen. The page fetches
  `/api/shared/*` itself and reuses only the presentational leaves
  (`message-bubble.tsx`, `shell-entry.tsx`), which take their data as props. There
  is no live stream for anonymous viewers, so the page carries an explicit Refresh
  control rather than appearing stuck.
- `components/shared/shared-file-panel.tsx` — the link's whitelisted files, with
  inline preview for HTML/PDF (same-origin iframe) and images, and download for
  everything else. The list is the whitelist, so nothing the owner did not select
  is reachable. It also mounts the same `MentionedFiles` section the workbench
  uses, over that whitelist rather than the live workspace: a file the reply
  mentions but the owner did not publish resolves to nothing and is dropped, which
  is correct twice over — the row would 404, and naming it would leak that it
  exists. `shared-session-view.tsx` picks the reply with the same `isFinalReply`
  rule the workbench uses, so a link and its owner's view agree on which message is
  "the reply" even at `full` visibility, where the tool-use steps are still present.
- `workbench/conversation.tsx` — routes to the agent chat or `shell-conversation.tsx`;
  renders committed messages + streaming bubbles + a runtime footer, with the
  notification bell in the header. The chat is one centered column at `--measure`,
  the same width the composer uses, and turns are separated by whitespace: the user
  side is a right-aligned bubble, the assistant side has no card or row fill. A
  shell transcript keeps its per-entry rules (it really is a log) but shares the
  centered column. `MessageBubble`/`ShellEntry` own only vertical rhythm, so every
  caller — including the shared-link page — supplies the centering wrapper.
- `workbench/prompt-suggestions.tsx` — generic dataset, report and log-analysis
  starting points. Picking one fills the composer rather than sending a request.
  The examples assume no private data store or organization-specific schema.
- `workbench/activity-indicator.tsx` — per-turn status (thinking / tool / streaming /
  compacting / retrying) with request count + elapsed time.
- `workbench/trace-list.tsx` + `trace-node.tsx` — progressive-disclosure execution
  trace (thinking phases + tool calls), collapsed behind a summary for long traces.
- `workbench/message-actions.tsx` — per-message action bar (copy, 👍/👎 feedback via
  `setMessageFeedback`). `message-bubble.tsx`, `markdown-view.tsx`, `inline-diff.tsx`,
  `viewers/` — rendering primitives.
- `workbench/composer.tsx` — floating auto-expanding chat bar.
- The queue rows (`QueuedTurns` in `conversation.tsx`) render `queued_turns` from the
  projection as-is, with two per-row actions and **no queue-wide operation**: ↑ sends
  that one turn, X withdraws it. One prompt at a time is deliberate — after a Stop the
  queue is usually stale, so the user picks what still matters instead of facing an
  all-or-nothing choice. A row parked by Stop says so; an unparked one is already on
  its way to the worker. The ↑ label follows what the press will do, from the same
  active/idle split the server uses: with a turn executing the text is steered into
  it, otherwise the parked row is released to run. A stale projection therefore
  mislabels a button rather than misrouting a prompt.
- `busy` and `stoppable` are distinct in `conversation.tsx`: a queued-only session is
  busy (the placeholder invites a follow-up) but not stoppable, since Stop ends the
  turn in flight and would have nothing to act on.
- `workbench/mentioned-files.tsx` — a collapsible **In this reply** section at the
  top of the workspace panel listing the files the latest reply refers to, one per
  row, styled as a section of the tree below it rather than a separate widget.
  Which files those are is decided by `lib/mentioned-files.ts`, not by this
  component. It renders nothing when the count is zero — an occasional pointer, not
  a permanent empty strip — and keeps the count visible while collapsed so putting
  it away does not look like a breakage. Collapse state is persisted **globally**
  (`lib/use-persisted-flag.ts`), not per session: collapsing it once means "I don't
  want this section", and re-opening it on every new reply would be nagging. The
  list caps at ~5 rows and scrolls internally so a chatty turn cannot push the file
  tree off screen. Mounted in both the pane's list and preview modes from two call
  sites rather than hoisted above the branch — it belongs *under* the Workspace
  header in list mode and on top in preview mode, and two call sites keep DOM order
  equal to visual order (a CSS `order` on one shared node would desync focus and
  screen-reader order). Alongside an open file it doubles as a switcher between the
  turn's own outputs; it is hidden in the full-screen overlay. Its file list is
  structurally typed (anything with a `path`), which is what lets the public-link
  panel reuse it with `SharedLinkFile`. Suppressed for `mode === "shell"` in both
  callers: shell entries are command output written with an empty `stop_reason`, so
  the structural fallback would read one as a final reply when there is no prose to
  mine at all. It also exports `SectionHeader` and `useMentionedPaths`: when the
  section is present the tree below it gains an **All files** header, so the two
  read as two labelled groups rather than one list split by an unexplained
  hairline (the same job `SessionSection` does for the sidebar's "Shared with me" /
  "History"). The tree's header is static text, not a button — only the mentioned
  section collapses, and a disclosure chevron on something that does not toggle is
  a lie. It appears *only* when the section is above it: alone in the panel the
  tree is self-evidently the files, and the Workspace header already says so. The
  parent asks `useMentionedPaths` rather than recomputing, so the two can never
  disagree about the count.
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
- `workbench/markdown-view.tsx` — `react-markdown` with component overrides that
  give the agent's two main outputs a real treatment: tables get a bounded scroll
  wrapper with a sticky header, zebra rows and right-aligned numeric columns
  (classified by `lib/table-cells.ts`, since CSS cannot inspect text); fenced code
  gets a header strip with the language and a hover-revealed copy button. Both are
  styled in `globals.css` under `.markdown-body`.
- `workbench/login-screen.tsx` — wordmark, username/password form; maps 429 to "too
  many attempts". `theme/` — theme script. `ui/` — shared primitives: `Button`,
  `Input`/`InputWithIcon`, `Card` (elevation bound to the `--shadow-*` scale),
  `Skeleton`, `EmptyState`, `Badge`/`StatusDot`/`StatusPill`, `Tabs`, `Textarea`,
  `Separator`, `ThemeToggle`, and `Wordmark`/`LogoMark` (shared by the sidebar
  header, the narrow-width top bar and the login screen; `public/favicon.svg` is
  the same drawing simplified for 16px).

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

## Bounded transcript and workspace rendering

When backend page capabilities are present, the workbench uses cursor message
windows (up to 200 retained messages per hot session) and `react-virtuoso`
4.18.13. It keeps at most five hot conversations and applies an estimated 32 MiB
message-content budget. Historical rows, live output and file metadata have
separate subscriptions. Text deltas are coalesced for 50ms; file invalidations
for 100ms. Live text previews retain at most 8,192 characters and live trace lists
retain at most 50 nodes. A completed message supplies the authoritative body.

Reading position uses a message ID and offset, not an absolute scroll height.
Switching back restores that anchor. Search loads a window around its target;
reading old messages does not force a jump to new output. Unmounted rows are not
available to the browser's native find command; application search locates
unloaded history. Large bodies use explicit previews and a modal chunk reader;
copy requests the full body and downloads stream it. Trace pages load only on
expansion, with a separate reader for large nodes.

Directory pages load after transcript readiness (or when the mobile file drawer
mounts). Directories default to collapsed; at most 50 expansions are retained
per cached session. Virtual file rows have fixed 28px height. Each directory page
has previous/next controls; search runs on the server. File metadata uses a 2 MiB shared cache plus a 2 MiB current-view budget;
text cache has 4 MiB and trace cache has 8 MiB. Text reads
revalidate, coalesce concurrent requests, and debounce updates by 300ms. Editing
text remains independent of preview refreshes. Auth changes abort reads and clear
resource caches. File, code and editor implementations load on demand.

Requests merge by message ID and preserve changes that arrived after request
start. An explicit `historyLoaded` flag distinguishes fetched windows from SSE
fragments. A separate HTTP tail watermark avoids skipping gaps when newer SSE
messages were observed. Visible selected conversations and directory pages
reconcile every 15 seconds; disconnected active views use the message fallback
poll. Switching cancels obsolete transcript requests. Public shares use the same
presentational components with their own provider-free, scoped data adapter.

Message and artifact pages are current product paths. The capability response lets
the frontend keep those views aligned with the daemon. The Khora architecture has
one current transcript/file path and one SSE event transport.
