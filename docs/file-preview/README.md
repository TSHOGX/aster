# File preview refactor contract

This document is the versioned implementation contract for the file reader and tree. Scale targets in this document are qualification workloads, not claims of current support.

## User task and completion

From a file tree or chat link, a user can open every target format, see meaningful content, read to the end, find, navigate, copy, switch supported modes, and understand incomplete or simplified fidelity. Rendering and memory must scale with the visible window; browser transport and server parsing have separate budgets. Selection and editor drafts remain independent. Paused project reads do not resume compute.

The qualification corpus includes 15,599 Word paragraphs with a 3,000 × 5 table, 200 presentation slides, 22,962 spreadsheet rows and 275,544 cells, a 2.2 MB HTML document with about 130,000 nodes, and 1–5 MB XML/YAML. Tests generate reproducible synthetic files with these structures.

## Reader contract

The reader owns one selection generation and one immutable content identity at a time. A format adapter declares source, edit, rendered, search, and interaction capabilities separately. Each returned window carries the content identity, range, coverage state (`indexing`, `partial`, or `complete`), a reliable count or explicitly labeled estimate, and a continuation token where needed. A late window from another generation or identity is discarded. Cancellation releases a consumer lease; shared work stops when no consumers remain.

Source and editing eligibility are independent. Source is a continuous, read-only view through the supported source budget. Editing retains its own size and permission checks. Format navigation uses semantic positions: line and offset, document block, slide ID, sheet and cell, or PDF page. A refresh keeps the displayed version readable until a new version is ready, and must label stale content. Authentication revocation removes protected content immediately.

The current bounded source adapter holds the complete text model through 20 MiB, while CodeMirror mounts visible lines. XML and YAML above 1 MiB index compact lexer state in a Worker and color visible lines from that state; smaller files load CodeMirror grammars. The index carries multiline XML comments, CDATA, attribute quotes and YAML block scalar indentation. Input is strict UTF-8 or BOM-marked UTF-16. The 256 KiB threshold now applies only to full-document Markdown/JSON formatting. Byte-windowed source above 20 MiB requires the snapshot protocol below and is not yet provided by this adapter.

The presentation adapter treats slide count as navigation metadata. The existing viewer loads a validated package but mounts one slide at a time; 41 and 200 slide synthetic decks have reached their final slides in a production browser build. ZIP entry count, expanded bytes, XML nodes/depth, image pixels, parser time, and session resources remain hard boundaries. Direct slide jumps and Left/Right keys avoid hundreds of Next clicks. Small static GIFs, legal deflate option flags, UTF-16 Office XML, and bounded Word printer metadata are admitted. Animated GIFs receive `unsupported_feature` until frame budgets and animation behavior are specified. This does not establish general 200 slide performance for chart or image heavy decks.

Presentations use an opaque sandbox origin with script permission for the trusted
viewer only. A dedicated CSP blocks connections, forms, objects and child frames.
Static chunks are CORS-readable; product APIs are not. Validated window messages
transfer bytes and bounded position/focus/theme state. The parent never reads the
child DOM, and the child has no parent storage/API authority. Browser process CPU
is still shared; structural/timeout admission is not a hard browser CPU limit.

HTML inspection uses a cancellable parser Worker. Static original layout is admitted through 30,000 elements. Higher node counts produce a clearly labeled simplified representation containing at most 2,000 text blocks of at most 2,000 characters, with explicit partial coverage and complete Source access. The original scripts never run inside the workspace preview. Self-contained script interaction currently uses Download; a resource-limited derived-rendering job is required before an in-app script execution route can be enabled. A browser iframe timeout is not a CPU limit.

### Failure record

`PreviewFailure` has `code`, `stage`, `retryable`, and optional `metric`, `actual`, and `limit`. Codes are `source_too_large`, `container_invalid`, `unsupported_feature`, `content_limit`, `resource_busy`, `parse_timeout`, and `renderer_failed`. Snapshot expiry uses `410 snapshot_expired`; missing and denied Project scopes use the same `404` response to avoid revealing foreign resources. Only bounded codes/counts cross Worker boundaries. UI messages are localized and provide Retry, Source, or Download when the capability exists. Logs never contain file bodies, raw paths, parser text, tokens, or unfiltered exceptions. Structural counts describe the failed condition; a resource reservation is never reported as a file content limit.

