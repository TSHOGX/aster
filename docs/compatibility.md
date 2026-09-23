# Release compatibility and rollback

Compatibility is declared for exact schema, runtime and protocol combinations.
A matching `release-identity` is necessary for owner handoff; it does not certify
arbitrary older code. Rollback uses the current database and retains writes accepted
by the newer owner. Restoring an older backup over those writes is unsupported.

## Qualified owner, gateway and client pair

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
| Guest runtime | Manifest 3, native protocol 1, Agent/journal 2 and reader 2 | Both owners use the recorded immutable runtime digest; format-2 templates are refused |
| Engine | Pinned `e8cd021a9c7c` engine/module | Existing process-recovery gate preserves resource identity; changing engine versions is outside this pair's rollback qualification |

The rollback run completed in 15.69 seconds. The retained-owner browser gate passed
two workflows in a 38.15-second runtime run. These observations qualify this pair,
not every build that reports the same protocol numbers.

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

Schema downgrade, format-2 native runtimes and pre-settlement owners are not rollback
targets. Unknown/modified schema is refused before a writer opens. Candidate identity
or pending owner-contract mismatch cannot migrate the database. No permanent legacy
reader/decoder exists to make those combinations appear compatible.

A different engine/template version, client protocol change or schema change needs
its own forward/rollback rehearsal and accepted-write proof. The current pair does
not finish that broader matrix. First deployment into an ungated installation also
requires an enforceable bootstrap; a quiet observation window is insufficient.
Those deployment gates remain open rather than being inferred from this rehearsal.
