# Fresh deployment and removal of historical support

The explicit destructive-reset follow-up is complete. Source `bd15992` was rebuilt,
qualified and deployed with a new application database, engine store and runtime.
The [sanitized deployment record](../dev/measurements/fresh-deployment.json) binds
the result to executable digests, the runtime digest and the frontend build ID.

## Removed implementation

- The 15-step schema construction/migration chain and historical fingerprints.
- Incremental ALTER/backfill scripts and tests that qualified conversion of old data.
- Startup branches for retired schemas and the old-executable rollback override.
- Rollback CLI/control paths and old-build rollback qualification suites.
- The migration-only read-command cleanup table and reconciliation branch.

The single canonical DDL initializes fresh databases atomically. Current databases
must match both version 1 of the new lineage and its exact fingerprint. Unknown
or modified schemas are refused without writes. Current release authority,
transaction atomicity, runtime verification and original-operation recovery remain
required; only the selected executable can complete activation.

## Reset and deployment

The stopped installation's application database/captures, engine state, seven
owned subvolumes/overlays, old state backups and 17 superseded release directories
were deleted. Old templates were replaced and the dedicated network namespace
recreated. Login credentials, provider configuration and TLS material were
preserved. Before smoke testing, all Project/Session/turn/message/schedule/terminal
and resource-binding tables were empty.

All seven services are active with zero restarts. The live smoke passed login,
recurrence preview, workspace upload/read before compute, runtime identity,
a native reply through the model gateway, browser schedule editing/global
navigation and network reload. The temporary user and smoke resources were
removed. Its deletion tombstone is new test metadata, not retained legacy data.

Go race/vet, integration compilation, frontend typecheck/lint, 186 unit tests,
production build, real release/network gates and three composed-browser workflows
passed. One unit benchmark and the separate combined-browser workload were opt-in
skips. Concurrent uncommitted preview work was outside the validated release.
