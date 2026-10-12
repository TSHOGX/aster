# Project workbench

Global pages use destination-specific routes. Search, Inbox, Shared with me, and
Scheduled discard Project, Session, message, draft, and turn-history selection
on navigation, URL parsing, and browser history. Mixed URLs are replaced with
their canonical form. Project workspace preferences restore only Project views;
leaving a Project checks unsaved files before releasing its view and subscriptions.

The account menu opens Settings without replacing the selected Project, Session,
draft or file editor. Its desktop dialog becomes a full-screen mobile panel.
Appearance supports system/light/dark mode and neutral/blue/green/violet accents;
semantic status colors retain their meaning. The system mode follows live OS
changes. Profile and security forms edit the display name, change the password
or sign out all sessions. Closing with typed passwords or an unsaved display name
requires a discard decision. Logout uses the existing unsaved-file guard and
leaves autonomous work running.

Preference/profile writes carry the current server revision. Appearance changes
preview immediately, with pending, saved or unsaved state. A failed or conflicting
save retains the changed fields and offers retry/reapply or revert; unrelated
server changes remain intact. The confirmed preference cache is keyed and bound
to immutable account identity. The Go edge replaces incoming page-identity metadata
with the account verified from the current cookie; the server-rendered page uses
that identity to select the first-paint cache. An expired cookie cannot select a
previous account's cache. Logout clears the active binding; anonymous/public
pages use system/neutral defaults. Local tabs synchronize through storage events,
and a returning visible page refreshes server preferences. Password fields never
enter browser storage.

Received conversation details use `/?view=shared&conversation=<session-id>`.
Selection is reflected in browser history. A grant-specific detail read validates
eligibility before messages or SSE start, independently of list pagination.
Loading, empty list, unavailable detail, and message content are distinct states.
Revocation clears detail content and closes its subscription. Owner access to the
generic conversation message API does not imply received-sharing eligibility.

The workbench in `frontend/components/project` uses Project → Session navigation
and displays one conversation at a time. Other Sessions continue independently.
The workspace heading provides two switches: a bottom terminal dock and a file
preview workspace. Both start closed and subsequently restore per-user/project
preferences. Layout changes never stop a runtime resource. Restored sidebar
expansions are checked against Project authority. A confirmed missing Project is
removed from the saved expansion list and its cached navigation data, so reload
does not repeatedly request it or show a global 404 banner. Absence from a paged
index alone does not remove a preference; transient failures remain visible.

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

Conversation history and Files remain available while compute is paused. The
Files panel also works before a Project's first sandbox starts; uploads create
storage without starting compute. Preview, Source, editing and file management
have the same controls in both states. Browsing never issues a resume request.

Files use a wide preview with a lazy directory tree on the right. Each directory
has independent pagination and refresh. Search matches filenames and relative
paths across the Project, including collapsed folders and later directory pages.
Matching is case-insensitive, normalizes Unicode, and ignores whitespace around
the query. Results show their containing folder and open the normal preview.
Search is debounced and cancelable; clearing it restores the directory view.
It never reads file bodies or starts compute. Each result window holds at most
100 files; Next results/Continue search advance a retained traversal rather than
silently searching only the loaded tree. First results and Refresh restart it.
Incomplete or resource-limited searches are distinguished from a complete search
with no matches. File changes mark the current search for Refresh without clearing its results.
The tree flattens expanded branches and renders visible rows with overscan.
Expansion, keyboard focus and selection remain independent of resident data.
Each directory retains up to five adjacent 100-entry pages within an estimated
8 MiB aggregate budget. Near-edge loading extends the continuous interval; explicit
Load more/Load previous controls provide the same navigation. Closing a branch
cancels its reads and releases its rows. Expired cursors relocate around the visible
filename with a notice. Deep links seek bounded windows around each ancestor and
the selected file; they never replay a directory's pages to locate a name. The
selected file and unsaved editor remain owned when their row leaves the window.
Directory scans are serialized per Project, with a second slot available for
selected file content. Temporary 202/429/503 read responses and captures changed
during a read retry silently with bounded jittered backoff (at most seven attempts).
Collapsing a branch or changing the selection cancels its demand. Persistent
failures retain available content and offer an explicit retry; authorization and
missing-file errors are immediate. Writes are never automatically repeated.

