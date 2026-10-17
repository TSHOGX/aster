# Back up and recover

[Documentation](../README.md) · Deployment and operations

Repair unavailable dependencies before retrying failed work. Preserve existing
state while an outcome is uncertain. Resetting a database or runtime journal is
not a normal recovery step.

## Back up the installation

A complete backup includes the stopped application's database and host artifacts,
plus a consistent engine/volume backup using the engine's supported procedure.
A SQLite copy alone does not contain workspace files or native runtime volumes.

1. Plan the maintenance window and stop new access/admission.
2. Let work settle and stop the application owner under its supervisor. Coordinate
   gateway, frontend and engine state with the chosen engine backup procedure.
3. Preserve application state, captures/shares and the matching engine/owned volumes.
   Keep credentials, TLS material and reviewed configuration in protected backup storage.
4. Record artifact identities, engine store identity and the backup's complete scope.
5. Validate the backup using an isolated restore procedure before relying on it.

Restore only a consistent set with matching executable/schema, runtime registrations,
engine store and owned data. Do not connect a second writable owner to production
storage. There is no built-in cross-host failover or active-active mode. Studio
requires its [separate backup](../../volumes-studio/deploy/README.md#backup-and-restore).
Include Evolution Space's [projection snapshot](configuration.md#back-up-and-restore-evolution-space)
for catalog history, run/evaluation journals, artifacts and retention receipts.
Stop all three writers before selecting a consistent recovery point; restoring
one service alone does not recreate another service's grants or operation records.
After restore, verify central configuration revisions, Studio accepted heads and
release identities, and Evolution Space run/artifact lineage before reopening work.
Browser sessions require fresh central login in both independent applications.

## Restart after a process or dependency failure

Restart the owner with the same database, engine store and saved runtime bindings.
It reconciles pending operations and reconnects to retained execution where possible.
A browser or engine transport disconnect alone does not prove a process exited.

Inspect [diagnostics](maintenance.md#inspect-health-and-capacity), operation progress
and the affected Project. Restore the missing engine, gateway, registered artifacts
or storage capacity, then use the explicit retry/restart appropriate to the reported
failure. Do not patch lifecycle rows or reuse another installation's engine identity.

## Resolve unknown outcomes

| Unknown operation | Safe next step |
| --- | --- |
| Agent prompt or external side effect | Preserve journals/captures; inspect conversation/recovery status before resubmitting |
| File write, upload, rename or delete | Inspect current file and original operation; do not automatically repeat the mutation |
| Terminal process | Reconnect and inspect process status before rerunning commands |
| Environment binding update | Let reconciliation inspect the complete binding; escalate unrelated bindings rather than forcing resume |
| Import/publication or volume allocation | Retain its reservations and staging until the effect or cleanup is observed |
| Administrative stop/suspension | Follow each target; accepted or disabled-account status does not establish process termination |

Use the original request identity for supported command/editor retries with uncertain
responses. A new request can duplicate work. An acknowledgment of uncertainty is
not evidence that external effects did or did not happen.

Missing or corrupt native journals require investigation of the original state;
replay does not start a new Agent to reproduce it. Keep the original files and
private diagnostics. Force restart terminates the old placement and may affect
sibling work; use it only as an explicit recovery decision.

## Activation failure

After upgrade activation, only the pinned replacement executable can acquire the
owner role and complete. Keep admission closed and repair that candidate's startup
failure. Deadline expiry does not reopen an activating update. Follow
[upgrades](upgrades.md); there is no old-candidate override.

## Deliberate destructive reset

Use this only for an explicitly chosen fresh installation, such as an unsupported
schema or engine-store transition. It permanently discards the selected old Project
data and does not convert history.

1. Define and review an exact installation inventory and deletion scope. Preserve
   a consistent offline backup if retention is required.
2. Close browser ingress and stop owner, gateway, frontends, engine proxy and engine
   under their supervisors. Verify old processes and sandbox descendants stopped.
3. Remove only the selected application state, captures/shares, engine state, owned
   volumes/overlays and superseded artifacts. Do not infer paths from old run notes.
4. Retain credentials, TLS and reviewed configuration unless included explicitly
   in the reset scope. Update the engine store identity and runtime manifest inputs.
5. Follow [fresh installation](installation.md): bootstrap, start, explicitly publish
   content and validate before reopening access.
6. Record the actual deletions, retained configuration and observed checks privately.

There is intentionally no generic recursive-delete command: resource ownership and
paths must be established for the target installation. Backup retention is a separate
choice. Do not restore an old incompatible database into the new installation.
