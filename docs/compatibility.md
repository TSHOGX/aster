# Current release contract

There is one supported application schema and protocol set. No historical schema
recognition, migration chain, old-client adapter, rollback candidate override or
old-release compatibility matrix is provided. A non-current database is refused
before a writer opens. A format-changing update requires an independently
separately authorized destructive reset. The queued-prompts transition is
tracked in [queued-prompts-release.md](queued-prompts-release.md); reset is not
implicit in application startup.

`release-identity` reports the executable, exact schema fingerprint, fixed
configuration, current runtime catalog and content digests. Its compatibility
field is a deployment-contract digest for guarded owner handoff, not a promise
that older builds or persisted formats are supported.

Direct/DNS defaults have a separate accepted policy revision. Owner handoff checks
that revision before acquiring the writer, and reload cannot change it while
release admission is fenced. A running Project retains its effective policy until
creation/resume selects a new snapshot.

For an update within this exact schema/protocol contract, the selected executable
must match the activation target. There is no override for another executable.
If a pinned gateway is replaced after activation, the selected owner can complete
only after all retained work settles and full quiescence is established.

The runtime still verifies its exact current manifest/native/journal/reader
protocols and pinned Khora build. Retained templates are references to immutable
images for current-format Projects, not readers for retired formats. Schema,
engine and runtime identity checks remain mandatory.

A deliberate data reset is described in [bootstrap](bootstrap.md). It removes the
old installation's state and starts a new engine store and application database;
it does not convert history or restore an old database afterward.

Release identity format 3 declares elapsed minute intervals in schedule recurrence.
The database schema is unchanged. Crossing from a Cron-only owner requires a
quiescent update; its different deployment digest prevents owner-only handoff to
a build that cannot execute interval rules.
