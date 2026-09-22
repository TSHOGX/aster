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


`agent_pressure` holds advisory samples from live runner attachments. The logical
journal limit includes retained records/command metadata, with a separate control
reserve; `journal_disk_bytes` includes the SQLite database and WAL. Runtime-volume
capacity must also cover native history and captures across sibling Sessions.
Send-window and control-queue bytes are reservations, not exact network traffic.
Samples disappear when their attachment ends and never authorize command replay.

## Admission drain

The application supports a persistent admission fence through its protected
control socket. Use a unique owner ID for each attempt:

```sh
orchestrator drain --socket /var/lib/batterygpt/control.sock --owner UPDATE_ID --timeout-seconds 120
orchestrator drain-status --socket /var/lib/batterygpt/control.sock
orchestrator drain-abort --socket /var/lib/batterygpt/control.sock --owner UPDATE_ID --generation GENERATION
```

`drain` returns the persisted generation, owner, phase and deadline. Repeating
that owner while draining returns the same deadline. New execution admissions
receive HTTP 503 with a draining explanation; explicit held prompts remain held.
Already queued prompts cannot dispatch until admission reopens. Original request
identities remain reusable, and replaying an already accepted request still
returns its recorded intent. Stop, evidence ingestion, observation and cleanup
continue. Abort/state-query dispatch takes priority over blocked steering and
can proceed without discarding another command's unknown outcome.

`drain-abort` requires the matching owner and generation. Deadline expiry aborts
the release attempt and reopens admission; it does not terminate admitted work.
The worker checks expiry once per second. A process crash leaves the gate closed
in the database, and reopening the same configuration retains it until abort or
expiry reconciliation. Status reports fixed-category durable blocker counts;
unknown command outcomes and unacknowledged unknown turns are blockers.

This admission slice does **not** report readiness or authorize an update or
shutdown. Runtime gateway requests, read leases, terminal input and metadata
model calls are counted, but an atomic quiescence/activation transition is still
required. Gateways currently share the
owner's lifetime, so restarting it can terminate an active upstream stream.
Independent gateway lifetime, ready/activating transitions and the first-deploy
bootstrap remain unqualified. Restart recovery uses the same published content
configuration; introducing a new content revision while draining is rejected.

`drain-status.runtime_work` and `diagnostics.runtime_work` report fixed-category
in-flight operation counts without credentials or content. Title-generation calls
and ordinary terminal input cannot acquire new work slots while draining; exact
Ctrl-C, termination, resizing and read access remain available during that phase.
Existing model turns may make further authorized requests while settling. A
captured file keeps its slot until its response body closes. Cancellation alone
does not decrement an operation whose backend has not returned. These counts are
process-local observations and must not be treated as update readiness.
