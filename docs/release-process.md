# Versioning and release process

Git tags `vMAJOR.MINOR.PATCH` identify product releases across backend, frontend,
deployment CLI and rootfs. Candidate tags use `-rc.N`. Never move an existing tag
or replace already published assets; create a new candidate or release.

## Development and planning

Use short-lived branches and PRs against main. PR descriptions state the problem,
resulting behavior and validation. Keep implemented contracts in reference docs;
track proposed work and unimplemented features in
[Issues](https://github.com/TSHOGX/agent-runtime-platform/issues). The existing
[core roadmap](https://github.com/TSHOGX/agent-runtime-platform/issues/3) remains
unchanged by documentation maintenance.

Do not create local proposal archives, deployment journals or per-version upgrade
directories. Schema fixtures that actively test old-format compatibility remain
under tests and are regenerated only through reviewed tooling.

## CI and build inputs

[CI](../.github/workflows/ci.yml) runs on branch pushes, PRs, manual dispatch and
reusable workflow calls. It builds/vets/tests Go, checks frontend lockfile/lint/
types/tests/build/standalone, tests the bridge and release privacy boundary, and
checks script/documentation syntax and references.

[scripts/release/toolchains.json](../scripts/release/toolchains.json) records the
release toolchain and runtime pins. The Go module's language version is not the
same thing as the pinned release compiler. Rootfs builds use the pinned Ubuntu
snapshot, Pi and Python requirements lock. Update these with artifact validation.

## Publishing

After main CI passes, create and push a tag on the intended commit:

```bash
git fetch origin --tags
git tag -a vX.Y.Z-rc.N origin/main -m "vX.Y.Z-rc.N"
git push origin vX.Y.Z-rc.N
```

[Release](../.github/workflows/release.yml) then runs entirely on disposable
GitHub-hosted Ubuntu runners:

1. Verify the tag matches the checkout and belongs to main history.
2. Run code CI on that exact commit.
3. Build static amd64 Go binaries, frontend standalone with Node, and generic
   sandbox rootfs. Upload the sandbox to GHCR by digest.
4. Assemble the release manifest/checksums and run packaged artifact smoke on
   Ubuntu 22.04/24.04, including synthetic migrations and real runsc startup.
5. Verify remote content, upload a complete draft, verify asset bytes and publish.

The repository Actions token needs the declared contents/packages permissions.
No self-hosted runner, production access or site-specific qualification adapter
is part of the release pipeline. An unavailable build input or failed check stops
publication. Actions artifacts are intermediate/expiring build evidence; the
complete Release is the normal installation source.

## Release notes and announcements

GitHub Release notes are the canonical version announcement and one-off upgrade
record. Write them in English and include user-visible behavior, compatibility,
required migrations, rollback limits and test scope. Do not include host
identities, real schema fingerprints, customer SQL or operational evidence.

The publisher requests GitHub-generated commit/PR notes and appends the generic
release/upgrade contract. Maintainers can edit the release body to add a reviewed
user-facing summary; edits must describe that tag, not future main changes.
`UPGRADE.md` remains a generic artifact for offline consumers, not the entire
release announcement.

Unreleased announcement drafts belong in the private handoff or issue/PR context
until a real tag exists. A historical file named after an uncreated version must
not be published as if that version had shipped. Release-note edits do not change
immutable binaries or tags.

## Deploying

Use [release deployment](delivery/operations.md) and private target configuration.
Build once, then download the same artifacts in each environment. A clean source
checkout alone does not establish the running version; CLI/health output and the
actual executable hash identify it.

Hosted reports describe artifact checks, not production acceptance. Complete
backup and local business acceptance before opening traffic. Rollback is not a
generic old-binary swap; see [database operations](database.md).
