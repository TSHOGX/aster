# Application configuration

`orchestrator serve --config FILE` and `orchestrator gateway --config FILE`
accept the same strict version-1 JSON document.
Unknown fields and trailing documents fail. Relative filesystem paths resolve
against the configuration's directory. Start from
[application.example.json](../config/application.example.json), replace the engine
store identity and release paths/digest, and save deployment values outside the
tracked example. The example denies guest egress until reachability is declared.

| Field | Purpose |
| --- | --- |
| `listen` | Browser edge address; default `127.0.0.1:8080` |
| `frontend` | Workbench Next.js HTTP(S) origin; default `http://127.0.0.1:3000` |
| `admin_frontend` | Independent `/admin` Next.js origin; default `http://127.0.0.1:3001` |
| `state_directory` | Product database, native captures and frozen-share files |
| `application_socket`, `control_socket` | Distinct private Unix sockets; default below state directory |
| `engine` | Explicit endpoint and persistent store ID; HTTPS requires CA and client certificate/key |
| `template` | Engine template ID, immutable application version, exported Go runtime manifest and `sha256:` digest |
| `project` | One-time bootstrap CPU/memory/storage/direct/DNS declarations; operator-owned service routes and hosts |
| `content` | Reviewed Agent configuration/shared-assets source paths and publication capacity |
| `limits` | Global/per-Project capacities, explicit qualified `max_mounts_per_project`, idle deadlines and worker interval |
| `notifications` | Per-source Inbox notification policy; changes apply after application restart |
| `gateways`, `agent_audience` | Independent gateway endpoints and upstream credential environment names |
| `titles` | Optional host-side title model: `base_url`, `model`, `api_key_env` |
| `tls_cert`, `tls_key` | Optional browser TLS certificate/key pair |

First allocation selects the database environment, exact registered sandbox and
built-in references, and effective user CPU/memory/storage requests in the same
transaction as its complete storage reservation. Saved Profiles retain environment
and resource revisions plus the release identity. Callers provide only engine
identity and operator service routes/hosts, never allocation sizes or mount plans.
Runtime binding fields cannot be supplied inside `project`.

Existing Projects retain their exact registered template, capacities and mounts.
Startup checks retained registrations against the current host protocol and engine
store; a different selected default does not replace their identities. Missing or
incompatible metadata fails closed without importing a caller profile. The current
runtime contract still restricts workspace/runtime roots to its declared paths.

`engine.endpoint` permits plain HTTP only on a loopback IP. HTTPS requires
`ca_file`, `client_cert` and `client_key`; ambient proxy configuration is disabled.
When gateways are enabled, the owner and gateway run as separate processes under
Khora's dedicated non-engine `--via-uid`: use non-loopback addresses for the engine TLS edge,
both frontends and model upstream because Khora blocks that UID's loopback connections.
See the [gateway contract](gateway-contract.md).

A service example inside `project.reachability` is:

```json
{
  "services": [{
    "audience": "model",
    "destination": "10.80.0.1",
    "port": 443,
    "gateway": "192.0.2.1:19030"
  }],
  "hosts": { "model.internal": "model" }
}
```

Pair it with a top-level gateway, a reachable frontend origin and mTLS engine:

```json
{
  "agent_audience": "model",
  "gateways": [{
    "audience": "model",
    "listen": "192.0.2.1:19030",
    "upstream": "https://model.example.invalid",
    "api_key_env": "MODEL_API_KEY",
    "max_requests": 8
  }]
}
```

These are documentation addresses, not working deployment endpoints. Direct
rules specify canonical IPv4 `cidr`, positive `port` and `protocol` (`tcp`/`udp`).
Resolvers are explicitly declared IPv4 addresses. No DNS default is invented.

Content publication hashes bounded reviewed source trees and records durable
publication intent in the product database. Successful publication volumes mount
read-only at `/opt/agent-config` and `/opt/shared-assets`. Sources are not watched
for arbitrary live changes. `publish` prepares content through the running owner;
publication registers an immutable version without changing the database default.
Existing Projects keep their original publication IDs. Selecting another environment
is a separate configuration operation; restart is not a default-selection action.

