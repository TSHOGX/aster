# Operations

Build the host application with `go -C orchestrator build -o bin/orchestrator
./cmd/orchestrator`. Prepare an immutable verified image and register it under the
configured Khora template ID. The engine store must use manual cold recovery.
Run Next.js, the owner and any service gateway under independent supervision
with explicit state, configuration and credential paths. Use the release
commands below for orderly updates.

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

For declared service gateways, start a separate process after the owner:

```sh
orchestrator gateway --config /etc/batterygpt/application.json
```

Keep it running across owner/frontend restarts. Both the frontend and
authenticated engine endpoint must be reachable on non-loopback host addresses; see [configuration](configuration.md).
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
orchestrator drain-ready --socket /var/lib/batterygpt/control.sock --owner UPDATE_ID --generation GENERATION
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
unacknowledged unknown outcomes are blockers. An explicitly acknowledged command
can cease blocking only after its original process exited, cleanup settled, and
replay is no longer pending. Its unknown outcome is preserved.

The default `quiescent` drain mode checks durable blockers and in-flight counts
atomically with runtime admission and the database writer. `drain-ready` reports
a revocable checkpoint only when both are empty. Reads, model requests and
resizing cannot acquire new leases while quiescent-ready. Stop/Ctrl-C remains available: authorized cancellation returns ready
to draining before it issues. New cleanup or unresolved evidence also revokes
ready in the same transaction. Ready retains the original deadline and supports
abort; a stale owner/generation cannot advance it.

Ready does **not** authorize a separate unchecked shutdown. The activation command
below consumes a fresh readiness check and retires the owner itself. The gateway
has an independent process lifetime. First-deployment bootstrap remains
unqualified; this workflow is for quiescent
replacement. Controlled owner-outage tests do not certify arbitrary active
updates or migrations.

`drain-status.runtime_work` and `diagnostics.runtime_work` report fixed-category
in-flight operation counts without credentials or content. Title-generation calls
and ordinary terminal input cannot acquire new work slots while draining; exact
Ctrl-C, termination, resizing and read access remain available during that phase.
Existing model turns may make further authorized requests while settling. A
captured file keeps its slot until its response body closes. Cancellation alone
does not decrement an operation whose backend has not returned. These counts are
process-local observations and must not be treated as update readiness.


## Activation handoff

Prepare and verify a replacement executable and configuration before closing
admission. Use the same state directory/database and engine bindings. Compute the
replacement executable's SHA-256, prefixed with `sha256:`; this is distinct from
the guest runtime manifest digest. After `drain-ready` succeeds:

```sh
orchestrator drain-activate --socket /var/lib/batterygpt/control.sock --owner UPDATE_ID --generation GENERATION --target sha256:EXECUTABLE_DIGEST
```

Activation rechecks generation, deadline, durable blockers and runtime work under
the fence. A busy or revoked checkpoint must be settled and checked again. Once
activation commits, it does not timeout-open. The retiring owner finishes the
control response, closes its listeners/workers and exits. Wait for that process
to exit before starting the verified replacement under the service supervisor.
The replacement starts behind the closed gate; its running Linux executable
inode (`/proc/self/exe`) must match the target before maintenance or publication
starts. Complete the handoff through the replacement's control socket:

```sh
orchestrator drain-complete --socket /var/lib/batterygpt/control.sock --owner UPDATE_ID --generation GENERATION
```

Startup checks the expected executable under the database lifetime lock before
opening a writer or migrating schema. An unexpected candidate leaves the database
and current Store identity unchanged.

Completion requires a different Store instance, matching executable digest, and
settled durable/runtime work. It then reopens admission and records the actual
finished digest. New configuration publication and original-journal recovery may
run on the replacement while ordinary work remains fenced. Maintenance cannot
start on the retiring instance. Late evidence remains durable and can block
completion. Coordinator identities cannot be reused, even after later attempts.

If the selected build fails startup, use a rollback build compatible with the
current schema and saved runtime protocols. Start it explicitly as a candidate,
using the original state directory, then acknowledge rollback:

```sh
orchestrator serve --config /etc/batterygpt/application.json --rollback-candidate
orchestrator drain-rollback --socket /var/lib/batterygpt/control.sock --owner UPDATE_ID --generation GENERATION
```

The flag permits an executable different from the planned target to start behind
the closed fence; it does not reopen admission. Rollback still requires a distinct
owner instance and settled work. It preserves accepted writes and records the
running digest; it never replaces the database with a backup. A schema-incompatible
build is refused before it can write. `drain-abort` applies before activation;
once activating, use the replacement completion or rollback path.


## Candidate identity

Inspect a replacement using that executable itself and the intended configuration:

```sh
/path/to/candidate/orchestrator release-identity --config /etc/batterygpt/application.json
```

