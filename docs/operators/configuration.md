# Configure the installation

[Documentation](../README.md) · Deployment and operations

`orchestrator serve --config FILE` and `orchestrator gateway --config FILE` accept
the same version-1 JSON configuration. Unknown fields and trailing JSON documents
are rejected. Relative paths resolve against the configuration file's directory.
Start with [the example](../../config/application.example.json); store actual
configuration under `private/` or outside the repository.

## Host configuration

| Field | Purpose |
| --- | --- |
| `version` | Required configuration format: `1` |
| `listen` | Browser edge address; default `127.0.0.1:8080` |
| `frontend` | Workbench Next.js HTTP(S) origin; default `http://127.0.0.1:3000` |
| `admin_frontend` | Independent `/admin` Next.js origin; default `http://127.0.0.1:3001` |
| `state_directory` | Product database, native captures and frozen-share files |
| `application_socket`, `control_socket` | Distinct private Unix sockets; default below state directory |
| `engine` | Explicit endpoint and persistent store ID; HTTPS requires CA and client certificate/key |
| `template` | Bootstrap-only ID/version, trusted runtime manifest and exact digest; optional for serving |
| `work_template` | Work sandbox ID/version, manifest and digest; include it when bootstrapping the two built-in environments |
| `public_dns_resolvers` | Bootstrap-only resolver IPs for Public; independent of Project service routes |
| `project` | One-time bootstrap CPU/memory/storage declarations; operator-owned service routes and hosts |
| `content` | Explicit system-publication source paths and capacity; optional for serving |
| `limits` | Global/per-Project capacities, bootstrap qualified `max_mounts_per_project`, idle deadlines and worker interval |
| `http_metrics` | Enable optional HTTP metrics collection |
| `volume_imports` | Trusted Studio sources and transfer/engine storage budgets; see below |
| `evaluation_space` | Optional private participant endpoint and credential environment variable; see below |
| `notifications` | Per-source Inbox notification policy; changes apply after application restart |
| `gateways`, `agent_audience` | Independent gateway endpoints and upstream credential environment names |
| `titles` | Optional host-side title model: `base_url`, `model`, `api_key_env` |
| `tls_cert`, `tls_key` | Optional browser TLS certificate/key pair |
| `front_door` | Optional independent TLS front door. `public_origin` is the exact HTTPS origin and `peer_ip` is the only trusted proxy peer; forwarded identity/client-address headers from other peers are rejected. |
| `identity` | Optional shared-identity adapter: HTTPS `issuer`, private loopback `listen`, and registered `clients` with `id`, exact application `callback`, and one or two `secret_envs` for credential rotation. Uses central accounts with scoped Studio access; no additional account store. |

The example is a bootstrap starting point, not a working production configuration.
Set the exact engine store ID and trusted runtime manifest digest. Template,
initial resource and content declarations seed a new database; later serving uses
the registered environment and Project settings. Startup does not overwrite them.
Bootstrap seeds **Restricted** with no direct/DNS access as the standard-user
default. **Public** permits public IPv4 TCP/80 and TCP/443 using
`public_dns_resolvers`, and requires a separate policy grant. Project
`reachability.direct` and `reachability.resolvers` do not seed network templates;
configure custom templates in Resource Center. Service routes and host mappings
remain deployment responsibilities.

Use [Resource Center](../admin/resources.md) for template and account changes.

`max_mounts_per_project` must be qualified for your installation. Valid declarations
range from 2 to 128 and count owned and system-publication mounts; the example's
8 is not an engine capacity guarantee. Existing Projects retain their saved paths,
capacities and runtime references when defaults change.

`engine.endpoint` accepts plain HTTP only on a loopback IP. HTTPS needs `ca_file`,
`client_cert` and `client_key`. Ambient proxy settings are not used. Accounts and
hashed passwords reside in the application database; there is no password-file
configuration block. See [installation](installation.md) for bootstrap.

## Model service gateways

Run owner and gateway as separate processes under the same dedicated non-engine
UID, configured as Khora's `--via-uid`. That UID cannot use loopback, link-local or
sibling-sandbox destinations. Use a reachable non-loopback engine mTLS endpoint,
frontend origins and model upstream. Do not weaken the engine's network restrictions
to make an unsuitable upstream reachable. Private control APIs use Unix sockets.