Workspace links reveal ancestor directories; on mobile the tree folds away after
selection. Files support watches, upload, download, mutation, text editing and
bounded Markdown, HTML, CSV/TSV, JSON, code, PNG/JPEG/static GIF, XLSX,
DOCX, PPTX and native browser PDF previews. Browser source/media reads allow up to
20 MiB for ordinary local previews; visual Office admits up to 100 MiB with separate structural limits.
Read-only source uses one continuous CodeMirror viewport with line numbers, find,
jump-to-line and model-backed selection/copy. XML and YAML use lazy grammars for
small files and a line-state index with visible-token rendering above 1 MiB;
the latter retains comment, CDATA, quote and YAML block-scalar context across
line windows. UTF-8 and BOM-marked UTF-16 are decoded strictly. Markdown and JSON
above 256 KiB open as labeled Source because their full-document formatting
is not admitted; HTML keeps its rendered mode across that threshold. Source
eligibility does not depend on the 1 MiB editing limit. Loading states show
file details and transfer progress when available, with cancel/retry/download
actions. Oversized files identify the 20 MB limit separately from content or
format limitations. Other formats (including legacy XLS and SVG) remain
downloadable. DOCX, XLSX and PPTX use a single visual preview architecture for all
admitted sizes. A separately built static sandbox hosts format-specific engines;
there is no self-built reading model or small/large renderer split. Office source
admission is 100 MiB with independent container and working-set limits. See the
[preview contract](file-preview/README.md) for engines, deadlines and limitations.

The file menu offers Edit text only after validating a supported text file
of at most 1 MiB, with no NUL bytes. Binary, unsupported and oversized inputs have
no misleading edit action. Source remains read-only; editing is an explicit mode
with the existing Save/Discard protection for unsaved changes.

See [preview contracts and qualification](file-preview/README.md).

Text/code/Markdown previews up to 256 KiB can reopen from the authenticated memory
cache while fresh bytes are authorized and checked. A validation notice identifies
that retained content; it does not claim the bytes are current. Same-byte replies
preserve the representation. Denied or missing files clear it. File events discard
affected cached entries even when those files are not selected; successful local
saves replace their cached bytes. Larger and rich previews keep their separate
loading, parse and renderer limits. Cached preview bytes are never editor drafts.

Application fullscreen expands the file workspace without browser fullscreen or
losing its editor. Escape restores the layout and focus. Hiding files or changing
Sessions retains edits; switching files/Projects offers save, discard or cancel
for dirty text. File changes do not overwrite an active editor. If an external rename or deletion
makes the selected file unavailable, the editor retains its unsaved text and shows
an explicit unavailable-file notice. Saving does not
claim atomic conflict detection against concurrent writers.

Conversation state lives in an authenticated memory repository. Pages carry
independent content frontiers; bounded deltas update loaded windows and tombstones
remove deleted blocks. Historical pages catch up before joining newer content.
The active window is bounded to sixteen pages and an estimated 8 MiB; edge eviction
retains cursors and the reading anchor. The shared memory store bounds retained
resources to 32 MiB and 256 entries. Only assembled conversation windows, directory
trees and eligible small preview bytes are retained; individual transport replies
are not cached a second time. Drafts and pending command identities remain
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

Scheduled is a list/detail workspace with a content-width breakpoint of 960px.
The two-pane list is clamped to 340–460px; smaller layouts show list or detail.
Task detail/create/edit and title/Project/status filters are explicit URL state,
independent of the chat route. Browser history restores context; list position
is retained in actor-scoped application memory. Reading and editing are distinct.

