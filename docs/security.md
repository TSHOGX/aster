# Security boundaries

The platform processes untrusted user prompts, generated code and workspace
files. Sandbox isolation, credential mediation, request ownership and host
operations are separate boundaries; success in one does not replace another.

## Sandbox execution

runsc provides the syscall isolation boundary. The orchestrator renders an OCI
spec with read-only rootfs, bounded resources, explicit mounts, namespace/network
policy and an unprivileged sandbox identity. Only the workspace, driver home and
required bridge paths are writable according to their contracts.

Generation manifests and image/driver digests bind runtime inputs. Cleanup must
use the recorded generation identity and validated paths. Never treat workspace
or agent history as disposable compute state.

## Network and credentials

The sandbox reaches the external model proxy through a stable hostname alias.
Real model credentials stay in the host-side gateway; the sandbox's dummy key
alone authorizes nothing. The gateway calls the orchestrator's Unix-socket
correlation API, authenticated with SO_PEERCRED, for active-turn authorization.

Optional data gateways follow an analogous host-side credential model. Alias
validation rejects direct IP literals and provider upstream names. Endpoints and
real credentials are deployment-private inputs, not production constants in
source. The host must enforce the configured egress/gateway network boundary.

Restricted egress permits configured destinations; `allow_all: true` deliberately
removes destination containment. Neither mode makes a shared private KB safe to
publish or prevents authorized tools from reading data their account can access.

The optional title/extraction LLM clients run on the host with their own private
credentials; they are not sandbox model requests and do not reuse turn proxy
correlation.

## Authentication and authorization

Private credential YAML stores PBKDF2-SHA256 hashes and optional user/admin roles.
The registry reloads valid changes; invalid replacements retain prior credentials
and report errors. Managed services require a nonempty valid file. Unmanaged open
mode is restricted to isolated development.

Session APIs enforce owner/shared-reader permissions. Administrator APIs use a
separate middleware subtree. Server-side auth sessions expire and can be revoked.
Login and request rate limits depend on a trustworthy client IP: reverse proxies
must overwrite forwarded headers, and directly exposed services must disable
trust of client-supplied proxy headers.

TLS termination protects browser credentials and Secure cookies. Keep backend
and frontend private behind the intended ingress. Configure CSP and framing
headers consistently with artifact/public-share preview routes.

## Files and public shares

Workspace reads and writes validate ownership, regular-file status, traversal,
symlink boundaries and configured limits. Text-edit concurrency checks prevent
silent overwrites of changed files. Do not weaken these checks to accommodate a
new filesystem layout.

A public share token is a bearer capability. Public links expose only the recorded
snapshot and selected copied files, expire, and can be revoked/rotated. Full
scope can include execution details; review that content before sharing. Public
share access is separate from normal authenticated workspace access.

## Optional memory and external evolution

The memory pipeline aggregates configured trajectories and publishes approved KB
snapshots shared with new generations. Prompt filters do not establish tenant
isolation or guarantee removal of every secret. Input scoping and human review
remain operational responsibilities.

The external evolution route has an explicit local viewer allowlist and requires
private project/state/database configuration. It launches a separately maintained
runner, not a core scheduler job. KB-review routes and admin feature flags are
separate surfaces. See [memory boundaries](agents-kb.md) and verify each handler
rather than assuming a shared authorization policy.

## Deployment and public source

Release artifacts are checked by hash and exact version; publication occurs only
after code and artifact checks. Private credentials never belong in generic
rootfs or release examples. Deployment CLI execution requires trusted local
configuration/hooks and the necessary host privileges.

Local schema catalogs are trusted compatibility assertions. Protect their write
permissions, review schema differences and rehearse backups; adding a fingerprint
does not itself make a database safe.

A public-source review includes code, comments, fixtures, documentation, all Git
refs/history and published artifacts. A clean current checkout does not erase old
objects or hosting-provider caches. Store audit evidence privately and coordinate
history rewriting separately. See [privacy policy](delivery/privacy.md).
