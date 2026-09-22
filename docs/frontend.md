# Project workbench

The workbench in `frontend/components/project` uses Project → Session navigation
and displays one conversation at a time. Other Sessions continue independently.
The workspace heading provides two switches: a bottom terminal dock and a file
preview workspace. Both start closed and subsequently restore per-user/project
preferences. Layout changes never stop a runtime resource.

New Chat keeps a project-scoped draft and creates its Session on first send.
Creation and first-turn request identities survive lost responses and explicit
retries. URL project/session/message targets, browser history, saved drafts and
reading positions remain separate from execution state. Preferences store one selected Session. Drafts are not evicted by layout-cache limits;
when browser storage fails, memory recovery lasts only for that page lifetime.

The terminal dock displays one selected PTY tab, with creation, reopening,
renaming, restart, stop and deletion controls. Closing a tab or hiding the dock
only detaches the browser. Terminal rendering keys include the process
incarnation; reconnect uses bounded replay and reports missing output. Read-only
observers explicitly request available input ownership. Resizing fits the PTY;
colors follow the application theme.

Files use a wide preview with a lazy directory tree on the right. Each directory
has independent pagination and refresh; filtering covers loaded entries only.
Workspace links reveal ancestor directories. Files support watches, upload,
download, mutation, text editing and bounded Markdown, code, PNG/JPEG, XLSX,
DOCX and PPTX previews. Other formats (including PDF, legacy XLS and SVG) remain
downloadable. Unsupported workbook drawings are identified. Office bytes pass
bounded format validation in a cancellable Worker before rendering. Each Office
renderer owns a disposable browser document; the old document stays readable
until its prepared replacement is ready. Cancel, timeout and explicit retry keep
that displayed version. Frame removal releases the renderer's URLs and global
handlers. Presentation controls support keyboard navigation, localized labels,
reading-position restoration, theme updates and Escape back to the workspace.
See [preview budgets and qualification](performance.md#preview-admission-and-validation).

Application fullscreen expands the file workspace without browser fullscreen or
losing its editor. Escape restores the layout and focus. Hiding files or changing
Sessions retains edits; switching files/Projects offers save, discard or cancel
for dirty text. File changes do not overwrite an active editor. Saving does not
claim atomic conflict detection against concurrent writers.

Conversation state lives in an authenticated memory repository. Pages carry
independent content frontiers; bounded deltas update loaded windows and tombstones
remove deleted blocks. Historical pages catch up before joining newer content.
The active window is bounded to sixteen pages and an estimated 8 MiB; edge eviction
retains cursors and the reading anchor. The shared memory store limits inactive
resources to 32 MiB and 256 entries. Drafts and pending command identities remain
independently owned. Oversized answer content is retrieved in chunks for complete
copy/download; a copy operation has a 16 MiB decoded working limit.

One Project resource transport routes conversation, control, index, terminal,
file and lifecycle changes. Healthy clients do no fixed conversation/list/file
polling. Reconnect replays from an accepted cursor; explicit gaps rebuild only
visible resources. Hidden tabs release transports and reconcile on return.
A thirty-second light list fallback runs only while disconnected. Pending display
builds retry bounded head/page reads. Control and projection status are distinct.
The former snapshot/raw-record reducers and HTTP reads are removed.

Workspace paths in prose and inline code open file previews directly; unique
referenced basenames are resolved without changing fenced code or existing links. Recovery and unknown-outcome controls remain visible when needed.
The composer follows output only while the reader is at the bottom and excludes
IME composition from Enter submission. Enter sends and Shift+Enter inserts a
newline (Ctrl/Cmd+Enter also remains supported). Both new and existing conversation
inputs grow from 58px to min(280px, 32dvh), then scroll with independent top/bottom text-transparency fades; there is no
manual resize handle. The fades affect overflowing glyphs only, not the input
surface, and are absent for empty/whitespace-only or non-overflowing values.

Scheduled tasks have an owner-scoped, paginated cross-project overview, backed by
`GET /api/schedules`. Task mutations and run history retain Project-scoped APIs.
Search, inbox, conversation grants and frozen public sharing remain supported;
read-only conversation sharing does not grant live Files or PTY access. Browser
auth expiry returns to sign-in and tears down user-scoped state/connections.

The UI supports browser-selected English/Chinese, light/dark themes, keyboard
menus/dialogs and resizable separators. Narrow screens use navigation and file
workspaces as drawers/overlays. Branding uses `NEXT_PUBLIC_PROJECT_BRAND_NAME`;
theme preference uses `project-theme`. Next.js only serves pages/assets: the Go
edge retains all HTTP/SSE/WebSocket authority, datastore and runtime ownership.

Run `npm run test:ui` for deterministic Chromium interactions and screenshots
(requires installed Playwright Chromium and builds production assets). These
fixtures do not qualify the runtime; the isolated Go/Khora browser suite described
in `dev/README.md` verifies the actual composed application.

Consecutive tool/thinking entries within one turn share an activity summary;
assistant or user prose always separates phases. The summary shows unique tool
names and the call count. Every pending/running call remains visible as its own
single line even when history is collapsed, with a command/path preview and a
slow text-only brightness wave. Thinking uses the latest emitted excerpt.
Reduced-motion and forced-colors preferences disable that effect. Expanding
history reveals a flat, unindented list capped at 360px with internal scrolling.
Each tool opens a bordered parameter/result panel whose output scrolls at 240px.
Partial results update the same open panel; completion stops only that call's
animation, without changing its identity/order. Calls left unfinished when a
turn stops show an unknown outcome instead of continuing to animate.

Each turn displays an elapsed-time header immediately on submission. Owner
view pages include historic `turn_summaries` independently of the bounded live
turn-control page, so duration and completion survive navigation/reload. Timers
use creation time (including queued wait), freeze at recorded completion, and
collapse intermediate thinking/tools/assistant output only after confirmed
successful completion. The final assistant message remains visible with copy,
browser-local like/dislike toggles and the completion time. Failed/interrupted
or unknown work remains inspectable, and message deep links reveal folded
history. There is no generic working notice at the bottom of the transcript.

Project arrow/folder/title form one expand/collapse button with distinct closed
and open folder icons. Project and Session hover/focus fills the entire row,
including the action-menu area. Tool failures remain in expanded tool details
without a separate badge on the activity summary.

A compact turn-navigation rail sits at the transcript's left edge. Each indexed
turn has a gray dash; scrolling updates the darker current-turn marker. Hover or
keyboard focus creates a short width wave centered on that dash and shows a
bounded prompt/final-answer preview. Arrow/Home/End keys move through markers,
Enter jumps, and Escape dismisses previews; reduced motion disables transitions.
The rail scrolls independently and retains at most 300 index entries plus one
current-turn marker. Earlier/later controls reload evicted edges in either
direction; refresh updates that same window instead of accumulating a latest
tail. The current marker remains reachable even outside the retained index. Clicking an off-page marker loads a bounded surrounding conversation window
and immediately positions the user message, without a long scrolling animation.
Already loaded destinations scroll directly without requesting or replacing content.
Scrolling near either edge prepends/appends adjacent pages with deduplication;
prepending preserves the visible message anchor. Live controls refresh without
inserting a disconnected live tail into historical content. The selected
turn restores on reload and Return to latest restores the normal live view.

Desktop turn markers use 12px pitch. Return to latest scrolls an already-live
window or loads the newest window when reading distant history. Historical
window loads and live refreshes reject obsolete responses during navigation.

The transcript has no permanent Earlier/Later/Return-to-latest text buttons.
History edges load through scrolling (including wheel/keyboard at short-window
boundaries), and the floating down-arrow is the single return-to-live control.
Only active page requests display a transient loading label.

Reading position follows explicit user intent. The transcript compensates for
page insertion and layout/font changes, and keeps followers at the bottom when
the viewport or composer changes height. Opening process details pauses following;
Return to latest resumes it. Browser find/print sees only loaded pages and mounted
process history: navigate to a turn/message and expand it first. Complete-answer
copy uses model data rather than the visible excerpt, within the documented copy
budget in [performance](performance.md#conversation-layout-and-reading-intent).

Reading bookmarks persist independently of cached conversation pages. Reloading
or returning after cache eviction reloads the surrounding item and restores the
visible block, including an open process detail. Bookmarks contain position
metadata only. Explicit message links override them; a removed item produces a
notice before showing latest. Pending or denied reads do not erase the bookmark.

Within an unfinished turn, collapsed tool/thinking bodies are also unmounted.
Running call summaries remain visible. Opening a detail mounts its content;
closing the group releases those bodies while retaining row state and detail
scroll. Truncated details provide a full-content download from their immutable
version only when explicitly requested.

Rich Markdown has a separate 8 KiB and syntax-density admission budget. Larger or
unusually dense content is shown as complete plain text within the existing
preview/excerpt bounds, with a notice; markup and links remain literal. Selection,
original-file download and model-based answer copying stay available. See
[Markdown qualification](performance.md#bounded-markdown-rendering) for the limits
and the measured large-file layout cost.
