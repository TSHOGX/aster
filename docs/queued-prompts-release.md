# Queued-prompts release procedure

For the ongoing build/qualification/activation process, use [delivery](delivery.md).
The reset below records a specific format transition; it is not the default
procedure for ordinary current-format updates.

The authorized reset, rebuild and restart completed on 2026-09-25. See the
[current deployment record](../dev/measurements/current-deployment.json) and
[acceptance](queued-prompts-acceptance.md). The procedure below applies to future resets.

The current product is a fresh-install contract. It uses schema version 3 with
compiled fingerprint `sha256:c1115d24305b7c578b237cfa9ee0aba3a0239d4ae180c2d9cb48f9266cb2528a`
and native protocol 4. The patched Pi RPC output is pinned at
`sha256:e533d9e5bbe59991460ca018b123fab699554918ca95ba5c8a860642f19a0952`.
The old protocol-3 candidate and old product database are retired artifacts;
the application does not recognize or convert them.

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

Backward compatibility is intentionally removed. Before activation:

1. Close browser ingress and stop the owner, gateway, frontend, engine proxy
   and engine under their supervisors.
2. Confirm no application or engine descendants remain and retain only the
   reviewed credentials, TLS material and configuration required for the new
   installation.
3. Delete the old application database and sidecars, captures, shares, engine
   state, owned workspace/runtime volumes, overlays, superseded binaries and
   retired runtime templates. Do not infer unrelated paths as deletion targets.
4. Install the new executable and frontend, initialize a new schema-3 database,
   register the protocol-4 immutable template, and bind the new engine store.
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

Future activations require their own task authorization. The completed reset
removed all former application and engine state and retained only the current
release plus credentials, TLS and reviewed configuration.
