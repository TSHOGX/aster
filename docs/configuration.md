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
| `frontend` | Next.js HTTP(S) origin; default `http://127.0.0.1:3000` |
| `state_directory` | Product database, native captures and frozen-share files |
| `application_socket`, `control_socket` | Distinct private Unix sockets; default below state directory |
| `engine` | Explicit endpoint and persistent store ID; HTTPS requires CA and client certificate/key |
| `template` | Engine template ID, exported Go runtime manifest and `sha256:` digest |
| `project` | Positive CPU, memory, overlay-disk, workspace/runtime capacities and reachability |
| `auth` | Credential YAML path, or an explicit loopback-only development actor |
| `content` | Reviewed Agent configuration/shared-assets source paths and publication capacity |
| `limits` | Global/per-Project capacities, idle deadlines and worker interval |
| `notifications` | Per-source Inbox notification policy; changes apply after application restart |
| `gateways`, `agent_audience` | Independent gateway endpoints and upstream credential environment names |
| `titles` | Optional host-side title model: `base_url`, `model`, `api_key_env` |
| `tls_cert`, `tls_key` | Optional browser TLS certificate/key pair |

The application derives Project store/template/release/publication bindings.
Those fields, including `network_policy_revision`, cannot be supplied inside
`project`. Existing Projects retain fixed template/resource/service settings;
direct/DNS defaults apply at the next creation/resume boundary. Startup
validates the single configured runtime against the current host protocols and
checks its registered engine template platform. Every non-deleted Project,
including paused Projects, must match that exact template ID and digest.
Replacing the runtime requires a fresh installation; there is no retained-image
catalog or compatibility adapter.

Startup rejects different runtime bindings and bindings to another engine store
before publication or workers start. It never treats a fresh engine's IDs as
authority over old resources.

`engine.endpoint` permits plain HTTP only on a loopback IP. HTTPS requires
`ca_file`, `client_cert` and `client_key`; ambient proxy configuration is disabled.
When gateways are enabled, the owner and gateway run as separate processes under
Khora's dedicated non-engine `--via-uid`: use non-loopback addresses for the engine TLS edge,
frontend and model upstream because Khora blocks that UID's loopback connections.
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
restart the application to select current publications as new Project defaults.
Existing Projects keep their original publication IDs.

Credential files contain `users` entries with `username`, `password_hash` and
`role` (`user` or `admin`). Missing authentication fails startup. Registry reload
retains the prior valid set on error; changed credentials invalidate existing
sessions through their saved fingerprint. An explicit `development_actor` is
mutually exclusive with `users_file` and requires a loopback browser listener.

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

## Mutable network defaults

The running owner checks its original configuration file every two seconds. Only
`project.reachability.direct` and `project.reachability.resolvers` are mutable.
It validates the complete file and rejects unsupported field changes as a whole;
malformed files retain the last accepted policy. Write the file atomically to
avoid transient parse errors. The protected `network-reload` command invokes the
same path immediately. Reload never pauses a Project or interrupts running work.

Accepted defaults are durable, immutable snapshots identified by a SHA-256
revision. Resource initialization and creation/resume intent select a snapshot
inside the product transaction. A paused binding is conditionally updated and
verified before resume; its matching DNS/hosts files are projected before work
is admitted. Later file edits cannot change an operation already in progress.
Model service routes, hosts, Project identity and both volumes are retained. A
running Project (even idle) retains its effective policy until a later resume.

Invalid startup configuration still refuses startup; persisted defaults do not
bypass the strict configuration contract. Correct the file to restart. Runtime
reload is fenced during release transitions. Owner handoff must match
the accepted policy revision before acquiring the database writer; the fixed
release compatibility digest excludes only these two mutable fields.

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