The server filters and orders the overview before paging. Scheduled owns one
owner-scoped metadata SSE transport, shared across consumers; it never opens
per-Project file watchers or connections. Healthy connections perform no fixed
list polling. Revision hints coalesce before authoritative reads; reconnect gaps
and returning hidden tabs reconcile visible resources. Disconnected visible
views fall back from five seconds to thirty seconds between reads.

Overview and full run history load continuously in fifty-row batches with
keyboard-accessible edge controls. Each retains five adjacent pages (at most
250 rows and five estimated MiB); larger windows render only visible rows with
overscan. Bidirectional opaque cursors reload evicted pages. Catalog changes
invalidate continuation; a bounded anchor read reconciles the resident window
rather than replaying all preceding pages. Transient failures keep readable
content and expose Retry, while confirmed unavailable resources clear it.
Selected full instructions never appear in overview or event replies. Background
reads preserve visible row anchors and do not remount editors. Actor-scoped
assembled windows share the application's bounded authenticated memory store.

Creation defaults visibly to Daily 09:00 in the detected IANA timezone (UTC with
an explanation if detection fails), using a new conversation each run. Daily,
Monday–Friday, weekly, monthly, multiple monthly dates, a minute interval,
manual-only, and advanced five-field Cron are supported. Interval defaults to
15 minutes and accepts whole minutes from 1 to 10,080. Its first run is one period
after creation/enabling; changing the interval restarts that timer. Other edits
and manual runs preserve its cadence. Timezone/DST do not change elapsed minutes.

The custom timezone picker searches IANA identifiers, localized zone names and
current UTC offsets. Its trigger and chevron open the same selectable list.
Repeat lists Interval first, above Daily; monthly day and conversation controls
use the same custom listbox. Time uses a typed 24-hour field and a custom
hour/minute popup with explicit confirmation. No browser select, datalist or
native time popup is used in the Schedule form.

Picker menus support arrow keys, typeahead (or search), Enter, Tab in form order,
Escape and focus restoration. Escape dismisses the inner picker before the form
guard. Popovers track their anchor and fit the visual viewport in either theme.
Timezone search text is separate from the saved value; choosing a zone preserves
the wall-clock time. Current saved IANA aliases remain selectable.

Only conservative rules are recognized visually; name/instruction
edits preserve the original expression exactly. Replacing an advanced rule
requires an explicit choice. Recurrence changes require an up-to-date server
preview using the same parser as execution. Displayed instants use the task's
zone and include each occurrence's offset.

Dirty edits share a guard for close, Escape, task/global selection and browser
navigation; resize does not remount drafts. Instructions are not persisted to
new browser storage. Uncertain creation retains its original identity and payload;
a read reconciles a lost response. Tick-only revision conflicts retry once with
the latest revision. Configuration conflicts preserve drafts and require an
explicit decision before overwriting. Run now retains an uncertain request ID,
stays in details, and links the admitted conversation. Pausing/deleting does not
claim to stop already admitted work.

Conversation choices load only when targeting an existing conversation and retain
at most 100 options plus an authorized selected lookup. History belongs to the
selected task. Details show five recent rows; full history opens an independently
scrolling panel, or a full-height drawer on narrow layouts. Reading older rows
buffers authoritative head changes behind a counted Back to latest control,
without changing scroll position. Escape closes history before navigating away
from the task and returns keyboard focus. Full history uses the same bounded
continuous window as the overview. Scheduled-trigger timestamps are not
actual start times; manual runs have no invented timestamp. Unknown outcomes,
history failures, empty history and paused/manual-only configurations stay distinct.
Search, inbox, conversation grants and frozen public sharing remain supported;
read-only conversation sharing does not grant live Files or PTY access. Browser
auth expiry returns to sign-in and tears down user-scoped state/connections.