Accounts and hashed credentials live in the product database. There is no authentication
configuration block. Before starting a fresh owner, use the protected offline
`account bootstrap` command described in [operations](operations.md). An empty
account database permits no product login. Sessions reference immutable account IDs
and revocation revisions; password changes and resets revoke earlier sessions.

Bootstrap atomically records the first administrator, `standard-users` membership,
its named permission policy, per-member resource defaults/ceilings, platform
per-user bounds, initial registered release/publication metadata, and the default
environment/network declarations. Repeating bootstrap refuses to overwrite any
of these records. Bootstrap validates local manifest/content inputs before opening
its write transaction and performs no engine allocation inside it.

`template.version` is an explicit immutable version label. Set
`limits.max_mounts_per_project` to a positive bound qualified for the installation;
all owned and system publication mounts count. The application accepts a bound
from 2 to 128; this validation range is not an engine capability claim. The example
uses 8 as an operator declaration that must be qualified on the chosen substrate.

The `notifications` object controls future Inbox writes independently by source:
`agent_run_outcome` defaults to `false`, while `conversation_shared`,
`operator_directed` and `operator_broadcast` default to `true`. These settings are
fixed application configuration and take effect on restart. Disabling a source
does not delete notifications already stored. A disabled operator notification
source makes `orchestrator notify` fail instead of silently discarding the message.

Start the owner before the independent `gateway` command. Only the gateway command
loads the configured service upstream keys and binds service listeners; `serve`
provides authority through its protected control socket. The gateway opens no
product Store or engine connection. Its mode-0600 operator socket is
`STATE_DIRECTORY/gateway.sock`, and `STATE_DIRECTORY/gateway.lock` excludes a
second gateway lifetime, including directory aliases. Never unlink the lock file.
Keep gateway supervision independent of owner/frontend restart policies.

## Frontend display name

The default display name is **Aster**. Set `NEXT_PUBLIC_PROJECT_BRAND_NAME` when
building the frontend to use your organization's name:

```sh
NEXT_PUBLIC_PROJECT_BRAND_NAME='Example Research' npm --prefix frontend run build
```

The same value appears on the sign-in page, desktop and mobile navigation,
public shares, browser title and application metadata. The sign-in and navigation
badges use its first character. Surrounding whitespace is removed; an unset,
empty or whitespace-only value uses the default. The bundled favicon is a static
asset at `frontend/public/favicon.svg` and can be replaced for a deployment.

This is a public **build-time** setting, not a field in the Go application JSON.
Changing it requires rebuilding and deploying the frontend; restarting an existing
build with a different environment value does not change its display name.
For development, set it before `npm --prefix frontend run dev` or in
`frontend/.env.local`. For immutable candidates, pass it to
`python3 scripts/candidate.py build /srv/candidates/new-candidate`; the builder
records the public build input in the candidate manifest and excludes local env files.
The release workflow also accepts the `NEXT_PUBLIC_PROJECT_BRAND_NAME` repository
variable. Never put credentials in public frontend settings.

Product branding does not change package names, API routes, database schemas or
operational identities. Documentation uses `/etc/agent-workbench`,
`/var/lib/agent-workbench` and the example template ID `agent-runtime`.
Existing installations must keep their actual configured paths and template IDs;
these example names do not require moving data or rebinding existing Projects.

## Network defaults and user overrides

Direct/DNS policy is seeded once at account bootstrap and then owned by the
product database. Startup does not rewrite it, and no config watcher or
`network-reload` command remains. Administrators use the revisioned
`GET/PATCH /api/admin/network/default` API. Edits require the current administrator
session, expected configuration revision and request ID; they are audited and
fenced during release transitions. Unknown fields and malformed complete rules
fail validation without changing the accepted policy.

Creation/resume selects the accepted content revision inside its product
transaction. A paused binding is conditionally updated and its complete mount and
network projection verified before admission reopens. Running placements retain
their actual policy; an edit does not stop or rewrite them. Services and host
mappings remain operator-owned. Administration’s Network page edits the default;
each account’s Network settings page chooses inheritance or a complete override.
`GET/PATCH /api/admin/users/{id}/network` exposes the user configuration revision,
current default configuration revision, default policy and desired policy. Saving
requires `expected_revision`, `expected_default_revision`, `request_id` and
`mode` (`inherit` or `override`). Override requires complete `direct` and
`resolvers` arrays; inheritance omits both. Empty override arrays deny direct
connections and DNS. Policies allow at most 256 direct rules and 16 resolvers.

