# Security boundaries

A Project is one Linux execution trust domain. Its Agents and Terminals share
workspace files, processes and readable runtime state under guest root. Logical
Session directories organize history and attribution; they do not isolate hostile
siblings. Separate Projects provide separate sandbox/resource authorization.

Go owns cookie authentication, credential fingerprints, CSRF Origin checks and
WebSocket authorization. The browser edge replaces forwarded identity headers and
preserves Origin. Private API and operator Unix sockets are mode 0600 under
protected directories. Keep them inaccessible to unrelated host users.

Every child lookup authorizes its stored Project parent in the same operation.
A child ID alone conveys no permission. Foreign and missing resources use the same
not-found response. Conversation reader grants do not expose live Files or PTYs.
Public shares use expiring, revocable bearer tokens and frozen selected files.
Rotation invalidates the prior token; no public link exposes the live workspace.

With active compute, the Files helper confines operations to selected regular files/directories below
`/workspace`, rejects symlink traversal and special files, bounds transfers and
never automatically repeats an uncertain mutation. The application independently
checks actor scope before starting the helper.

Runtime authority requires the configured release manifest and per-placement
component/native-tree verification. The release includes the pinned public Khora
module and native Pi/Node versions. Build inputs and reviewed content are operator
trust; a digest does not make malicious authored content safe.

Project gateways keep upstream secrets on the host and derive request attribution
from live database authority. The dedicated host UID remains under Khora's
second-hop network floor. Direct reachability is available to every Project
process and must be reviewed as such.

Only the protected owner control socket and the configured host file can reload
direct/DNS defaults. Browser clients cannot supply a policy revision or mutate
engine bindings. Invalid/unsupported edits preserve accepted defaults, and
running placements keep their existing permissions. Conditional paused updates
verify original identity, resource limits, mounts and retained model routes before
admission reopens. Candidate startup checks the accepted policy revision during
owner handoff/rollback independently of fixed release compatibility.

Frontend Markdown does not enable raw HTML. File responses use restricted preview
headers and the browser uses bounded fetched blobs. Office previews are separate
renderers and should be treated as parsing untrusted documents. HTML previews
use a separate opaque-origin iframe with `sandbox="allow-scripts"` and no
same-origin, popup, form or top-navigation permission. A leading CSP allows only
inline scripts/styles and embedded image/font data; network connections, nested
frames, objects and base URLs are blocked. Source and table cells render as text. The default page
CSP permits portal framing; operators should restrict `frame-ancestors` at their
outer edge where embedding is unnecessary. Origin checks do not prevent a user
being tricked into clicking inside an allowed frame.

Use TLS for externally reachable browser/engine endpoints and protect the
frontend origin from untrusted direct access. Do not trust user-supplied forward
headers at an outer proxy without replacing them under a configured trust policy.


Paused Files use the authenticated engine volume boundary, pinned to the saved
Project workspace and engine store identity. The application does not gain host
volume-directory access. Direct reads/writes share the lifecycle execution lane
with resume/deletion and validate path components while compute remains stopped.
Runtime and publication volumes are never accepted from browser-supplied IDs.
