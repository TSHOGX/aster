# Current release contract

There is one supported application schema and protocol set. No historical schema
recognition, migration chain, old-client adapter, rollback candidate override or
old-release compatibility matrix is provided. A non-current database is refused
before a writer opens. A format-changing update requires an independently
authorized destructive reset. The queued-prompts transition is
tracked in [queued-prompts-release.md](queued-prompts-release.md); reset is not
implicit in application startup.

`release-identity` reports the executable, exact schema fingerprint, fixed
configuration, bootstrap publication/runtime inputs. Its compatibility
field is a deployment-contract digest for guarded owner handoff, not a promise
that older builds or persisted formats are supported.

Owner handoff pins a digest of persisted registry, environment, network and
account-policy/resource configuration. Readiness, activation, completion and
read-only startup validation recheck that digest. Candidate inspection does not
substitute bootstrap network declarations for the accepted database policy.
Administrative edits remain fenced while release admission is closed. A running Project retains its effective policy until
creation/resume selects a new snapshot.

For an update within this exact schema/protocol contract, the selected executable
must match the activation target. There is no override for another executable.
If a pinned gateway is replaced after activation, the selected owner can complete
only after all retained work settles and full quiescence is established.

The runtime still verifies its exact current manifest/native/journal/reader
protocols and pinned Khora build. There is one runtime template per installation. Schema, engine and runtime
identity checks remain mandatory; a different image requires a fresh installation.

A deliberate data reset is described in [bootstrap](bootstrap.md). It removes the
old installation's state and starts a new engine store and application database;
it does not convert history or restore an old database afterward.

Release identity format 4 declares schema 4, runtime manifest 4, native protocol 4
and schedule platform protocol 1. Conversational scheduling includes immutable
mutation receipts, one-shot admission, bounded checks and an owner invalidation
outbox. Old databases and runtime manifests are refused without conversion.

Saved Project contracts are independently checked against their exact retained
database registrations at startup; a different database default does not replace
their template or runtime digest.
