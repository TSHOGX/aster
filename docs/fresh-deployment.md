# Fresh deployment

This is a historical installation record, not verification of the current branch
tip or an instruction to reset the next deployment. Use [delivery](delivery.md)
for new candidates and [operations](operations.md) for current-format updates.

The installed application is rebuilt from the current source after deleting
incompatible database, engine, volume, capture and deployment state. The reset
preserves login/provider credentials, TLS and reviewed configuration only.

The canonical schema is initialized directly. There is no migration chain,
format converter, historical fingerprint reader or rollback override. Current
process recovery, journal identity and owner fencing remain required.

The [current sanitized deployment record](../dev/measurements/current-deployment.json)
was written after live verification. All final business tables are empty;
the smoke run was followed by an empty reinitialization of the same verified artifacts.
See [bootstrap](bootstrap.md), [release](queued-prompts-release.md) and
[verification](verification.md) for the procedure and acceptance checks.
