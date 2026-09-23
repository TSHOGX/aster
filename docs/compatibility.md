# Release compatibility and rollback

Compatibility is declared for exact schema, runtime and protocol combinations.
A matching `release-identity` is necessary for owner handoff; it does not certify
arbitrary older code. Rollback uses the current database and retains writes accepted
by the newer owner. Restoring an older backup over those writes is unsupported.

## Schedule catalog update

The chronological schedule overview requires schema 14. The migration adds
owner-scoped catalog revisions and ordering metadata, preserving all schedules
and run history. Deployment requires the full quiescent activation gate. The
new browser uses opaque overview cursors and recurrence previews; older clients
and schema-13 owners are not supported rollback targets for this release. The
historical qualification records below do not certify this change.

## Workspace storage update

The [qualified independent workspace update](../dev/measurements/paused-volumes.json) requires schema 13. Upgrade from schema 12
uses the full quiescent activation gate; it is not a compatible owner-only
handoff. Known schema migration preserves accepted history and file outcomes.
The pinned Khora engine and manifest-3 runtime are unchanged. Older schema-12
executables are refused after migration; the historical rollback tuples below
do not authorize a schema downgrade or restoring old database contents.

## Recorded schema-12 owner, gateway and client pair

The [final rehearsal](../dev/measurements/final-rollback.json) uses retained
`e1e4e665580427f1daf7ce3f46345dfec8489bed` and candidate
`fcc38780b5a8fa281ce0e5e68413847cf2b23773` on schema 12 and the current manifest-3,
native-1, Agent/journal-2, reader-2 image. The candidate adds bounded atomic Agent ingestion;
the retained owner uses the prior single-record commit path. Schema, wire and
runtime catalog contracts are unchanged. Owner and
gateway A→B→A preserves five completed native turns, held intent and post-upgrade
workspace bytes in 17.90 seconds. The current client passes all three browser
workflows against the retained owner in 44.50 seconds, including metadata-only
schedule overview and complete prompt edit/save/disable.

The [current infrastructure rehearsal](../dev/measurements/final-infrastructure.json)
passes in 107.19 seconds with the batched owner, two artifacts of the pinned engine
and runtime defaults v16→v15→v16. All three saved Project bindings, eight native
turn identities/states and post-upgrade workspace bytes survive. The non-default
image remains in the host-validated catalog; omitting it refuses startup.

The exact artifacts, frontend tree and runtime digest are in that record. This
is the recorded schema-12 client/API rollback pair. Earlier pairs below remain historical
qualification and do not imply that the current client supports old schedule
responses or pre-catalog owner handoff.

## Earlier owner and infrastructure qualification

The [revision rehearsal](../dev/measurements/revision-rollback.json) upgrades from
`525ec8fa543f3b1cd8ce13c8fa6a3d3605b25f8b` to
`cc8dc882e66a1a2e24484f59fe74588c7cd8cfd5` and rolls back to the retained executable.
The candidate's uncommitted changes concern qualification harnesses/tests/docs;
its production owner code matches the recorded revision. Exact executable digests
and Go build information are in the record.

| Deployment unit | Qualified combination | Rehearsal and boundary |
| --- | --- | --- |
| Owner/API | Recorded revisions, schema 12, owner protocol 1 | Upgrade, accept new writes, gated rollback, then continue native work |
| Gateway | Both recorded executables, authority protocol 1 and owner handoff protocol 1 | Gated replacement A→B→A, running inode digests checked, native work after each replacement |
| Browser/API | Client source tree `37cf927907c0445174fa010b01c548074a67313f` with either recorded owner | Client tree is identical at both revisions; native browser/Files/PTY workflows pass against the retained owner and the current owner |
| Product schema | Schema 12 unchanged across this pair | Five completed turns, held intent and post-upgrade workspace bytes survive; no schema downgrade is implied |
| Guest runtime | Two recorded immutable manifest-3 images, native protocol 1, Agent/journal 2 and reader 2 | Default A→B→A changes preserve Projects bound to either image; format-2 templates are refused |
| Engine | Pinned `e8cd021a9c7c` engine/module, two recorded artifacts | Gated artifact A→B→A replacement reuses the same persistent engine store; other advertised engine builds are refused |

The rollback run completed in 15.69 seconds. The retained-owner browser gate passed
two workflows in a 38.15-second runtime run. These observations qualify this pair,
not every build that reports the same protocol numbers.

The [infrastructure rehearsal](../dev/measurements/infrastructure-rollback.json)
records a separate 78.35-second run. It replaces two distinct artifacts of the
supported engine revision on the same store and rolls template defaults A→B→A.
The images differ in reviewed builtin context and have distinct release digests.
Three Projects retain their saved bindings, eight completed native turn IDs/states
and exact workspace bytes. A Project created under B continues to use B after the
default rolls back to A; this does not rebind existing Projects or migrate their
runtime volumes. Retain both referenced templates and exported manifests until
their Projects retire. Current builds require the non-default binding in
`retained_templates`; a subsequent infrastructure rehearsal verified startup
refusal without that catalog and successful A→B→A continuation with it.

The current host validates all catalog manifests and the actual placement manifest
against its required protocols. Catalog-aware builds have a different compatibility
identity from the recorded earlier owner pair, so crossing that boundary requires
full drain. The earlier owner-handoff rehearsal does not qualify a live handoff
across this change.

An attempted candidate advertising a different engine build was rejected before
application serving. Earlier fixture attempts also exposed that a paused placement
can still have Agent-transition/cleanup evidence outstanding. The qualified sequence
waits for both before closing admission; it never counts a paused placement alone
as an idle application.

## Rollback window

For a deployment of a qualified pair, retain both exact host executables, the
client production bundle, configuration, immutable guest image/manifest and engine
artifact for seven days after activation. Keep their digests and qualification
record together. Extending the window or changing any matrix input requires fresh
qualification. This is an artifact-retention policy, not permission to erase accepted
data or a database TTL. Repository maintainers own requalification; operators own
retention and selection of the deployed artifacts.

Use full admission drain for rollback that changes configuration, schema, template,
engine or gateway. Owner-only handoff requires an unchanged compatibility contract
and the same live gateway registration. The old owner starts behind the activating
fence and explicitly acknowledges rollback; neither a stale coordinator nor the
retiring process can reopen admission. See the [operations runbook](operations.md).

## Unsupported and remaining cases

Schema downgrade, manifest-format-2 native runtimes and pre-settlement owners are
not rollback targets. Current guest builds accept journal format 2 only; journal
format 1 is refused without conversion or loss of its original evidence. Unknown/modified schema is refused before a writer opens. Candidate identity
or pending owner-contract mismatch cannot migrate the database. No permanent legacy
reader/decoder exists to make those combinations appear compatible.

An engine source revision outside the pin, another template protocol, client
protocol change or schema change needs its own forward/rollback rehearsal and
accepted-write proof. Only the recorded combinations above are qualified. First deployment into an ungated installation also
requires an [enforceable bootstrap](bootstrap.md); a quiet observation window is
insufficient. That deployment remains pending rather than being inferred from
these rehearsals.
