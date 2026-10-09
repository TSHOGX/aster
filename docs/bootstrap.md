# Fresh installation and deliberate reset

Build and verify the current application, frontend, pinned engine and runtime
before replacing an installation. The product initializes one canonical schema
in a fresh database. Existing databases must match that schema exactly; no
migration or legacy-format conversion runs at startup.

For a deliberate destructive reset, close browser ingress and stop the owner,
gateway, frontend, engine proxy and engine under their supervisors. Confirm the
old processes and sandbox descendants have stopped before deleting resources.
Use an explicit reviewed inventory for the installation's application database
and sidecars, captures/shares, engine state, owned workspace/runtime volumes and
overlays. Preserve a consistent offline backup when the reset scope requires it;
backup retention and removal are separate decisions. Delete only the selected
state and superseded artifacts. Do not infer targets from unrelated paths, another
engine store or historical reset records.

Keep the deployment's login registry, provider credentials, TLS material and
reviewed configuration unless those are explicitly part of the reset. Install
fresh artifacts, register the new immutable template, and update configuration to
the new engine store ID and runtime manifest. Clear the retained-template catalog
when no Projects survive. Start the engine/proxy, frontend, owner and gateway,
then verify readiness before reopening browser ingress.

Validate the running executable and frontend build against the prepared artifacts.
Check authentication, new Project provisioning, retained workspace access,
scheduling and network policy, then remove disposable smoke resources. Report
what was deleted, what configuration was retained and which checks actually ran.

This reset permanently discards the old Project data. Ordinary updates that
preserve current-format work use the [release runbook](operations.md); data reset
is not a substitute for unknown-outcome reconciliation during normal operation.
