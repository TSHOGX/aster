# Queued-prompts release preparation

Status: development artifact, not deployment authorization. No live database,
runtime image, service or queue was modified while preparing this document.

## Candidate contract

The implementation uses product schema version 2 and exact compiled SQLite
schema fingerprint `sha256:0a9f6a6ccb1b8cb0c96b1e57ebf5b969ac5adbb6ec95c55de97ef9b175bff729`.
The pinned Pi 0.77.0 RPC input digest is
`sha256:d0f92418f6aedaa4c64da68f65cb6f93e18dc65d13969eba45349d99f36dfca7`;
the patched output digest is
`sha256:b9327e55144e4d7947365e916b1aea2feb7c57892167b3e84a7d5f598e9ff7aa`.
The runtime advertises native protocol 3 and owner-handoff protocol 2. The
final executable, runtime manifest, frontend build, configuration and content
digests must be measured from the built candidate with `release-identity`.
Source hashes alone are not a deployable identity.

## Format decision

Development uses the new contract directly. There is no old-client adapter,
dual-write phase or in-place ALTER path. `product.Open` intentionally refuses
any database whose compiled DDL fingerprint differs. An intermediate
development version with the same schema version number is also refused if
its fingerprint differs.
Read-only inspection on 2026-09-24 measured the installed database at
`/var/lib/batterygpt/product.db` as version 1 with exact fingerprint
`sha256:c945336d67883b9452c1d0e5f5e591bec42d981fafd12f330a727d1765d85983`,
matching the `358eb30` source schema. SQLite integrity and foreign-key checks
passed. At that instant it contained four Sessions, 11 turns, 39 messages,
ten completed normal commands and one applied Steer; all Sessions were idle
and there were no active incarnations. Counts are a point-in-time observation,
not a release lock or a copy of private content. Re-measure immediately before
conversion and reject source drift.

Preserving a backup alone does not make old Sessions usable in the new product.
The offline `format-convert` tool accepts only a quiescent copy of that exact
version-1 fingerprint and writes a separate version-2 database; it is not an
application startup migration. It copies shared authority rows and their
identities, maps completed normal prompts to explicit historical inputs with
unknown native-consumption time, maps the applied old Steer to an unknown
delivery/run, rebuilds derived conversation state and search, and validates
foreign keys and the candidate fingerprint before publishing its output path.
No ACK-only input is relabeled consumed. Unknown or contradictory source
shapes fail closed. The source snapshot is opened read-only.

From this checkout, the isolated format commands are:

```sh
go -C orchestrator run ./cmd/format-convert --candidate
go -C orchestrator run ./cmd/format-convert --source /path/to/source-snapshot.db --inspect
go -C orchestrator run ./cmd/format-convert --source /path/to/source-snapshot.db --destination /path/to/staging.db
```

The source must be a consistent SQLite backup with no uncheckpointed WAL. The
destination must not exist. The converter never takes a live Store lock and
does not replace the installed database. Retain the source backup with the
matching guest journal, native captures and workspace volumes for rollback of
the complete installation.

## Qualification sequence

1. Build an immutable candidate and run `release-identity --config FILE` with
   the intended configuration. Verify schema, native, journal, gateway,
   owner-handoff and content digests against this artifact and the build.
2. Inspect the incumbent with its own release identity and a read-only
   database snapshot. Use `format-convert --source SNAPSHOT.db --inspect` to
   record `PRAGMA user_version` and the compiled DDL fingerprint, plus
   integrity and foreign-key checks, all live/unknown runs and deliveries,
   pending controls, captures, service slots and release blockers. Do not
   infer consumption from `turns.state=completed` or a command ACK.
3. Use the existing quiescent drain, not owner handoff, for a schema/runtime
   change. Wait for durable and process blockers to settle. Do not acknowledge
   an unknown delivery merely to make readiness pass.
4. Stop the incumbent owner and producers. Make a consistent SQLite backup
   with its WAL state and retain the corresponding guest journal, captures,
   workspace volumes, engine state and immutable runtime manifest. Hash the
   copies and keep the source untouched. Conversion writes only a separate
   staging database and artifacts.
5. Run `format-convert --source SNAPSHOT.db --destination STAGING.db` against a
   staging snapshot of the **actual** incumbent fingerprint. The destination
   must not exist. Check exact row/identity
   accounting, all foreign keys, unique live-run and delivery-claim indexes,
   unknown/removed/consumed evidence, capture paths, share/export chronology
   and deterministic projection rebuild. The tool verifies the candidate
   Store's compiled schema fingerprint and publishes the output only after
   projection reaches ready. Reject any unexplained data loss.
6. Exercise the candidate against that converted staging copy and the pinned
   runtime with controlled model streams, Stop/Steer settlement, gateway and
   browser reconnection. Record latency, bounded queue/page work and the
   acceptance results in [queued-prompts-acceptance.md](./queued-prompts-acceptance.md).
7. Only after those checks and separate deployment authorization, use the
   repository's `drain-activate`/selected-executable procedure. Preserve the
   pre-conversion backup; there is no old-executable rollback into the new
   database. A failed activation remains fenced for repair or restoration of
   the complete pre-conversion installation, not a partial database swap.

The converter passed on a temporary consistent backup of the measured source.
Its output retained 4 Projects, 4 Sessions, 11 prompts, 39 messages, 11 commands,
38,727 raw records, 10 captures and one schedule; it produced 10 run records,
one unknown delivery and two ready retained conversation projections. The
remaining two Sessions are retired. Foreign keys and SQLite integrity passed;
two independent converted copies produced identical item/position/data hashes.
No source message text or credentials were exported to this repository.

Current state: the format transition is qualified on a staging copy, but the
final candidate executable/runtime/configuration identity and full integrated
failure matrix are not yet qualified. The old Steer remains explicitly unknown.
Activation remains closed. No live-data reset or deployment is implied.