User edits validate against that user’s retained service bindings. Default edits
validate inheriting Projects; existing overrides remain independent. Returning to
inheritance revalidates current default bindings. The editor preserves unsaved
edits on stale reload and resolves an uncertain response using the original
request receipt. A save never performs an implicit apply-now operation.

Resolvers authorize UDP/53. Deployments requiring TCP DNS fallback must declare
an explicit TCP/53 direct rule. Direct permissions are IPv4 CIDR/port/protocol
tuples, not domain or HTTP filters. DNS permission alone does not grant HTTPS
egress. Public downloads also require the deployment's forwarding/NAT/upstream
path; the isolated runtime gate does not qualify that external path. Python
package installation still requires a suitable virtual environment under PEP 668.

Each `gateways` entry optionally sets `schedule_tools` (default false). This
permits the bounded schedule platform handler for that declared audience; it
does not add a service binding to an existing Project. A Project with no eligible
gateway continues to manage schedules through the browser. Enable runtime tools
only after the gateway advertises platform protocol 1 and the measured current
runtime contains the scheduling extension. See the [gateway contract](gateway-contract.md).

## Resource entitlement editing

Administration links each group to its Resource policy and each account to
Resources and usage. Group dimensions are omitted or a complete default/ceiling
pair. User dimensions inherit or carry an explicit requested value; zero is an
explicit zero allowance. Effective values take maximum group grants, platform
bounds and user requests, with source groups and capped requests shown together.
The workbench Settings dialog exposes Resources and usage read-only, including
the user's desired network selection. It provides no self-service quota edit.

The transport uses decimal **strings** for all resource quantities to preserve
integers above JavaScript's safe range. Values range from zero to signed 64-bit
maximum, with existing platform/entitlement bounds enforced independently. Bytes
are bytes, CPU is millis, and counts are integer counts. Fractional units, numeric
JSON quantities and incomplete group entitlement pairs are rejected. Revisions
remain numbers. A null user override clears it; a null group dimension removes
that offer, while omission leaves the field unchanged.

- `GET/PATCH /api/admin/groups/{id}/resource-policy`
- `GET/PATCH /api/admin/users/{id}/resource-settings`
- `POST /api/admin/changes/preview` with `kind`, `target_id`, `expected_revision`
  and `dimensions` or `overrides`; `limit`/`cursor` paginate affected accounts.
- `GET /api/admin/users/{id}/usage` and `GET /api/me/usage`

Edits require preview's `expected_dependencies`, the target revision and a request
ID. Scope is server-derived. Stale dependencies reject save; the editor retains
changed fields for another review/preview. Unknown outcomes use the original
request receipt. Previews distinguish current usage overages, default environment
fit and conflicts in saved Project capacities. Reductions preserve data and
allocations; they block incompatible provisioning/resume instead of resizing
storage. Cleanup and already-admitted settlement remain available. Shared source
artifacts are not charged repeatedly to user-owned storage.

## Default runtime environment API

`GET /api/admin/runtime/default` returns the revisioned release/mount document,
required publication metadata and qualified mount bound. Administrators submit
the complete `sandbox_release` and `mounts` with `expected_revision` to
`POST /api/admin/runtime/default/preview`. Preview reports all affected accounts,
the count that cannot fit, and a bounded `limit`/`cursor` page of exact plans,
resource provenance, usage/overages and saved-plan conflicts. Owned data capacities
and resolved byte quantities are decimal strings. Workspace/runtime capacities
use the corresponding resource setting; they have no environment-level override.

`PATCH /api/admin/runtime/default` adds `request_id` and the preview's
`expected_dependencies`. Save checks current registry, publication, platform and
account-resource configuration under the writer transaction, then writes the new
revision with audit and receipt. Missing/disabled/mismatched artifacts, conflicting
image paths and stale dependencies are refused. A valid plan can exceed some
users' allowances; saving it does not raise entitlements or reserve capacity.
First allocation uses current admission rules. An environment edit never changes
an already-bound Project's release, paths, storage capacities or sources.
