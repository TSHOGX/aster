# Queued-prompts release preparation

Status: development artifact, not deployment authorization. No live database,
runtime image, service or queue was modified while preparing this document.

## Candidate contract

The implementation uses product schema version 3 and exact compiled SQLite
schema fingerprint `sha256:44895d2a2e545ba7232cd42ef06f7d5c7a5342086e8962818b1e9d34a3cf8e22`.
The pinned Pi 0.77.0 RPC input digest is
`sha256:d0f92418f6aedaa4c64da68f65cb6f93e18dc65d13969eba45349d99f36dfca7`;
the patched output digest is
`sha256:90c8a74773b7143a1a83825784c0dd4ba12106db72ef90ef75926e30ee0821dc`.
The runtime advertises native protocol 4 and owner-handoff protocol 2. A fresh
isolated candidate template at `/srv/batterygpt/qualification/queued-prompts-20260924`
has manifest digest `sha256:efa87b576da80bb011f0897bcab176b028a2e64d16acaf3b314d43070245ded3`.
The final executable, frontend build, configuration and content digests must
still be measured from the intended deployed candidate with `release-identity`.
Source and staging hashes alone are not a deployable identity.

The staging executable is retained at
`/srv/batterygpt/qualification/queued-prompts-20260924-orchestrator` with digest
`sha256:728d751920d6d59a80d3944a62a7c018c03ada0660dd4d98dab33ceba831b5ed`.
`release-identity` on a temporary copy of the installed configuration with
only its template manifest/digest changed reported the previous schema version 2 and the
schema/runtime digests above, owner protocol 2, gateway protocol 1, assets
digest `sha256:ac6ead9319c331dfa44117991a8b3972ce3fb57c7e00e00c346557c4751b7096`
and config-content digest
`sha256:b33ec4e2b43935a8f3b670622152e9ba4070950852cfceaa933a74d78cf98ea6`.
The production frontend build ID is `aNp5C89s_zeiIUPg-sIuA`.
The staging configuration digest was
`sha256:51c4e213a375aa6a03166d62e2169122b880984fcb0a3bc81df3e40eb23260bd`;
it is **not** a deployment digest because its base directory was `/tmp`.
The temporary configuration was removed after inspection.

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
delivery/run, retargets paused Project profiles to a supplied protocol-3
runtime digest while retaining their workspace/runtime volume IDs and clearing
the old sandbox placement, rebuilds derived conversation state and search, and validates
foreign keys and the candidate fingerprint before publishing its output path.
No ACK-only input is relabeled consumed. Unknown or contradictory source
shapes fail closed. The source snapshot is opened read-only.

From this checkout, the isolated format commands are:

```sh
go -C orchestrator run ./cmd/format-convert --candidate
go -C orchestrator run ./cmd/format-convert --source /path/to/source-snapshot.db --inspect
go -C orchestrator run ./cmd/format-convert --source /path/to/source-snapshot.db --destination /path/to/staging.db --target-runtime sha256:efa87b576da80bb011f0897bcab176b028a2e64d16acaf3b314d43070245ded3
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
   integrity and foreign-key checks, all live/unknown turns and commands and,
   where those tables exist, runs and deliveries,
   pending controls, captures, service slots and release blockers. Do not
   infer consumption from `turns.state=completed` or a command ACK.
3. Use the existing quiescent drain, not owner handoff, for a schema/runtime
   change. Wait for durable and process blockers to settle. Pause every retained
   Project; the converter refuses active Projects. Do not acknowledge an unknown
   delivery merely to make readiness pass.
4. Stop the incumbent owner and producers. Make a consistent SQLite backup
   with its WAL state and retain the corresponding guest journal, captures,
   workspace volumes, engine state and immutable runtime manifest. Hash the
   copies and keep the source untouched. Conversion writes only a separate
   staging database and artifacts.
5. Record each paused source sandbox identity and its two volume identities.
   Before activation, retire each old protocol-1 sandbox through the engine
   while keeping both volumes intact; verify the old sandbox is absent. The
   converted copy clears that placement binding so resume creates a new
   sandbox from the protocol-3 template. Do not mount the same retained volume
   into two live sandboxes. This engine action belongs to the separately
   authorized release, not to the offline converter or this development task.
6. Run the `format-convert` command above against a staging snapshot of the
   **actual** incumbent fingerprint. The destination must not exist. Check exact row/identity
   accounting, all foreign keys, unique live-run and delivery-claim indexes,
   unknown/removed/consumed evidence, capture paths, share/export chronology
   and deterministic projection rebuild. The tool verifies the candidate
   Store's compiled schema fingerprint and publishes the output only after
   projection reaches ready. Reject any unexplained data loss.
7. Exercise the candidate against that converted staging copy and the pinned
   runtime with controlled model streams, Stop/Steer settlement, gateway and
   browser reconnection. Record latency, bounded queue/page work and the
   acceptance results in [queued-prompts-acceptance.md](./queued-prompts-acceptance.md).
8. Only after those checks and separate deployment authorization, use the
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
The paused retained Project's staged profile was retargeted to the candidate
manifest, its workspace/runtime volumes remained, and its old sandbox binding
was cleared; a candidate Store reopened and read that profile successfully.
No source message text or credentials were exported to this repository.

Current state: the format transition is qualified on a staging copy and the
new runtime template passed isolated native, release, fault, real-browser and
combined workload suites. The staging executable/configuration identities are
measured; the final `/etc` configuration and executable must be inspected again
at release time. The old Steer remains explicitly unknown. Engine retirement
of the actual old paused sandbox has not been performed. Activation remains
closed. No live-data reset or deployment is implied.
