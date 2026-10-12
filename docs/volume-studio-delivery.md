# Volume Studio delivery and qualification

The independent authoring application, central identity adapter, authenticated
distribution, Aster import/engine integration and admin release selector are
implemented. The final operational scope is documented in
[Volume Studio](volume-studio.md). The protocol rejects unsupported versions;
no historical application adapters or shared business/storage dependencies remain.

| Stage | Delivered behavior | Verification |
| --- | --- | --- |
| Portable content | Canonical manifests, strict paths/parents/limits, stable source identities | Independent Go producer/consumer, Node vectors, fuzz and boundary tests |
| One identity | Registered callback/PKCE, one-use codes, private grants/introspection, durable Studio sessions, shared logout | Concurrent code-replay, expiry, credential rotation, disable/demotion and separate-process browser gates |
| Durable authoring | Immutable blobs, revision/receipt mutations, directories, rename/delete/restore, resumable uploads | Restart, stale-tab/target, changed-chunk, zero-byte, quota, cancellation and expiry tests |
| Source publication | Frozen versions, principal-gated validation, export grants/ranges, reader pins, retirement | Freeze isolation, generation fencing, service expiry/revocation, active-reader/GC and backup/restore tests |
| Authoring interface | Libraries/tree/text editor, raster preview, upload resume, changes/history, review/restore/retire | Production Next build and real Go/identity/TLS browser journey; desktop/mobile inspection |
| Consumer | Trusted source catalog, exact admission receipts, local cache, streamed materialization, profile retention and ready registration | Transfer faults/restart, offline acquired source, quota/receipt/registry tests and real Khora runs |
| Admin/runtime lifecycle | Release picker, local readiness/provenance, existing Environment selection, retained Project plans and cleanup | Admin browser test, saved unallocated Project pinning, independent owned copies and engine descendant refusal |
| Independent operations | Studio CI/release inventory, packaged smoke, schema steps and private backup | Committed-source independent bundle build, asset/deep-link smoke, upgrade/refusal and restore checks |

## Measured envelope

- Real Studio → Aster → pinned Khora: 32 MiB source, then four 256 MiB files
  (1 GiB tree). Studio was stopped after acquisition; materialization, streamed
  readback, local registration, independent snapshot copies and descendant-aware
  cleanup continued locally. The maximum-size run took about 58 seconds on the
  qualification host. This is a measured scenario, not a service-level guarantee.
- A disk-backed 10,000-entry freeze measured p95 about 84 ms across five runs;
  metadata heap was about 6 MiB at the sample point. This isolates freeze on the
  local host. Production contention and filesystem behavior can change timing.
- Two concurrent 256 MiB uploads completed with about 17 MiB peak Go heap
  above a sub-MiB baseline in the isolated authoring test; input chunks were 8 MiB.
- Directory/API pages are at most 200 entries. Hash/readback buffers, chunks and
  API concurrency are bounded independently of file size. One-over file/tree/entry
  cases are refused before registration; unsupported profiles fail closed.
- Maximum authority evidence is 60 seconds; Studio currently introspects on every
  privileged request, so an identity outage immediately refuses new privileged
  actions. Worker principal checks are independent of ordinary browser logout.

## Explicit release choices

Studio runs one SQLite owner with WAL/FULL and transactional additive migrations.
Aster uses its current exact-schema policy (schema 8), with no history conversion.
The public frontend uses the exact `/volumes-studio/` prefix; private identity and
distribution listeners are loopback services. Cross-host use requires verified TLS
termination/tunneling at those private boundaries. Source/API versions are explicit;
unsupported versions fail rather than adding compatibility adapters.

The qualified preview set is UTF-8 text and raster images. PDF, SVG, Office and
other binaries remain exact-byte downloads. Per-team self-service, automatic
Project upgrades, live shared-volume authoring, archive/Git/URL ingestion and a
full OIDC-provider replacement remain outside this release.

## Reproducible gates

`studio`, `studio-frontend`, `studio-browser`, `backend`, `race` and `frontend` are
available through `scripts/check.sh`. Privileged engine qualification uses
`TestStudioImportOnRealKhora` with the existing disposable substrate and the real
Studio binary; `STUDIO_TEST_MAX_SIZE=1` selects the maximum-size corpus. It never
addresses a live deployment. Private logs, screenshots and machine-specific
measurements are excluded from source and release bundles.

## Final regression results

The completed checks include the full backend gate (build, vet, unit tests,
privileged-test compilation and release predicates), the core product/agent/runtime
race suite, Studio build/vet/race/protocol checks, all three frontend lint/build/type
checks, and source/workflow validation. Browser results: 234 workbench cases,
23 admin UI cases, nine real-edge admin cases and three separate-process Studio
journeys passed. Twenty-eight existing opt-in workbench profiling cases were not
selected. The final standalone Studio bundle passed byte inventory and root,
static-asset and deep-link smoke checks from its packaged files.
