# Queued-prompts release preparation

Status: development artifact, not deployment authorization.

The current product is a fresh-install contract. It uses schema version 3 with
compiled fingerprint `sha256:44895d2a2e545ba7232cd42ef06f7d5c7a5342086e8962818b1e9d34a3cf8e22`
and native protocol 4. The patched Pi RPC output is pinned at
`sha256:d700150f40b6e1f942b8082c4244cdc234948d38aa9e975b386aefb8278b9a92`.
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

Activation remains separately authorized. No live database, service or runtime
image is modified by development work alone.
