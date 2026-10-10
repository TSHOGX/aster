# Security boundaries

A Project is one Linux execution trust domain. Its Agents and Terminals share
workspace files, processes and readable runtime state under guest root. Logical
Session directories organize history and attribution; they do not isolate hostile
siblings. Separate Projects provide separate sandbox/resource authorization.

Go owns cookie authentication, database account/session revisions, CSRF Origin checks and
WebSocket authorization. The browser edge replaces forwarded identity headers and
preserves Origin. Private API and operator Unix sockets are mode 0600 under
protected directories. Keep them inaccessible to unrelated host users.

For the workbench document, the edge strips any incoming `X-Project-Account-ID`
and supplies only the current normal-session account ID for first-paint appearance.
Anonymous, expired and restricted sessions have no page identity. The marker is
removed from API, asset and public-share requests. It grants no API authority;
all product requests continue to authenticate the session cookie. Identity-bearing
pages are private and non-cacheable.

Account usernames are canonical lowercase labels; immutable random account IDs own
resources. Permanent sessions last seven days. Password replacement, recovery and
sign-out-all invalidate previous cookies. Active SSE and terminal connections
recheck current database identity when a committed security mutation broadcasts
invalidation, with a periodic fallback at least once per second. Single-use temporary
passwords expire after 24 hours and grant only a 15-minute password-setup session.
Restricted sessions cannot access product data. Credential hashes and session
secrets are excluded from identity responses and security audit records.

Project ownership references a canonical account ID in the database. Project
creation, metadata reads and edits check current named group grants in product
services; a guessed foreign parent/child remains not found before action denial.
Owned reduction operations retain their narrowly scoped cleanup authority after
functional grants are removed. Internal ownership-only helpers are distinct from
user authorization and remain available to already-admitted settlement.

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
owner handoff independently of fixed release compatibility.

Frontend Markdown does not enable raw HTML. File responses use restricted preview
headers and the browser uses bounded fetched blobs. All Office formats use an
opaque-origin visual renderer with `sandbox="allow-scripts"`. A cancellable Worker
checks ZIP CRC, actual expansion, duplicate paths, XML entities/depth, coordinates
and pixels before engine import. The static viewer has no application bootstrap,
API credentials or storage access. Its CSP blocks HTTP connections, forms,
nested frames and objects; blob connections support the pinned Excel engine's
internal byte loading. Parent/child messages validate window, origin, protocol
and per-frame token. Public viewer assets expose CORS, never API responses.
Browser iframe execution is not a hard CPU/RSS isolation boundary.
HTML previews
use a separate opaque-origin iframe with an empty `sandbox` and no
same-origin, popup, form or top-navigation permission. A leading CSP allows only
inline styles and embedded image/font data; scripts, network connections, nested
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

Agent enqueue, explicit queue release, steering, process reservation, and command
dispatch check current `agents.run` authority against the Project owner's immutable
account ID. Replays of work-increasing requests also require the current grant.
Ordinary policy revocation rejects pending dispatch with `permission_revoked`;
unsent inputs return to held prompts with a durable `dispatch_error`. Grant
restoration does not release them automatically. Already sent commands retain
journal ingestion and settlement authority. An active owner can still hold,
cancel, or stop owned work after losing functional grants. Background execution
checks account status and policy independently of browser sessions and temporary
password setup.

Terminal start, attachment, keyboard claim, input and resize require
`terminals.use`; Project metadata read does not substitute for it. Each observer
retains its original account/Project scope and incarnation fence. Service observers
and WebSockets recheck on committed identity/policy notifications with a one-second
fallback and close when interactive authority ends. A closed view does not signal
the PTY. Active owners retain explicit termination (SIGTERM/SIGKILL), EOF, deletion,
and remaining-life inspection after functional grant loss; settlement uses internal
incarnation authority. Arbitrary signals are not a reduction-only capability.

File permissions are separate feature controls: list, read, export, upload, edit
and manage. Export additionally requires read. Preview bytes are intentionally
copyable; these grants do not implement DRM or an egress restriction. Browser
upload and editor routes select their own fixed action and reuse the same bounded
I/O and workspace confinement. Current grants apply to paused-volume access,
cached preview acquisition, file-watch events and mutation replay as well as live
helpers. Durable mutation identity includes its action. The workbench retrieves
current capabilities for affordances; cached browser state never grants authority.

Conversation sharing requires `conversations.share` and a selected active account
ID. Self-grants are refused, and explicit reader queries/search cannot substitute
for an owner's denied Project read. Reader lists retain reduction authority for
revocation. Creating or rotating a public frozen share requires
`shares.public.create`; ordinary grant removal does not invalidate published links.
Disabled owner status denies public content access at lookup time.