This command reads the pinned guest manifest and reviewed content trees, hashes
its running executable inode, and reports schema version/digest, configuration
and content digests, gateway protocol and a compatibility digest. It does not
open the product Store, acquire its lock, contact the engine/provider or create
application state. It can run while the current owner is serving. Raw configuration,
content and credential values are not returned. `diagnostics.release_identity`
reports the running owner's corresponding startup identity.

The compatibility digest excludes executable bytes, so separately built candidates
can describe the same inputs. Configuration, configuration base directory, reviewed
content, guest release or schema changes alter it. Startup publishes the exact
measured content revisions; files that change before upload are refused by the
publisher. A matching digest describes compatibility inputs and does not grant
permission to bypass admission/drain or prove arbitrary binary compatibility.
The owner-only workflow below applies an additional transfer predicate and live
gateway checks. Full updates continue to require quiescence.

## Compatible owner handoff

Use `--mode owner` only for an owner/API executable replacement with matching
candidate and running-owner compatibility digests. The contract includes the
owner handoff protocol, exact schema, configuration/base directory, guest release,
reviewed content and gateway protocol. Schema, template, engine, configuration or
gateway changes require the default full drain. Keep the gateway process running.

```sh
orchestrator drain --socket /var/lib/batterygpt/control.sock --owner UPDATE_ID --mode owner --target TARGET_DIGEST --compatibility COMPATIBILITY_DIGEST --timeout-seconds 120
orchestrator drain-ready --socket /var/lib/batterygpt/control.sock --owner UPDATE_ID --generation GENERATION
orchestrator drain-activate --socket /var/lib/batterygpt/control.sock --owner UPDATE_ID --generation GENERATION --target TARGET_DIGEST
# Wait for the source to retire, then start the inspected executable.
/path/to/candidate/orchestrator serve --config /etc/batterygpt/application.json
/path/to/candidate/orchestrator drain-complete --socket /var/lib/batterygpt/control.sock --owner UPDATE_ID --generation GENERATION
```

Copy target and compatibility digests from `release-identity`; use the generation
returned by `drain`. Begin pins the target and gateway registrations. Ready,
activation and completion require a fresh protected gateway observation, whose
registration/capacity must match both SQLite and that pinned snapshot. The gateway
must advertise the same owner handoff protocol as the application. Observations
older than two seconds are refused. A changed or unavailable gateway prevents
handoff; another process cannot silently stand in for the original stream.

Owner-ready can retain running turns with durably applied prompts and their
original active incarnation/Project, plus requests authorized for those turns.
Starting/stopping/unknown work, pending/uncertain sends, host writes, publication,
cleanup and all host operation leases still block. `durable_blockers` reports the
full work set; `handoff_blockers` identifies work that cannot transfer. Retained
work is not idle. Late authority/evidence changes revoke pending readiness.

Existing admitted turns may make new authorized model requests during owner-ready
and activating while the authority is available. New requests fail closed during
the actual outage. Existing streams retain the five-second maximum continuation
window described in the [gateway contract](gateway-contract.md). Exceeding that
window cancels forwarding; it is not a successful continuity result. A successful
`gateway-stop` always requires quiescent mode, even between model requests.

Replacement startup verifies exact schema/contract before any writer or migration.
It reattaches original native journal identities; completion may reopen admission
while transferable work is still running. Stale coordinators and the retiring
Store instance cannot finish the handoff. Deadline expiry aborts a pending drain
or readiness checkpoint, but never reopens an activating handoff automatically.

Explicit compatible rollback uses the existing rollback-candidate/rollback
commands and preserves accepted writes. If the gateway itself was lost/replaced,
rollback can reopen only after all work is fully settled and the new gateway is
observed. It cannot claim preservation of the old HTTP stream. The native fixture
qualifies this mechanism between distinct executable artifacts with the same
contract; the [recorded real-provider run](../dev/measurements/owner-handoff.json) also
retained a 24.3-second GLM-5.2 stream. The complete version matrix and bootstrap
remain separate qualification gates.

## Gateway replacement

Close application admission with `drain`, retain its owner/generation, and let
existing turns settle. The gateway continues accepting authorized requests for
those turns while the application is draining. Then run:

```sh
orchestrator gateway-status --socket /var/lib/batterygpt/gateway.sock
orchestrator gateway-stop --socket /var/lib/batterygpt/gateway.sock --owner RELEASE_ID --generation GENERATION
orchestrator gateway --config /etc/batterygpt/application.json
orchestrator drain-abort --socket /var/lib/batterygpt/control.sock --owner RELEASE_ID --generation GENERATION
```

`gateway-stop` refuses active work, stale release identity or unavailable owner
checks. Wait for the process to exit before starting its replacement; do not
remove its lifetime lock or clear request rows. Verify the replacement's status
before reopening admission. If the drain times out, admission reopens and the
update is abandoned. Arrange supervision so a successful gated stop does not
immediately restart the old executable. Forced termination is fault recovery,
not graceful stream preservation.
