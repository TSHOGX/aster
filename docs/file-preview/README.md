# File preview contract

Office files have one visual preview path for all admitted sizes. A document opens
as formatted pages, a workbook as a spreadsheet with sheet tabs and selectable
cells, and a presentation as navigable slides. There is no record reader, server
Office index, or second large-file interface. HTML and CSV keep their independent
static/continuous table representations; source viewing and editing are separate.

## Architecture

Authenticated file read → immutable Blob and digest → cancellable ZIP/XML
inspection Worker → opaque visual iframe → format adapter. The shell owns
selection, progress, cancellation, retry, download, refresh and authority. Adapters
own layout only. They cannot read application storage or fetch project files.

The viewer is built separately with esbuild into a content-addressed static
`/office/<digest>/<format>.html` and one classic script per format. It has no Next routing, hydration,
React application providers or storage initialization. The build runs before Next
in development and production and is included in standalone candidate bundles.
Content hashes include HTML, bootstrap and engine bytes, keeping assets consistent across releases.
The scripts target Safari 16.4 and later and avoid dynamic module loading inside
the opaque iframe. A small bootstrap reports script-load failures immediately
instead of leaving a loading spinner until timeout. Playwright WebKit checks are
engine tests; they do not replace testing the user’s installed macOS Safari.

The parent transfers file bytes once. Messages check the sending window, origin,
protocol version and per-frame random token. The iframe has `sandbox=allow-scripts`
without same-origin authority. Its CSP permits only shipped scripts/styles, inline
layout styles, embedded images/fonts and blob connections needed internally by
the Excel engine. HTTP connections, forms, objects and nested frames are blocked.
Static viewer files are CORS-readable; application APIs are not. No source text,
file paths or library exceptions are logged by the viewer.

The frame receives real layout dimensions while preparing. Loading UI overlays
the preparation area instead of giving it zero height. A replacement preserves
the displayed frame until ready, then transfers position and focus. Revocation
removes protected content. Disposal releases frames, URLs, handlers and resource
reservations. An iframe remains a browser authority/lifecycle boundary, not a
hard CPU or memory sandbox.

## First visual engine set

| Format | Engine | Current behavior |
| --- | --- | --- |
| DOCX | docx-preview 0.4.0 | Formatted pages, paragraphs, tables and embedded images; native browser text selection |
| XLSX | @js-preview/excel 1.7.14 | Read-only canvas spreadsheet, tabs, formatting and native engine selection/copy; some chart/drawing fidelity is unavailable and labeled |
| PPTX | pptx-preview 1.0.7 | One visual slide at a time, direct slide navigation and keyboard arrows |

Engine sources are pinned. Small, hash-verified upstream patches cover known
presentation lifecycle issues and the Excel engine's missing chart drawing
reconciliation. There is no retry with a different parser mode and no rewriting
of the original package to pass admission. Unsupported legacy/encrypted formats
retain an explicit download fallback.

## Budgets and waiting

Office source admission is 100 MiB; other local previews keep 20 MiB. Office ZIP
limits are 4,096 entries, 64 MiB per entry and 256 MiB expanded. XML has a 4 million
node limit and depth 64. Spreadsheet coordinates and populated-cell budgets are
separate; row 2,001 or a sparse distant cell does not by itself reject a workbook.
One million populated cells and a 384 MiB working-set estimate bound admission.
These estimates are not measured browser memory guarantees. Tab reservations are
768 MiB, with at most two overlapping renderers and serialized preparation.

Inspection can take up to 60 seconds, viewer bootstrap 30 seconds, and engine
preparation 180 seconds. Bootstrap and render deadlines are distinct. The shell
shows the actual phase and offers Cancel/Download throughout; it never invents
percentages. Longer waits do not waive CRC, decompression, structure or pixel
checks, and do not imply all files below 100 MiB have faithful rendering.

## Engine evolution

Reviewed reference: [GenOffice](https://github.com/genspark-ai/genoffice), Apache
2.0. Its public sheets implementation combines a native Rust sidecar and Univer;
its document and slide engines also depend on TypeScript packages and layout
infrastructure. It is not a drop-in browser WASM viewer. Importing the entire
Electron suite would add editing, native IPC and application coupling that this
read-only product does not need.

A future Rust/WASM engine can replace an adapter behind the same byte-input,
render, navigation, resize and disposal boundary. It must first demonstrate
layout fidelity, bounded memory, cancellation, font support and browser builds
on the actual corpus. This release does not claim to implement a Rust/WASM layout
engine or server Office-to-PDF conversion.

## Verification

`office-visual.spec.ts` opens real OOXML bytes through the application file link,
Worker admission and static sandbox in a production build. It covers small files,
15,599-paragraph Word tables, 275,544-cell workbooks, 200 slides, chart workbooks,
390px/desktop layouts and revocation. No fake reading-job API is used. Separate
replacement/accessibility tests cover cancellation, refresh, theme and focus.
Use the browser projects in `playwright.office.config.ts` for Chromium, Firefox
and WebKit qualification. Test timings are local observations, not production
p95 guarantees.

Immutable source snapshot endpoints remain available for independent range-read
consumers. The retired `/files/previews` job/window/find/copy endpoints and companion
preview worker are removed. Opening a preview does not resume paused compute or
write source files.