For example, the following is the reachability fragment inside `project`:

```json
{
  "reachability": {
    "services": [{
      "audience": "model",
      "destination": "10.80.0.1",
      "port": 443,
      "gateway": "192.0.2.1:19030"
    }],
    "hosts": {"model.internal": "model"}
  }
}
```

Pair it with top-level settings such as:

```json
{
  "agent_audience": "model",
  "gateways": [{
    "audience": "model",
    "listen": "192.0.2.1:19030",
    "upstream": "https://model.example.invalid",
    "api_key_env": "MODEL_API_KEY",
    "max_requests": 8,
    "schedule_tools": false
  }]
}
```

These are fragments, not complete configuration files. Documentation addresses
must be replaced with reviewed installation values. Store the upstream API key
in the gateway environment under the referenced name. Only the gateway loads it;
do not place provider credentials in guest files, public frontend variables or JSON examples.

Start the owner before the gateway. Keep the gateway independently supervised
across owner/frontend restarts. Its protected socket defaults to
`STATE_DIRECTORY/gateway.sock`; never delete its lifetime lock to force startup.
Model streams are not automatically resubmitted after a disconnect.

The [Agent configuration example](../../config/agent-config.example/README.md)
uses a non-secret `project-service-token` placeholder. The runtime supplies a
scoped service grant. All processes in a Project share a trust domain; put
mutually untrusted workloads in different Projects.

## Scheduling tools

Set `schedule_tools: true` only on the intended Agent gateway audience, with a
compatible gateway and runtime containing the scheduling extension. Existing
Projects do not acquire new service bindings merely because host configuration
changed. The Scheduled browser page remains available without native scheduling tools.

