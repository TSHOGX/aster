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
| `identity` | Optional shared-identity adapter: HTTPS `issuer`, private loopback `listen`, and registered `clients` with `id`, exact Studio `callback`, and one or two `secret_envs` for credential rotation. Uses existing central administrators; no additional account store. |

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
participant secret. This requires `evaluation_space` configuration as well. The
worker reserves Studio outputs before Evaluation Space adoption, then adopts Studio
reservations and settles only after both exact receipts are recorded.

Register Evaluation Space in central `identity.clients` with ID `evaluation-space`
and the exact callback `${identity.issuer}/evaluation-space/auth/callback`.
Studio's callback remains `${identity.issuer}/volumes-studio/auth/callback`.
The two services cannot exchange callback paths. Each registration references its
own identity credential environment variable; participant credentials are separate.

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
