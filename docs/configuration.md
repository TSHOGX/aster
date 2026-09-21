# Application configuration

`orchestrator serve --config FILE` accepts one strict version-1 JSON document.
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
| `gateways`, `agent_audience` | Optional service audience endpoints and upstream credentials |
| `titles` | Optional host-side title model: `base_url`, `model`, `api_key_env` |
| `tls_cert`, `tls_key` | Optional browser TLS certificate/key pair |

The application derives Project store/template/release/publication bindings.
Those fields cannot be supplied inside `project`. Existing Projects retain their
saved profile; a changed default affects newly configured Projects. Startup
rejects existing bindings without a release identity or bound to another engine
store. It never treats a fresh engine's IDs as authority over old resources.

`engine.endpoint` permits plain HTTP only on a loopback IP. HTTPS requires
`ca_file`, `client_cert` and `client_key`; ambient proxy configuration is disabled.
When gateways are enabled, the application runs under Khora's dedicated
non-engine `--via-uid`: use non-loopback addresses for the engine TLS edge,
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