The gateway reserves `/platform/` locally, including when tools are disabled; do
not configure a provider that requires that path to be forwarded. Install gateway
changes through the [quiescent update procedure](upgrades.md#replace-the-gateway).

## Notifications

| Setting | Default | Effect |
| --- | --- | --- |
| `agent_run_outcome` | `false` | Agent outcome notices |
| `conversation_shared` | `true` | Conversation-sharing notices |
| `operator_directed` | `true` | Notices addressed to one account |
| `operator_broadcast` | `true` | Notices addressed to all accounts |

These fields live under `notifications`. Changes take effect after application
restart and apply to future notices; existing Inbox entries remain. Sending an
operator notice while its source is disabled fails explicitly. Scheduled-task
notification choices are managed on the task itself.

## Frontend display name

Set the public build-time input `NEXT_PUBLIC_PROJECT_BRAND_NAME` when building:

```sh
NEXT_PUBLIC_PROJECT_BRAND_NAME='Example Research' \
  python3 scripts/candidate.py build /srv/candidates/aster-branded
```

Unset, empty or whitespace-only values use **Aster**. The name appears in sign-in,
navigation, public shares and page metadata. Changing a running service's environment
does not change an already built frontend; rebuild and deploy it. The favicon is
`frontend/public/favicon.svg` and can be replaced in the reviewed source before building.
Never use a public build variable for credentials. Branding does not rename
configuration paths, runtime IDs, protocols or stored Projects.

## Browser TLS and shared identity

For direct browser TLS, configure the certificate/key pair. Behind the supported
front door, configure its exact `front_door.public_origin` and trusted `peer_ip`.
Restrict access so only the proxy reaches private business listeners. Replace
untrusted forwarded headers at the edge; do not trust arbitrary browser-supplied identity.

Studio uses central Aster accounts. Configure `identity` with an HTTPS issuer,
private loopback listener and exact callback/client registration. The complete
[Studio deployment guide](../../volumes-studio/deploy/README.md) covers startup,
secret rotation and private distribution. Studio does not maintain a second password store.

## Trusted Studio sources

Only configured `volume_imports.sources` can supply portable versions. Pin a stable
publisher ID, private endpoint, service-client ID, credential environment reference
and allowed libraries:

```json
{
  "volume_imports": {
    "transfer_bytes": "4294967296",
    "engine_bytes": "8589934592",
    "sources": [{
      "id": "studio",
      "publisher_id": "deployment-publisher",
      "endpoint": "http://127.0.0.1:8084",
      "client_id": "aster",
      "secret_env": "STUDIO_DISTRIBUTION_SECRET",
      "libraries": ["*"]
    }]
  }
}
```

Use explicit library IDs when access should be restricted. Private loopback HTTP
is allowed; remote sources require verified HTTPS. In a dedicated gateway-UID
topology, provide an endpoint that UID can actually reach, for example through a
reviewed TLS tunnel. Redirects and ambient proxies cannot forward source credentials.

`transfer_bytes` bounds transfer reservations; `engine_bytes` bounds aggregate
Aster declared engine storage at import/retry admission. Staging, derived copies
and uncertain attempts can each retain capacity. Project allocation quotas still
apply independently. Removing a source or rotating credentials stops future source
access; it does not delete ready local copies or recall acquired bytes.

## Evaluation Space participant

Set `evaluation_space.endpoint` to the private Evaluation Space service origin and
`evaluation_space.credential_env` to the name of an environment variable containing
its central-service credential (at least 32 characters). The credential must match
Evaluation Space's `EVALUATION_SPACE_CENTRAL_SECRET`. The origin requires HTTPS,
except for numeric loopback HTTP addresses. Redirects and environment proxies are
not used for this transport. Keep the private endpoint inaccessible to browsers.

With this configuration, a dedicated worker reconciles pending Evaluation Space
projections from the central database. It survives browser logout and application
restart, verifies exact operation receipts, and retries uncertain transport outcomes
by looking up participant state. It does not settle activation without Studio's
receipt. Omitting this configuration disables automatic Evaluation Space dispatch;
it does not erase pending operations.

Studio can expose its evolution participant API on a separate private loopback
listener using `--participant-listen 127.0.0.1:8085`. Set
`STUDIO_CENTRAL_SECRET` to a service credential of at least 32 characters, or select
a different environment variable with `--participant-secret-env`. This credential
is separate from Studio's registered identity-client credential. The listener is
disabled when `--participant-listen` is omitted and is never mounted on the browser
gateway.

Each participant request authenticates central service credentials, resolves the
saved operation actor through the private central identity endpoint, and rechecks
library configure authority. Central identity outages prevent new participant
effects. The listener supports reservation lookup, reserve, adopt, and cancellation;
configure central dispatch with `studio_participant.endpoint` and
`studio_participant.credential_env`. The credential must match Studio’s central
participant secret. New activation requires `evaluation_space` configuration as well. The
worker reserves Studio outputs before Evaluation Space adoption, then adopts Studio
reservations and settles only after both exact receipts are recorded. Pending
Studio discard cleanup can run with only `studio_participant` configured.

Register Evaluation Space in central `identity.clients` with ID `evaluation-space`
and the exact callback `${identity.issuer}/evaluation-space/auth/callback`.
Studio's callback remains `${identity.issuer}/volumes-studio/auth/callback`.
Only `studio` and `evaluation-space` are supported identity applications. They cannot
exchange callback paths, grants or entitlements. Evaluation Space admission requires
platform administration or current group membership with explicit `config.read` allow.
Studio admission separately requires administrator status or an effective library-read
grant. Admission confers no source evidence, execution or content authority. Both
code exchange and introspection recheck current application eligibility; membership
loss or denial can revoke entrance. Logout through either application's own grant
revokes the originating central login and its derived grants, including after that
application's content access has been revoked. Each registration references its own
identity credential environment variable; participant credentials are separate.

Evaluation Space can run a separate browser gateway alongside its private worker
listener. Set `--browser-listen 127.0.0.1:18485`, `--public-origin` to the registered
HTTPS issuer, and `--frontend` to its frontend origin together. Plain HTTP frontend
origins must use a literal loopback address. Keep this listener behind the shared
TLS front door and route only `/evaluation-space/` browser traffic to it. Never
route public traffic to the private worker listener. The browser gateway provides
login, callback, logout, `/evaluation-space/api/v1/session`, and readable groups at
`/evaluation-space/api/v1/groups` and `/evaluation-space/api/v1/groups/{group}`.
Group lists filter current grants and membership before pagination. They expose
configuration revision/status and separate management, execution and evidence
permissions. Source evidence still requires an explicit grant for administrators.
It does not expose the private catalog or worker APIs.
Workflow editor routes under `/evaluation-space/groups/{group}/workflows/{workflow}`
use the same client-bound central grant. Draft saves validate executable graph JSON
and a separate layout object with independent revisions; conflicts retain the
current draft until the editor reloads. Publication records an immutable workflow
revision and does not activate it for a group. `workflow.edit` is explicit and
separate from `config.manage`, `run.start` and `source.read`.
The workbench also exposes keyboard node editing, typed connection controls,
ordered node inspection, undo/redo and bounded account-local recovery. Publishing
offers an explicit handoff to group configuration for activation; it does not
silently change the active revision. Run detail links use
`/evaluation-space/groups/{group}/runs/{run}`; the browser API returns only the
current group's frozen run metadata, deduplicated event cursor and authorized
attempt records, including effective prompts and node inputs/outputs when the
current `source.read` grant permits them. Pause, resume and cancel are
compare-and-swap controls and retain `run.start` authority. Evaluation reports use
`/evaluation-space/groups/{group}/evaluations/{job}` and expose the frozen subject,
observed metrics, coverage and outcome; dataset bytes and evaluator credentials
remain private. A report can be canceled while it is queued or running.
Frontend credentials and cookies are stripped before proxying. Application sessions
persist in Evaluation Space's state database; Studio owns a separate session store
and cookie path. Central identity outages reject session authorization immediately.

Build the frontend with `npm ci --prefix evaluation-space/frontend` followed by
`npm --prefix evaluation-space/frontend run build`. Supervise it independently with
`npm --prefix evaluation-space/frontend run start -- --hostname 127.0.0.1 --port 3003`
and set the browser gateway's `--frontend http://127.0.0.1:3003`. The frontend uses
`/evaluation-space` as its fixed base path. Add
`-evaluation-space http://127.0.0.1:18485` to the shared `front-door` command.
An unconfigured Evaluation Space route returns unavailable rather than falling
through to the central application. Browser cookies remain confined to their
application path; an Evaluation Space outage leaves the other application routes
available.

### Provision Evaluation Space catalog revisions

Stop Evaluation Space before offline catalog publication. Build the provisioning
command from `evaluation-space/backend` with
`go build -o evaluation-catalog ./cmd/evaluation-catalog`, then run
`./evaluation-catalog --state-directory /absolute/evaluation-state < publication.json`.
The command refuses access while the service owns that database. Restart the service
after publication.

A publication contains `kind` (`workflow`, `service`, `template`, or `profile`),
`ref` (`id`, positive `revision`, `digest`), and the typed definition in `body`.
Publish nested workflows before their parents and workflows before templates.
Revisions start at one and increase sequentially; an identical publication can be
retried. Existing revisions cannot be replaced or deleted.

Workflow digests hash the shared contract's canonical workflow JSON. Service,
template, and profile digests use `evolution.DefinitionDigest`, which clears only
the definition's own `ref.digest` when hashing. Their envelope reference must match
the body reference. Definitions contain credential references, never secret values.
Projection adoption rejects missing or incompatible catalog revisions, including
unsupported workflow capabilities and incompatible output profiles. An explicit validation
rejection records a failed central operation with code `catalog_rejected`. Correct
the catalog before explicitly retrying that operation; automatic retries stop after
this rejection. Transport outages retain pending state for reconciliation.

Parameter schemas must be self-contained JSON Schema documents. Local `$ref`
references are supported; remote URLs and filesystem references are rejected.
Schemas without `$schema` use draft 2020-12. Binding parameters must satisfy both
the selected template and workflow schemas before projection adoption. Required
fields, types, bounds, enums, and additional-property rules are enforced without
coercing values or supplying defaults.

To change a service's admission state while Evaluation Space is stopped, run
`./evaluation-catalog --state-directory /absolute/evaluation-state --action service-state`
with a JSON object on stdin containing `request_id`, `service_id`,
`expected_revision`, and `enabled`. The admission-state revision starts at one
when the first service definition is published. Successful changes return its next
revision. Reuse the original request ID and body after an uncertain response.
Disabling prevents new configuration validation from selecting any revision of that
service; immutable definitions and previously adopted projection receipts remain
available as provenance. Publishing another definition does not reset admission state.

Inspect retained catalog reference metadata while the service is stopped with
`./evaluation-catalog --state-directory /absolute/evaluation-state --action list`.
Provide `{"kind":"service","after_id":"","after_revision":0,"limit":50}` on stdin.
Supported kinds match publication. Continue using returned `next_id` and
`next_revision` when `has_more` is true; limits range from 1 to 100. Results include
all retained immutable references, including disabled service definitions, and do
not imply current admission eligibility. Connection and credential details are not
included in this listing.

Before changing service state, inspect its current admission revision with
`./evaluation-catalog --state-directory /absolute/evaluation-state --action inspect-service`
and `{"service_id":"SERVICE_ID"}` on stdin. The result contains `service_id`,
`revision`, and `enabled`. Use that revision as `expected_revision` for the mutation;
it is independent of immutable catalog definition revisions. Inspection follows
the same stopped-service ownership requirement as other offline catalog commands.

### Online Evaluation Space catalog access

When the Evaluation Space service is running, its private API serves catalog metadata
and exact definitions under `/evaluation-space/api/v1/catalog/{kind}`. Callers authenticate with HTTP Basic user `central` and the configured
`EVALUATION_SPACE_CENTRAL_SECRET`. Evaluation Space then uses its separate registered
`evaluation-space` identity credential to check central catalog authority for the
requested kind/action and, for payload reads, the exact immutable reference. Browser Cookie and Origin headers are rejected; these routes are private
service/API boundaries, not browser endpoints.

Metadata listing uses `limit`, `after_id`, and `after_revision` cursors. Definition
reads use `/catalog/{kind}/{id}/{revision}?digest=...`; the digest is mandatory and
must match the immutable stored definition. A mismatch returns a conflict. The
metadata response omits connection and credential fields; definition payload access
requires the `read` catalog authority decision. This privileged central-service API
returns the complete definition, including connection and credential *references*
(no credential values). It is not a user-facing redacted catalog API. Offline provisioning remains available when the service
is stopped.

### Run assigned evaluators

Evaluation Space can run registered HTTP evaluators with
`-evaluation-connections /absolute/path/evaluators.json`. The file contains connection
IDs used by immutable evaluator service definitions, origins, client IDs and secret
environment references:

```json
{
  "connections": [
    {
      "id": "team-evaluator",
      "endpoint": "https://evaluator.example.com",
      "client_id": "evaluation-space",
      "secret_env": "TEAM_EVALUATOR_SECRET",
      "input_secret_env": "TEAM_EVALUATOR_INPUT_SECRET"
    }
  ]
}
```

Set each outbound secret to a credential accepted by that evaluator. Set a separate
input secret that the evaluator uses to authenticate as its connection ID when
reading assigned datasets and candidate/reference files. Remote
origins require HTTPS; loopback HTTP is allowed. Redirects are refused. Connections
cannot be supplied by workflow inputs. The provider implements the versioned
`/evaluation/v1/jobs/{id}` submit/status/result/cancel protocol and retains request
identity across retries. Uncertain provider responses retain their spending hold.

Also set `EVALUATION_SPACE_STUDIO_SECRET` (or select its variable with
`-studio-secret-env`) for private Studio-to-Evaluation Space requests. This differs
from the central participant credential and outbound central identity credential.
The evaluation endpoints are available only when evaluator connections are configured;
keep the listener private. A service credential alone does not grant dataset use:
central authorization binds the exact prepared check, requester, dataset, evaluator
revision and budget. Grant revocation stops new execution while cleanup can reconcile
previous spending. Required checks remain pending until Studio receives matching
results; configuring a provider alone does not create a passing merge check.

Connect Studio to the same private Evaluation Space origin with
`-evaluation-endpoint` and provide its credential through
`EVALUATION_SPACE_STUDIO_SECRET` (or `-evaluation-secret-env`). Studio journals a
reviewer's request before central authorization and remote admission. Restart/retry
retains the same job and budget reservation. Result refresh verifies subject and
profile measurements before recording immutable evidence; approval and merge recheck
current central grant and evaluator availability.

Set Evaluation Space's `-studio-input-endpoint` to Studio's private participant origin
and provide `STUDIO_EVALUATION_INPUT_SECRET` (or select it with
`-studio-input-secret-env`). Studio uses the same inbound value, selected with
`-evaluation-input-secret-env`; its participant listener must be enabled. These
credentials differ from provider input credentials and central identity credentials.
Provider input reads require the job grant and original submission fence, and stop
on cancellation, expiry, completion or revocation.

To provision an explicit dataset while Evaluation Space is stopped, run
`evaluation-catalog --state-directory /absolute/state --action seal-artifact --artifact-file /absolute/dataset.json`
with the complete artifact reference JSON on stdin. The command verifies size and
SHA-256 digest, retains the exact reference, and refuses a live store or symlink
source. Provisioning bytes does not grant their use; authorize the exact artifact
and evaluator through the group's dataset grant before requesting an evaluation.

### Provision example goal definitions

While Evaluation Space is stopped, `evaluation-catalog --state-directory
/absolute/state --action examples` reads `{"model":{"id":"...","revision":1,
"digest":"sha256:..."}}` from stdin and publishes the skills and corrections
example packages. Choose an exact registered model revision; provisioning the same
package again is idempotent, while substituting a different model under an existing
workflow revision is rejected. The command outputs the exact template/workflow
references and port schema documents for inspection and registry configuration.

The skills graph drafts proposals, critiques/revises at most twice, and builds
relative skill files. Corrections use a separate extract/verify/build graph. Both
preserve unsupported proposals as held evidence and reject unsafe output names;
the skill builder also rejects a critic that changes the original corpus. These
packages do not grant source access, activate a group, or write a Volume. Running
them requires the native model/implementation registry and authorized run admission.
Their suggested output prefixes are `skills` and `lessons`; final publication still
requires Studio review and the configured checks.

Studio's private participant listener also accepts generation result lookup/submission
when its Evaluation Space connection is configured. It authenticates Evaluation Space
with `STUDIO_EVALUATION_INPUT_SECRET`, then asks central identity to verify the exact
successful run fence, base, output reservation and bound patch digest. Studio fetches
only declared patch artifacts from Evaluation Space using its separate Studio service
credential; the receiver verifies current central scope and immutable handoff membership.
Neither listener accepts browser cookies or origins. A service credential by itself
cannot authorize a generated branch. Result acceptance creates an isolated review
branch and does not advance main or publish a version.

### Configure native generation

Evaluation Space accepts `-native-config /absolute/path/native.json` to enable the
native workflow worker and private generation admission API. Configure exact model
revisions, authenticated model-service origins, credential environment references,
and per-call token/cost ceilings. Set `example_builders` to true to register the
versioned skills/corrections builders and their schemas:

```json
{
  "models": [
    {
      "reference": {"id": "team-model", "revision": 1, "digest": "sha256:REPLACE_WITH_REGISTERED_DIGEST"},
      "endpoint": "https://model-service.example.com",
      "client_id": "evaluation-space",
      "secret_env": "TEAM_MODEL_SECRET",
      "ceiling": {"tokens": 10000, "cost_micros": 1000000}
    }
  ],
  "schemas": {},
  "example_builders": true
}
```

Use the same exact model reference when provisioning example definitions. Model
services must support durable lookup/idempotent submission/cancellation; an ordinary
untracked chat endpoint is insufficient. Native configuration does not accept inline
secrets, executable file paths or arbitrary transform code. Additional schema entries
map exact schema digests to self-contained JSON Schema objects.

Configure `-studio-input-endpoint` and `STUDIO_EVALUATION_INPUT_SECRET` for private
Studio base lookup and result submission. `EVALUATION_SPACE_STUDIO_SECRET` protects
Studio's reverse artifact fetches. Native generation can run with evaluator connections
absent and evaluation off. It still requires active central group configuration,
explicit sealed-input reuse grants, and Studio output reservations.

The private `POST /evaluation-space/internal/v1/generation/admit` route accepts a
complete frozen generation assignment from HTTP Basic user `central` with the central
participant credential. Central identity checks requester/configuration/input/budget
scope; local admission validates catalog pins and sealed bytes. `read` and `control`
use the same private boundary and exact group/run identity. Do not expose this listener
to browsers or use provider credentials for admission.

Generation completion, spending settlement and Studio handoff are separate durable
states. Successful generation is not a merge or publication. The worker retries
uncertain model calls by their original key and looks up Studio acceptance before
submitting a result again. Generated branches retain the captured base and require
normal human review; disjoint goal outputs remain separate branches until merged.

A trusted central collector can transfer a sealed corpus while Evaluation Space is
running with `POST /evaluation-space/internal/v1/generation/seal-input`. Authenticate
as `central`, send raw bytes as `application/octet-stream`, and set
`X-Corpus-Artifact` to unpadded base64url of the complete artifact-reference JSON.
Evaluation Space obtains fresh central input-use authority before reading the body,
then verifies digest/size and seals it under the immutable reference. Changed bytes,
revoked policies and ungranted references are rejected. The endpoint has the artifact
store's 64 MiB per-artifact bound and requires native configuration to be enabled.


The pinned native runtime records start/end boundaries for each execution in its
Session log. Corpus extraction uses those boundaries to exclude prior and imported
history. Keep the runtime’s native capture export enabled; absent or incomplete boundaries
remain explicitly partial evidence. Curated corpus JSON must include `family_kind`
for each episode alongside `family_id`.

With both central participant connections and identity configured, the central
service also runs automatic generation dispatch. Declare
workflow input ports in the active binding and enable its schedule or event trigger.
The dispatcher reads Studio output bases through the authenticated
`/volumes-studio/internal/v1/evolution/bases` route, freezes a batch, reserves central
capacity, transfers locally captured inputs, and submits the exact native assignment.
A batch resolves each output Volume once. Source snapshots and composed inputs retain
immutable references; raw exports require the activation author's current explicit
`source.read` grant. Already provisioned artifact inputs need their exact reuse grant.

Inspect the group's trigger metadata for pending reasons and dispatch errors. On an
uncertain admission response, the dispatcher reuses the stored run ID and assignment;
restart does not select a new base or input range. Disabling automation supersedes
unadmitted assignments and lets admitted work drain under current authority. Do not
remove pending assignment or authorization records to force a retry. For typed
cross-binding dependencies, the dispatcher retains waiting consumer plans
and reads exact successful output references from
`POST /evaluation-space/internal/v1/generation/output`. This private route requires
the producer's exact group, run, output port and winning fence plus current central
read authority. It exposes only declared output ports from the completed workflow
checkpoint. Failed prerequisites mark consumers skipped; output-transport outages
remain retryable without changing the batch's original pins.

Manual backfill uses the same participant connections and native worker. Studio's
`manual-bases` participant action permits base selection for the current active
configuration while automatic triggers are off. Only the configuration's saved actor
can be resolved by central authority; the backfill requester still needs current
run, input and output-read permissions. Preview records retain exact historical
input bytes; the dispatcher checks current authority again before transferring them.
The native admission contract carries the explicit requested budget and never widens
it to the binding's maximum. Keep model per-call reservation ceilings within the
budgets selected for those runs.


The central `evaluation_space` participant connection also supplies group configuration
catalog selectors and compatibility previews in Administration. Browser requests use
central sessions; the server authenticates to Evaluation Space as `central`, validates
exact definition identities and rechecks the user's current group authority before
returning results. Remote catalog errors appear as unavailable, not an empty list.
Service connection and credential metadata is omitted from the browser response.
Immutable definitions remain distinct from current service enablement. The configuration
preview checks current catalogs without adopting a projection; activation repeats those
checks and coordinates Studio reservations through the existing participant worker.