## Immutable source snapshots

`GET …/files/snapshots?path=…&max_bytes=…` captures a fresh verified version and returns a scoped lease with `id`, SHA-256 `digest`, `size`, and `expires`. `GET …/files/snapshots/{id}/content` serves complete content or a single HTTP byte range from that inode. Concurrent requests have independent offsets. `DELETE …/files/snapshots/{id}` releases the lease; active ranges retain their own references until completion. Every operation reauthorizes the actor and Project, including cache hits and conditional responses. IDs grant no authority.

Snapshots expire after five minutes, with at most 64 leases, two simultaneous captures, 100 MiB per source, and 256 MiB of retained snapshot bytes. The caller can impose a smaller source limit; active and paused reads reject it at metadata admission before capturing bytes. Concurrent identical opens share capture work; a later refresh always captures again so equal size/mtime cannot hide changed bytes. Canceling the final waiting consumer cancels capture. Opening a snapshot never resumes a paused Project. Completed snapshots release guest/runtime activity, survive pause/resume, and remain one immutable version until release or expiry. Deleted or unauthorized Projects deny access through old handles. Expiry returns `410 snapshot_expired`; source admission returns `413 source_too_large`. Unlinked inodes vanish on final close or process exit, so restart invalidates handles without leaving temporary files.

The direct download endpoint continues to capture fresh bytes for each request. Derived adapters and large-source windows use leases; they must not issue repeated ranges against live content and assume a shared version.

## Derived reading jobs

Large DOCX and XLSX files can be submitted with a snapshot lease to the isolated
Office reading adapter. A job is keyed by actor scope, Project, source digest,
adapter version, and format; concurrent requests reuse one conversion. The worker receives
one immutable source and a private output directory, has no network, Agent,
application database, cookies, or host paths, and is bounded by CPU, address space,
temporary disk, output size, and a 45-second deadline. Admission allows two
simultaneous workers, 16 resident jobs, 64 consumer leases, and 512 MiB aggregate
source/output reservations. Scratch tmpfs is 64 MiB per worker; output files are
128 MiB maximum. Source admission is at most 100 MiB; ZIP expansion is 256 MiB,
entries 4,096, XML nodes 4 million, XML depth 64, populated spreadsheet cells
1 million, and reading records 100,000 per sheet. `GET …/files/previews` returns
an expiring job lease; status reports `indexing`, `ready`, or a structured failure.
Windows contain at most 100 block/row records, cap response bytes at 2 MiB, and
carry source digest, range, total count and coverage. Spreadsheet requests can
select bounded column tiles instead of sending complete rows to the browser;
find and rectangular copy operate on the same index. Copy is bounded to 1,000
records, 256 columns, and 1 MiB. Cancellation or the final lease release kills
the process and removes source, SQLite, and temporary files. Jobs do not resume
paused Projects. The DOCX model preserves block/table-row order, headings, merged
cell metadata, and text; the XLSX model uses sparse streamed rows, sheet visibility,
merges, frozen panes, cached values, and explicit formula text when no cached value
exists. Drawings, macros, external links, recalculation, and layout pagination are
omitted and reported in the manifest. Source/download remains available.

DOCX/XLSX browser DOMs mount at most 28 records and 14 visible spreadsheet columns;
selected values remain readable in a plain textarea. Sheet/outline navigation,
full-index find, direct record jumps, adjacent scrolling, and cross-window copy
do not require the original visual renderer. Reopening restores a digest-scoped
semantic bookmark; same-digest validation preserves subsequent user navigation,
and refresh preserves displayed content until the replacement's
initial window is ready. Public frozen shares use these same readers and check
current share authority on every request. The former docx-preview and Excel DOM/
canvas libraries and retry-on-drawing parser have been removed.

## Format and security decisions

