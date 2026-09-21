# Frontend workbench

The Next.js workbench lives in [frontend/](../frontend). Browser requests stay on
the frontend origin and reach the Go daemon through Node route handlers. See the
[API reference](api.md) for DTOs and authorization rules.

## Development and configuration

The stack is Next.js 16, React 19, strict TypeScript, Tailwind CSS 4 and
`react-virtuoso`. Markdown uses `react-markdown`, GFM and highlighted code without
raw HTML rendering. [package.json](../frontend/package.json) and the lockfile define
exact dependency inputs.

```bash
cd frontend
npm ci
HARNESS_API_BASE_URL=http://127.0.0.1:8090 PORT=8000 npm run dev
```

`dev` uses webpack and listens on `0.0.0.0`. `build` creates the production bundle;
`start` serves it. `lint`, `typecheck`, `test`, `test:e2e` and `test:performance`
provide separate checks. See [verification](verification.md) for fixture and
real-daemon browser commands.

| Setting | Default | Effect |
|---|---|---|
| `HARNESS_API_BASE_URL` | `ORCHESTRATOR_URL`, then `http://127.0.0.1:8090` | Server-side daemon origin |
| `PORT` | Next.js default | Frontend listening port |
| `HARNESS_DEV_ORIGINS` | localhost behavior | Comma-separated development origin allowlist |
| `NEXT_PUBLIC_HARNESS_BRAND_NAME` | `BatteryGPT` | Product wordmark/title and assistant byline |
| `NEXT_PUBLIC_HARNESS_UPLOAD_UI` | off | `1` exposes upload buttons and drop targets |
| `NEXT_PUBLIC_HARNESS_MESSAGE_PAGES` | enabled | `0` selects the existing unpaged transcript/shared-view fallback |
| `NEXT_PUBLIC_HARNESS_ARTIFACT_PAGES` | enabled | `0` selects the existing unpaged artifact-pane fallback |

`NEXT_PUBLIC_*` values are baked into the browser bundle and require a rebuild.
The two page switches and unpaged components still exist in the current code;
removal of the WebSocket transport did not remove these frontend fallbacks. The
normal capable-backend path uses bounded message and artifact pages.

Language follows the first supported browser language: `zh` selects Chinese,
otherwise English. `lib/i18n/messages-en.ts` defines the translation keys and the
Chinese dictionary is type-checked against them. There is no in-app language selector.
Theme, pane widths, reading position and composer drafts use their own UI state;
authentication remains server-side cookie state.

## Proxy and browser boundaries

| Handler | Upstream surface |
|---|---|
| `app/api/[...path]/route.ts` | `/api/*`; `/api/healthz` maps to `/healthz` |
| `app/artifacts/[sessionId]/[...path]/route.ts` | Workspace download/edit requests |
| `app/shared/[token]/files/[...path]/route.ts` | GET/HEAD of frozen public-link files |

[orchestrator-proxy.ts](../frontend/lib/orchestrator-proxy.ts) checks the origin
host on state-changing requests, then strips `origin`, `referer` and hop-by-hop
headers. It forwards cookies and sets forwarded host/protocol headers. Requests
without an Origin header are allowed; when a reverse proxy supplies
`X-Forwarded-Host`, that header must be trusted. See [security](security.md).

Response bodies stream through the proxy, including SSE, with `Cache-Control:
no-store`; upstream connection failures become 503. Non-GET/HEAD request bodies
are currently buffered in Node memory before forwarding, including uploads. The
backend enforces upload limits; streaming responses do not imply streaming uploads.

[next.config.mjs](../frontend/next.config.mjs) defines CSP and other browser headers.
The workbench permits embedding with `frame-ancestors *` and omits X-Frame-Options.
This permits framing and does not prevent clickjacking. HTML/PDF artifact responses
use their own restricted preview headers from the daemon. `/s/` and public file
responses suppress referrers because the URL contains a bearer token. Development
HMR uses WebSockets; product events use SSE only.

## State, creation and turn control

`components/harness-provider.tsx` owns sessions, per-session conversation/runtime
slices, capabilities, connection status, notifications and auth. Selector hooks
allow consumers to subscribe to just the state they render. `lib/api.ts` returns a
`RequestResult<T>` for API calls; `lib/types.ts` defines the product DTOs.

The workbench first calls `/api/me`. After authentication it fetches capabilities
and sessions and connects SSE. Both sidebar creation controls wait for capabilities
and submit `default_mode`, including `shell` on a shell-only deployment. Creation
selects the new session and loads its initial durable history window.

The chat composer supports queued turns and individual release/cancel actions.
Stop aborts the active turn and parks its queue; sending a parked prompt either
steers into the active Pi turn or releases it as a new turn. Shell has Run/Stop
controls and cannot accept Pi steering. Busy and stoppable state are distinct;
queued work alone is not a running turn. Turn admission/control errors are shown
without discarding the user's draft. See [runtime synchronization](runtime-state-synchronization.md).

