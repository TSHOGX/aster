# Current release contract

There is one supported application schema and protocol set. No historical schema
recognition, migration chain, old-client adapter, rollback candidate override or
old-release compatibility matrix is provided. A non-current database is refused
before a writer opens. Format changes use a clean redeployment with the current
schema and protocols. Startup refuses mismatched state rather than deleting a
database implicitly.

`release-identity` reports the executable, exact schema fingerprint, fixed
host configuration and current host protocols. Bootstrap runtime/source files
are explicit operator inputs, excluded from host compatibility. Its compatibility
field is a deployment-contract digest for guarded owner handoff, not a promise
that older builds or persisted formats are supported.

Owner handoff pins a digest of persisted registry, environment, network and
account-policy/resource configuration. Readiness, activation, completion and
read-only startup validation recheck that digest. Candidate inspection does not
substitute bootstrap network declarations for the accepted database policy.
Administrative edits remain fenced while release admission is closed. A running Project retains its effective policy until
an explicit project configuration change selects a new snapshot.

For an update within this exact schema/protocol contract, the selected executable
must match the activation target. There is no override for another executable.
If a pinned gateway is replaced after activation, the selected owner can complete
only after all retained work settles and full quiescence is established.

The runtime still verifies its exact current manifest/native/journal/reader
protocols and pinned Khora build. Several immutable templates can coexist when
each registration satisfies the current host/runtime contract. New defaults do
not rebind existing Projects. Schema, engine-store and exact runtime identity
checks remain mandatory; unsupported protocols require a fresh installation.

A deliberate data reset is described in [bootstrap](bootstrap.md). It removes the
old installation's state and starts a new engine store and application database;
it does not convert history or restore an old database afterward.

Release identity format 7 declares schema 8, runtime manifest 5, native protocol 4
and schedule platform protocol 1. Conversational scheduling includes immutable
mutation receipts, one-shot admission, bounded checks and an owner invalidation
outbox. Old databases and runtime manifests are refused without conversion.

Saved Project contracts are independently checked against their exact retained
database registrations at startup; a different database default does not replace
their template or runtime digest.
