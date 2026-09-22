# Operations

Build the host application with `go -C orchestrator build -o bin/orchestrator
./cmd/orchestrator`. Prepare an immutable verified image and register it under the
configured Khora template ID. The engine store must use manual cold recovery.
Run Next.js and the application under service supervision with explicit state,
configuration and credential paths. A normal shutdown uses SIGTERM.

```sh
cd frontend
npm ci
npm run build
npm start -- --hostname 127.0.0.1 --port 3000
```

Start the application separately:

```sh
orchestrator serve --config /etc/batterygpt/application.json
```

With embedded gateways, both the frontend and authenticated engine endpoint must
be reachable on non-loopback host addresses; see [configuration](configuration.md).
Protect the engine API and its TLS termination independently of browser access.
The application user needs its state directory and reviewed configuration files,
not engine database or volume-directory access.

Manage credentials without a second product writer:

```sh
orchestrator user add --file /etc/batterygpt/users.yaml --username researcher --role user --password-stdin
orchestrator user password --file /etc/batterygpt/users.yaml --username researcher --password-stdin
orchestrator user role --file /etc/batterygpt/users.yaml --username researcher --role admin
orchestrator user list --file /etc/batterygpt/users.yaml
orchestrator user remove --file /etc/batterygpt/users.yaml --username researcher
```

Password commands read from stdin, never an argument. Feed input using an
appropriate secret mechanism. Output lists identity/role, not password hashes.
Credential edits are atomic and the live owner reloads the registry.

Use the protected control socket for live operations:

```sh
orchestrator diagnostics --socket /var/lib/batterygpt/control.sock
orchestrator diagnostics --socket /var/lib/batterygpt/control.sock --project PROJECT_ID
orchestrator publish --socket /var/lib/batterygpt/control.sock
orchestrator notify --socket /var/lib/batterygpt/control.sock --audience researcher --title Notice --body 'Maintenance is scheduled.'
```

Diagnostics report resource counts, occupied Projects, pending work and database
bytes. Project diagnostics compare declared mounts/network to public engine
observations and expose blockers. Publication selects new defaults on the next
application restart. Omit notification audience only for an intentional broadcast.

Closing a browser tab is not termination. Stop a Terminal, retire a Session or
request Project pause/delete through its scoped application action. Pause refuses
real blockers; forced restart establishes a fresh placement after terminating the
old one. Deletion waits for all owned resources and host artifacts. Shared
publication/template volumes remain outside Project ownership.

For process recovery, restart the owner with the same database, engine store and
saved bindings. Repair unavailable dependencies before retrying failed recovery.
Keep the original native journal/captures when an outcome is unknown. Never
resolve uncertainty by clearing database rows or deleting arbitrary engine state.

Backups must include the stopped application's database and host artifacts plus
a consistent engine/volume backup under the engine's supported procedures. A
SQLite copy alone does not contain the workspace or native runtime volume. This
repository does not provide cross-host failover or active-active operation.


Workspace content captures use Linux `O_TMPFILE` under `STATE/read-captures`, with
aggregate admission bounded to 512 MiB and no named capture files to recover.
Place application state on a filesystem with that support and sufficient free
space. Disk exhaustion fails a read; it never changes a durable mutation outcome.
Operator status reports `workspace_reads` entries, active leases, captured bytes
and the aggregate limit. Reader bindings in SQLite are process cleanup authority,
not file command outcomes or activity reservations.


Operator status also reports `writer` queues by `stop_control`, `control`, `ingest`
and `background`: current/peak queued count and byte reservations, rejection and
cancellation counts, cumulative/max wait and transaction nanoseconds. Control
reservations estimate one MiB per queued request; ingestion accounts for its
actual native payload plus fixed envelope allowance. These counters contain no
prompt bodies, credentials or resource-ID labels. A background pending result
means work yielded capacity, not that its projection failed.