Session menus provide rename, sharing and retirement. Identity readers appear in
Shared with me and cannot rename, retire, send turns or mutate files. Shared views
have explicit Refresh controls because session SSE is owner-only. The notification
bell is in the expanded sidebar and collapsed rail.

## SSE and reconciliation

`lib/ws.ts` builds `/api/events/stream?view=workbench` and the reconnect cursor;
despite its filename it creates no WebSocket. `lib/use-event-stream.ts` registers
named SSE events explicitly. Durable event IDs support replay; a replay gap or
reconnection triggers HTTP reconciliation. Notification refetch signals have no
durable replay ID.

Runtime HTTP responses and `session.runtime` events share a monotonic revision.
Clients apply strictly newer snapshots. Session-list merging preserves local changes
that happened after a request started. Message-window merging preserves newer SSE
commits, and an HTTP tail watermark prevents an SSE preview from hiding missing
history. `historyLoaded` distinguishes a fetched window from live fragments.

Visible tabs reconcile sessions, the selected runtime and eligible message windows
every 15 seconds, also responding to visibility and network changes. Active views
have a fallback message poll when SSE is disconnected. Directory views reconcile
independently. Switching sessions cancels obsolete transcript reads; auth changes
abort reads and clear resource caches.

## Bounded transcript and workspace views

With page capabilities enabled, `paged-transcript.tsx` and `virtual-transcript.tsx`
retain up to 200 messages per hot session, at most five hot conversations, and an
estimated 32 MiB message-content budget. Live deltas are coalesced for 50ms, text
previews retain at most 8,192 characters and live traces at most 50 nodes. Message,
live-output and file subscriptions are separate.

Reading position is a message ID and offset. Prepending history and switching
sessions preserve it; search loads a window around the target. Browser find cannot
search unmounted rows, so application search is the route to unloaded messages.
Large message bodies use previews and a chunk reader; copy fetches full content,
and downloads stream it. Trace detail loads on expansion.

`paged-artifact-pane.tsx` fetches server directory/search pages, renders virtual
28px rows and keeps at most 50 expanded directories per cached session. File metadata
uses a 2 MiB shared cache plus a 2 MiB current-view budget; text and trace caches
have 4 MiB and 8 MiB budgets. File invalidations coalesce for 100ms; text refreshes
revalidate and debounce for 300ms. Preview/editor modules load on demand.

The workspace provides file/folder uploads when enabled, server-side search,
downloads and previews for text, code, Markdown, images, tables, PDF, HTML and Office
files. Editing requires write access and no running turn; conditional writes detect
stale file versions. Inventory may lag a successful mutation and can require retry.
Mentioned-file reads resolve assistant references against accessible files instead
of treating arbitrary prose as a filesystem path.

## Sharing and schedules

`app/s/[token]/page.tsx` mounts the public shared-session view without
`HarnessProvider`. Its adapter only calls `/api/shared/{token}` routes, using the
link's frozen message boundary, visibility and file whitelist. It reuses
presentational message/file components without authenticated session requests or
SSE. Refresh reloads the same published snapshot; it does not extend its boundary.

`app/schedules/` provides schedule management. Runs are server-owned durable rows,
not browser timers. Cron configuration, manual runs, result routing and owner gates
are described in [API schedules](api.md#selected-requestresponse-shapes). The stored
`cron_agent` flag is exposed by the admin API but the current frontend does not
consume it, and it does not stop queued work in the daemon.

## Source map and checks

| Area | Main sources |
|---|---|
| Product state and APIs | `components/harness-provider.tsx`, `lib/api.ts`, `lib/types.ts` |
| Event and runtime reduction | `lib/use-event-stream.ts`, `lib/session-events.ts`, `lib/harness-events.ts` |
| Transcript and reading position | `components/workbench/paged-transcript.tsx`, `virtual-transcript.tsx`, `lib/message-window.ts` |
| Workspace | `components/workbench/paged-artifact-pane.tsx`, `artifact-viewer.tsx`, `lib/upload-entries.ts` |
| Shared view | `components/shared/paged-shared-session.tsx`, `shared-file-panel.tsx` |
| Proxy and browser headers | `lib/orchestrator-proxy.ts`, `next.config.mjs` |

Vitest checks pure reducers and helpers. Playwright fixtures exercise bounded
rendering, switching, replay races and public views with intercepted responses.
`e2e/daemon-journey.mjs` separately tests a real daemon through the Next proxy;
it is invoked by the Khora harness with `--browser`. See
[recorded evidence](development-evidence.md) for what was actually executed.
