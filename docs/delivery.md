# Delivery: checks, candidates and activation

The delivery boundary has three stages: repository checks, immutable candidate
qualification, and an explicit installation update. A green hosted CI run or a
Git tag alone is not deployment approval or real-runtime qualification.

## Candidate build

Use a clean committed Linux checkout with the Go toolchain from
`orchestrator/go.mod` and Node from `.node-version`. Fetch the pinned private
engine dependency using your reviewed local Git credential setup, or
`scripts/fetch-go-dependencies.sh` with `KHORA_READ_TOKEN` in CI. Then:

```sh
python3 scripts/candidate.py build /srv/candidates/new-candidate
python3 scripts/candidate.py verify /srv/candidates/new-candidate
```

The output must be a new directory outside the checkout. The builder exports
only the committed Git tree into a temporary source directory, runs `npm ci`,
builds Next standalone output and static/public assets, and compiles the host
`orchestrator` and `edge-proxy`. Ignored environment files, private deployment
inputs and stale frontend output cannot enter through the working directory.
The build intentionally runs dependency install/build hooks; use reviewed source.
`NEXT_PUBLIC_PROJECT_BRAND_NAME` is an explicit public build input recorded in
the manifest. Do not pass credentials as public build variables.

The bundle contains `bin/`, `frontend/` and `candidate.json`. The manifest binds
source commit, platform, tool versions, dependency lock hashes, frontend build ID
and every artifact's bytes, mode or internal symlink. Changed, missing or extra
files fail verification. A local HTTP smoke checks the packaged page, public asset
and JavaScript chunk. The host package excludes guest images, engine state,
configuration, credentials, databases and fixture executables.

Office visual assets are built before Next and included under `frontend/public/office`.
The application and viewer use the same content-addressed asset identity.

Checksums detect changes relative to a trusted manifest; they are not signatures
or evidence that arbitrary downloaded code is trustworthy. Preserve the workflow
run/source identity and the original artifact. Do not rebuild a qualified candidate
at activation time. Next's build ID and host executable bytes may differ between
builds of the same source.

The [release workflow](../.github/workflows/release.yml) runs for `v*` tags or an
explicit dispatch, requires the selected commit to belong to current `main`
history, runs CI and creates a Linux amd64 candidate archive plus checksum.
It retains artifacts for 14 days. It does not publish a GitHub Release, choose a
production target or restart services. Other Linux architectures use the same
builder on a matching native host. Existing historical GitHub Releases remain
historical artifacts, not defaults for the Project architecture.

## Exact-artifact qualification

Prepare the pinned Khora engine and a new immutable guest template according to
[image assembly](sandbox-image.md). Install Playwright in the matching source
checkout and qualify with a new evidence directory:

```sh
dev/verify-khora.sh --candidate /srv/candidates/new-candidate --template /srv/build/battery --manifest /srv/build/battery.manifest.json --output /srv/qualification/new-candidate --suite all
```

The harness uses the candidate's actual owner and standalone frontend, records
its manifest hash, and builds only the selected test packages. The alternate
owner executable is an explicit handoff fixture, not a second deployable candidate.
Engine/image evidence must match the target platform and native protocol. Provider
and combined-load gates remain explicit options; do not report them as passed
when omitted. Private qualification outputs stay outside CI/public artifacts.

## Deployment handoff

1. Preserve the candidate archive, trusted manifest, runtime image/manifest,
   engine identity and required qualification evidence together.
2. Verify the staged bundle, then inspect it against the reviewed target config:

   ```sh
   python3 scripts/candidate.py inspect /srv/candidates/new-candidate --config /etc/batterygpt/application.json
   ```

   This runs `release-identity` after inventory verification. It does not open
   the product Store, contact the engine/provider or change the installation.
   Keep the resulting target/compatibility identity outside the public repository.
3. Select the update procedure from the table below. Coordinate supervision so
   the retired process cannot restart with obsolete bytes. Run the standalone
   frontend with `HOSTNAME=127.0.0.1 PORT=3000 node frontend/server.js` from the
   bundle root, adjusting the reviewed bind address for the gateway topology.
4. Use the [operations runbook](operations.md) for drain, ready, activate and
   complete. Verify installed executable/frontend/runtime identities and smoke
   authentication, workspace access, a native reply and relevant browser controls.
   Remove only the disposable smoke resources created by that verification.
5. Record the exact artifacts, checks, omissions and observed outcomes. A source
   branch/archive is not a database or engine-volume backup.

| Change | Required procedure |
| --- | --- |
| Owner/API binary only, matching compatibility and transfer predicate | Owner handoff; retain the independent gateway |
| Frontend, gateway, engine, configuration or current-format runtime changes | Full quiescent procedure; preserve exact retained Project bindings |
| Incompatible schema/protocol | Stop; a deliberate destructive reset needs its own explicit scope and authorization |

Do not clear state or silently fall back to an old executable after activation
failure. Repair the selected candidate while admission remains fenced, following
[operations](operations.md) and [compatibility](compatibility.md). The historical
queued-prompts reset is evidence for its recorded installation only; future
updates do not inherit that reset authorization.
