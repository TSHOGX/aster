# Queued-prompts release procedure

For the ongoing build/qualification/activation process, use [delivery](delivery.md).
The reset below records a specific format transition; it is not the default
procedure for ordinary current-format updates.

The 2026-09-25 queued-prompts reset is historical; see its
[acceptance record](queued-prompts-acceptance.md). It does not authorize a reset
for subsequent features. Current source uses schema 4, native protocol 4, runtime
manifest 4 and schedule protocol 1. Exact schema and artifact digests come from
the compiled release identity, never a copied historical fingerprint. Startup
refuses mismatched databases and unsupported retained manifests without conversion.

## Release identity

Build a new immutable runtime image from the pinned source and run
`release-identity` against the exact executable, configuration, frontend
assets, schema and runtime manifest selected for activation. The native patch
must be generated from the pinned upstream bytes and its output digest must
match the current constant. A protocol or schema change invalidates every
previous candidate identity and requires a new image.

The final executable, frontend build, configuration and content digests must be
measured from the intended candidate. Source and staging hashes are not a
deployment identity.

## Destructive reset

Use a fresh-lineage reset only when the deployment task explicitly authorizes
its concrete data-loss scope. Before that separately authorized activation:

1. Close browser ingress and stop the owner, gateway, frontend, engine proxy
   and engine under their supervisors.
2. Confirm no application or engine descendants remain and retain only the
   reviewed credentials, TLS material and configuration required for the new
   installation.
3. Delete the old application database and sidecars, captures, shares, engine
   state, owned workspace/runtime volumes, overlays, superseded binaries and
   retired runtime templates. Do not infer unrelated paths as deletion targets.
4. Install the new executable and frontend, initialize a new schema-4 database,
   register the manifest-4/native-protocol-4 immutable template, and bind the new engine store.
5. Start services and verify readiness, authentication, Project creation,
   workspace access, scheduling, native reply, network policy and browser
   controls. Remove disposable smoke resources afterward.

This permanently discards old Project, Session, prompt, message, schedule,
terminal and runtime state. There is no conversion command, startup migration,
old-client adapter or rollback into the new database. Preserve credentials and
TLS only when they are explicitly outside the reset scope.

## Qualification gates

Run repository checks first, then qualify the new candidate through the actual
stdin/stdout RPC, runner journal, host reducer and browser API. Cover handled
extension inputs, listener failures during cleanup, delayed Steer preflight,
process loss, unknown recovery, duplicate expanded text, retry and compaction,
queue frontier handoff, segment rebuild and destructive reset.

Current-format releases use the normal delivery process. The scheduling source
change performs no database reset, live runtime rebuild or deployment.
