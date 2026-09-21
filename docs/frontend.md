# Project workbench

The only workbench is `frontend/components/project`. It selects a Project,
composes up to four visible Agent views and retained Terminal views, and keeps
Project Files mounted as views open or close. Closing a view does not retire a
resource. Terminal rendering keys include the process incarnation.

Agent snapshots, event cursors, recent tool traces and turn/history pages have
count and byte bounds. Reconnect begins from an authorized snapshot; an explicit
gap resets a renderer rather than silently losing output. Large messages and
files remain downloadable when bounded previews cannot display them.

Files support directory navigation, watch-driven rescans, uploads/downloads,
mutations, text editing and previews. CodeMirror edits text up to the configured
one-Mi-character editor limit. Markdown, code, images, PDF, spreadsheets, Word and
PowerPoint have dedicated preview paths. Preview buffers remain bounded and
object URLs/renderers are disposed when the selected file changes. For workbook
drawing relationships unsupported by the parser, cell data uses spreadsheet
conversion and the interface identifies drawings that require downloading the
original. Binary documents are not offered to the text editor.

Schedules, shared conversation grants, frozen public shares, search and the
notification inbox use the same Project-scoped API. Native tools and history are
visible alongside user and assistant messages. Browser auth expiry returns the
user to sign-in rather than leaving stale mutation controls active.

The interface resolves English or Chinese from the browser's top language
preference. The catalog is scoped to current workbench copy and checked for key
and placeholder parity. `NEXT_PUBLIC_PROJECT_BRAND_NAME` selects build-time
branding. Theme preference is stored under `project-theme`; semantic CSS tokens
serve both light and dark appearances.

Next.js serves pages and assets. It has no product datastore, runtime worker,
Node API-forwarding routes or browser-to-engine authority. The Go edge carries
HTTP, SSE and WebSocket on one origin. Development-only allowed hosts come from
`PROJECT_DEV_ORIGINS`.