The UI supports browser-selected English/Chinese, system/light/dark themes, keyboard
menus/dialogs and resizable separators. Narrow screens use navigation and file
workspaces as drawers/overlays. Branding uses `NEXT_PUBLIC_PROJECT_BRAND_NAME`;
appearance preferences belong to the signed-in account. Next.js only serves pages/assets: the Go
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

Pending prompts live in the bounded FIFO queue and show no transcript bubble or
Worked-for clock. The composer has one Send/Stop primary slot selected by the
active execution run; Enter remains an ordinary queue submission while active.
Held and released rows expose Hold/Release, Steer and Cancel Prompt in stable
slots. A native-accepted Steer waits in the queue until correlated consumption;
a consumed input remains as a synchronization row until the client applies its
transcript anchor. Stop never submits or clears a draft.

Visible response segments use their first correlated native consumption as the
Worked-for start. Queue and native-injection wait do not count. Terminal segment
duration comes from native run-relative elapsed evidence; missing historical
start evidence has no precise duration. Owner view pages include historic
`turn_summaries` independently of the bounded control head, so segment outcome
survives navigation/reload. Intermediate thinking/tools/assistant output folds
only after confirmed successful segment completion. The final assistant message remains visible with copy,
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
Earlier/Later controls page the bounded rail explicitly. Scroll restoration and
programmatic movement never fetch extra index pages.
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

Administration includes independent Groups and Policies pages. Groups expose
revisioned metadata, complete policy bindings, paginated membership editing, and
a deletion preview showing effective grant losses and resource entitlement changes.
Confirmed populated-group deletion sends the preview dependency fingerprint; a
stale preview must be refreshed. No replacement membership is assigned silently.
Policies edit only the closed action registry with unset/allow/deny selectors.

User details include effective access with group/policy provenance and account/role
constraints. Editors retain the original request body after an uncertain response,
replay the durable receipt, and preserve changed fields when explicitly reloading
stale data. Unsaved edits guard navigation across admin screens and the workbench.

Workbench capabilities also gate Project creation/rename/resume/restart, Agent
submission/release/steer, and Terminal start/attachment. Denied Agent submission
retains editable drafts. Terminal capability loss removes the live view while
retaining its owned termination/delete controls. Pause, stop, cancel, delete and
recovery remain available through their separate reduction authority.


## Advanced configuration and shared directories

Account settings lazily loads configuration when Advanced / Developer is opened.
Shared system pickers expose inherited defaults, explicit choices and unavailable saved
selections. Existing projects have a separate network-change action; choosing a
preference does not change them. English and Chinese strings cover these flows.

`account-advanced.tsx` coordinates preferences and project configuration;
`shared-directory-browser.tsx` owns mount-scoped navigation and file operations.
Listings are keyed by project, mount, path and cursor, so a late or failed request
cannot expose stale rows in a different directory. Text editing is limited to
bounded valid UTF-8; other files remain downloadable. Shared uploads explain
replacement, edits require a discard decision, and uncertain file operations
require a refreshed view before a new mutation. This editor does not provide
cross-user merge or compare-and-swap semantics for concurrent file changes.

`web/select-picker.tsx` and `web/picker-popover.ts` provide the shared dropdown
behavior for scheduling, administration and account settings. `web/select.tsx`
adapts form options to the same component while retaining a hidden native control
for form serialization and constraint validation. Visible menus use system
surfaces, compact selection marks, keyboard navigation, typeahead, disabled options
and focus restoration. Popovers stay within the viewport and inside an enclosing
modal's top layer. Checkbox and disclosure controls retain native behavior.

The administration shell lives in the root layout. Route changes preserve account,
appearance and navigation state while updating the selected content; they do not
remount the shell or repeat its initial authentication load. Navigation and action
selection use quiet surface changes without underlines. Responsive spacing and
wrapping keep controls usable at narrow widths. The deterministic browser suite covers saved
overrides, single-option network repair, confirmed-write refresh recovery,
shared-edit discard protection and stale listings.
