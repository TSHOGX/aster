# Evaluation Space deployment

Evaluation Space is an independent service. Build a matched backend/frontend
bundle from a committed source revision:

```sh
python3 evaluation-space/scripts/release.py build /srv/releases/evaluation-space-v1
python3 evaluation-space/scripts/release.py verify /srv/releases/evaluation-space-v1
```

The bundle contains `bin/evaluation-space`, the offline
`evaluation-space-backup` and `evaluation-space-restore` maintenance commands,
and the frontend under the fixed `/evaluation-space/` base path. Keep the bundle
manifest with the release; it records the source commit, Go toolchain and every
packaged file digest.

Run the backend with an application-owned state directory and loopback private
listener. Configure the shared front door to route `/evaluation-space/` to the
browser gateway and run the frontend as its own process. Evaluation Space can be
restarted independently of Studio and the central application; its health and
readiness probes are `/evaluation-space/healthz` and `/evaluation-space/readyz`.

Before an upgrade, stop the service and take a complete backup:

```sh
bin/evaluation-space-backup \
  -state-directory /var/lib/evaluation-space \
  -output /srv/backups/evaluation-space-snapshot
```

Restore only into a new state directory. The restore command verifies the
manifest digest, rejects incomplete snapshots, and opens the database against the
current schema before returning:

```sh
bin/evaluation-space-restore \
  -input /srv/backups/evaluation-space-snapshot \
  -output /var/lib/evaluation-space-restored
```

Start the restored service with reviewed identity/evaluator configuration, verify
the probes, catalog identities, run ancestry and artifact digests, then switch the
front door. Browser sessions are intentionally not restored; users authenticate
again through central identity. Never restore over a live state directory.
