# Database operations

The control and memory stores are separate SQLite databases. Their runtime
configuration comes from `HARNESS_DB_PATH` and `HARNESS_MEMORY_DB_PATH`; memory
defaults beside the control file. Neither database belongs in the repository.

## Managed schema lifecycle

```bash
orchestrator db status --config /private/harness.yaml
orchestrator db plan --config /private/harness.yaml
# Only after all writers stop and a consistent backup is verified:
orchestrator db migrate --config /private/harness.yaml
```

`status` and `plan` inspect existing files without schema migration and fail if a
file is missing. `migrate` takes the owner/path locks, validates known structures,
migrates a copy, checks integrity and foreign keys, then replaces each stopped
file. A nonempty old WAL prevents replacement; checkpoint it correctly after
stopping writers and backing up, never delete it to silence an error.

Each file records migration version/checksum in `harness_schema_migrations`.
The pair is not one transaction: a completed first database and failed second
remain a recorded partial upgrade that can be resumed or restored as a pair.
`--initialize` is for a new empty pair, not for recovering missing production
files. The deployment tool's recorded initialization resume also requires any
existing database to have a valid completed stamp.

`HARNESS_MANAGED=1` makes normal service startup verify schema rather than execute
DDL. Development store opens can still initialize/upgrade schemas; never start a
development binary against a production database to test compatibility.

## Private local schema baseline catalog

`HARNESS_SCHEMA_BASELINES_PATH` optionally names a deployment-local JSON catalog.
It adds previously unknown reviewed fingerprints; embedded entries win on
conflict. `db status` returns `baseline_source` (`embedded` or `local`). Missing or
malformed explicitly configured files fail rather than silently falling back.

The shape matches the embedded catalog:

```text
{
  "store": { "<reviewed control schema SHA256>": "<reviewed baseline label>" },
  "memorystore": { "<reviewed memory schema SHA256>": "<reviewed baseline label>" }
}
```

This is a format illustration, not a catalog to copy verbatim. Fingerprints
include DDL, indexes and triggers, so historical ALTER sequences can differ from
fresh databases. A local entry is an operator compatibility decision, not proof
that an arbitrary schema is safe. Review the actual schema and test a consistent
backup copy before admitting it. Managed validation still checks migration
version/checksum and the target-baseline label.

Store this file outside Git with trusted ownership and no untrusted write access.
With `harness-deploy`, put the environment variable in the private target JSON's
`environment` mapping so preflight, migration and the service see the same file.
Include the file in the private configuration backup. Do not publish real hashes
or manufacture a target label merely to bypass a failed preflight.

## Backup and restore

Stop the backend, frontend-triggered external writers, timers and standalone jobs.
Back up both databases and any WAL/SHM files together with persistent workspaces,
agent homes, KB snapshots/pending state, trajectory archives and shared-file blobs.
Keep configuration and the matching executable/rootfs versions recoverable.

Restoring a previous main DB file next to a newer WAL is unsafe. Restore the whole
stopped state into clean directories. Once user traffic has resumed, restoring a
pre-upgrade snapshot loses subsequent changes; preserve the new state and plan
reconciliation instead of treating a binary swap as a lossless rollback.

## Legacy split tooling

`orchestrator db-split` remains an explicit compatibility tool for older single-file
installations. Its dry-run reports where the nine memory tables currently live;
forward migration copies and verifies before dropping source tables. The reverse
path is for an application version that actually expects the single-file layout.
Use `db-split --help` and review `internal/dbsplit` tests before a legacy adoption.
Do not run forward or reverse splitting during ordinary upgrades of already
separated stores. No fixed production table counts or one-host migration steps
are part of this guide.