- HTML renders statically by default in an opaque origin. Self-contained interaction requires an explicit user action, with network and application authority blocked. A busy script must leave a recovery route; an iframe timeout alone is not CPU isolation.
- Small Office documents can use existing viewers while independently budgeted adapters provide large document reading, presentation navigation, and sparse spreadsheet tiles. Unsupported drawings cannot make cell data unreadable. A simplified data or reading view labels its fidelity.
- The backend snapshot protocol captures one immutable version, authorizes every operation, reuses that capture across ranges, and never concatenates changed versions. Derived jobs run outside the project Agent with CPU, memory, disk, time, and network limits, reference leases, deduplication, expiry, and cleanup.
- The tree separates expanded paths, focus, selection, directory snapshots, resident pages, and visible rows. Cursor pages are transport units; users see continuous scrolling and progressive search with honest completeness.

## Qualification gates

The required workload is the actual sample structures plus adversarial boundaries around 8 KiB, 256 KiB, 1 MiB, and 20 MiB. Include malformed ZIP, CRC and length mismatches, XML entities and depth, sparse coordinates, long lines, UTF boundaries, multi-line syntax, script loops, rapid switching, revocation, pause, and cancellation. Verify full-content reachability, semantic find and copy, keyboard and screen-reader paths, 390 px and 200% zoom layouts, repeated open/close cleanup, and stable DOM and memory growth. Candidate latency and 50–150 MB targets remain measurements until production build, device, and trial protocol are fixed.

Implementation order: shared diagnostics and public corpus; continuous source and Office admission; HTML modes and tree windows; snapshots and derived jobs; large DOCX/XLSX models; end-to-end pressure and accessibility qualification. Each stage removes the legacy path it replaces and updates tests and documentation in the same commit.

## Delivered behavior and qualification

The delivered reader architecture uses continuous source, static HTML, a
single-slide presentation adapter, isolated DOCX block/table-row indexes, sparse
XLSX tiles, immutable source leases, and a virtual file tree. The old text-section
UI, directory replacement pager, browser Word renderer, and browser spreadsheet
renderer are removed. There is no compatibility switch back to those paths.
Runtime reader protocol 3 is required; rebuild the immutable runtime image when
adopting this release. Candidate bundles include the companion preview worker
and third-party notices. Installation requires `bwrap` and `prlimit`.

The current release contract is a simplified reading representation for Word and
workbooks. It preserves complete body/table text and populated spreadsheet data,
while original page layout, images/drawings, animation, editing, and recalculation
belong to the downloaded original. Sheet manifests retain merge/frozen-pane
metadata; the data viewer does not reproduce original worksheet layout. Nested
Word tables retain their text inside the containing cell. Semantic bookmarks
contain positions/digests, are scoped by authenticated actor, and never retain
file text. HTML scripts execute only outside the workspace through Download.

Qualification performed on 2026-10-09 with the local Linux arm64 Go/Node toolchains,
Next 16.3.5 production build, and Playwright Chromium:

| Check | Result and scope |
| --- | --- |
| Frontend lint/typecheck/build | Passed |
| Vitest | 221 tests passed; one opt-in instrumentation test skipped |
| Production UI suite | 189 passed; 28 explicitly optional performance profiles skipped |
| Final adapter/accessibility checks | 20 passed after the final window and diagnostic changes |
| Backend/source gates | Build, vet, ordinary Go tests, isolated worker test, integration compilation, document/lockfile/shell/candidate checks passed |
| Core race gate | Product, Agent runner, and runtime release passed |
| 50 open/close/navigation/theme cycles | Passed; DOM nodes and listeners stayed at 1,175 and 529 in quiescent snapshots, with zero Office frames and one active text URL |
| Actual Word/workbook sample indexing | Complete 3,599 semantic Word records and 22,962 worksheet rows; final records remain queryable |
| Synthetic corpus | 15,599 Word paragraphs/3,000 table rows; 275,544 worksheet cells; 200 slides; end access/find/copy/mobile tests passed |

The actual Word/workbook samples indexed in roughly 0.38/1.71 seconds in single
local worker trials. These are feasibility observations, not production p95 or
RSS guarantees. Browser UI fixtures exercise real derived models and controlled
API responses; they do not certify a live deployment or production network.
The installed project was not modified or restarted during this refactor.

Candidate source windows beyond 20 MiB, server Office-to-PDF layout conversion,
in-app HTML scripting, and a persistent metadata search index remain separate
product extensions. The current implementation exposes its limits and fidelity
explicitly instead of admitting those workloads through an unbounded legacy
renderer. No supported file is rewritten by preview, derivation, or fallback.
