# Independent public front door

Build the proxy separately from Aster and Studio:

```sh
go -C volumes-studio/backend build -o /srv/volume-studio/bin/front-door ./cmd/front-door
/srv/volume-studio/bin/front-door \
  -listen 0.0.0.0:8443 -public-origin https://example.com:8443 \
  -application http://127.0.0.1:8080 -studio http://127.0.0.1:8082 \
  -cert /etc/volume-studio/public.crt -key /etc/volume-studio/public.key
```

Origins and certificate paths are operator configuration, never browser input.
Remote upstreams require verified HTTPS; loopback IPs may use HTTP. Both business
listeners must be private and accept traffic only from the front door. Studio's
distribution listener is separate and never exposed by this proxy.

When Aster sits behind this TLS terminator, add the following to its configuration:

```json
{"front_door":{"public_origin":"https://example.com:8443","peer_ip":"127.0.0.1"}}
```

Aster rejects other peers/hosts and reconstructs the original HTTPS authority from
configuration. Only that peer supplies the client IP. Direct Aster deployments
omit `front_door`. Keep the peer address aligned with the proxy's actual source
IP, and include this change in the normal Aster release configuration review.

The front door preserves query, ranges, conditional headers and event streaming,
checks the public Host, strips spoofed identity/forwarding headers, and filters
request cookies for each destination. Studio responses may set only its session
and login cookies with `Secure; HttpOnly; SameSite=Lax; Path=/volumes-studio/` and
no Domain. Unexpected cookies fail the entire upstream response. Aster receives
only `project_login`. Public `/distribution/` routes are refused.

The proxy has request-header/idle/upstream-header deadlines; streaming bodies
retain cancellation without a whole-response deadline. Each application must
bound its individual upload/read operations. Stopping either business process
does not take the other route down. The proxy and sole identity authority remain
explicit deployment dependencies.

Build and run the independent Studio gateway:

```sh
go -C volumes-studio/backend build -o /srv/volume-studio/bin/studio ./cmd/studio
/srv/volume-studio/bin/studio \
  -state-directory /var/lib/volume-studio -publisher-id deployment-publisher \
  -public-origin https://example.com:8443 \
  -identity-endpoint http://127.0.0.1:8083 \
  -identity-client studio -identity-secret-env STUDIO_IDENTITY_CLIENT_SECRET \
  -frontend http://127.0.0.1:3002
```

Register the same public issuer/callback and secret environment in Aster's
[identity adapter](../contracts/v1/identity.md), including its private loopback
listener. Studio's gateway listens on loopback behind the front door. It never
receives `project_login`, and never forwards its session cookie to the frontend.

The first implementation performs fresh introspection on every privileged request;
it does not use a local authorization cache. Identity outages immediately return
`identity_unavailable`; login and logout can be retried after recovery. Central
evidence itself is bounded to at most 60 seconds, and no response or local session
extends that interval. Admitted worker phases use the separate principal check.

`sessions.db` contains only login attempts and app sessions referencing central
authority. Local cookie tokens are stored as digests; back-channel grant references
are private credentials needed for introspection. Keep the directory owner-only.
SQLite uses WAL/FULL, foreign keys, an exclusive owner lock and transactional schema
steps. Unknown newer schemas fail without reset. A lost callback exchange requires
a fresh login; durable app sessions survive a normal Studio restart.

## Private distribution and source retirement

Supply a private JSON registration file with `-distribution-config FILE`; the
separate listener defaults to `127.0.0.1:8084`. Credentials are environment
references, never literal JSON secrets:

```json
{"clients":[{"id":"aster","secret_envs":["STUDIO_DISTRIBUTION_SECRET"],
 "grant_revision":1,"libraries":["*"]}]}
```

Prefer explicit library IDs over `*` when the consumer should see a subset. Two
secret references allow rotation; removing a credential stops its new requests.
Increment `grant_revision` to invalidate all outstanding exports for that client.
Configuration changes require a Studio restart. Host the loopback listener behind
an authenticated TLS tunnel for cross-host consumption; never expose it through
the public proxy. Service requests with Origin or Cookie are refused.

Exports expire after 15 minutes, pin the exact descriptor and require service
credentials on every request. Reads permit one explicit range of at most 8 MiB
(or an entire file of at most 8 MiB). A reader has a durable pin until response
completion; a closed/expired export cannot admit another read. Startup reconciles
abandoned readers only after acquiring the exclusive process lock.

Archiving stops discovery/new exports and renewal. Admitted exports retain bounded
access until close/expiry. Retirement waits for export, active-reader and validation
blockers, then removes the version's blob references. Metadata and immutable labels
remain retained; drafts and other versions can still pin the same blobs. Consumer
copies are never deleted or revoked by source retirement.

## Backup and restore

Stop Studio for the supported operator backup command. Its exclusive database
lock refuses a running writer. Supply a new, empty destination outside the source:

```sh
go -C volumes-studio/backend build -o /srv/volume-studio/bin/studio-backup ./cmd/studio-backup
/srv/volume-studio/bin/studio-backup \
  -state-directory /var/lib/volume-studio -publisher-id deployment-publisher \
  -output /srv/backups/volume-studio-snapshot
```

The backup pins metadata, blobs and acknowledged upload files against GC, snapshots
SQLite with `VACUUM INTO`, copies and fsyncs bytes, and records file digests in
`backup.json`. An interrupted snapshot retains `backup.incomplete`; startup refuses
it. Preserve the complete private directory and its inventory, publisher ID and
configuration. Restore to an independent directory, verify the inventory against
trusted backup evidence, and start Studio with the original publisher ID. Never
run a writable restore against the source blob paths. Sessions are intentionally
excluded: sign in again through central identity after restore. Draft/version
history and acknowledged uploads are retained independently of those sessions.

## Independent release

Studio releases are built from the committed `volumes-studio/` tree only. They do
not build Aster, download Khora, drain runtime work, or include local credentials,
databases and ignored files. Build into a new directory outside the checkout:

```sh
python3 volumes-studio/scripts/release.py build /srv/candidates/studio-new
python3 volumes-studio/scripts/release.py verify /srv/candidates/studio-new
```

The bundle contains `bin/studio`, `bin/front-door`, `bin/studio-backup`, the
standalone frontend with its static assets, contracts, deployment guidance and
`studio-release.json`. The inventory binds source commit, dependency locks,
frontend build ID, file digests/modes and safe internal symlinks. Verification
refuses changed, missing or extra files. Checksums require trusted release evidence;
they do not authenticate an arbitrary downloaded bundle.

Run the packaged frontend from the bundle with
`HOSTNAME=127.0.0.1 PORT=3002 node frontend/server.js`. Supervise frontend, Studio
and front door independently. The `studio-v*` workflow creates an independent
artifact; it neither publishes to a deployment nor invokes Aster's update lifecycle.
Stop Studio and take a verified private backup before schema upgrades. Ordered
transactional Studio migrations preserve author history; unknown newer schemas
are refused. After upgrade check shared login, draft history, a resumed upload,
a published descriptor and the private service catalog before restoring traffic.
