# File preview refactor contract

This document is the versioned implementation contract for the file reader and tree. The private research package at `private/file-preview-research-20261009/` contains the measurements and original deployed samples; it must remain untracked. The observed failures and candidate scale targets in that package are evidence and qualification targets, not claims of current support.

## User task and completion

From a file tree or chat link, a user can open every target format, see meaningful content, read to the end, find, navigate, copy, switch supported modes, and understand incomplete or simplified fidelity. Rendering and memory must scale with the visible window; browser transport and server parsing have separate budgets. Selection and editor drafts remain independent. Paused project reads do not resume compute.

The target corpus includes the equivalent structures of 15,599 Word paragraphs with a 3,000 × 5 table, 200 presentation slides, 22,962 spreadsheet rows and 275,544 cells, a 2.2 MB HTML document with about 130,000 nodes, and 1–5 MB XML/YAML. Tests must generate distributable equivalents. The original captured samples and hashes remain private.

## Reader contract

The reader owns one selection generation and one immutable content identity at a time. A format adapter declares source, edit, rendered, search, and interaction capabilities separately. Each returned window carries the content identity, range, coverage state (`indexing`, `partial`, or `complete`), a reliable count or explicitly labeled estimate, and a continuation token where needed. A late window from another generation or identity is discarded. Cancellation releases a consumer lease; shared work stops when no consumers remain.

Source and editing eligibility are independent. Source is a continuous, read-only view through the supported source budget. Editing retains its own size and permission checks. Format navigation uses semantic positions: line and offset, document block, slide ID, sheet and cell, or PDF page. A refresh keeps the displayed version readable until a new version is ready, and must label stale content. Authentication revocation removes protected content immediately.

The current bounded source adapter holds the complete text model through 20 MiB, while CodeMirror mounts visible lines. XML and YAML above 1 MiB index compact lexer state in a Worker and color visible lines from that state; smaller files load CodeMirror grammars. The index carries multiline XML comments, CDATA, attribute quotes and YAML block scalar indentation. Input is strict UTF-8 or BOM-marked UTF-16. The 256 KiB threshold now applies only to full-document Markdown/JSON formatting. Byte-windowed source above 20 MiB requires the snapshot protocol below and is not yet provided by this adapter.

The presentation adapter treats slide count as navigation metadata. The existing viewer loads a validated package but mounts one slide at a time; 41 and 200 slide synthetic decks, plus the private captured 200 slide package, have reached their final slides in a production browser build. ZIP entry count, expanded bytes, XML nodes/depth, image pixels, parser time, and session resources remain hard boundaries. Direct slide jumps and Left/Right keys avoid hundreds of Next clicks. Small static GIFs, legal deflate option flags, UTF-16 Office XML, and bounded Word printer metadata are admitted. Animated GIFs receive `unsupported_feature` until frame budgets and animation behavior are specified. This does not establish general 200 slide performance for chart or image heavy decks.

HTML inspection uses a cancellable parser Worker. Static original layout is admitted through 30,000 elements. Higher node counts produce a clearly labeled simplified representation containing at most 2,000 text blocks of at most 2,000 characters, with explicit partial coverage and complete Source access. The original scripts never run inside the workspace preview. Self-contained script interaction currently uses Download; a resource-limited derived-rendering job is required before an in-app script execution route can be enabled. A browser iframe timeout is not a CPU limit.

### Failure record

`PreviewFailure` has `code`, `stage`, `retryable`, and optional `metric`, `actual`, and `limit`. Codes are `source_too_large`, `container_invalid`, `unsupported_feature`, `content_limit`, `resource_busy`, `parse_timeout`, and `renderer_failed`. Later snapshot work adds `snapshot_expired` and `permission_denied`. Only these values cross Worker boundaries. UI messages are localized and provide Retry, Source, or Download when the capability exists. Logs never contain file bodies, raw paths, parser text, tokens, or unfiltered exceptions. Structural counts describe the failed condition; a resource reservation is never reported as a file content limit.

## Format and security decisions

- HTML renders statically by default in an opaque origin. Self-contained interaction requires an explicit user action, with network and application authority blocked. A busy script must leave a recovery route; an iframe timeout alone is not CPU isolation.
- Small Office documents can use existing viewers while independently budgeted adapters provide large document reading, presentation navigation, and sparse spreadsheet tiles. Unsupported drawings cannot make cell data unreadable. A simplified data or reading view labels its fidelity.
- The backend snapshot protocol captures one immutable version, authorizes every operation, reuses that capture across ranges, and never concatenates changed versions. Derived jobs run outside the project Agent with CPU, memory, disk, time, and network limits, reference leases, deduplication, expiry, and cleanup.
- The tree separates expanded paths, focus, selection, directory snapshots, resident pages, and visible rows. Cursor pages are transport units; users see continuous scrolling and progressive search with honest completeness.

## Qualification gates

The required workload is the actual sample structures plus adversarial boundaries around 8 KiB, 256 KiB, 1 MiB, and 20 MiB. Include malformed ZIP, CRC and length mismatches, XML entities and depth, sparse coordinates, long lines, UTF boundaries, multi-line syntax, script loops, rapid switching, revocation, pause, and cancellation. Verify full-content reachability, semantic find and copy, keyboard and screen-reader paths, 390 px and 200% zoom layouts, repeated open/close cleanup, and stable DOM and memory growth. Candidate latency and 50–150 MB targets remain measurements until production build, device, and trial protocol are fixed.

Implementation order: shared diagnostics and public corpus; continuous source and Office admission; HTML modes and tree windows; snapshots and derived jobs; large DOCX/XLSX models; end-to-end pressure and accessibility qualification. Each stage removes the legacy path it replaces and updates tests and documentation in the same commit.
