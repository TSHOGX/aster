# Release deployment

All paths, accounts and service names below are examples. Keep actual target
configuration, inventories, credentials and reports outside the repository.
See [privacy](privacy.md) and [routine operations](../operations.md).

## Delivery model

GitHub-hosted runners build the application and sandbox and test packaged
artifacts. A target host downloads a selected release and performs its own
preflight, backup, migration, switch and business acceptance. No production CI
runner, source checkout, compiler, npm or container engine is required.

Packaged support is Linux amd64, Ubuntu 22.04/24.04, systemd. The host supplies
nginx, runsc, ip, nft, runuser and GNU tar with ACL/xattr support. The frontend
archive carries Node; it does not require changing the host's Node installation.
Model/data gateways and an optional external evolution runner remain local
prerequisites, not bundled third-party projects.

## Artifact commands

Download the deployment CLI from a trusted Release and verify its checksum.
Artifact commands do not read target configuration or invoke systemd.

```bash
harness-deploy fetch --repository example-org/example-repo --release vX.Y.Z --cache /var/cache/harness/releases
harness-deploy verify --release vX.Y.Z --cache /var/cache/harness/releases
harness-deploy export --release vX.Y.Z --cache /var/cache/harness/releases --output /secure/release.tar.gz
harness-deploy import --archive /media/release.tar.gz --manifest-sha256 <trusted-sha256> --cache /var/cache/harness/releases
sudo harness-deploy stage --release vX.Y.Z --cache /var/cache/harness/releases --releases /opt/harness/releases --rootfs /opt/harness/rootfs
```

Use a separate cache per repository and an exact version, never `latest`.
`export` can fetch an uncached release when `--repository` is provided. `verify`
and `stage` use existing cache. Offline import requires the manifest hash from a
trusted channel. Stage needs root to preserve sandbox ownership, but does not
stop services or change the active release.

For private releases, inject `HARNESS_GITHUB_TOKEN` into the tool's environment.
The sandbox is normally a Release attachment and uses the same credential. Older
releases without that attachment can fall back to GHCR using
`HARNESS_GHCR_USER`/`HARNESS_GHCR_TOKEN`. Do not put credentials in shell arguments,
public examples or logs.

## Configure a local target once

Copy [the target template](../../deploy/target.example.json) outside the checkout,
for example to `/etc/harness/deploy.json`. It specifies application config,
services, data/backup/cache paths, health probes and local hooks. `label` is
optional display text; it does not identify a host or determine operation locks.
No instance registry or implicit adjacent-config scan is required.

The installation destination and operation directory are locked, and schema
operations also lock canonical database paths. Operators must still prevent
separate configurations from sharing mutable data, services or listeners.

Before accepting the configuration:

- Set every persistent root and the runsc/network resources for this target.
- Supply a nonempty valid auth file, resolver and unprivileged frontend account.
- Inventory all external writers, timers and detached workers before setting
  `writers_inventoried` to true.
- Include databases, KB, workspaces, agent homes, trajectories, share blobs,
  checkpoints and private configuration in the non-overlapping backup roots.
- Keep backups outside those roots and reserve space beside the database for
  migration copies, not just on the download/backup filesystem.
- Add the [maintenance-marker snippet](../../deploy/nginx-server.inc.example) to
  this target's existing TLS server, preserving its TLS and reverse-proxy policy.
- Keep any `HARNESS_SCHEMA_BASELINES_PATH` file private and include it in the
  backup; see [local catalogs](../database.md#private-local-schema-baseline-catalog).

The three hooks use an absolute executable and argv, not shell evaluation:

| Hook | Exit-zero contract |
|---|---|
| `external_idle_command` | No external in-flight writers; also verifies legacy automatic jobs and detached workers when the backend cannot report them |
| `cold_start_command` | Supported lifecycle operations release old sandboxes, preserve user state and prevent restoration of incompatible memory checkpoints |
| `smoke_command` | Controlled validation of health, historical data, files/KB, real model access and required queue behavior |

Do not replace these checks with unconditional success. Hooks remain target-local
because gateways, credentials and business acceptance differ between deployments.

## Adopt existing services

Adoption preserves existing data paths. Inspect the actual executable, not just
a repository HEAD or service directory. `inspect` reads the configured systemd
MainPID and `/proc/<pid>/exe`, including its SHA256 and Go build metadata. Treat
its output as private host information.

For an unmanaged legacy binary, record its real `legacy_binary_sha256` and retain
a usable previous release directory. Existing systemd units must be reviewed and
adapted to the configured `current` link and `runtime.env`; incompatible units
are refused rather than overwritten. A new installation writes the explicitly
configured units but does not provision nginx, accounts or external projects.

## Preflight and upgrade

```bash
sudo harness-deploy inspect --config /etc/harness/deploy.json
sudo harness-deploy check --config /etc/harness/deploy.json --release vX.Y.Z
# For a genuinely new empty deployment only:
sudo harness-deploy check --config /etc/harness/deploy.json --release vX.Y.Z --new-install
sudo harness-deploy install --config /etc/harness/deploy.json --release vX.Y.Z
# For an existing deployment:
sudo harness-deploy upgrade --config /etc/harness/deploy.json --release vX.Y.Z
harness-deploy status --config /etc/harness/deploy.json
sudo harness-deploy resume --config /etc/harness/deploy.json --operation <id>
```

Application `orchestrator --config` selects YAML; deployment-tool `--config`
selects the target JSON. Preflight/install/upgrade do not implicitly download;
fetch and stage first. Checks include platform/dependencies, identity, schema,
space, permissions, backup coverage and service configuration. A different runsc
version produces a warning requiring target-local startup validation.

The recorded stages are `stage → maintenance → drain → stop → backup → migrate →
switch → verify → traffic → background`. Drain blocks new user/automatic work and
waits for existing work. Parked turns remain durable but do not count as automatic
pending work. Timeout does not cancel user turns. All inventoried writers stop
before the consistent backup. Maintenance ingress alone is not a stop-the-world
barrier for existing connections or detached jobs.

During internal smoke, ingress stays closed and background work stays held. The
operation records possible traffic exposure before opening ingress. `resume`
checks target configuration and release digest against the saved operation;
inspect failures rather than repeatedly starting a fresh upgrade.

## Recovery

```bash
sudo harness-deploy rollback --config /etc/harness/deploy.json --operation <id>
```

Before traffic exposure, snapshot rollback stops writers, preserves the failed
state, restores complete roots into clean directories and switches to the prior
program/configuration. This prevents pairing an old DB with a new WAL. After
smoke, maintenance and background hold remain for explicit operator release.
Restore only the external units recorded as previously active.

After possible traffic exposure, automatic T0 restore is refused because it could
lose new messages, workspace changes or approvals. Preserve T1 and prefer a
forward fix or reviewed reconciliation. Initial installation has no prior release
and does not automatically delete newly created data on failure.

## Acceptance boundaries

Hosted CI verifies artifact integrity, packaged version/Node/standalone behavior,
synthetic old-schema migrations and a real network-disabled runsc startup. Its
report explicitly says `production_verified=false`. It uses no production data
or real model credentials.

Each target validates actual model/data gateways, old history/files/KB, queued
work, Stop behavior, schedules and rollback readiness. Keep that evidence private.
The same configuration is reused on subsequent upgrades; no extra deployment
platform or production GitHub runner is required.
