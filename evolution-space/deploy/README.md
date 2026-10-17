# Evolution Space deployment

Evolution Space is an independent service. Build a matched backend/frontend
bundle from a committed source revision:

```sh
python3 evolution-space/scripts/release.py build /srv/releases/evolution-space-v1
python3 evolution-space/scripts/release.py verify /srv/releases/evolution-space-v1
python3 evolution-space/scripts/release.py smoke /srv/releases/evolution-space-v1
```

The bundle contains `bin/evolution-space`, the offline
`evolution-space-catalog`, `evolution-space-backup` and `evolution-space-restore`
maintenance commands,
and the frontend under the fixed `/evolution-space/` base path. Keep the bundle
manifest with the release; it records the source commit, Go toolchain and every
packaged file digest.

For a complete Aster installation, use `python3 scripts/rebuild.py` from the
repository root. It builds this bundle beside the application and Studio
bundles and records its manifest digest in `deployment-build.json`; do not build
an unpinned Evolution Space directory beside an otherwise complete release.
The deployment plan must supervise all of these units:

| Unit | Required responsibility |
| --- | --- |
| `aster-evolution-space.service` | Run `bin/evolution-space` with the private worker listener and `127.0.0.1:18485` browser listener. |
| `aster-evolution-space-frontend.service` | Run the packaged `frontend/server.js` from the release. |
| `aster-evolution-space-tls.service` | Expose the browser listener on the private TLS address (reference port `18447`). |

The `aster-web.service` command must pass `-evolution-space` for that TLS
address. The front door owns public `/evolution-space/` routing; never expose
the worker listener directly. `scripts/deploy.py` rejects missing units,
unresolved `@EVOLUTION_SPACE@` paths, missing browser/TLS ports, absent front
door routing, mixed source revisions, and changed release manifests before it
stops any service or clears state.

The smoke command uses a disposable state directory to seal, inspect and expire
an artifact through the packaged catalog command, then backs up and restores the
state and checks that the expired body cannot be resealed. It also starts the
packaged frontend on a temporary loopback port, checks `/evolution-space/`, every
prefixed asset referenced by the page and a deep link, then terminates it. It does
not require identity or a live backend and does not validate a live deployment.

Use `bin/evolution-space-catalog` while the service is stopped for catalog publication,
service controls and artifact retention. For retention, send the complete artifact
reference on stdin with `--action inspect-artifact`. Send an object containing a
unique `request_id`, the same exact `artifact` reference and a nonempty `reason`
with `--action expire-artifact`. Both commands require `--state-directory` with
an absolute application-owned path. Active work and immutable profile datasets
remain pinned; expiry releases sealed storage without deleting metadata, earlier
backups, exports or journal copies. Keep the receipt; exact retries return it.

Run the backend with an application-owned state directory and loopback private
listener. Configure the shared front door to route `/evolution-space/` to the
browser gateway and run the frontend as its own process. Evolution Space can be
restarted independently of Studio and the central application; its health and
readiness probes are `/evolution-space/healthz` and `/evolution-space/readyz`.

Before an upgrade, stop the service and take a complete backup:

```sh
bin/evolution-space-backup \
  -state-directory /var/lib/evolution-space \
  -output /srv/backups/evolution-space-snapshot
```

Restore only into a new state directory. The restore command verifies the
manifest and copied database digests, rejects incomplete snapshots, and checks
database integrity and the current schema before returning:

```sh
bin/evolution-space-restore \
  -input /srv/backups/evolution-space-snapshot \
  -output /var/lib/evolution-space-restored
```

An interrupted restore retains `restore.incomplete`; service startup refuses
that directory. Keep it for diagnosis and retry the original backup into another
new directory. Do not remove the marker to start a partial restore. Incomplete
backups are also rejected at startup. Snapshots exclude browser sessions and
pending logins without signing users out of the source installation.

Start the restored service with reviewed identity/evaluator configuration, verify
the probes, catalog identities, run ancestry and artifact digests, then switch the
front door. Browser sessions are intentionally not restored; users authenticate
again through central identity. Never restore over a live state directory.
