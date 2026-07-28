# System Notifications (Inbox)

> Broadcast system messages (e.g. release notes) to all users, plus notifications
> directed at one user, with per-user durable read state. Backend in
> `orchestrator/internal/store/notifications.go`
> + `orchestrator/internal/server/server.go`; frontend bell in
> `frontend/components/notifications/notification-bell.tsx`. Builds on the
> existing event/SSE and auth infrastructure documented in
> [api.md](./api.md) and [control-plane.md](./control-plane.md).

## Model

Two tables, content and read-state kept separate so one notification isn't
duplicated per user:

| Table | Purpose |
|---|---|
| `notifications` | `id`, `user_id` (nullable audience), `title`, `body`, `created_at` — one row per message |
| `notification_reads` | `(notification_id, user_id)` PK, `read_at` — per-user read marker, `INSERT OR IGNORE` for idempotent "mark read" |

`notifications.user_id` is the **audience**: `NULL` means broadcast (release
notes, system status), non-null means only that user sees it. Rows written before
the column existed read back `NULL`, so the migration needs no backfill and a
rollback keeps working.

**Both** reads of the table must carry the audience predicate
`(user_id IS NULL OR user_id = ?)`. There are exactly two —
`ListNotificationsForUser` and `CountUnreadNotifications` — and they share a
single `notificationAudiencePredicate` constant for that reason: applying it to
one and not the other produces an unread badge that disagrees with the list it
opens, which is a worse bug than either being wrong alone.

Directed notifications are currently emitted when a session is shared with
someone (see [api.md](./api.md) sharing routes). That send is best-effort: the
grant is already committed and is what matters, so a notification failure is
logged rather than failing the request.

The `notification.created` SSE signal is **not** audience-filtered. Its payload is
an opaque `notification_id` and nothing else, and the content is gated by the
audience predicate in the list query, so a user who refetches on someone else's
directed notification simply sees no new row.

## HTTP routes

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/api/notifications` | required | list all notifications, newest first, with `read` flag for the caller |
| POST | `/api/notifications/{id}/read` | required | mark one notification read for the caller (idempotent) |

Both routes reuse `requireAuth`/`userIDFromContext` — no new auth path. There
is **no public create endpoint**; notifications are inserted internally (see
below).

`GET /api/notifications` response:

```json
{"notifications":[
  {"id":"notif_...","title":"...","body":"...","created_at":"...","read":false}
]}
```

## Creating a notification

`Server.CreateNotification(ctx, title, body)` creates a **broadcast**; the
internal `createNotification(ctx, userID, title, body)` takes an audience (`""`
for everyone). Both do two things:

1. Inserts the row via `Store.CreateNotification`.
2. Publishes a `notification.created` event on the existing `events.Hub` with
   no `SessionID` set, so it lands in the hub's global (`""`) subscriber key
   and reaches every connected SSE client for free — no changes needed to
   `eventVisibleTo`/`sessionVisibleTo`.

The event payload only carries `{"notification_id": "..."}` — it's a "go
refetch" signal, not the notification content, and is **not** written to the
durable `events` table. A client that misses it while briefly disconnected
just sees the new notification on its next `GET /api/notifications` (page
load, dropdown open, or reconnect-triggered refresh); it does not replay via
`Last-Event-ID`.

### CLI: `cmd/notify`

```
go run ./cmd/notify -title "v2.3 released" -body "Changelog: ..." \
  -db /var/lib/harness/state/orchestrator.db
```

Writes directly to the orchestrator's SQLite file (WAL mode, safe for a
second short-lived process), independent of whether the orchestrator is
running. Intended for deploy scripts / release automation.

**Trade-off:** since it bypasses the running server process, it does not
trigger the `Hub.Publish` broadcast — connected browser tabs won't get an
instant toast, only the badge/list on their next poll or reload. Acceptable
for deploy-time announcements; not suited for time-sensitive alerts.

## Frontend

- `lib/types.ts` — `ApiNotification { id, title, body, created_at, read }`.
- `lib/api.ts` — `fetchNotifications()`, `markNotificationRead(id)`.
- `harness-provider.tsx` — `notifications` in `HarnessState`, fetched on boot
  and on every `notification.created` SSE event (added to
  `SSE_TYPED_EVENT_TYPES`); `markNotificationRead` optimistically flips the
  local `read` flag before the request resolves. Exposed via
  `useNotifications()`.
- `components/notifications/notification-bell.tsx` — bell icon with an
  unread-count badge and a dropdown panel (click-to-mark-read). No dropdown
  primitive exists elsewhere in this codebase, so open/close-on-outside-click
  is hand-rolled with a `useRef` + `document` `mousedown` listener.
- Mounted in both conversation headers —
  `workbench/conversation.tsx` (agent/chat) and
  `workbench/shell-conversation.tsx` (shell) — since each renders its own
  independent header.

## Verification

- Go: `orchestrator/internal/store/notifications_test.go` (create/list,
  per-user isolation, idempotent mark-read) and
  `orchestrator/internal/server/notifications_test.go` (handler-level
  per-user scoping, mark-read, SSE broadcast on create).
- Manual: run the orchestrator + `npm run dev`, insert a notification with
  `cmd/notify`, confirm the bell badge, dropdown content, mark-read, and that
  the read state survives a page reload (proving server-side persistence, not
  `localStorage`).
